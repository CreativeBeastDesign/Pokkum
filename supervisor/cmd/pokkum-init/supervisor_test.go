package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// TestSIGTERMForwardedToWellBehavedChild covers the ordinary rollout: a
// container runtime sends SIGTERM to pid 1, the supervisor relays it to the
// application, the application drains and exits 0, and the supervisor follows
// it out immediately rather than sitting on the graceful window.
//
// The shutdown timeout is deliberately the production default of 30s. If
// forwarding were broken, or if the supervisor waited for the deadline instead
// of reacting to SIGCHLD, this test would not merely fail - it would hang, and
// the elapsed-time assertion turns that into a diagnosis.
func TestSIGTERMForwardedToWellBehavedChild(t *testing.T) {
	cmd := helperCommand(t, "graceful")
	ready := helperReadyFile(t)

	h := startSupervisor(t, testConfig(cmd, 30*time.Second))
	pid := h.waitRunning(t, 10*time.Second)
	waitReady(t, ready, 10*time.Second)

	// The child must lead its own process group, or every signal we relay would
	// be delivered right back to the supervisor.
	if pgid, err := syscall.Getpgid(pid); err != nil {
		t.Fatalf("Getpgid(%d): %v", pid, err)
	} else if pgid != pid {
		t.Fatalf("child pgid = %d, want %d (its own pid); Setpgid did not take effect", pgid, pid)
	}
	if pgid, err := syscall.Getpgid(os.Getpid()); err == nil && pgid == pid {
		t.Fatalf("supervisor shares the child's process group %d; forwarded signals would loop back", pgid)
	}

	start := time.Now()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signalling the supervisor: %v", err)
	}

	if code := h.waitExit(t, 15*time.Second); code != 0 {
		t.Fatalf("exit code = %d, want 0; logs:\n%s", code, h.logs.String())
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("shutdown took %s with a 30s deadline; the supervisor waited for the clock "+
			"instead of reacting to the child's exit", elapsed)
	}

	h.assertLogged(t, "signal forwarded to child process group")
	h.assertLogged(t, "graceful shutdown deadline started")
	h.assertNotLogged(t, "graceful shutdown deadline exceeded; sending SIGKILL to child process group")

	if st := h.sup.State(); st.Running || !st.Started || st.ExitCode != 0 {
		t.Fatalf("final state = %+v, want started, not running, exit code 0", st)
	}
}

// TestChildIgnoringSIGTERMIsKilledAfterDeadline is the case this program exists
// for. The SvelteKit server installs a SIGTERM handler that stops the listener
// but never calls process.exit(), so a supervisor that only forwards and waits
// hangs until the runtime's own timeout on every rollout.
//
// The child here reproduces that exactly: it catches SIGTERM, SIGINT, SIGHUP
// and SIGQUIT and never exits for any of them. Only the escalation can end it,
// and the exit code proves which signal did.
func TestChildIgnoringSIGTERMIsKilledAfterDeadline(t *testing.T) {
	cmd := helperCommand(t, "ignore-term")
	ready := helperReadyFile(t)

	const timeout = 200 * time.Millisecond
	h := startSupervisor(t, testConfig(cmd, timeout))
	pid := h.waitRunning(t, 10*time.Second)
	waitReady(t, ready, 10*time.Second)

	start := time.Now()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signalling the supervisor: %v", err)
	}

	code := h.waitExit(t, 15*time.Second)
	elapsed := time.Since(start)

	if want := 128 + int(syscall.SIGKILL); code != want {
		t.Fatalf("exit code = %d, want %d (128+SIGKILL); the child was not killed by the escalation; logs:\n%s",
			code, want, h.logs.String())
	}
	if elapsed < timeout {
		t.Fatalf("child died after %s, before the %s deadline; the graceful window was not honoured", elapsed, timeout)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("child took %s to die with a %s deadline", elapsed, timeout)
	}

	h.assertLogged(t, "graceful shutdown deadline started")
	h.assertLogged(t, "graceful shutdown deadline exceeded; sending SIGKILL to child process group")

	if st := h.sup.State(); st.Signal != syscall.SIGKILL {
		t.Fatalf("state signal = %v, want SIGKILL", st.Signal)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("child %d still exists after the supervisor exited (kill(pid,0) = %v)", pid, err)
	}
}

// TestExitCodePropagationNormal checks that an ordinary non-zero exit reaches
// the caller unchanged. A container runtime reports this verbatim, so an
// application exiting 42 must not become 1.
func TestExitCodePropagationNormal(t *testing.T) {
	for _, want := range []int{0, 1, 42} {
		t.Run("exit_"+itoa(want), func(t *testing.T) {
			h := startSupervisor(t, testConfig([]string{"/bin/sh", "-c", "exit " + itoa(want)}, 30*time.Second))
			if code := h.waitExit(t, 15*time.Second); code != want {
				t.Fatalf("exit code = %d, want %d; logs:\n%s", code, want, h.logs.String())
			}
			if st := h.sup.State(); st.Running || st.ExitCode != want || st.Signal != 0 {
				t.Fatalf("final state = %+v, want exit code %d and no signal", st, want)
			}
		})
	}
}

// TestExitCodePropagationSignalled checks the 128+signum convention on a child
// with the default disposition. /bin/sleep is used rather than a Go helper
// because the Go runtime installs handlers of its own for several signals, and
// the point here is what an unmodified process does.
//
// The signal travels the full path - into the supervisor, out to the child's
// process group - so this also proves that forwarding reaches a child that is
// not a Go program and not cooperating.
func TestExitCodePropagationSignalled(t *testing.T) {
	h := startSupervisor(t, testConfig([]string{"sleep", "30"}, 30*time.Second))
	h.waitRunning(t, 10*time.Second)

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signalling the supervisor: %v", err)
	}

	code := h.waitExit(t, 15*time.Second)
	if want := 128 + int(syscall.SIGTERM); code != want {
		t.Fatalf("exit code = %d, want %d (128+SIGTERM); logs:\n%s", code, want, h.logs.String())
	}
	if st := h.sup.State(); st.Signal != syscall.SIGTERM {
		t.Fatalf("state signal = %v, want SIGTERM", st.Signal)
	}
}

// TestOrphanReapIsNotMistakenForChildExit is the subtle one.
//
// wait4(-1) returns whatever died, and in a real pod that includes processes
// the supervisor never started: anything the kernel reparented onto pid 1. If
// the reaper treated one of those as the tracked child exiting, a healthy
// container would tear itself down the first time a stray process was adopted -
// intermittently, under load, and impossible to reproduce from the logs.
//
// The orphan here is a direct child of the test process that the supervisor did
// not create. That is precisely what an adopted process looks like from inside
// the reaper: a SIGCHLD carrying an unknown pid. The assertions are that the
// supervisor collects it (so no zombie is left), that it says so, and that the
// tracked child is untouched and the supervisor still running afterwards.
func TestOrphanReapIsNotMistakenForChildExit(t *testing.T) {
	h := startSupervisor(t, testConfig([]string{"sleep", "30"}, 30*time.Second))
	childPID := h.waitRunning(t, 10*time.Second)

	const orphanExit = 3
	orphanPID := spawnOrphan(t, orphanExit)

	rec := h.awaitRecord(t, "the orphan reap", 15*time.Second, func(rec map[string]any) bool {
		if rec["msg"] != "reaped orphaned process" {
			return false
		}
		pid, ok := rec["pid"].(float64)
		return ok && int(pid) == orphanPID
	})
	if got, want := rec["status"], "exit "+itoa(orphanExit); got != want {
		t.Fatalf("orphan status = %v, want %q", got, want)
	}

	// wait4 returned the orphan, so its entry is gone from the process table. A
	// zombie would still answer kill(pid, 0) successfully.
	if err := syscall.Kill(orphanPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("orphan %d lingers after being reaped (kill(pid,0) = %v); it is still a zombie", orphanPID, err)
	}

	// The reap must not have looked like the application dying.
	select {
	case <-h.done:
		t.Fatalf("supervisor exited when an orphan was reaped: code=%d; logs:\n%s", h.code, h.logs.String())
	default:
	}
	st := h.sup.State()
	if !st.Running || st.PID != childPID || st.ShuttingDown {
		t.Fatalf("state after orphan reap = %+v, want the tracked child %d still running", st, childPID)
	}
	h.assertNotLogged(t, "child exited")

	// And the supervisor still works: the tracked child's own exit is still
	// recognised, through the same code path that just ignored the orphan.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signalling the supervisor: %v", err)
	}
	if code, want := h.waitExit(t, 15*time.Second), 128+int(syscall.SIGTERM); code != want {
		t.Fatalf("exit code = %d, want %d; logs:\n%s", code, want, h.logs.String())
	}
	if rec := h.assertLogged(t, "child exited"); rec["pid"].(float64) != float64(childPID) {
		t.Fatalf("child exited record names pid %v, want %d", rec["pid"], childPID)
	}
}

// TestMultipleOrphansDrainedFromOneSIGCHLD covers the reason the reaper is a
// loop. SIGCHLD is not queued: any number of children can die between two
// deliveries and the kernel raises the flag once. A supervisor that reaped one
// process per signal would accumulate zombies exactly when it was busiest.
func TestMultipleOrphansDrainedFromOneSIGCHLD(t *testing.T) {
	h := startSupervisor(t, testConfig([]string{"sleep", "30"}, 30*time.Second))
	h.waitRunning(t, 10*time.Second)

	const n = 8
	pids := make([]int, 0, n)
	for i := 0; i < n; i++ {
		pids = append(pids, spawnOrphan(t, 0))
	}

	for _, pid := range pids {
		h.awaitRecord(t, "reap of orphan "+itoa(pid), 20*time.Second, func(rec map[string]any) bool {
			if rec["msg"] != "reaped orphaned process" {
				return false
			}
			got, ok := rec["pid"].(float64)
			return ok && int(got) == pid
		})
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("orphan %d is still a zombie", pid)
		}
	}

	if st := h.sup.State(); !st.Running {
		t.Fatalf("tracked child stopped running after draining orphans: %+v", st)
	}
}

// TestNonTerminatingSignalIsForwardedWithoutShutdown checks that forwarding is
// not limited to the two signals that start the shutdown clock, and that a
// signal outside that set does not arm the deadline. An application using
// SIGUSR1 to rotate logs or dump state must be able to receive it without the
// supervisor deciding the pod is terminating.
func TestNonTerminatingSignalIsForwardedWithoutShutdown(t *testing.T) {
	cmd := helperCommand(t, "record-usr1")
	ready := helperReadyFile(t)

	h := startSupervisor(t, testConfig(cmd, 30*time.Second))
	h.waitRunning(t, 10*time.Second)
	waitReady(t, ready, 10*time.Second)

	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatalf("signalling the supervisor: %v", err)
	}

	// The helper exits 21 only if it actually received the forwarded SIGUSR1.
	if code := h.waitExit(t, 15*time.Second); code != 21 {
		t.Fatalf("exit code = %d, want 21; SIGUSR1 was not forwarded; logs:\n%s", code, h.logs.String())
	}
	h.assertNotLogged(t, "graceful shutdown deadline started")
}

// TestContextCancellationShutsDownGracefully checks the context path. It has to
// behave like an inbound SIGTERM - forward first, then start the clock - and in
// particular the deadline must not be derived from the cancelled context, which
// would make it expire instantly and turn every graceful stop into a SIGKILL.
func TestContextCancellationShutsDownGracefully(t *testing.T) {
	cmd := helperCommand(t, "graceful")
	ready := helperReadyFile(t)

	buf := &safeBuffer{}
	h := &harness{
		sup:  New(testConfig(cmd, 30*time.Second), newTestLogger(buf)),
		logs: buf,
		done: make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		defer close(h.done)
		h.code, h.err = h.sup.Run(ctx)
	}()

	h.waitRunning(t, 10*time.Second)
	waitReady(t, ready, 10*time.Second)

	cancel()
	if code := h.waitExit(t, 15*time.Second); code != 0 {
		t.Fatalf("exit code = %d, want 0; logs:\n%s", code, h.logs.String())
	}
	h.assertLogged(t, "shutdown requested by context cancellation")
	h.assertLogged(t, "signal forwarded to child process group")
	// The child caught SIGTERM and exited 0. If the deadline had inherited the
	// cancelled context it would have fired immediately and the code would be
	// 137 instead.
	h.assertNotLogged(t, "graceful shutdown deadline exceeded; sending SIGKILL to child process group")
}

// TestStartFailureExitCodes checks that a broken entrypoint is distinguishable
// from an application that started and then failed. 127 and 126 are what a
// shell would report, and what an operator reads them as.
func TestStartFailureExitCodes(t *testing.T) {
	dir := t.TempDir()
	notExecutable := dir + "/not-executable"
	if err := os.WriteFile(notExecutable, []byte("#!/nonexistent\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		command []string
		want    int
	}{
		{"missing binary", []string{dir + "/definitely-not-here"}, 127},
		{"not on PATH", []string{"pokkum-definitely-not-a-real-command"}, 127},
		{"not executable", []string{notExecutable}, 126},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sup := New(testConfig(tc.command, time.Second), newTestLogger(&safeBuffer{}))
			code, err := sup.Run(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			if code != tc.want {
				t.Fatalf("exit code = %d, want %d (err %v)", code, tc.want, err)
			}
			if st := sup.State(); st.Started || st.Running {
				t.Fatalf("state = %+v, want neither started nor running", st)
			}
		})
	}
}

// TestStateSeamForProbeServer pins the contract the probe server (W8) is going
// to build on, so that a later refactor of the state machine cannot quietly
// change what /healthz and /readyz report.
func TestStateSeamForProbeServer(t *testing.T) {
	cmd := helperCommand(t, "graceful")
	ready := helperReadyFile(t)

	cfg := testConfig(cmd, 30*time.Second)
	cfg.Port = 3100
	cfg.ProbePort = 8181

	buf := &safeBuffer{}
	sup := New(cfg, newTestLogger(buf))

	// Before Run: a probe server started first must see a coherent zero state,
	// not a panic and not a nil pointer.
	if st := sup.State(); st.Started || st.Running || st.PID != 0 {
		t.Fatalf("pre-start state = %+v, want the zero value", st)
	}
	if sup.ProbePort() != 8181 || sup.AppPort() != 3100 {
		t.Fatalf("ProbePort/AppPort = %d/%d, want 8181/3100", sup.ProbePort(), sup.AppPort())
	}

	// The probe server depends on the interface, not the concrete type.
	var ps ProcessState = sup
	h := &harness{sup: sup, logs: buf, done: make(chan struct{})}
	go func() {
		defer close(h.done)
		h.code, h.err = sup.Run(context.Background())
	}()

	h.waitRunning(t, 10*time.Second)
	waitReady(t, ready, 10*time.Second)

	st := ps.State()
	if !st.Started || !st.Running || st.ShuttingDown || st.PID == 0 || st.StartedAt.IsZero() {
		t.Fatalf("running state = %+v, want started, running, not shutting down", st)
	}

	// ShuttingDown has to flip as soon as the termination signal is handled,
	// because that is what takes the pod out of the load balancer. Observing it
	// requires a child that lingers, so this uses the ignore-term-style timing:
	// the graceful helper exits fast, so poll rather than assume.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	h.waitExit(t, 15*time.Second)

	final := ps.State()
	if final.Running || !final.Started || final.ExitedAt.IsZero() || final.ExitCode != 0 {
		t.Fatalf("final state = %+v, want started, stopped, exit code 0", final)
	}
	if !final.ExitedAt.After(final.StartedAt) && !final.ExitedAt.Equal(final.StartedAt) {
		t.Fatalf("ExitedAt %v precedes StartedAt %v", final.ExitedAt, final.StartedAt)
	}
}

// TestShuttingDownIsObservableDuringDrain checks the readiness half of the seam
// specifically: while the child is draining, State must report Running and
// ShuttingDown together, which is the combination /readyz turns into a failure
// while /healthz still passes.
func TestShuttingDownIsObservableDuringDrain(t *testing.T) {
	cmd := helperCommand(t, "ignore-term")
	ready := helperReadyFile(t)

	// Long enough to observe the draining state without making the suite wait
	// on it: the assertion below synchronises on the log record, not the clock.
	h := startSupervisor(t, testConfig(cmd, 500*time.Millisecond))
	h.waitRunning(t, 10*time.Second)
	waitReady(t, ready, 10*time.Second)

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	h.awaitRecord(t, "the shutdown deadline", 10*time.Second, msgIs("graceful shutdown deadline started"))

	st := h.sup.State()
	if !st.Running || !st.ShuttingDown {
		t.Fatalf("state during drain = %+v, want running and shutting down", st)
	}

	if code := h.waitExit(t, 20*time.Second); code != 128+int(syscall.SIGKILL) {
		t.Fatalf("exit code = %d, want %d", code, 128+int(syscall.SIGKILL))
	}
}

// TestChildEnvForcesPort checks that PORT is overridden rather than inherited.
// It is the one variable the image config, the probe configuration and the
// generated containerPort all have to agree on.
func TestChildEnvForcesPort(t *testing.T) {
	got := childEnv([]string{"PATH=/bin", "PORT=9999", "PORTAL=keep", "HOME=/root"}, Config{Port: 3000})

	var ports []string
	seen := map[string]bool{}
	for _, kv := range got {
		seen[kv] = true
		if len(kv) > 5 && kv[:5] == "PORT=" {
			ports = append(ports, kv)
		}
	}
	if len(ports) != 1 || ports[0] != "PORT=3000" {
		t.Fatalf("PORT entries = %v, want exactly [PORT=3000]", ports)
	}
	for _, want := range []string{"PATH=/bin", "PORTAL=keep", "HOME=/root"} {
		if !seen[want] {
			t.Fatalf("childEnv dropped %q; got %v", want, got)
		}
	}
}

// TestForwardableSignalSet guards the omissions in signals.go, each of which
// would be a live bug: forwarding SIGCHLD lies to the application about its own
// children, and notifying on SIGURG turns the supervise loop into a spin
// because the Go runtime uses it for preemption.
func TestForwardableSignalSet(t *testing.T) {
	forbidden := map[syscall.Signal]string{
		syscall.SIGCHLD: "handled internally; forwarding it would lie to the application about its own children",
		syscall.SIGKILL: "cannot be caught",
		syscall.SIGSTOP: "cannot be caught",
		syscall.SIGURG:  "used by the Go runtime for goroutine preemption",
		syscall.SIGPIPE: "has special meaning in the Go runtime",
		syscall.SIGSEGV: "a synchronous fault of this process, not a request to relay",
	}
	for _, sig := range forwardableSignals() {
		if why, bad := forbidden[sig.(syscall.Signal)]; bad {
			t.Errorf("%v must not be forwarded: %s", sig, why)
		}
	}

	required := []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGUSR2}
	for _, want := range required {
		found := false
		for _, sig := range forwardableSignals() {
			if sig == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%v must be forwarded", want)
		}
	}

	if !isTerminating(syscall.SIGTERM) || !isTerminating(syscall.SIGINT) {
		t.Error("SIGTERM and SIGINT must start the graceful shutdown clock")
	}
	if isTerminating(syscall.SIGUSR1) || isTerminating(syscall.SIGHUP) {
		t.Error("only SIGTERM and SIGINT may start the graceful shutdown clock")
	}
}

func newTestLogger(w *safeBuffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func itoa(n int) string { return strconv.Itoa(n) }
