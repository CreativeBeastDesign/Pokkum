// Command pokkum-static is the PID 1 static file server Pokkum places in
// images built with --strategy=static.
//
// It serves a prebuilt, purely-static SvelteKit site (the client bundle under
// /app/client and the prerendered pages under /app/prerendered) with no Bun
// runtime and no supervisor: the process is its own init, streaming files with
// ETag, Range and Content-Encoding negotiation against the .gz/.br/.zst
// sidecars that precompressutils generates at build time. It also serves
// /healthz and /readyz on the probe port so Kubernetes probes keep answering
// independently of content traffic.
//
// Usage:
//
//	pokkum-static [flags]
//
// See internal/ports/packager.go, which builds the entrypoint
// [/pokkum/static] and sets PORT, POKKUM_PROBE_PORT, POKKUM_STATIC_ROOTS and,
// for SPA sites, POKKUM_STATIC_FALLBACK (an opt-in fallback page served with
// 200 for unmatched GET/HEAD routes).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"
)

// exitUsage is the conventional shell exit code for a usage error.
const exitUsage = 2

// HTTP server timeouts. Small on purpose: a static server has nothing that
// needs a long-lived request.
const (
	serverHeaderTimeout = 10 * time.Second
	serverWriteTimeout  = 30 * time.Second
	serverReadTimeout   = 10 * time.Second
	serverIdleTimeout   = 60 * time.Second

	probeHeaderTimeout = 3 * time.Second
)

// newContentServer builds the content http.Server for cfg. Addr is set
// explicitly to bind all interfaces (host part empty, via net.JoinHostPort)
// on cfg.Port: http.Server.ListenAndServe falls back to ":http" (port 80)
// whenever Addr is the zero value, which silently discards the configured
// PORT. Binding all interfaces, rather than a specific host, is the correct
// choice here because this runs as PID 1 inside a container network
// namespace — the Pod/Service only reaches the container through whatever
// interface the runtime attaches (never loopback), so anything narrower
// than the wildcard address would make the server unreachable from outside
// the container.
func newContentServer(cfg Config, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(cfg.Port)),
		Handler:           handler,
		ReadHeaderTimeout: serverHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       serverIdleTimeout,
	}
}

// newProbeServer builds the probe http.Server for cfg, binding all
// interfaces on cfg.ProbePort. See newContentServer's comment for why Addr
// must be set explicitly and why the wildcard host is correct here.
func newProbeServer(cfg Config, shuttingDown *atomic.Bool) *http.Server {
	return &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(cfg.ProbePort)),
		Handler:           probeHandler(shuttingDown),
		ReadHeaderTimeout: probeHeaderTimeout,
	}
}

func main() {
	cfg, warnings, err := parseConfig(os.Args[1:], os.Getenv, os.Stderr)
	switch {
	case errors.Is(err, errVersionRequested):
		return
	case errors.Is(err, flag.ErrHelp):
		return
	case err != nil:
		fmt.Fprintf(os.Stderr, "pokkum-static: %v\n", err)
		os.Exit(exitUsage)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	for _, w := range warnings {
		log.Warn(w)
	}

	// Serve content on the app port and probes on the probe port. Both must
	// bind failure, unlike the supervisor's probe server (where the app is the
	// real product and probes are best-effort): here if we cannot bind the
	// content port there is nothing to serve, so that is fatal, whereas a
	// probe-port bind failure is logged and the server still runs.
	svc := newContentServer(cfg, newStaticServer(cfg.Roots, cfg.Fallback, log).handler())

	errc := make(chan error, 2)
	go func() {
		log.Info("static server listening", "addr", svc.Addr, "roots", cfg.Roots)
		if err := svc.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("static server: %w", err)
		}
		errc <- nil
	}()

	var shuttingDown atomic.Bool

	// cfg.Port != cfg.ProbePort is guaranteed by config.go's validate(): a
	// collapsed configuration is rejected at startup (exitUsage) before this
	// point is ever reached, so the two listeners below are unconditional.
	probe := newProbeServer(cfg, &shuttingDown)
	go func() {
		log.Info("probe server listening", "addr", probe.Addr)
		if err := probe.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Warn("probe server failed", "addr", probe.Addr, "error", err)
		}
	}()
	defer func() {
		if err := probe.Shutdown(context.Background()); err != nil {
			log.Warn("probe server did not shut down cleanly", "error", err)
		}
	}()

	// SIGTERM is the container runtime's way of asking for a graceful stop;
	// SIGINT is the interactive equivalent. Serve and wait, then shut down.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	log.Info("pokkum-static ready", "port", cfg.Port, "probe_port", cfg.ProbePort)
	select {
	case <-ctx.Done():
		log.Info("received termination signal; shutting down")
	case serr := <-errc:
		if serr != nil {
			log.Error("static server exited unexpectedly", "error", serr)
		} else {
			log.Info("static server stopped")
		}
	}

	// Flip readiness before starting the drain so a load balancer stops
	// routing new connections here for the whole shutdown window, not just
	// once the listener actually closes — mirroring pokkum-init's
	// readiness-drain-on-SIGTERM behavior.
	shuttingDown.Store(true)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := svc.Shutdown(shutdownCtx); err != nil {
		log.Warn("static server did not shut down cleanly", "error", err)
	}
}

// probeHandler serves liveness and readiness. Liveness always answers 200
// once the process is up. Readiness answers 200 until shuttingDown is set,
// then 503 — so a load balancer stops routing new traffic here for the whole
// shutdown grace window, not just once the listener actually closes.
func probeHandler(shuttingDown *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if shuttingDown.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}
