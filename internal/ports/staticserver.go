package ports

import "context"

// StaticServerProvider yields the pokkum-static PID-1 static file server binary
// for a platform. It is implemented by internal/adapters/staticserver.
//
// pokkum-static is a separate, statically linked Go program. It is cross-compiled
// for every supported platform at pokkum release time and embedded into the
// pokkum CLI binary with go:embed, exactly like the pokkum-init supervisor (see
// supervisor.go), so that `pokkum build --static` needs no Go toolchain and no
// network access to obtain it. The provider is the seam that hides that
// mechanism: a test can substitute a fake, and a future implementation could
// download or build on demand without any other package noticing.
//
// It must be static. It runs as PID 1 in a distroless/static-class image that
// has no libc at all, so a dynamically linked server would not start.
//
// Its runtime responsibilities are fixed by the image runtime contract declared
// in packager.go: serve the static roots (default /app/client and
// /app/prerendered) with ETag, Range and Content-Encoding negotiation against
// .gz/.br/.zst sidecars, and serve liveness/readiness probes on
// POKKUM_PROBE_PORT.
//
// Implementations must be safe for concurrent use.
type StaticServerProvider interface {
	// Binary returns the complete contents of the pokkum-static executable for
	// the given platform.
	//
	// The returned slice belongs to the caller and may be retained, but the
	// caller must not modify it, since an embedded implementation will hand out
	// the same backing array on every call. It is never nil or empty on a nil
	// error.
	//
	// Error expectations: core.ErrUnsupportedPlatform when p is not Supported,
	// core.ErrStaticServerUnavailable when the binary for a supported platform
	// is absent from the embedded set — which means the pokkum release was
	// built without running `make static-server`.
	Binary(ctx context.Context, p Platform) ([]byte, error)

	// Version reports the version of the static server being provided, for
	// image labels and the build summary. Empty string with a nil error means
	// "unknown", which is acceptable in development builds.
	Version(ctx context.Context) (string, error)
}
