package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestProbeHandler(t *testing.T) {
	var shuttingDown atomic.Bool
	h := probeHandler(&shuttingDown)
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
	}
}

func TestProbeHandler_ReadyzReportsNotReadyDuringShutdown(t *testing.T) {
	var shuttingDown atomic.Bool
	h := probeHandler(&shuttingDown)
	shuttingDown.Store(true)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET /readyz during shutdown = %d, want 503", rec.Code)
	}

	// Liveness must stay unaffected by shutdown state.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /healthz during shutdown = %d, want 200", rec.Code)
	}
}
