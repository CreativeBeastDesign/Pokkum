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

// exitUsage is the conventional shell exit code for a usage error.
const exitUsage = 2

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

	sup := New(cfg, log)

	// The probe server (W8) is started here, before Run blocks, and stopped
	// after Run returns. It depends on ProcessState, not *Supervisor, and
	// takes its ports from sup rather than re-reading cfg so it can never
	// disagree with the configuration the supervisor itself resolved. See the
	// seam documented on the State and ProcessState types in supervisor.go,
	// and TestStateSeamForProbeServer in supervisor_test.go, which pins the
	// contract probe.go builds against.
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
