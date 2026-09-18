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

func healthHandler(w http.ResponseWriter, r *http.Request) {
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

	for range ticker.C {
		cur, err := load.ReadCPU()
		if err != nil {
			log.Printf("reading /proc/stat: %v", err)
			continue
		}
		cpu := load.Util(prev, cur)
		prev = cur

		mem, err := load.ReadMemUsed()
		if err != nil {
			log.Printf("reading /proc/meminfo: %v", err)
			continue
		}

		overloaded.Store(state.Next(cpu, mem))
	}
}
