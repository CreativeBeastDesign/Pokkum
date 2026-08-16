// Command pokkum-init is the PID 1 supervisor Pokkum places in every image it
// builds.
//
// It exists because a compiled SvelteKit server is not an init system. Run
// directly as PID 1 it inherits two responsibilities it was never written to
// handle: reaping every process the kernel reparents onto pid 1, and being the
// process a container runtime sends SIGTERM to. Getting either wrong shows up
// in production as zombie accumulation or as pods that take the full
// terminationGracePeriodSeconds to die on every rollout.
//
// Usage:
//
//	pokkum-init [flags] -- /app/server [args...]
//
// Everything after the bare "--" is the child command. See
// internal/ports/packager.go, which builds exactly this argv as the image
// entrypoint.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
)

const (
	// exitUsage is the conventional shell exit code for a usage error.
	exitUsage = 2

	// exitAttestationMismatch is returned when startup attestation is enabled
	// (cfg.AttestationDigest non-empty) and the live /app tree does not match
	// the build-time digest. It is deliberately distinct from 126: that code
	// is already owned by startExitCode below for a child binary that exists
	// but cannot be exec'd (EACCES/EPERM/ENOEXEC), and collapsing the two
	// would make "the filesystem was tampered with" and "bad permissions on
	// the entrypoint" indistinguishable from exit code alone — exactly the
	// ambiguity an operator triaging a crash-looping pod from a dashboard
	// (status code only, no logs) cannot resolve. 125 is unclaimed by any
	// other exit path in this binary (1, 2, 126, 127 and the 128+signum range
	// are all already spoken for; see startExitCode and exitCode in
	// supervisor.go). See Vocabulary.md's "Environment Variables (Runtime,
	// Read by Supervisor)" section for the documented, operator-facing
	// scheme; 126 remains reserved there for the pre-existing exec-failure
	// case this constant was split out from.
	exitAttestationMismatch = 125
)

func main() {
	cfg, warnings, err := parseConfig(os.Args[1:], os.Getenv, os.Stderr)
	switch {
	case errors.Is(err, errVersionRequested):
		return
	case errors.Is(err, flag.ErrHelp):
		return
	case err != nil:
		fmt.Fprintf(os.Stderr, "pokkum-init: %v\n", err)
		os.Exit(exitUsage)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	for _, w := range warnings {
		log.Warn(w)
	}

	// Startup attestation (layered hardening Option C): if the packager stamped
	// an expected digest, verify the live /app tree matches it before the child
	// ever runs. This is the tamper-evidence gate — a modified /app fails fast
	// here, before resolveChildPath, before the probe binds and before any app
	// code is reached. A mismatch is a hard refusal to start (fail closed).
	if err := verifyAttestation(log, cfg.AttestationDigest); err != nil {
		log.Error("startup attestation failed", "error", err)
		os.Exit(exitAttestationMismatch)
	}

	// Resolve the child executable to an absolute path up front, so the fork in
	// Run never waits on a PATH walk. This is the parallelization the probe
	// server's own goroutine rides on: once Run is entered, start() forks
	// immediately while the probe server below binds its listener concurrently,
	// instead of serializing the two. resolveChildPath is deliberately done
	// here, ahead of the thread-pinned fork, because it is the one slow-ish,
	// pure lookup that has no dependency on the probe server or the fork. See
	// the comment on resolveChildPath and the WithChildPath seam.
	childPath, err := resolveChildPath(cfg.Command)
	if err != nil {
		log.Error("failed to resolve child command", "error", err, "command", cfg.Command)
		os.Exit(startExitCode(err))
	}

	sup := New(cfg, log, WithChildPath(childPath))

	// The probe server (W8) is started here, before Run blocks, and stopped
	// after Run returns. It depends on ProcessState, not *Supervisor, and
	// takes its ports from sup rather than re-reading cfg so it can never
	// disagree with the configuration the supervisor itself resolved. See the
	// seam documented on the State and ProcessState types in supervisor.go,
	// and TestStateSeamForProbeServer in supervisor_test.go, which pins the
	// contract probe.go builds against.
	//
	// Because start() no longer performs exec.LookPath, the probe listener
	// binds and the child fork overlap instead of serializing: /healthz can be
	// answered as soon as the listener is up, while the Bun child is still
	// being forked/exec'd. That overlap is the "fast sub-millisecond supervisor
	// startup" the roadmap item names, guarded by the startup-overlap test.
	//
	// A probe server that fails to bind its port logs and returns rather than
	// exiting the process; it must never be able to take the application down.
	probeSrv := NewProbeServer(sup, sup.ProbePort(), sup.AppPort(), log)
	go probeSrv.Serve(context.Background())
	defer probeSrv.Shutdown()

	// context.Background, not signal.NotifyContext: signals are the
	// supervisor's subject matter, not its plumbing, and they are handled
	// inside Run so they can be forwarded rather than merely observed. The
	// context is threaded through for the shutdown deadline and so an embedder
	// can request a graceful stop without a signal.
	code, err := sup.Run(context.Background())
	if err != nil {
		log.Error("supervisor failed", "error", err)
	}
	os.Exit(code)
}
