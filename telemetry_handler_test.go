package main

import (
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
