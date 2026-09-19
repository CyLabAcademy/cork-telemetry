package main

import (
	"github.com/CyLabAcademy/cork-telemetry/internal/load"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The response body is a contract with cork, which polls this endpoint and
// parses it to decide placement. The shape is deliberately minimal and must not
// drift: cork reads the `overloaded` field and nothing else, and a body it
// cannot parse counts as a missed poll.
func TestHealthHandlerContract(t *testing.T) {
	haveVerdict.Store(true)
	for _, over := range []bool{false, true} {
		overloaded.Store(over)

		rec := httptest.NewRecorder()
		healthHandler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

		if rec.Code != http.StatusOK {
			t.Fatalf("status %d, want 200: cork treats anything else as a missed poll", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("Content-Type %q, want application/json", ct)
		}
		want := `{"overloaded":false}`
		if over {
			want = `{"overloaded":true}`
		}
		if got := strings.TrimSpace(rec.Body.String()); got != want {
			t.Fatalf("body %s, want %s", got, want)
		}
	}
}

// The agent must not answer until it has something to answer with.
//
// The listener binds before the sampler has taken a sample, and `overloaded`
// is an atomic.Bool whose zero value reads false -- so without this the agent
// would report a healthy box during startup, and would go on reporting one
// forever if it could never read /proc at all. cork counts a non-200 as a
// missed poll and moves the load axis to unknown, which is visible; a false
// "not overloaded" is indistinguishable from a measured one.
func TestHealthIsRefusedUntilSomethingHasBeenSampled(t *testing.T) {
	haveVerdict.Store(false)
	t.Cleanup(func() { haveVerdict.Store(true) })

	rec := httptest.NewRecorder()
	healthHandler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d before the first sample, want 503: a fresh agent must not claim the box is fine", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "overloaded") {
		t.Fatalf("body %q carries a verdict the agent does not have", rec.Body.String())
	}
}

// The wiring, not the rule: that what the sampler publishes is what the handler
// serves. Driven through publish() rather than by setting the flag by hand,
// because the flag being set by hand is exactly what hid this -- storing a bare
// `true` there passed every other test in this package.
func TestTheHandlerServesWhatTheSamplerPublished(t *testing.T) {
	state := load.State{Sustain: 4}
	t.Cleanup(func() { haveVerdict.Store(true) })

	status := func() int {
		rec := httptest.NewRecorder()
		healthHandler(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
		return rec.Code
	}

	// Climbing towards a trip: the agent cannot yet tell a box pegged for an
	// hour from one busy for half a second, and must not guess.
	publish(&state, 0.99, 0.10)
	if got := status(); got != http.StatusServiceUnavailable {
		t.Fatalf("status %d one sample into a climb, want 503: the agent would green-light a box it has not measured", got)
	}

	// The run completes and the verdict is real.
	for i := 0; i < 3; i++ {
		publish(&state, 0.99, 0.10)
	}
	if got := status(); got != http.StatusOK {
		t.Fatalf("status %d after the trip, want 200", got)
	}
	if !overloaded.Load() {
		t.Fatal("the trip was published but the served verdict is not overloaded")
	}

	// Memory never waits behind the CPU window, even mid-climb.
	fresh := load.State{Sustain: 8}
	publish(&fresh, 0.99, 0.95)
	if got := status(); got != http.StatusOK {
		t.Fatalf("status %d for a box out of memory while CPU was still climbing, want 200: memory is the axis that must never wait", got)
	}
	if !overloaded.Load() {
		t.Fatal("a box over the memory mark was not served as overloaded")
	}
}
