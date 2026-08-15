package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"
	"time"
)

// The tests in this file are the regression guard for the roadmap item "Fast
// Sub-Millisecond Supervisor Startup: Parallelize Bun child process fork/exec
// with the /healthz HTTP probe binding".
//
// The parallelization is a wiring contract in main.go: the child path is
// resolved up front (resolveChildPath -> WithChildPath) so start() forks
// immediately with no PATH walk, while the probe server binds its listener on
// its own goroutine. The two therefore overlap instead of serializing. These
// tests pin that contract - not wall-clock microseconds, which would be flaky
// on a laptop - by exercising the exact main.go wiring against a real child
// and asserting the deterministic consequences of the overlap.

// TestProbeServesWhileChildIsRunning exercises the whole startup wiring the way
// main.go does: a WithChildPath supervisor and a probe server bound to an
// OS-chosen ephemeral port, both running concurrently. It proves the probe
// server consults live supervisor state (so /healthz flips to 200 the moment
// the child is running) and that the supervisor keeps running while the probe
// answers - the overlap the optimization exists to create.
func TestProbeServesWhileChildIsRunning(t *testing.T) {
	cmd := helperCommand(t, "graceful")
	ready := helperReadyFile(t)

	cfg := testConfig(cmd, 30*time.Second)

	// main.go resolves the child path ahead of the fork and hands it to New.
	childPath, err := resolveChildPath(cfg.Command)
	if err != nil {
		t.Fatalf("resolveChildPath(%q): %v", cfg.Command, err)
	}

	sup := New(cfg, newTestLogger(&safeBuffer{}), WithChildPath(childPath))

	// Bind the probe listener on an ephemeral port, the pattern probe_test.go
	// establishes, so the test never guesses at a free port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding probe listener: %v", err)
	}
	defer ln.Close()
	probePort := ln.Addr().(*net.TCPAddr).Port

	p := NewProbeServer(sup, probePort, sup.AppPort(), nil)
	p.interval = 5 * time.Millisecond

	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		p.serve(context.Background(), ln)
	}()

	runDone := make(chan struct{})
	var code int
	var runErr error
	go func() {
		defer close(runDone)
		code, runErr = sup.Run(context.Background())
	}()

	// /healthz requires Started && Running (probe.go handleHealthz), so it only
	// flips to 200 once the child is actually running. Polling it to 200 over a
	// real socket proves the probe server serves live supervisor state while
	// the child runs - the seam main.go's overlap rides on.
	start := time.Now()
	waitHealthz(t, ln.Addr().String(), http.StatusOK, 10*time.Second)
	elapsed := time.Since(start)

	// The supervisor must still be in Run (child alive) while the probe
	// answered: that is the overlap. A supervisor that exited before the probe
	// served would prove nothing about concurrency.
	select {
	case <-runDone:
		t.Fatalf("supervisor exited (code=%d err=%v) while /healthz was still serving; no overlap",
			code, runErr)
	default:
	}
	st := sup.State()
	if !st.Running || st.PID == 0 {
		t.Fatalf("state = %+v during probe-served window, want running child", st)
	}

	t.Logf("overlap window: /healthz 200 %s after probe bind; child still running (pid %d)",
		elapsed.Round(time.Millisecond), st.PID)

	// Wait for the child's own signal handlers to be installed (the graceful
	// helper writes its ready file only after signal.Notify) so the forwarded
	// SIGTERM below is caught and answered with a clean exit 0, matching the
	// other signal tests. Without this the fork-to-Notify window would let the
	// default SIGTERM disposition kill the child and report 143, which is a
	// test artifact, not the graceful path.
	waitReady(t, ready, 10*time.Second)

	// Shut down through the real signal path and require a clean exit, then
	// stop the probe server and its readiness loop.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signalling the supervisor: %v", err)
	}
	select {
	case <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("supervisor did not exit after SIGTERM")
	}
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 after graceful SIGTERM", code)
	}

	p.Shutdown()
	select {
	case <-probeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("probe server did not stop after Shutdown")
	}
}

// TestResolveChildPathFailureMapsToStartCodes pins that a failed up-front
// resolution still produces the exact 127/126 exit codes main.go now exits
// with (the same mappings TestStartFailureExitCodes proves through start()).
func TestResolveChildPathFailureMapsToStartCodes(t *testing.T) {
	tests := []struct {
		name    string
		command []string
		want    int
	}{
		{"missing binary", []string{"/definitely/not/here"}, 127},
		{"not on PATH", []string{"pokkum-definitely-not-a-real-command"}, 127},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveChildPath(tc.command)
			if err == nil {
				t.Fatalf("resolveChildPath(%v) succeeded, want an error", tc.command)
			}
			if code := startExitCode(err); code != tc.want {
				t.Fatalf("startExitCode(%v) = %d, want %d (err %v)", err, code, tc.want, err)
			}
		})
	}
}

// waitHealthz polls the probe server over HTTP until it answers want, proving
// the server is bound and serving on the given address. It mirrors waitReadyz
// but drives a real socket (like TestServeAndShutdown) because the point here
// is the bound listener, not just the handler.
func waitHealthz(t *testing.T, addr string, want int, d time.Duration) int {
	t.Helper()
	url := "http://" + addr + "/healthz"
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			code := resp.StatusCode
			_ = resp.Body.Close()
			if code == want {
				return code
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s never answered within %s: %v", url, d, err)
	}
	defer resp.Body.Close()
	t.Fatalf("healthz = %d, want %d within %s", resp.StatusCode, want, d)
	return 0
}
