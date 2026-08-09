package core

import "errors"

// Sentinel errors for every failure mode that a caller might reasonably want to
// distinguish. They are declared here, in core, rather than in ports because
// they are policy: what counts as "too old" a Bun, or "incompatible" a base
// image, is an application decision, while ports holds only vocabulary.
//
// # Wrapping convention
//
// Adapters must never return a bare sentinel and must never return an
// unclassified error. Every error crossing a port boundary must:
//
//  1. Be classifiable with errors.Is against exactly one sentinel below.
//  2. Name the adapter and the operation, so a user reading a one-line error
//     knows which subsystem failed.
//  3. Preserve the underlying cause, so that -v or errors.As can reach it.
//
// The idiom is a double-%w wrap, which Go 1.20 and later support:
//
//	if err := cmd.Run(); err != nil {
//	    return fmt.Errorf("bunexec: compile %s: %w: %w", plat, err, core.ErrCompileFailed)
//	}
//
// When there is no underlying error, use a single %w:
//
//	return fmt.Errorf("registry: no repository configured: %w", core.ErrNoDockerRepo)
//
// Put the human-readable detail in the message, not in a new error type.
// Subprocess failures must include the captured stderr — an exit status alone
// is worthless to a user debugging their own application, and it is the single
// most common thing to get wrong in a tool that shells out.
//
// Context cancellation is the one exception: return a wrapped ctx.Err()
// (context.Canceled or context.DeadlineExceeded) rather than reclassifying a
// cancelled operation as a failure of the thing it was doing.
var (
	// ErrInvalidRequest reports a malformed request that failed validation
	// before any work started. It is the fallback for BuildRequest.Validate
	// when no more specific sentinel applies.
	ErrInvalidRequest = errors.New("invalid request")

	// ErrUnsupportedPlatform reports a build target Pokkum cannot produce.
	// v0.1 supports linux/amd64 and linux/arm64 only.
	ErrUnsupportedPlatform = errors.New("unsupported platform")

	// ErrProjectNotFound reports that the project directory does not exist or
	// does not look like a SvelteKit project (no package.json, no
	// svelte.config.js).
	ErrProjectNotFound = errors.New("sveltekit project not found")

	// ErrBunNotFound reports that no bun executable could be located on PATH.
	ErrBunNotFound = errors.New("bun not found")

	// ErrBunTooOld reports that the located bun is older than the minimum
	// version required to compile a working standalone executable. The message
	// must state both the found and the required version.
	ErrBunTooOld = errors.New("bun version too old")

	// ErrAdapterMissing reports that @jesterkit/exe-sveltekit is not a
	// dependency of the project. Without it there is no single-file entrypoint
	// to compile, and the fix is a one-line install the message should name.
	ErrAdapterMissing = errors.New("sveltekit adapter missing")

	// ErrPrepareFailed reports that the SvelteKit build (stage one) failed or
	// did not produce the expected entrypoint.
	ErrPrepareFailed = errors.New("sveltekit build failed")

	// ErrCompileFailed reports that `bun build --compile` (stage two) failed.
	ErrCompileFailed = errors.New("bun compile failed")

	// ErrSupervisorUnavailable reports that the pokkum-init binary for a
	// supported platform is not present in the embedded set. This means the
	// pokkum release itself was built incorrectly; the message should say so,
	// because it is not something the user can fix in their project.
	ErrSupervisorUnavailable = errors.New("supervisor binary unavailable")

	// ErrInvalidBaseImage reports an unparseable base image reference or an
	// unknown preset.
	ErrInvalidBaseImage = errors.New("invalid base image")

	// ErrBaseImageIncompatible reports a base image that cannot run a
	// Bun-compiled binary, or that does not cover every requested platform.
	//
	// The overwhelmingly common cause is a static base: distroless/static,
	// scratch, or a musl-only image. Bun executables are dynamically linked
	// against libc.so.6, libstdc++.so.6 and libgcc_s.so.1, so such an image
	// produces a container that dies instantly with a loader error. The message
	// must name what was missing and point at the cc-debian12 default.
	ErrBaseImageIncompatible = errors.New("base image incompatible")

	// ErrPackageFailed reports a failure assembling image layers or config.
	ErrPackageFailed = errors.New("image assembly failed")

	// ErrNoDockerRepo reports that no destination repository was configured.
	// This is the ko-equivalent of an unset KO_DOCKER_REPO and should be
	// reported with the flag and environment variable that would fix it.
	ErrNoDockerRepo = errors.New("no destination repository configured")

	// ErrRegistryAuth reports that a registry rejected the supplied
	// credentials, or that none could be found. The message should name the
	// registry host and the credential sources that were tried.
	ErrRegistryAuth = errors.New("registry authentication failed")

	// ErrPushFailed reports any other failure publishing to a registry or
	// loading into a daemon.
	ErrPushFailed = errors.New("image publish failed")

	// ErrDaemonUnavailable reports that no local Docker daemon could be
	// reached for the --local output mode.
	ErrDaemonUnavailable = errors.New("docker daemon unavailable")

	// ErrTarballFailed reports a failure writing the OCI archive.
	ErrTarballFailed = errors.New("tarball write failed")

	// ErrInvalidSBOMFormat reports an unknown SBOM format, or a request to
	// generate one while the format is "none".
	ErrInvalidSBOMFormat = errors.New("invalid sbom format")

	// ErrSBOMFailed reports a failure scanning the project or serialising the
	// bill of materials.
	ErrSBOMFailed = errors.New("sbom generation failed")

	// ErrManifestInvalid reports a Kubernetes manifest that is not parseable
	// YAML.
	ErrManifestInvalid = errors.New("invalid kubernetes manifest")

	// ErrManifestUnresolved reports a pokkum:// reference that could not be
	// resolved to a digest. The message must name the document and the value.
	ErrManifestUnresolved = errors.New("unresolved image reference")

	// ErrInvalidOutputMode reports an unknown output mode.
	ErrInvalidOutputMode = errors.New("invalid output mode")

	// ErrInvalidRuntimeConfig reports a runtime configuration that cannot
	// produce a working container, such as a probe port colliding with the
	// application port.
	ErrInvalidRuntimeConfig = errors.New("invalid runtime configuration")
)
