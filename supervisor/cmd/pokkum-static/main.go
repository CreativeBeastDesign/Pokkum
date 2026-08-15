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
// [/pokkum/static] and sets PORT, POKKUM_PROBE_PORT and POKKUM_STATIC_ROOTS.
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
	svc := &http.Server{
		Handler:           newStaticServer(cfg.Roots, log).handler(),
		ReadHeaderTimeout: serverHeaderTimeout,
		ReadTimeout:       serverReadTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       serverIdleTimeout,
	}

	errc := make(chan error, 2)
	go func() {
		addr := net.JoinHostPort("", strconv.Itoa(cfg.Port))
		log.Info("static server listening", "addr", addr, "roots", cfg.Roots)
		if err := svc.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- fmt.Errorf("static server: %w", err)
		}
		errc <- nil
	}()

	if cfg.Port != cfg.ProbePort {
		probe := &http.Server{
			Handler:           probeHandler(),
			ReadHeaderTimeout: probeHeaderTimeout,
		}
		go func() {
			addr := net.JoinHostPort("", strconv.Itoa(cfg.ProbePort))
			log.Info("probe server listening", "addr", addr)
			if err := probe.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Warn("probe server failed", "addr", addr, "error", err)
			}
		}()
		defer probe.Shutdown(context.Background())
	}

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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := svc.Shutdown(shutdownCtx); err != nil {
		log.Warn("static server did not shut down cleanly", "error", err)
	}
}

// probeHandler serves liveness and readiness. A static server is conceptually
// ready as soon as it is serving, so both answer 200 once the process is up.
func probeHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}
