package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// fakeState is a ProcessState whose snapshot a test can change at will, so
// /healthz and /readyz can be driven through every combination of
// Started/Running/ShuttingDown without a real child process. Its own
// concurrency story mirrors Supervisor.state: an atomic pointer to an
// immutable snapshot, safe to read from the handler goroutines started by
// net/http while a test goroutine calls set.
type fakeState struct {
	st atomic.Pointer[State]
}

func newFakeState(st State) *fakeState {
	f := &fakeState{}
	f.set(st)
	return f
}

func (f *fakeState) State() State { return *f.st.Load() }
func (f *fakeState) set(st State) { f.st.Store(&st) }

var _ ProcessState = (*fakeState)(nil)

// doRequest exercises the probe server's handler directly, without opening a
// socket for the probe port itself. handleReadyz still performs a real TCP
// dial against p.appPort when the readiness loop ticks, so this is not
// cutting the corner the tests exist to cover - it only removes the
// irrelevant network hop between the test and the probe server's own mux.
func doRequest(t *testing.T, p *ProbeServer, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	p.srv.Handler.ServeHTTP(rec, req)
	return rec
}

// waitReadyz polls /readyz until it reports want or the deadline passes. The
// readiness loop updates its cache asynchronously on a ticker, so tests
// synchronise on the observable HTTP response rather than sleeping for a
// guessed number of intervals.
func waitReadyz(t *testing.T, p *ProbeServer, want int, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if rec := doRequest(t, p, "/readyz"); rec.Code == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("readyz did not reach %d within %s; last = %d", want, d, doRequest(t, p, "/readyz").Code)
}

// reservePort hands back a port number nothing is currently listening on, by
// binding to an OS-chosen ephemeral port and immediately releasing it. Good
// enough for a fast local test suite; it is not immune to another process
// grabbing the same port in the window before the caller rebinds it.
func reservePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("releasing reserved port: %v", err)
	}
	return port
}

// TestHealthzLiveChildThenDead covers case 1: /healthz must answer from the
// supervisor's own bookkeeping of the child, not from the application port.
// It reuses the real child-process harness from helper_test.go rather than a
// fake, so this is the same "graceful" helper and startSupervisor/waitRunning
// machinery TestStateSeamForProbeServer itself builds on.
func TestHealthzLiveChildThenDead(t *testing.T) {
	cmd := helperCommand(t, "graceful")
	ready := helperReadyFile(t)

	h := startSupervisor(t, testConfig(cmd, 2*time.Second))
	h.waitRunning(t, 10*time.Second)
	waitReady(t, ready, 10*time.Second)

	p := NewProbeServer(h.sup, h.sup.ProbePort(), h.sup.AppPort(), nil)

	if rec := doRequest(t, p, "/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("healthz with live child = %d, want %d", rec.Code, http.StatusOK)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	h.waitExit(t, 10*time.Second)

	if rec := doRequest(t, p, "/healthz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("healthz after child exited = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestHealthzBeforeStart covers the pre-start half of the seam: a probe
// server built before the child ever starts must report unhealthy, not panic
// on a nil snapshot.
func TestHealthzBeforeStart(t *testing.T) {
	state := newFakeState(State{})
	p := NewProbeServer(state, 0, 0, nil)

	if rec := doRequest(t, p, "/healthz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("healthz before start = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// TestReadyzTracksAppPortListener covers cases 2 and 3: /readyz must be 503
// before anything listens on the application port, flip to 200 once a
// listener is up, and flip back to 503 when that listener disappears. The
// last transition is the anti-latch case the spec calls out explicitly - a
// readiness probe that only ever proves true is worse than none.
func TestReadyzTracksAppPortListener(t *testing.T) {
	appPort := reservePort(t)

	state := newFakeState(State{Started: true, Running: true})
	p := NewProbeServer(state, 0, appPort, nil)
	p.interval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.runReadinessLoop(ctx)

	waitReadyz(t, p, http.StatusServiceUnavailable, time.Second)

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(appPort)))
	if err != nil {
		t.Fatalf("listening on reserved app port: %v", err)
	}
	waitReadyz(t, p, http.StatusOK, time.Second)

	if err := ln.Close(); err != nil {
		t.Fatalf("closing app port listener: %v", err)
	}
	waitReadyz(t, p, http.StatusServiceUnavailable, time.Second)
}

// TestReadyzDuringShutdown covers case 4: once the supervisor is draining,
// /readyz must report 503 immediately - not on the next ticker tick - while
// /healthz keeps reporting the child as healthy, because killing a pod
// mid-drain is exactly wrong.
func TestReadyzDuringShutdown(t *testing.T) {
	appPort := reservePort(t)
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(appPort)))
	if err != nil {
		t.Fatalf("listening on reserved app port: %v", err)
	}
	defer ln.Close()

	state := newFakeState(State{Started: true, Running: true})
	p := NewProbeServer(state, 0, appPort, nil)
	p.interval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.runReadinessLoop(ctx)

	// Establish that readiness is genuinely true first, so the assertion below
	// proves ShuttingDown overrides a positive cache rather than merely
	// agreeing with an already-negative one.
	waitReadyz(t, p, http.StatusOK, time.Second)

	state.set(State{Started: true, Running: true, ShuttingDown: true})

	if rec := doRequest(t, p, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz during shutdown = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if rec := doRequest(t, p, "/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("healthz during shutdown = %d, want %d", rec.Code, http.StatusOK)
	}
}

// TestUnknownPathReturns404 covers case 5.
func TestUnknownPathReturns404(t *testing.T) {
	state := newFakeState(State{})
	p := NewProbeServer(state, 0, 0, nil)

	if rec := doRequest(t, p, "/nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestServeAndShutdown is the one end-to-end test in this file: it drives
// ProbeServer.serve against a real listener with a real net/http.Client,
// proving the wiring in main.go actually serves traffic and that Shutdown
// makes serve return - which is also what stops the readiness loop, since it
// runs on a context derived from serve's own and cancelled in its defer.
func TestServeAndShutdown(t *testing.T) {
	state := newFakeState(State{Started: true, Running: true})
	p := NewProbeServer(state, 0, 0, nil)
	p.interval = 5 * time.Millisecond

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		p.serve(context.Background(), ln)
	}()

	url := "http://" + ln.Addr().String() + "/healthz"
	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	for time.Now().Before(deadline) {
		resp, err = http.Get(url)
		if err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	_ = resp.Body.Close()

	p.Shutdown()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not return after Shutdown")
	}
}
