//go:build unix

package bunexec

import (
	"os/exec"
	"syscall"
	"time"
)

// processKillGrace bounds how long Cmd.Wait will wait, after the kill signal
// is sent on context cancellation, before forcibly closing any redirected
// I/O pipes to unblock it. See setNewProcessGroup.
const processKillGrace = 5 * time.Second

// setNewProcessGroup configures cmd to run in its own process group and
// arranges for context cancellation to kill that whole group, not just the
// direct child.
//
// This matters because `bun run build` (stage one) drives an npm-script-style
// build that may itself fork helper processes, and because Cmd.Wait cannot
// return until every process holding the write end of the redirected
// stdout/stderr pipes has closed it. exec.CommandContext's default
// cancellation behaviour sends the kill signal to the direct child only; if
// that child had already forked a grandchild which inherited the pipe file
// descriptors, the grandchild keeps the pipe open and Wait blocks until the
// grandchild happens to exit on its own — which, for a hung or runaway
// process, may be indefinitely. Killing the whole process group (the
// negative-PID form of kill(2)) reaches every descendant in one signal, and
// WaitDelay is a last-resort backstop that forcibly closes the pipes after
// processKillGrace even if some descendant somehow escaped the group.
func setNewProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = processKillGrace
}
