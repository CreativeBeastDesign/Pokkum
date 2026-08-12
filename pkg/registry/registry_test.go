package registry

import (
	"net/http"
	"sync/atomic"
	"testing"
)

func TestEphemeralRegistry_Lifecycle(t *testing.T) {
	srv, err := NewServer()
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}
	defer srv.Close()

	if srv.URL == "" {
		t.Errorf("expected non-empty URL")
	}

	if srv.Host() == "" {
		t.Errorf("expected non-empty Host")
	}

	if srv.Addr() == "" {
		t.Errorf("expected non-empty Addr")
	}

	if expected := srv.Host() + "/my-app"; srv.Repo("my-app") != expected {
		t.Errorf("expected Repo('my-app') = %q, got %q", expected, srv.Repo("my-app"))
	}

	if srv.HTTPServer() == nil {
		t.Errorf("expected non-nil HTTPServer")
	}

	// Verify ping v2 endpoint
	resp, err := http.Get(srv.URL + "/v2/")
	if err != nil {
		t.Fatalf("GET /v2/ failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected HTTP 200 OK from /v2/, got %d", resp.StatusCode)
	}
}

func TestEphemeralRegistry_WithHandlerWrapper(t *testing.T) {
	var count int64
	wrapper := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt64(&count, 1)
			next.ServeHTTP(w, r)
		})
	}

	srv, err := NewServer(WithHandlerWrapper(wrapper))
	if err != nil {
		t.Fatalf("NewServer with wrapper failed: %v", err)
	}
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v2/")
	if err != nil {
		t.Fatalf("GET /v2/ failed: %v", err)
	}
	defer resp.Body.Close()

	if atomic.LoadInt64(&count) != 1 {
		t.Errorf("expected wrapper count to be 1, got %d", atomic.LoadInt64(&count))
	}
}
