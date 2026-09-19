// Command telemetry is a minimal worker health agent.
//
// It samples host CPU and memory utilization from /proc on a fixed ticker and
// exposes the result at GET /health as {"overloaded": true|false}. The rule
// lives in internal/load, which the debug build shares, and its doc comment
// explains why memory and CPU are judged differently.
//
// It reads only world-readable files under /proc, so it requires no privileges
// and should be run as an unprivileged user. All diagnostics go to stderr;
// under systemd/journald or docker that is captured automatically, leaving the
// binary stateless with no log files to rotate.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/CyLabAcademy/cork-telemetry/internal/load"
)

const (
	defaultPort = "2136"
	portEnv     = "TELEMETRY_PORT"
)

// overloaded holds the latest verdict. It is written only by the sampler
// goroutine and read by HTTP handlers, so an atomic is sufficient.
var overloaded atomic.Bool

// haveVerdict reports whether the verdict above is worth anything: false until
// a sample has both succeeded AND settled (load.State.Settled), and false
// again once the sampler has failed to take one for staleLimit ticks running.
//
// Without it the agent fails open on the axis that matters. The listener binds
// before the sampler has produced anything, and an atomic.Bool reads false, so
// a fresh agent answers "not overloaded" for the gap -- and, worse, an agent
// that can never read /proc/meminfo (a systemd hardening option that hides it,
// say) takes the `continue` below on every tick forever and goes on answering
// "not overloaded" for its whole life with nothing behind it. cork would
// record a healthy load for a box it has never actually measured.
//
// Counted in failed samples rather than elapsed time on purpose: a sampler
// starved of CPU is not running to count, so a genuinely loaded box cannot
// declare itself unmeasured just for being busy -- which is precisely when its
// reading matters most.
var haveVerdict atomic.Bool

// staleLimit is how many consecutive failed samples make the last verdict too
// old to serve. Five is 2.5 seconds at the sample interval, comfortably more
// than a blip and comfortably less than cork's tolerance for missed polls.
const staleLimit = 5

func main() {
	port := os.Getenv(portEnv)
	if port == "" {
		port = defaultPort
	}
	if _, err := strconv.Atoi(port); err != nil {
		log.Fatalf("invalid %s=%q: must be a port number", portEnv, port)
	}

	sustain, err := load.SustainFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("cpu must hold past a mark for %d samples (%s) before the verdict moves; memory moves at once",
		sustain, time.Duration(sustain)*load.SampleInterval)

	go sampler(sustain)

	http.HandleFunc("/health", healthHandler)

	addr := ":" + port
	log.Printf("telemetry listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// healthHandler answers the verdict, or refuses to answer at all.
//
// 503 rather than a cheerful default: cork counts a non-200 as a missed poll,
// which moves the worker's load axis to unknown. Unknown still takes
// placements -- a box whose agent is quiet is usually still serving -- but it
// is visible in worker-list, and it is what tells an autoscaler that a newly
// built box has not finished coming up. A false "not overloaded" would be
// indistinguishable from a healthy machine.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	if !haveVerdict.Load() {
		http.Error(w, "no sample yet", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"overloaded": overloaded.Load()})
}

// sampler periodically measures utilization and updates the overloaded verdict.
// CPU utilization is a delta between consecutive /proc/stat reads, so it keeps
// the previous sample across ticks.
func sampler(sustain int) {
	prev, err := load.ReadCPU()
	if err != nil {
		log.Fatalf("reading /proc/stat: %v", err)
	}

	state := load.State{Sustain: sustain}

	ticker := time.NewTicker(load.SampleInterval)
	defer ticker.Stop()

	misses := 0
	fail := func(format string, err error) {
		log.Printf(format, err)
		misses++
		if misses == staleLimit {
			// Once, on the way out: a box whose /proc has become unreadable
			// would otherwise log this every tick forever.
			log.Printf("no sample in %d ticks; reporting unavailable until one succeeds", misses)
		}
		if misses >= staleLimit {
			haveVerdict.Store(false)
		}
	}

	for range ticker.C {
		cur, err := load.ReadCPU()
		if err != nil {
			fail("reading /proc/stat: %v", err)
			continue
		}
		cpu := load.Util(prev, cur)
		prev = cur

		mem, err := load.ReadMemUsed()
		if err != nil {
			fail("reading /proc/meminfo: %v", err)
			continue
		}

		publish(&state, cpu, mem)
		misses = 0
	}
}

// publish records one sample: the verdict, and whether the verdict is worth
// serving.
//
// Split out of the loop above so that pairing is reachable from a test. The
// sampler is a ticker wrapped around /proc reads and nothing can get inside it,
// so with this inline the line that carries Settled through to the handler was
// the one line in the file no test could see -- and storing a bare true there
// instead, which is what this used to do, passed the whole suite.
func publish(state *load.State, cpu, mem float64) {
	overloaded.Store(state.Next(cpu, mem))
	haveVerdict.Store(state.Settled())
}
