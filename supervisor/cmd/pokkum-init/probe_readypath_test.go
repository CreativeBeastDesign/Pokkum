package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// readyApp is a stand-in application on 127.0.0.1 whose readiness endpoint
// answers whatever status the test sets, and counts the requests it gets.
type readyApp struct {
	srv    *httptest.Server
	status atomic.Int32
	hits   atomic.Int32
	paths  atomic.Value // last request path
}

func newReadyApp(t *testing.T, status int, handler http.HandlerFunc) *readyApp {
	t.Helper()
	a := &readyApp{}
	a.status.Store(int32(status))
	if handler == nil {
		handler = func(w http.ResponseWriter, r *http.Request) {
			a.hits.Add(1)
			a.paths.Store(r.URL.Path)
			w.WriteHeader(int(a.status.Load()))
			_, _ = w.Write([]byte("db: unreachable"))
		}
	}
	a.srv = httptest.NewServer(handler)
	t.Cleanup(a.srv.Close)
	return a
}

func (a *readyApp) port(t *testing.T) int {
	t.Helper()
	return a.srv.Listener.Addr().(*net.TCPAddr).Port
}

func startReadyLoop(t *testing.T, p *ProbeServer, interval time.Duration) {
	t.Helper()
	p.interval = interval
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go p.runReadinessLoop(ctx)
}

// TestReadyzFollowsAppReadyEndpoint is the feature: with a ready path, the
// app's own answer decides /readyz, in both directions. The listener is up
// the whole time, so a TCP-connect check would say "ready" throughout; the
// 503 phases are what only the HTTP check can see.
func TestReadyzFollowsAppReadyEndpoint(t *testing.T) {
	app := newReadyApp(t, http.StatusServiceUnavailable, nil)
	p := NewProbeServer(newFakeState(State{Started: true, Running: true}), 0, app.port(t), nil)
	p.UseReadyPath("/api/ready")
	startReadyLoop(t, p, 5*time.Millisecond)

	waitReadyz(t, p, http.StatusServiceUnavailable, time.Second)
	app.status.Store(http.StatusOK)
	waitReadyz(t, p, http.StatusOK, time.Second)
	app.status.Store(http.StatusServiceUnavailable)
	waitReadyz(t, p, http.StatusServiceUnavailable, time.Second)

	if got, _ := app.paths.Load().(string); got != "/api/ready" {
		t.Errorf("readiness request went to %q, want /api/ready", got)
	}
}

// TestReadyzWithoutReadyPathIsUnchanged pins the default: an image built with
// no ready path never sends the app an HTTP request, and a listening port is
// ready even if every HTTP answer would be a 503.
func TestReadyzWithoutReadyPathIsUnchanged(t *testing.T) {
	app := newReadyApp(t, http.StatusServiceUnavailable, nil)
	p := NewProbeServer(newFakeState(State{Started: true, Running: true}), 0, app.port(t), nil)
	startReadyLoop(t, p, 5*time.Millisecond)

	waitReadyz(t, p, http.StatusOK, time.Second)
	time.Sleep(30 * time.Millisecond)
	if n := app.hits.Load(); n != 0 {
		t.Errorf("app received %d HTTP requests with no ready path configured, want 0", n)
	}
}

// A redirect is not followed: an auth hook sending /api/ready to a login page
// that answers 200 must not read as ready.
func TestReadyzDoesNotFollowRedirects(t *testing.T) {
	app := newReadyApp(t, 0, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	})
	p := NewProbeServer(newFakeState(State{Started: true, Running: true}), 0, app.port(t), nil)
	p.UseReadyPath("/api/ready")
	ready, detail := p.checkReadyPath(context.Background())
	if ready || !strings.Contains(detail, "303") {
		t.Errorf("redirect to a 200 page: ready=%v detail=%q, want not ready naming 303", ready, detail)
	}
}

// A readiness endpoint that does not answer within the request timeout is
// not ready, rather than a tick that blocks the loop.
func TestReadyzSlowEndpointIsNotReady(t *testing.T) {
	release := make(chan struct{})
	app := newReadyApp(t, 0, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	defer close(release)
	p := NewProbeServer(newFakeState(State{Started: true, Running: true}), 0, app.port(t), nil)
	p.UseReadyPath("/api/ready")

	start := time.Now()
	ready, _ := p.checkReadyPath(context.Background())
	if ready {
		t.Error("a readiness endpoint that never answered was reported ready")
	}
	if took := time.Since(start); took > readyRequestTimeout+500*time.Millisecond {
		t.Errorf("check took %s, want it bounded by readyRequestTimeout (%s)", took, readyRequestTimeout)
	}
}

// Probe traffic never reaches the app: /readyz answers from the cached result,
// so a burst of kubelet probes costs the app (and the database its endpoint
// checks) nothing beyond the loop's own one request per tick.
func TestReadyzProbesDoNotMultiplyAppRequests(t *testing.T) {
	app := newReadyApp(t, http.StatusOK, nil)
	p := NewProbeServer(newFakeState(State{Started: true, Running: true}), 0, app.port(t), nil)
	p.UseReadyPath("/api/ready")
	startReadyLoop(t, p, time.Hour) // only the immediate first check runs

	waitReadyz(t, p, http.StatusOK, time.Second)
	for range 50 {
		doRequest(t, p, "/readyz")
	}
	if n := app.hits.Load(); n != 1 {
		t.Errorf("app received %d readiness requests for 50 probes, want 1", n)
	}
}

func TestParseConfigReadyPath(t *testing.T) {
	for _, tc := range []struct {
		raw, want string
		warns     bool
	}{
		{"", "", false},
		{"/api/ready", "/api/ready", false},
		{"  /app/api/ready?deep=1 ", "/app/api/ready?deep=1", false},
		{"api/ready", "", true},            // not absolute
		{"//evil.example/ready", "", true}, // protocol-relative: another host
		{"/api/ready#x", "", true},
		{"/api/re ady", "", true},
		{"http://127.0.0.1/ready", "", true},
	} {
		env := map[string]string{envReadyPath: tc.raw}
		cfg, warnings, err := parseConfig([]string{"--", "true"}, func(k string) string { return env[k] }, &strings.Builder{})
		if err != nil {
			t.Fatalf("%q: parseConfig: %v", tc.raw, err)
		}
		if cfg.ReadyPath != tc.want {
			t.Errorf("%q: ReadyPath = %q, want %q", tc.raw, cfg.ReadyPath, tc.want)
		}
		warned := false
		for _, w := range warnings {
			if strings.Contains(w, envReadyPath) {
				warned = true
			}
		}
		if warned != tc.warns {
			t.Errorf("%q: warned = %v, want %v (%v)", tc.raw, warned, tc.warns, warnings)
		}
	}
}
