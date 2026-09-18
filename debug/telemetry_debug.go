// Command telemetry_debug is a debug build of the telemetry agent.
//
// It behaves exactly like the production telemetry agent (same /proc sampling,
// same rule, same TELEMETRY_PORT/2136 config) but its /health response also
// exposes the raw CPU and memory percentages behind the verdict:
//
//	{"overloaded": false, "cpu": 12.3, "mem": 34.5}
//
// Use it to watch the live numbers while tuning. The production binary keeps the
// strict {"overloaded": bool} contract.
//
// "Exactly like" is structural rather than a promise kept by hand: both
// binaries take the sampling and the verdict from internal/load, so this one
// cannot drift into judging load differently from the binary being debugged.
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

// snapshot is the latest sampled state. The debug endpoint reports all of it.
type snapshot struct {
	Overloaded bool    `json:"overloaded"`
	CPU        float64 `json:"cpu"` // percent, 0-100
	Mem        float64 `json:"mem"` // percent, 0-100
}

// state holds the latest snapshot. It is written only by the sampler goroutine
// and read by HTTP handlers, so an atomic pointer is sufficient.
var state atomic.Pointer[snapshot]

// haveVerdict reports whether that snapshot is worth anything. Identical to the
// production binary's, and for the same reason: the listener binds before the
// first sample exists, and an agent that can never read /proc would otherwise
// serve a zero snapshot -- "not overloaded", 0% cpu, 0% mem -- indefinitely.
var haveVerdict atomic.Bool

// staleLimit is how many consecutive failed samples make the snapshot too old
// to serve. Kept identical to the production binary.
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

	state.Store(&snapshot{})

	go sampler(sustain)

	http.HandleFunc("/health", healthHandler)

	addr := ":" + port
	log.Printf("telemetry (debug) listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if !haveVerdict.Load() {
		http.Error(w, "no sample yet", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(state.Load())
}

// publish records one sample and returns the verdict. Split out for the same
// reason as the production binary's: the line carrying Settled through to the
// handler is otherwise inside a ticker no test can reach.
func publish(ls *load.State, cpu, mem float64) bool {
	overloaded := ls.Next(cpu, mem)
	state.Store(&snapshot{
		Overloaded: overloaded,
		CPU:        cpu * 100,
		Mem:        mem * 100,
	})
	haveVerdict.Store(ls.Settled())
	return overloaded
}

// sampler periodically measures utilization, updates the verdict, and logs the
// numbers behind it.
func sampler(sustain int) {
	prev, err := load.ReadCPU()
	if err != nil {
		log.Fatalf("reading /proc/stat: %v", err)
	}

	ls := load.State{Sustain: sustain}

	ticker := time.NewTicker(load.SampleInterval)
	defer ticker.Stop()

	misses := 0
	fail := func(format string, err error) {
		log.Printf(format, err)
		misses++
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

		overloaded := publish(&ls, cpu, mem)
		misses = 0
		log.Printf("cpu=%.1f%% mem=%.1f%% overloaded=%t", cpu*100, mem*100, overloaded)
	}
}
