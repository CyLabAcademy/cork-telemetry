package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The debug build is a superset: it must still answer the `overloaded` field
// cork reads, so it can be dropped in place of the production binary while
// someone watches the numbers, and it adds the raw percentages behind it.
func TestDebugHealthHandlerIsASupersetOfTheContract(t *testing.T) {
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
