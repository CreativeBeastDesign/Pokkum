package main

import (
	"os"
	"syscall"
)

// forwardableSignals is the set the supervisor intercepts and relays to the
// child's process group.
//
// It is an allowlist, not "everything catchable", and every omission is
// deliberate:
//
//   - SIGKILL and SIGSTOP cannot be caught at all. Nothing to do.
//   - SIGCHLD is the supervisor's own bookkeeping. Forwarding it would tell the
//     application that one of *its* children died, which is a lie, and it is
//     handled on a separate channel so a storm of child exits can never crowd
//     out a termination signal.
//   - SIGURG is used by the Go runtime for goroutine preemption and arrives
//     constantly. signal.Notify on it would turn the supervise loop into a
//     spin.
//   - SIGPIPE has special meaning inside the Go runtime (writes to a closed
//     descriptor) and must be left to it. SIGPROF and SIGVTALRM likewise belong
//     to the runtime's profiling and timer machinery.
//   - The synchronous fault signals (SIGSEGV, SIGBUS, SIGFPE, SIGILL, SIGTRAP,
//     SIGABRT, SIGSYS) describe something wrong with *this* process, not a
//     request to pass along. Catching them would also suppress the crash
//     report we would want if the supervisor itself were broken.
//   - The resource signals (SIGXCPU, SIGXFSZ) are raised by the kernel against
//     the process that exceeded the limit. Relaying ours to the child would be
//     meaningless; the child gets its own directly.
//
// Everything a container runtime, a human with kubectl exec, or a job
// controller might plausibly send is present.
func forwardableSignals() []os.Signal {
	return []os.Signal{
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGQUIT,
		syscall.SIGTERM,
		syscall.SIGUSR1,
		syscall.SIGUSR2,
		syscall.SIGALRM,
		syscall.SIGCONT,
		syscall.SIGTSTP,
		syscall.SIGTTIN,
		syscall.SIGTTOU,
		syscall.SIGWINCH,
	}
}

// isTerminating reports whether sig, in addition to being forwarded, starts the
// graceful shutdown clock. These are the two signals a container runtime uses
// to ask for a drain, and the two the SvelteKit server installs handlers for.
func isTerminating(sig syscall.Signal) bool {
	return sig == syscall.SIGTERM || sig == syscall.SIGINT
}
