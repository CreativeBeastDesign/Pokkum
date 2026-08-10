package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// hardKillGrace bounds how long the supervisor waits for the process group to
// disappear after SIGKILL. SIGKILL cannot be blocked, so this window only
// expires if the child is wedged in an uninterruptible wait on a hung mount or
// device. An init that hangs forever in that case is worse than one that exits
// and lets the runtime tear the namespace down, so the loop refuses to wait
// indefinitely for anything.
const hardKillGrace = 5 * time.Second

// errChildUnkillable reports that the child survived SIGKILL for longer than
// hardKillGrace and the supervisor gave up waiting for it.
var errChildUnkillable = errors.New("child did not terminate after SIGKILL")

// State is an immutable snapshot of the supervised child, safe to read from any
// goroutine at any time.
//
// This is the seam for the probe server (W8). A liveness handler answers from
// Started && Running; a readiness handler additionally requires !ShuttingDown,
// so that a pod is taken out of the load balancer the moment SIGTERM arrives
// rather than when the child finally closes its listener.
type State struct {
	// Started reports whether the child was ever successfully spawned. It stays
	// true after the child exits; use Running for liveness.
	Started bool

	// Running reports whether the child has been started and not yet reaped.
	Running bool

	// ShuttingDown reports whether a termination signal has been forwarded and
	// the graceful deadline is counting down.
	ShuttingDown bool

	// PID is the child's process id, which is also its process group id. Zero
	// before the child starts.
	PID int

	// StartedAt and ExitedAt are zero until the corresponding transition.
	StartedAt time.Time
	ExitedAt  time.Time

	// ExitCode is the code the supervisor will exit with, valid once Started is
	// true and Running is false. It follows the shell convention: the child's
	// own status, or 128+signum when the child was signalled.
	ExitCode int

	// Signal is the signal that killed the child, or zero if it exited on its
	// own terms.
	Signal syscall.Signal
}

// ProcessState is the read-only view of the supervised child. The probe server
// depends on this interface rather than on *Supervisor, so its handlers can be
// tested against a literal snapshot with no processes involved.
type ProcessState interface {
	State() State
}

var _ ProcessState = (*Supervisor)(nil)

// Supervisor runs one child process as if it were PID 1: it forwards signals to
// the child's process group, reaps every process the kernel reparents onto it,
// escalates to SIGKILL when a graceful shutdown overstays its deadline, and
// propagates the child's exit status.
//
// It works correctly as PID 1 but never assumes it is PID 1. Nothing here reads
// getpid() for anything but a log field; the reaping loop is simply harmless
// when no orphans are ever reparented, which is the situation in tests.
type Supervisor struct {
	cfg Config
	log *slog.Logger

	// state is published by the supervise goroutine and read by anyone.
	state atomic.Pointer[State]

	// Everything below is owned exclusively by the goroutine inside Run. There
	// are no mutexes because there is no sharing: signal delivery, child exit,
	// deadline expiry and context cancellation are all funnelled into a single
	// select, so the state machine only ever advances one event at a time. The
	// races this package exists to avoid are all races between concurrent
	// handlers, and the cheapest way to not have them is to not have
	// concurrent handlers.
	proc         *os.Process
	pid          int
	pgid         int
	startedAt    time.Time
	exitedAt     time.Time
	shuttingDown bool
	killIssued   bool
	childExited  bool
	status       syscall.WaitStatus

	// deadline is the currently armed timer, expressed as a context so the
	// shutdown window participates in ordinary context plumbing. It is nil
	// whenever no deadline is armed, which makes its Done channel nil in the
	// select and therefore never ready.
	deadline     context.Context
	deadlineStop context.CancelFunc
}

// New returns a Supervisor for cfg. A nil logger discards output.
func New(cfg Config, log *slog.Logger) *Supervisor {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Supervisor{cfg: cfg, log: log}
	s.state.Store(&State{})
	return s
}

// State returns the current snapshot. It never blocks and is safe to call
// before Run, during Run and after Run returns.
func (s *Supervisor) State() State {
	if st := s.state.Load(); st != nil {
		return *st
	}
	return State{}
}

// ProbePort reports the port the probe server should listen on. It exists so
// the probe server takes its configuration from the same resolved Config the
// supervisor used, instead of re-reading the environment and possibly
// disagreeing with it.
func (s *Supervisor) ProbePort() int { return s.cfg.ProbePort }

// AppPort reports the port the child was told to listen on via PORT. A
// readiness check that wants to prove the application is actually accepting
// connections, rather than merely alive, dials this.
func (s *Supervisor) AppPort() int { return s.cfg.Port }

// Run starts the child and supervises it until it exits, returning the exit
// code the supervisor should terminate with.
//
// Cancelling ctx requests a graceful shutdown; it is treated exactly like an
// inbound SIGTERM, including the deadline and the SIGKILL escalation. It does
// not abandon the child, because there is nobody else to clean it up.
//
// Run must be called at most once.
func (s *Supervisor) Run(ctx context.Context) (int, error) {
	// Pin this goroutine to its OS thread for the lifetime of the child.
	//
	// os.StartProcess forks from the calling thread, and Linux delivers
	// Pdeathsig when that specific thread exits rather than when the process
	// does. If the Go runtime were to retire the thread while the child was
	// still alive, the child would be SIGKILLed for no reason. Holding the lock
	// until Run returns makes that impossible, and by then the child is gone so
	// releasing it is free.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Signal handlers are installed before the fork, not after. A child that
	// exits immediately - a missing shared library, a bad argument, an instant
	// panic - can deliver SIGCHLD before os.StartProcess has even returned, and
	// a supervisor that registered afterwards would wait forever for a
	// notification the kernel already sent.
	sigCh := make(chan os.Signal, 64)
	signal.Notify(sigCh, forwardableSignals()...)
	defer signal.Stop(sigCh)

	// SIGCHLD gets its own channel with room for exactly one token, and that
	// capacity is a correctness argument rather than a guess.
	//
	// os/signal drops a signal when the channel is full, so a shared channel
	// would let a burst of forwarded signals evict the one notification that
	// says the child is dead. On its own channel the only thing that can be
	// dropped is a second SIGCHLD arriving while the first is still queued -
	// and the queued token will cause a drain that reaps that process too. The
	// token means "at least one child may have exited", never "exactly one
	// did", so coalescing is not a loss. The reverse ordering is equally safe:
	// a SIGCHLD arriving after the loop has taken the token but before the
	// drain finishes leaves a fresh token behind, and the extra drain that
	// follows is a no-op. There is no interleaving that loses a wakeup, which
	// is why no polling ticker is needed as a backstop.
	chldCh := make(chan os.Signal, 1)
	signal.Notify(chldCh, syscall.SIGCHLD)
	defer signal.Stop(chldCh)

	if err := s.start(); err != nil {
		s.log.Error("failed to start child", "error", err, "command", s.cfg.Command)
		return startExitCode(err), err
	}
	defer func() {
		// Release drops the process handle. It never waits, so it cannot steal
		// a status from the reaper.
		if s.proc != nil {
			_ = s.proc.Release()
		}
	}()

	return s.supervise(ctx, sigCh, chldCh)
}

// supervise is the single-threaded event loop. Every mutation of the state
// machine happens here or in a function called from here.
func (s *Supervisor) supervise(ctx context.Context, sigCh, chldCh <-chan os.Signal) (int, error) {
	defer s.disarmDeadline()

	// ctxDone is nil'd after it fires. A cancelled context's Done channel stays
	// ready forever, and leaving it in the select would spin the loop at full
	// speed for the entire shutdown window.
	ctxDone := ctx.Done()

	for {
		// A nil channel blocks forever, so an unarmed deadline simply drops out
		// of the select rather than needing a flag to guard it.
		var deadlineDone <-chan struct{}
		if s.deadline != nil {
			deadlineDone = s.deadline.Done()
		}

		select {
		case <-ctxDone:
			ctxDone = nil
			s.log.Info("shutdown requested by context cancellation")
			s.forward(syscall.SIGTERM)
			s.beginShutdown(ctx)

		case sig := <-sigCh:
			ss, ok := sig.(syscall.Signal)
			if !ok {
				continue
			}
			s.log.Info("signal received", "signal", ss.String())
			s.forward(ss)
			if isTerminating(ss) {
				s.beginShutdown(ctx)
			}

		case <-chldCh:
			s.reap()
			if s.childExited {
				return s.finish()
			}

		case <-deadlineDone:
			// Disarm first: the deadline context stays ready once expired, and
			// re-entering the select with it armed would spin.
			s.disarmDeadline()
			if s.killIssued {
				s.log.Error("child survived SIGKILL; abandoning it",
					"pid", s.pid, "pgid", s.pgid, "grace", hardKillGrace)
				s.publish()
				return 128 + int(syscall.SIGKILL), errChildUnkillable
			}
			s.escalate(ctx)
		}
	}
}

// start spawns the child.
//
// os.StartProcess is used directly, and os/exec deliberately is not, for one
// reason: exec.Cmd's Wait calls wait4 on a specific pid, and this program calls
// wait4(-1) from its reaper. Two callers waiting on the same child is a race
// with a silent failure mode - whichever one loses gets ECHILD and the exit
// status is destroyed. There is exactly one wait4 caller in this package, and
// os.Process.Wait is never called.
func (s *Supervisor) start() error {
	name := s.cfg.Command[0]
	path, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("resolving command %q: %w", name, err)
	}

	proc, err := os.StartProcess(path, s.cfg.Command, &os.ProcAttr{
		Env:   childEnv(os.Environ(), s.cfg),
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		Sys:   childSysProcAttr(),
	})
	if err != nil {
		return fmt.Errorf("starting %q: %w", path, err)
	}

	// The child is a group leader, so its pgid is its pid. This is known
	// without a getpgid call because os.StartProcess only returns once the
	// child has passed its exec, which happens after setpgid; there is no
	// window in which a forwarded signal could miss the new group.
	s.proc = proc
	s.pid = proc.Pid
	s.pgid = proc.Pid
	s.startedAt = time.Now()
	s.publish()

	s.log.Info("child started",
		"pid", s.pid,
		"pgid", s.pgid,
		"path", path,
		"argv", s.cfg.Command,
		"port", s.cfg.Port,
		"shutdown_timeout", s.cfg.ShutdownTimeout,
		"supervisor_pid", os.Getpid(),
		"is_init", os.Getpid() == 1)
	return nil
}

// forward relays sig to the child's process group.
//
// Signals are relayed the instant they arrive and are never queued behind
// anything else, because the whole point of an init is that the application
// finds out about SIGTERM as fast as it would have without one.
func (s *Supervisor) forward(sig syscall.Signal) {
	if s.childExited {
		// The pid, and therefore the pgid, is free for reuse the moment the
		// child is reaped. Signalling a group that no longer belongs to us is
		// the one way this program could hurt an unrelated process.
		s.log.Debug("not forwarding signal; child already exited", "signal", sig.String())
		return
	}
	err := signalGroup(s.pgid, sig)
	switch {
	case err == nil:
		s.log.Info("signal forwarded to child process group", "signal", sig.String(), "pgid", s.pgid)
	case errors.Is(err, syscall.ESRCH):
		// The group is gone but not yet reaped. The SIGCHLD is already on its
		// way; nothing to do.
		s.log.Debug("child process group already gone", "signal", sig.String(), "pgid", s.pgid)
	default:
		s.log.Warn("failed to forward signal", "signal", sig.String(), "pgid", s.pgid, "error", err)
	}
}

// beginShutdown arms the graceful deadline. It is idempotent: repeated
// termination signals are still forwarded, but they do not restart or shorten
// the clock, so an impatient operator sending SIGTERM three times cannot make
// the window drift.
func (s *Supervisor) beginShutdown(ctx context.Context) {
	if s.shuttingDown {
		s.log.Debug("shutdown already in progress", "pid", s.pid)
		return
	}
	s.shuttingDown = true
	s.publish()

	// context.WithoutCancel matters here. The deadline is derived from ctx so
	// it inherits nothing but its values, because cancelling ctx is one of the
	// ways a shutdown *starts*. A plain WithTimeout(ctx, ...) would be born
	// already expired in that case and SIGKILL the child instantly, turning the
	// graceful path into the violent one.
	s.deadline, s.deadlineStop = context.WithTimeout(context.WithoutCancel(ctx), s.cfg.ShutdownTimeout)

	s.log.Info("graceful shutdown deadline started",
		"pid", s.pid, "pgid", s.pgid, "timeout", s.cfg.ShutdownTimeout)
}

// escalate sends SIGKILL to the whole group after the graceful window expires.
//
// This is not defensive padding. The SvelteKit server's SIGTERM handler emits
// its shutdown event and calls server.stop(true), but it never calls
// process.exit(); any lingering handle keeps the process alive indefinitely.
// Without this escalation the container would hang until the runtime's own
// timeout on every single rollout.
func (s *Supervisor) escalate(ctx context.Context) {
	s.killIssued = true
	s.log.Warn("graceful shutdown deadline exceeded; sending SIGKILL to child process group",
		"pid", s.pid, "pgid", s.pgid, "timeout", s.cfg.ShutdownTimeout)

	if s.childExited {
		return
	}
	if err := signalGroup(s.pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		s.log.Error("failed to SIGKILL child process group", "pgid", s.pgid, "error", err)
	}

	// SIGKILL is not an acknowledgement, only a request the kernel is very
	// likely to honour. The loop still exits on SIGCHLD; this second, much
	// shorter window exists purely so a wedged child cannot hang the supervisor
	// forever. It is harmless if the child was already reaped between the
	// deadline firing and this call, because that path returns above and the
	// window is disarmed when supervise returns.
	s.deadline, s.deadlineStop = context.WithTimeout(context.WithoutCancel(ctx), hardKillGrace)
}

func (s *Supervisor) disarmDeadline() {
	if s.deadlineStop != nil {
		s.deadlineStop()
	}
	s.deadline = nil
	s.deadlineStop = nil
}

// reap drains every exited child the kernel has for us.
//
// The loop, not a single call, is the whole point: SIGCHLD is not queued. Any
// number of children can die between two deliveries and the kernel will still
// only raise the flag once, so a supervisor that reaps one process per signal
// accumulates zombies exactly when it is under the most load.
//
// Distinguishing the tracked child from an adopted orphan is the subtlest part
// of this package. Wait4(-1) returns whatever died, and in a pod that includes
// processes this supervisor never started and knows nothing about: a shell left
// behind by kubectl exec, a helper the application spawned and detached, the
// remains of anything whose parent died first. Treating one of those as the
// application exiting would tear down a perfectly healthy container. The pid
// comparison is the only thing standing between the two, and it is guarded by
// childExited so that a recycled pid cannot re-trigger it either.
func (s *Supervisor) reap() {
	for {
		var ws syscall.WaitStatus
		pid, err := reapOne(&ws)
		switch {
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.ECHILD):
			// No children at all. If the tracked child is among the missing we
			// have already recorded it; otherwise it was never started.
			return
		case err != nil:
			s.log.Error("wait4 failed", "error", err)
			return
		case pid == 0:
			// Children exist, none have exited. Nothing more to drain.
			return
		}

		if !s.childExited && pid == s.pid {
			s.childExited = true
			s.status = ws
			s.exitedAt = time.Now()
			s.publish()
			if ws.Signaled() {
				s.log.Info("child exited",
					"pid", pid, "signal", ws.Signal().String(), "exit_code", exitCode(ws))
			} else {
				s.log.Info("child exited",
					"pid", pid, "exit_code", exitCode(ws))
			}
			// Keep draining. Other zombies may have accumulated under the same
			// SIGCHLD, and this is the last chance to collect them.
			continue
		}

		s.log.Info("reaped orphaned process", "pid", pid, "status", describeStatus(ws))
	}
}

// finish computes the exit code and cleans up whatever the application left
// behind.
func (s *Supervisor) finish() (int, error) {
	code := exitCode(s.status)

	// The tracked child is gone but its group may not be empty: a worker it
	// forked, something it detached. As PID 1 this is mostly a courtesy,
	// because exiting collapses the PID namespace and the kernel SIGKILLs the
	// survivors anyway. When pokkum-init is not PID 1 - a developer running it
	// on a laptop, this package's own tests - it is the difference between a
	// clean exit and a stray process nobody owns.
	if err := signalGroup(s.pgid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		s.log.Debug("process group cleanup failed", "pgid", s.pgid, "error", err)
	}
	// One last non-blocking sweep for anything already dead. Survivors of the
	// SIGKILL above are not waited for: the supervisor's job is finished and
	// blocking here would only delay the container's exit.
	s.reap()

	s.publish()
	s.log.Info("supervisor exiting", "pid", s.pid, "exit_code", code)
	return code, nil
}

// publish writes a fresh immutable snapshot for readers such as the probe
// server. Called from the supervise goroutine only.
func (s *Supervisor) publish() {
	st := State{
		Started:      s.pid != 0,
		Running:      s.pid != 0 && !s.childExited,
		ShuttingDown: s.shuttingDown,
		PID:          s.pid,
		StartedAt:    s.startedAt,
	}
	if s.childExited {
		st.ExitedAt = s.exitedAt
		st.ExitCode = exitCode(s.status)
		if s.status.Signaled() {
			st.Signal = s.status.Signal()
		}
	}
	s.state.Store(&st)
}

// exitCode maps a wait status onto a process exit code using the shell
// convention: the child's own status, or 128+signum when it was killed. A
// container runtime reports this verbatim, so 137 has to mean "OOM-killed or
// SIGKILLed" and 143 has to mean "SIGTERM" for any of the usual debugging
// reflexes to work.
func exitCode(ws syscall.WaitStatus) int {
	switch {
	case ws.Signaled():
		return 128 + int(ws.Signal())
	case ws.Exited():
		return ws.ExitStatus()
	default:
		// Stopped or continued. WNOHANG without WUNTRACED cannot produce this,
		// but a wait status is a bitfield and silently returning 0 for an
		// unknown one would report success for something we did not understand.
		return 1
	}
}

func describeStatus(ws syscall.WaitStatus) string {
	switch {
	case ws.Signaled():
		return "signal " + ws.Signal().String()
	case ws.Exited():
		return "exit " + strconv.Itoa(ws.ExitStatus())
	default:
		return "unknown"
	}
}

// startExitCode maps a failure to launch onto the exit codes a shell would use,
// so that a typo in the entrypoint is distinguishable from an application that
// started and then failed.
func startExitCode(err error) int {
	switch {
	case errors.Is(err, exec.ErrNotFound), errors.Is(err, syscall.ENOENT):
		return 127
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM), errors.Is(err, syscall.ENOEXEC):
		return 126
	default:
		return 1
	}
}

// childEnv returns base with PORT forced to cfg.Port.
//
// The value is overridden rather than defaulted. PORT is the one variable the
// image config, the supervisor's own probe configuration and the generated
// Kubernetes containerPort all have to agree on, and an inherited PORT from an
// unrelated part of the environment would silently move the application to a
// port nothing is checking.
func childEnv(base []string, cfg Config) []string {
	out := make([]string, 0, len(base)+1)
	for _, kv := range base {
		if strings.HasPrefix(kv, envPort+"=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, envPort+"="+strconv.Itoa(cfg.Port))
}
