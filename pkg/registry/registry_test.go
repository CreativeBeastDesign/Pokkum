package registry

import (
	"net/http"
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
