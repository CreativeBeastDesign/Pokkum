package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

// The tests run the supervisor in-process and use the test binary itself as the
// supervised child, re-executed with POKKUM_INIT_TEST_HELPER set. That is the
// only way to get a child whose signal behaviour is exactly specified - one
// that catches SIGTERM and exits cleanly, one that catches it and refuses to -
// without depending on which shell or coreutils the host happens to ship.
//
// One consequence deserves stating, because it is a real hazard rather than a
// detail: while a Supervisor is running it has signal.Notify on SIGCHLD and
// calls wait4(-1). It will therefore reap *any* child of the test process,
// including one started by unrelated test code. No test in this package may use
// exec.Cmd.Wait while a supervisor is live; the deliberately orphaned processes
// below are started with os.StartProcess and never waited for.
const (
	helperEnv      = "POKKUM_INIT_TEST_HELPER"
	helperReadyEnv = "POKKUM_INIT_TEST_READY"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		os.Exit(helperMain(mode))
	}
	os.Exit(m.Run())
}

// helperMain is the body of the re-executed child process.
func helperMain(mode string) int {
	// ready announces that signal handlers are installed. Without this
	// handshake a test could forward SIGTERM into the window between fork and
	// signal.Notify, where the default disposition still applies, and the
	// child would die of SIGTERM while the test was asserting that it caught
	// it. The file is written after Notify, never before.
	ready := func() {
		if p := os.Getenv(helperReadyEnv); p != "" {
			_ = os.WriteFile(p, []byte(strconv.Itoa(os.Getpid())), 0o600)
		}
	}

	switch mode {
	case "graceful":
		// The well-behaved case: catch the termination signal, exit 0 promptly.
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		ready()
		<-ch
		return 0

	case "ignore-term":
		// The Bun server's behaviour, distilled: handlers are installed for
		// every terminating signal and none of them ever exits. Only SIGKILL
		// ends this process.
		ch := make(chan os.Signal, 8)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT)
		go func() {
			for range ch {
			}
		}()
		ready()
		// A sleep loop rather than select{}: a goroutine blocked forever on a
		// bare channel can trip the runtime's deadlock detector, which would
		// exit 2 and look like a supervisor bug.
		for {
			time.Sleep(time.Hour)
		}

	case "record-usr1":
		// Catches SIGUSR1 and exits with a distinctive code, so a test can
		// prove that non-terminating signals are forwarded too and do not
		// start the shutdown clock.
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGUSR1)
		ready()
		<-ch
		return 21
	}
	return 99
}

// helperCommand returns the argv that re-executes this test binary in the given
// helper mode, and the path of the readiness file it will create.
func helperCommand(t *testing.T, mode string) []string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	ready := filepath.Join(t.TempDir(), "ready")
	// t.Setenv rather than a per-child env slice: the supervisor builds the
	// child environment from os.Environ() exactly as it will in production, so
	// this exercises childEnv on the real path. t.Setenv also restores the
	// variables afterwards and fails the test if it is parallel, which these
	// must not be - they send signals to the whole test process.
	t.Setenv(helperEnv, mode)
	t.Setenv(helperReadyEnv, ready)
	return []string{exe}
}

func helperReadyFile(t *testing.T) string {
	t.Helper()
	return os.Getenv(helperReadyEnv)
}

// testConfig returns a valid Config with the given command and shutdown window.
func testConfig(command []string, timeout time.Duration) Config {
	return Config{
		Command:         command,
		Port:            3000,
		ProbePort:       8081,
		ShutdownTimeout: timeout,
		LogLevel:        slog.LevelDebug,
	}
}

// harness runs a Supervisor on its own goroutine and captures its log as JSON
// so tests can assert on state transitions rather than on timing alone.
type harness struct {
	sup  *Supervisor
	logs *safeBuffer
	done chan struct{}
	code int
	err  error
}

func startSupervisor(t *testing.T, cfg Config) *harness {
	t.Helper()
	buf := &safeBuffer{}
	h := &harness{
		sup:  New(cfg, slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))),
		logs: buf,
		done: make(chan struct{}),
	}
	go func() {
		defer close(h.done)
		h.code, h.err = h.sup.Run(context.Background())
	}()
	t.Cleanup(func() {
		select {
		case <-h.done:
			return
		default:
		}
		// A test that failed early must not leave a supervisor and its child
		// running into the next test, where its wait4(-1) would steal statuses.
		if pid := h.sup.State().PID; pid > 0 {
			_ = signalGroup(pid, syscall.SIGKILL)
		}
		select {
		case <-h.done:
		case <-time.After(5 * time.Second):
			t.Errorf("supervisor did not stop during cleanup; logs:\n%s", buf.String())
		}
	})
	return h
}

// waitExit blocks until Run returns and reports its exit code.
func (h *harness) waitExit(t *testing.T, d time.Duration) int {
	t.Helper()
	select {
	case <-h.done:
		return h.code
	case <-time.After(d):
		t.Fatalf("supervisor still running after %s; logs:\n%s", d, h.logs.String())
		return 0
	}
}

// waitRunning blocks until the child is reported running and returns its pid.
func (h *harness) waitRunning(t *testing.T, d time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if st := h.sup.State(); st.Running && st.PID > 0 {
			return st.PID
		}
		select {
		case <-h.done:
			t.Fatalf("supervisor exited before the child was running: code=%d err=%v logs:\n%s",
				h.code, h.err, h.logs.String())
		default:
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("child never reported running within %s; logs:\n%s", d, h.logs.String())
	return 0
}

// waitReady blocks until the helper child has installed its signal handlers.
func waitReady(t *testing.T, path string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("helper child never became ready within %s", d)
}

// records parses the captured log into structured records.
func (h *harness) records() []map[string]any {
	var out []map[string]any
	for _, line := range bytes.Split([]byte(h.logs.String()), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err == nil {
			out = append(out, rec)
		}
	}
	return out
}

// awaitRecord waits for a log record matching pred, so tests synchronise on the
// state transition they care about instead of on a sleep.
func (h *harness) awaitRecord(t *testing.T, what string, d time.Duration, pred func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		for _, rec := range h.records() {
			if pred(rec) {
				return rec
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("no log record for %s within %s; logs:\n%s", what, d, h.logs.String())
	return nil
}

func msgIs(msg string) func(map[string]any) bool {
	return func(rec map[string]any) bool { return rec["msg"] == msg }
}

func (h *harness) assertLogged(t *testing.T, msg string) map[string]any {
	t.Helper()
	for _, rec := range h.records() {
		if rec["msg"] == msg {
			return rec
		}
	}
	t.Fatalf("expected a %q log record; logs:\n%s", msg, h.logs.String())
	return nil
}

func (h *harness) assertNotLogged(t *testing.T, msg string) {
	t.Helper()
	for _, rec := range h.records() {
		if rec["msg"] == msg {
			t.Fatalf("did not expect a %q log record; logs:\n%s", msg, h.logs.String())
		}
	}
}

// spawnOrphan starts a direct child of the *test* process that the supervisor
// never started and knows nothing about, and never waits for it. From the
// supervisor's point of view this is indistinguishable from a process the
// kernel reparented onto pid 1: it arrives as a SIGCHLD for an unknown pid.
func spawnOrphan(t *testing.T, code int) int {
	t.Helper()
	proc, err := os.StartProcess("/bin/sh", []string{"sh", "-c", "exit " + strconv.Itoa(code)}, &os.ProcAttr{
		Env:   os.Environ(),
		Files: []*os.File{nil, nil, nil},
	})
	if err != nil {
		t.Fatalf("spawning orphan: %v", err)
	}
	pid := proc.Pid
	// Release drops the handle without waiting, leaving the status for the
	// supervisor's reaper to collect. Calling Wait here would be the exact race
	// this package is written to avoid.
	_ = proc.Release()
	return pid
}

// safeBuffer is a bytes.Buffer that tolerates the supervise goroutine writing
// while the test goroutine reads.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
