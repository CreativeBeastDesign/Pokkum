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

	// ---------------------------------------------------------------------
	// Seam for W8, the probe server.
	//
	// Start it here, before Run blocks, and stop it after Run returns:
	//
	//	srv := probe.New(sup, cfg.ProbePort, log)   // takes a ProcessState
	//	go srv.Serve(ctx)
	//	defer srv.Shutdown()
	//
	// Everything the probe server needs is already resolved and reachable
	// without touching this package's internals:
	//
	//   - sup.State() returns an immutable State snapshot, lock-free and safe
	//     from the HTTP handler goroutines. /healthz answers from Running;
	//     /readyz answers from Running && !ShuttingDown, so a pod leaves the
	//     load balancer the instant SIGTERM lands rather than when the child
	//     finally closes its listener.
	//   - sup.ProbePort() is the resolved listen port and sup.AppPort() is
	//     where the application was told to bind, for a readiness check that
	//     wants to dial the app rather than trust that it is alive.
	//   - The ProcessState interface in supervisor.go is what the probe server
	//     should depend on, so its handlers can be tested against a State
	//     literal with no processes involved.
	//
	// The probe server must not exit the process or wait on the child; the
	// supervise loop owns both. Nothing below this comment needs to change.
	// ---------------------------------------------------------------------

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
