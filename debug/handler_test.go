package main

import (
	"encoding/json"
	"github.com/CyLabAcademy/cork-telemetry/internal/load"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The debug build is a superset: it must still answer the `overloaded` field
// cork reads, so it can be dropped in place of the production binary while
// someone watches the numbers, and it adds the raw percentages behind it.
func TestDebugHealthHandlerIsASupersetOfTheContract(t *testing.T) {
	haveVerdict.Store(true)
	state.Store(&snapshot{Overloaded: true, CPU: 12.5, Mem: 34.25})

	rec := httptest.NewRecorder()
	healthHandler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type %q, want application/json", ct)
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not JSON: %s (%s)", err, rec.Body.String())
	}
	if got["overloaded"] != true {
		t.Fatalf("overloaded = %v, want true: the debug build must still answer the field cork reads", got["overloaded"])
	}
	if got["cpu"] != 12.5 || got["mem"] != 34.25 {
		t.Fatalf("cpu/mem = %v/%v, want 12.5/34.25", got["cpu"], got["mem"])
	}
}

// The debug build refuses on the same terms, so that swapping it in does not
// change what cork sees during startup.
func TestDebugHealthIsRefusedUntilSomethingHasBeenSampled(t *testing.T) {
	haveVerdict.Store(false)
	t.Cleanup(func() { haveVerdict.Store(true) })

	rec := httptest.NewRecorder()
	healthHandler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d before the first sample, want 503", rec.Code)
	}
}

// Same wiring check as the production binary, because the two must not
// disagree about when a verdict is worth serving: a debug build that
// green-lights a box the real one refuses is worse than no debug build.
func TestDebugHandlerServesWhatTheSamplerPublished(t *testing.T) {
	ls := load.State{Sustain: 4}
	t.Cleanup(func() { haveVerdict.Store(true) })

	status := func() int {
		rec := httptest.NewRecorder()
		healthHandler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		return rec.Code
	}

	publish(&ls, 0.99, 0.10)
	if got := status(); got != http.StatusServiceUnavailable {
		t.Fatalf("status %d one sample into a climb, want 503", got)
	}
	for i := 0; i < 3; i++ {
		publish(&ls, 0.99, 0.10)
	}
	if got := status(); got != http.StatusOK {
		t.Fatalf("status %d after the trip, want 200", got)
	}
	if snap := state.Load(); snap == nil || !snap.Overloaded {
		t.Fatalf("snapshot %+v does not carry the trip", snap)
	}
}
