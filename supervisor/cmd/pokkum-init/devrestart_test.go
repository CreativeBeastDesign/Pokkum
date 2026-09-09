package main

import (
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// waitNewPID blocks until the supervisor reports a running child whose pid is
// not oldPID, which is the only externally visible proof that the child was
// genuinely replaced rather than merely signalled.
func waitNewPID(t *testing.T, h *harness, oldPID int, d time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if st := h.sup.State(); st.Running && st.PID > 0 && st.PID != oldPID {
			return st.PID
		}
		select {
		case <-h.done:
			t.Fatalf("supervisor exited instead of restarting: code=%d err=%v logs:\n%s", h.code, h.err, h.logs.String())
		default:
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("child was never replaced within %s; logs:\n%s", d, h.logs.String())
	return 0
}

// requestRestart deletes the readiness handshake file, sends SIGHUP to the
// test process (which is where the supervisor's own signal.Notify is
// installed), and waits for a genuinely new child that has finished
// installing its signal handlers.
//
// Waiting for the handshake, not merely for a new pid, is load-bearing: a
// replacement child exists from the moment os.StartProcess returns, but there
// is a window before its signal.Notify runs in which the default disposition
// still applies. A test that sent the next signal inside that window would
// see the child killed by it rather than handling it, and would report a
// restart bug that is really a test race.
func requestRestart(t *testing.T, h *harness, oldPID int) int {
	t.Helper()
	ready := helperReadyFile(t)
	if err := os.Remove(ready); err != nil && !os.IsNotExist(err) {
		t.Fatalf("clear readiness file: %v", err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}
	next := waitNewPID(t, h, oldPID, 10*time.Second)
	waitReady(t, ready, 10*time.Second)
	return next
}

// TestDevMode_SIGHUPRestartsChild is the core of the in-pod hot swap: with
// POKKUM_DEV_MODE on, SIGHUP must stop the running application and start a
// fresh one in its place, without the supervisor itself exiting — because a
// supervisor that exits collapses the container, and the container's
// filesystem, which is where the freshly synced code lives.
func TestDevMode_SIGHUPRestartsChild(t *testing.T) {
	cfg := testConfig(helperCommand(t, "graceful"), 5*time.Second)
	cfg.DevMode = true

	h := startSupervisor(t, cfg)
	first := h.waitRunning(t, 5*time.Second)
	waitReady(t, helperReadyFile(t), 5*time.Second)

	second := requestRestart(t, h, first)
	if second == first {
		t.Fatalf("pid did not change: %d", second)
	}

	// The supervisor must still be alive and must still be able to shut down
	// normally afterwards: a restart that left the state machine wedged
	// would show up here as a shutdown that never completes.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	if code := h.waitExit(t, 10*time.Second); code != 0 {
		t.Errorf("exit code = %d, want 0 (the replacement child exits cleanly on SIGTERM); logs:\n%s", code, h.logs.String())
	}
}

// TestDevMode_SIGHUPRestartsChildRepeatedly is the multi-iteration case: one
// restart working proves nothing about a dev session, which does this on
// every file save. A state machine that leaks a flag, a deadline or a process
// handle per cycle passes the single-restart test above and fails here.
func TestDevMode_SIGHUPRestartsChildRepeatedly(t *testing.T) {
	cfg := testConfig(helperCommand(t, "graceful"), 5*time.Second)
	cfg.DevMode = true

	h := startSupervisor(t, cfg)
	pid := h.waitRunning(t, 5*time.Second)
	waitReady(t, helperReadyFile(t), 5*time.Second)

	seen := map[int]bool{pid: true}
	for i := 0; i < 3; i++ {
		next := requestRestart(t, h, pid)
		if seen[next] {
			t.Fatalf("cycle %d: pid %d was already used; the child was not genuinely replaced", i, next)
		}
		seen[next] = true
		pid = next
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	if code := h.waitExit(t, 10*time.Second); code != 0 {
		t.Errorf("exit code = %d, want 0; logs:\n%s", code, h.logs.String())
	}
}

// TestDevModeOff_SIGHUPIsStillForwarded pins the production behaviour the
// restart path must not disturb: without the opt-in, SIGHUP means exactly
// what it always meant — relay it to the child.
func TestDevModeOff_SIGHUPIsStillForwarded(t *testing.T) {
	cfg := testConfig(helperCommand(t, "record-hup"), 5*time.Second)
	cfg.DevMode = false

	h := startSupervisor(t, cfg)
	h.waitRunning(t, 5*time.Second)
	waitReady(t, helperReadyFile(t), 5*time.Second)

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}

	// 22 is record-hup's exit code: the child received SIGHUP and exited on
	// its own terms. A restart would instead have produced a new child and
	// no exit at all.
	if code := h.waitExit(t, 10*time.Second); code != 22 {
		t.Errorf("exit code = %d, want 22 (child caught the forwarded SIGHUP); logs:\n%s", code, h.logs.String())
	}
}

// TestDevMode_ShutdownDuringRestartExits is the interleaving that would
// otherwise leave a container spawning a fresh server while the runtime waits
// for it to die: a SIGTERM arriving while a restart is still in flight must
// abandon the restart, not be swallowed by it.
func TestDevMode_ShutdownDuringRestartExits(t *testing.T) {
	// ignore-term never dies on its own, so the restart cannot complete and
	// the interleaving is deterministic rather than a race with a fast child.
	cfg := testConfig(helperCommand(t, "ignore-term"), 200*time.Millisecond)
	cfg.DevMode = true

	h := startSupervisor(t, cfg)
	h.waitRunning(t, 5*time.Second)
	waitReady(t, helperReadyFile(t), 5*time.Second)

	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("send SIGHUP: %v", err)
	}
	// Wait until the restart is actually in flight before overtaking it, so
	// this test cannot silently degrade into "SIGTERM arrived first".
	deadline := time.Now().Add(5 * time.Second)
	for !h.sup.State().Restarting {
		if time.Now().After(deadline) {
			t.Fatalf("restart never started; logs:\n%s", h.logs.String())
		}
		time.Sleep(time.Millisecond)
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}

	// 137 == 128+SIGKILL: the shutdown deadline expired and escalated, which
	// is the ordinary production path for a child that ignores SIGTERM. What
	// matters is that the supervisor EXITED rather than starting a
	// replacement.
	code := h.waitExit(t, 20*time.Second)
	if code != 137 {
		t.Errorf("exit code = %d, want 137 (SIGKILL escalation); logs:\n%s", code, h.logs.String())
	}
	if st := h.sup.State(); st.Restarting {
		t.Error("supervisor exited with a restart still marked in flight")
	}
	if !strings.Contains(h.logs.String(), "abandoning in-flight restart") {
		t.Errorf("expected the abandoned restart to be logged; logs:\n%s", h.logs.String())
	}
}

// TestDevMode_LivenessHoldsWhileReadinessDropsDuringRestart pins the one
// place the two probes must disagree. Answering 503 on liveness through a
// restart window would let the kubelet kill the container on the very rebuild
// the developer is waiting for; answering 200 on readiness would route
// traffic at a pod with no server process at all.
func TestDevMode_LivenessHoldsWhileReadinessDropsDuringRestart(t *testing.T) {
	st := newFakeState(State{Started: true, Running: false, Restarting: true, PID: 4242})
	p := NewProbeServer(st, 0, 0, nil)
	p.AttestationPassed()

	if rec := doRequest(t, p, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("/healthz = %d during a dev restart, want 200; the kubelet would kill the pod mid-rebuild", rec.Code)
	}
	if rec := doRequest(t, p, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/readyz = %d during a dev restart, want 503; traffic would be routed at a pod with no server process", rec.Code)
	}

	// The same state without the restart marker — a child that simply died —
	// must still fail liveness, or the carve-out has swallowed the
	// crash-detection this probe exists for.
	st.set(State{Started: true, Running: false, PID: 4242})
	if rec := doRequest(t, p, "/healthz"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/healthz = %d for a dead child that is not restarting, want 503", rec.Code)
	}
}
