package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// This file is a real-listener regression test for the bug where neither
// http.Server literal in main.go set Addr, so http.Server.ListenAndServe
// fell back to its zero-value default (":http", port 80) and silently
// ignored the configured PORT / POKKUM_PROBE_PORT entirely — both servers
// then raced to bind port 80, and Kubernetes probes/Services targeting the
// documented ports never connected to anything.
//
// TestProbeHandler and friends in main_test.go exercise probeHandler
// exclusively through httptest, which supplies its own in-memory listener
// and never calls ListenAndServe or reads Addr — that test shape structurally
// cannot catch this bug no matter how thorough it is. The tests below instead
// build the real production *http.Server values via newContentServer /
// newProbeServer, start them with the real ListenAndServe call main() uses,
// and make a real TCP connection to the address they were configured with.

// freeTCPPort returns a currently unused TCP port on loopback by opening a
// listener and immediately closing it, so the caller can rebind that same
// port number. There is an inherent, accepted race (another process could
// grab the port in between) — the standard Go idiom for an ephemeral test
// port, safe enough for CI.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freeTCPPort: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// waitForListenerOrFail polls addr with real TCP dials until one succeeds,
// fails fast if errc reports a startup error (e.g. permission denied binding
// a privileged port), or fails the test once timeout elapses. A successful
// dial is closed immediately; it only proves the address is accepting
// connections.
func waitForListenerOrFail(t *testing.T, addr string, errc <-chan error, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-errc:
			t.Fatalf("server failed to start: %v", err)
		case <-deadline:
			t.Fatalf("timed out waiting for %s to accept connections", addr)
		case <-ticker.C:
			conn, dialErr := net.DialTimeout("tcp", addr, 200*time.Millisecond)
			if dialErr == nil {
				conn.Close()
				return
			}
		}
	}
}

// serveInBackground starts srv's real ListenAndServe path in a goroutine,
// reporting any non-graceful error on the returned channel, and registers a
// t.Cleanup that shuts it down.
func serveInBackground(t *testing.T, srv *http.Server) <-chan error {
	t.Helper()
	errc := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return errc
}

// TestNewContentServer_BindsConfiguredPort_TwoPortMode covers the two-port
// case (content and probe on distinct, separately configured ports), which
// is the default runtime shape (PORT=3000, POKKUM_PROBE_PORT=8081).
func TestNewContentServer_BindsConfiguredPort_TwoPortMode(t *testing.T) {
	cfg := Config{
		Port:      freeTCPPort(t),
		ProbePort: freeTCPPort(t),
	}

	var shuttingDown atomic.Bool
	content := newContentServer(cfg, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	probe := newProbeServer(cfg, &shuttingDown)

	wantContentAddr := net.JoinHostPort("", strconv.Itoa(cfg.Port))
	wantProbeAddr := net.JoinHostPort("", strconv.Itoa(cfg.ProbePort))
	if content.Addr != wantContentAddr {
		t.Fatalf("content.Addr = %q, want %q", content.Addr, wantContentAddr)
	}
	if probe.Addr != wantProbeAddr {
		t.Fatalf("probe.Addr = %q, want %q", probe.Addr, wantProbeAddr)
	}

	contentErrc := serveInBackground(t, content)
	probeErrc := serveInBackground(t, probe)

	contentAddr := fmt.Sprintf("127.0.0.1:%d", cfg.Port)
	probeAddr := fmt.Sprintf("127.0.0.1:%d", cfg.ProbePort)
	waitForListenerOrFail(t, contentAddr, contentErrc, 2*time.Second)
	waitForListenerOrFail(t, probeAddr, probeErrc, 2*time.Second)

	// Prove each server answers on ITS OWN configured port, not the other's
	// (and, crucially, not port 80 — the bug this test exists to catch).
	resp, err := http.Get("http://" + contentAddr + "/")
	if err != nil {
		t.Fatalf("GET content server on its configured port: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("content server GET / = %d, want 200", resp.StatusCode)
	}

	resp, err = http.Get("http://" + probeAddr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz on configured probe port: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("probe server GET /healthz = %d, want 200", resp.StatusCode)
	}
}

// TestNewContentServer_BindsConfiguredPort_SinglePortMode exercises
// newContentServer directly with an equal-port Config (PORT ==
// POKKUM_PROBE_PORT), bypassing parseConfig/validate() — which now rejects
// that combination outright before main() ever constructs a server (see
// config.go's validate() and config_test.go's
// TestParseConfig_RejectsEqualPorts). This test still earns its keep at the
// lower level: it proves the Addr-binding fix (newContentServer's explicit
// net.JoinHostPort) holds for any Config value, independent of whatever the
// config layer does or doesn't allow above it, rather than silently falling
// back to port 80.
func TestNewContentServer_BindsConfiguredPort_SinglePortMode(t *testing.T) {
	port := freeTCPPort(t)
	cfg := Config{Port: port, ProbePort: port}

	content := newContentServer(cfg, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	wantAddr := net.JoinHostPort("", strconv.Itoa(port))
	if content.Addr != wantAddr {
		t.Fatalf("content.Addr = %q, want %q", content.Addr, wantAddr)
	}

	errc := serveInBackground(t, content)

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	waitForListenerOrFail(t, addr, errc, 2*time.Second)

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET content server on its configured single port: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("content server GET / = %d, want 200", resp.StatusCode)
	}
}
