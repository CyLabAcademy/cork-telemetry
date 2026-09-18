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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(state.Load())
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

		overloaded := ls.Next(cpu, mem)
		state.Store(&snapshot{
			Overloaded: overloaded,
			CPU:        cpu * 100,
			Mem:        mem * 100,
		})
		log.Printf("cpu=%.1f%% mem=%.1f%% overloaded=%t", cpu*100, mem*100, overloaded)
	}
}
