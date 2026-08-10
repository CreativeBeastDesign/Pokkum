//go:build linux

package main

import (
	"errors"
	"syscall"
	"testing"
	"time"
)

// prSetChildSubreaper is PR_SET_CHILD_SUBREAPER from <linux/prctl.h>. The
// constant is not exported by the syscall package, and pulling in
// golang.org/x/sys for one integer would make a third-party module a direct
// dependency of a program whose entire selling point is that it has none.
const prSetChildSubreaper = 36

func setChildSubreaper(t *testing.T, on bool) {
	t.Helper()
	var arg uintptr
	if on {
		arg = 1
	}
	if _, _, errno := syscall.RawSyscall(syscall.SYS_PRCTL, prSetChildSubreaper, arg, 0); errno != 0 {
		t.Fatalf("prctl(PR_SET_CHILD_SUBREAPER, %d): %v", arg, errno)
	}
}

// TestAdoptedOrphanIsReaped is the real thing, and it can only exist on Linux.
//
// TestOrphanReapIsNotMistakenForChildExit exercises the reaper against a
// process the supervisor did not start, which is what adoption looks like from
// inside wait4. This test produces an adoption for real: it marks the test
// process as a child subreaper, so a grandchild whose parent dies is
// reparented onto it exactly as it would be onto pid 1 in a container.
//
// macOS has no subreaper facility and no PID namespaces, so an orphan there
// goes to launchd and the supervisor never sees it. There is nothing to skip
// around; the file is tagged out.
//
// The shell pipeline is arranged so that the intermediate process exits while
// the tracked child lives:
//
//	sh -c 'sh -c "(sleep 0.3; exit 5) & exit 0"; exec sleep 30'
//
// The outer shell is the tracked child. It runs an inner shell, which starts a
// background subshell and immediately exits, orphaning it. The outer shell then
// execs sleep, keeping the tracked pid alive. When the subshell finishes, its
// SIGCHLD arrives at the supervisor for a pid it has never heard of.
func TestAdoptedOrphanIsReaped(t *testing.T) {
	setChildSubreaper(t, true)
	t.Cleanup(func() { setChildSubreaper(t, false) })

	script := `sh -c '(sleep 0.3; exit 5) & exit 0'; exec sleep 30`
	h := startSupervisor(t, testConfig([]string{"/bin/sh", "-c", script}, 30*time.Second))
	childPID := h.waitRunning(t, 10*time.Second)

	rec := h.awaitRecord(t, "an adopted orphan reap", 20*time.Second, func(rec map[string]any) bool {
		if rec["msg"] != "reaped orphaned process" {
			return false
		}
		pid, ok := rec["pid"].(float64)
		return ok && int(pid) != childPID
	})
	if got, want := rec["status"], "exit 5"; got != want {
		t.Fatalf("adopted orphan status = %v, want %q", got, want)
	}

	orphanPID := int(rec["pid"].(float64))
	if err := syscall.Kill(orphanPID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("adopted orphan %d is still a zombie (kill(pid,0) = %v)", orphanPID, err)
	}

	// The adoption must not have been read as the application exiting.
	select {
	case <-h.done:
		t.Fatalf("supervisor exited when an adopted orphan was reaped: code=%d; logs:\n%s", h.code, h.logs.String())
	default:
	}
	if st := h.sup.State(); !st.Running || st.PID != childPID {
		t.Fatalf("state after adoption = %+v, want the tracked child %d still running", st, childPID)
	}
	h.assertNotLogged(t, "child exited")
}
