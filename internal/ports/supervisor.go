package ports

import "context"

// SupervisorProvider yields the pokkum-init PID-1 supervisor binary for a
// platform. It is implemented by internal/adapters/supervisor.
//
// pokkum-init is a separate, statically linked Go program. It is cross-compiled
// for every supported platform at pokkum release time and embedded into the
// pokkum CLI binary with go:embed, so that `pokkum build` needs no Go toolchain
// and no network access to obtain it. The provider is the seam that hides that
// mechanism: a test can substitute a fake that returns three bytes, and a future
// implementation could download or build on demand without any other package
// noticing.
//
// It must be static. It runs as PID 1 in a distroless image whose only reason
// for including libc is the application binary, and it must keep working if a
// user selects a different base; a dynamically linked supervisor would couple
// the two choices together.
//
// Its runtime responsibilities are fixed by the image runtime contract declared
// in packager.go: reap orphaned children, forward SIGTERM/SIGINT to the app,
// wait up to POKKUM_SHUTDOWN_TIMEOUT before escalating to SIGKILL, and serve
// liveness/readiness probes on POKKUM_PROBE_PORT.
//
// Implementations must be safe for concurrent use.
type SupervisorProvider interface {
	// Binary returns the complete contents of the pokkum-init executable for
	// the given platform.
	//
	// The returned slice belongs to the caller and may be retained, but the
	// caller must not modify it, since an embedded implementation will hand out
	// the same backing array on every call. It is never nil or empty on a nil
	// error.
	//
	// Bytes rather than a path or an io.Reader: the supervisor is a few
	// megabytes at most, it is consumed exactly once by the packager to build a
	// single-file layer, and a []byte keeps the embedded implementation free of
	// temp files that would then need deterministic mtimes of their own.
	//
	// Error expectations: core.ErrUnsupportedPlatform when p is not Supported,
	// core.ErrSupervisorUnavailable when the binary for a supported platform is
	// absent from the embedded set — which means the pokkum release was built
	// incorrectly and is not something the user can fix.
	Binary(ctx context.Context, p Platform) ([]byte, error)

	// Version reports the version of the supervisor being provided, for image
	// labels and the build summary. It is the pokkum version for an embedded
	// implementation. Empty string with a nil error means "unknown", which is
	// acceptable in development builds.
	Version(ctx context.Context) (string, error)
}
