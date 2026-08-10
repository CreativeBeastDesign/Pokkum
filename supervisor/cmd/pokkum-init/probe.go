package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// Timeouts and intervals for the probe server. These are small on purpose: a
// probe endpoint that itself hangs defeats the point of having one, and a
// kubelet's default probe timeout is one second.
const (
	// probeHeaderTimeout bounds how long the probe server waits for a client to
	// finish sending request headers. gosec flags a bare http.Server with no
	// ReadHeaderTimeout as a slow-loris exposure; a probe port that only ever
	// answers /healthz and /readyz has no reason to hold a connection open
	// longer than this.
	probeHeaderTimeout = 3 * time.Second

	// probeShutdownTimeout bounds how long Shutdown waits for in-flight probe
	// requests to finish. The probe server is not the thing anyone is waiting
	// on when the container exits, so this stays short rather than sharing the
	// child's much longer graceful-shutdown window.
	probeShutdownTimeout = 2 * time.Second

	// defaultReadyInterval is how often /readyz re-dials the application port
	// in production. Tests override it directly (this file's tests live in
	// package main) so they never sleep for a second to observe a tick.
	defaultReadyInterval = 1 * time.Second

	// dialTimeout bounds a single readiness dial. It must be well under
	// defaultReadyInterval so a wedged dial cannot delay the next tick
	// indefinitely.
	dialTimeout = 500 * time.Millisecond
)

// ProbeServer serves the liveness and readiness endpoints a container runtime
// polls. It depends on ProcessState rather than *Supervisor - see the seam
// comment on that interface in supervisor.go and on TestStateSeamForProbeServer
// in supervisor_test.go, which pins the contract this file builds against.
//
// A ProbeServer must not be able to take the child or the supervisor down: a
// failure to bind its port, or any error out of ListenAndServe, is logged and
// swallowed. The application's own liveness has nothing to do with whether
// this diagnostic endpoint happens to be reachable.
type ProbeServer struct {
	state   ProcessState
	appPort int
	log     *slog.Logger

	// interval is the readiness re-check period. A struct field rather than a
	// package constant so tests can inject a short one instead of sleeping for
	// real ticker intervals.
	interval time.Duration

	// dialer is how /readyz proves the application is accepting connections.
	// Injected so tests can exercise the anti-latch behaviour without needing
	// dialTimeout to actually elapse against a firewalled port.
	dialer func(ctx context.Context, network, address string) (net.Conn, error)

	// ready is the cached outcome of the last dial, read by every /readyz
	// request and written only by the readiness loop. Probe traffic can never
	// be used to hammer the application because a request never triggers a
	// dial of its own.
	ready atomic.Bool

	srv *http.Server
}

// NewProbeServer builds a ProbeServer listening on probePort that answers
// liveness from state and readiness by dialing appPort.
//
// A nil logger discards output, matching New in supervisor.go.
func NewProbeServer(state ProcessState, probePort, appPort int, log *slog.Logger) *ProbeServer {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	p := &ProbeServer{
		state:    state,
		appPort:  appPort,
		log:      log,
		interval: defaultReadyInterval,
		dialer:   (&net.Dialer{}).DialContext,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", p.handleHealthz)
	mux.HandleFunc("/readyz", p.handleReadyz)

	p.srv = &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(probePort)),
		Handler:           mux,
		ReadHeaderTimeout: probeHeaderTimeout,
		ReadTimeout:       probeHeaderTimeout,
		WriteTimeout:      probeHeaderTimeout,
		IdleTimeout:       probeHeaderTimeout,
	}

	return p
}

// Serve binds the probe port and serves until Shutdown is called or ctx is
// cancelled. It is meant to be run on its own goroutine:
//
//	probeSrv := NewProbeServer(sup, sup.ProbePort(), sup.AppPort(), log)
//	go probeSrv.Serve(context.Background())
//	defer probeSrv.Shutdown()
//
// A failure to bind the probe port is logged and Serve simply returns. It
// must never be treated as fatal: an operator who cannot curl /healthz still
// wants the application itself to keep running.
func (p *ProbeServer) Serve(ctx context.Context) {
	ln, err := net.Listen("tcp", p.srv.Addr)
	if err != nil {
		p.log.Error("probe server failed to bind; liveness and readiness checks are unavailable",
			"addr", p.srv.Addr, "error", err)
		return
	}
	p.serve(ctx, ln)
}

// serve runs the readiness loop and the HTTP server against an
// already-created listener. Split out from Serve so tests can supply a
// listener bound to an OS-chosen ephemeral port and read back its address,
// rather than guessing at a free port to configure the server with up front.
func (p *ProbeServer) serve(ctx context.Context, ln net.Listener) {
	loopCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go p.runReadinessLoop(loopCtx)

	p.log.Info("probe server listening", "addr", ln.Addr().String())
	if err := p.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		p.log.Error("probe server stopped unexpectedly", "error", err)
	}
}

// Shutdown stops the probe server, closing its listener and ending the
// readiness loop (which exits when serve's Serve call returns and cancels
// loopCtx). It is safe to call even if Serve was never started or failed to
// bind.
func (p *ProbeServer) Shutdown() {
	ctx, cancel := context.WithTimeout(context.Background(), probeShutdownTimeout)
	defer cancel()
	if err := p.srv.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		p.log.Warn("probe server did not shut down cleanly", "error", err)
	}
}

// handleHealthz answers liveness purely from the supervisor's own bookkeeping
// of the child - never by touching the application's HTTP port. This is the
// entire point of a liveness probe: it must cost nothing and must never
// trigger application work (an SSR render, a DB ping) just because a kubelet
// asked if the process is alive.
//
// This mirrors the seam contract documented on State in supervisor.go: a
// liveness handler answers from Started && Running.
func (p *ProbeServer) handleHealthz(w http.ResponseWriter, r *http.Request) {
	st := p.state.State()
	if !st.Started || !st.Running {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleReadyz answers readiness from the cached result of the readiness
// loop, plus an immediate check of ShuttingDown. ShuttingDown is read fresh on
// every request rather than folded into the cached ready flag so that a pod
// leaves the load balancer the instant a termination signal lands, without
// waiting for the next dial tick.
func (p *ProbeServer) handleReadyz(w http.ResponseWriter, r *http.Request) {
	st := p.state.State()
	if st.ShuttingDown || !st.Running || !p.ready.Load() {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// runReadinessLoop re-dials the application port on a ticker for as long as
// ctx is live, keeping p.ready current.
//
// The ticker, not a one-shot check, is the whole point: a readiness probe
// that only ever proves true and never re-verifies is worse than none,
// because it lets a load balancer keep sending traffic to an application that
// has since stopped accepting connections.
func (p *ProbeServer) runReadinessLoop(ctx context.Context) {
	// Check once immediately rather than waiting for the first tick, so
	// readiness converges promptly after the application starts listening
	// instead of lagging by up to a full interval.
	p.checkReady(ctx)

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.checkReady(ctx)
		}
	}
}

// checkReady performs one dial against the application port and updates the
// cached readiness flag. A successful TCP connect is proof enough that the
// application is accepting connections; nothing about the flag's consumers
// requires an HTTP round trip, and skipping one keeps the check cheap enough
// to run every tick indefinitely.
func (p *ProbeServer) checkReady(ctx context.Context) {
	dialCtx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(p.appPort))
	conn, err := p.dialer(dialCtx, "tcp", addr)
	if err != nil {
		p.ready.Store(false)
		return
	}
	_ = conn.Close()
	p.ready.Store(true)
}
