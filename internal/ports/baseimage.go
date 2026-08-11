package ports

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// BaseImagePreset names a supported base-image tier. It is a string type so
// that it round-trips through flags, config files and image labels without a
// lookup table.
type BaseImagePreset string

const (
	// BaseImageDistroless is the default: gcr.io/distroless/cc-debian12:nonroot.
	//
	// The "cc" variant is mandatory, not a preference. A Bun-compiled binary is
	// dynamically linked against libc.so.6, libstdc++.so.6 and libgcc_s.so.1;
	// distroless/static and scratch provide none of these and produce a
	// container that exits immediately with a loader error. Any resolver that
	// substitutes a static base must fail with core.ErrBaseImageIncompatible
	// rather than produce an image that cannot start.
	BaseImageDistroless BaseImagePreset = "distroless"

	// BaseImageChainguard selects cgr.dev/chainguard/glibc-dynamic, a hardened,
	// nonroot, glibc-dynamic base with more aggressive CVE patching than
	// distroless. Same libc contract, different maintainer.
	BaseImageChainguard BaseImagePreset = "chainguard"

	// BaseImageCustom means the user supplied an explicit reference in
	// BaseImageRequest.Ref. Pokkum performs the same libc compatibility check
	// but does not otherwise vet the image.
	BaseImageCustom BaseImagePreset = "custom"
)

// DefaultBaseImagePreset is the preset used when a BuildRequest leaves the
// base-image options at their zero value.
const DefaultBaseImagePreset = BaseImageDistroless

// Canonical references for the non-custom presets. They are tags, not digests:
// the resolver pins them to a digest at resolve time and reports the pin in
// BaseImage.Digest, so a build remains reproducible for as long as the caller
// feeds the resolved digest back in.
const (
	DistrolessBaseRef = "gcr.io/distroless/cc-debian12:nonroot"
	ChainguardBaseRef = "cgr.dev/chainguard/glibc-dynamic:latest"
)

// Valid reports whether p is a known preset. The zero value ("") is NOT valid;
// core normalises it to DefaultBaseImagePreset before validating.
func (p BaseImagePreset) Valid() bool {
	switch p {
	case BaseImageDistroless, BaseImageChainguard, BaseImageCustom:
		return true
	default:
		return false
	}
}

// String implements fmt.Stringer.
func (p BaseImagePreset) String() string { return string(p) }

// DefaultRef returns the canonical reference for a preset, and reports false
// for BaseImageCustom (which has no default — the caller must supply one) and
// for unknown presets.
func (p BaseImagePreset) DefaultRef() (string, bool) {
	switch p {
	case BaseImageDistroless:
		return DistrolessBaseRef, true
	case BaseImageChainguard:
		return ChainguardBaseRef, true
	default:
		return "", false
	}
}

// BaseImageRequest asks the resolver to fetch and validate the base image for
// every requested platform.
type BaseImageRequest struct {
	// Preset selects the tier. Required and must be Valid; core normalises the
	// zero value to DefaultBaseImagePreset before calling.
	Preset BaseImagePreset

	// Ref is the image reference to resolve.
	//
	// Required and non-empty when Preset is BaseImageCustom. For the other
	// presets it is optional: empty means "use Preset.DefaultRef()", and a
	// non-empty value overrides the default while keeping the preset's
	// semantics (this is how a user pins the distroless base to a digest).
	//
	// A digest reference ("…@sha256:…") makes the build fully reproducible; a
	// tag reference does not, and the resolver should say so in a warning it
	// surfaces through its logger, not through an error.
	Ref string

	// Platforms is the set of targets that must be present in the resolved
	// image. Required and non-empty. If the reference points at a single-arch
	// image, it satisfies exactly one platform and the resolver must return
	// core.ErrBaseImageIncompatible if any other platform was requested.
	Platforms []Platform

	// Insecure permits plain HTTP and skips TLS verification when pulling the
	// base image. False by default; intended for local test registries only.
	Insecure bool

	// LockfilePath is the path to pokkum.lock (optional).
	LockfilePath string

	// UpdateBase forces re-resolving base image tags against remote registry and updating pokkum.lock.
	UpdateBase bool

	// Offline strictly enforces using pokkum.lock and local cache without remote registry calls.
	Offline bool
}

// PokkumLockfileName is the canonical lockfile name.
const PokkumLockfileName = "pokkum.lock"

// BaseLockEntry records a locked base image digest in pokkum.lock.
type BaseLockEntry struct {
	Ref       string `json:"ref"`
	Digest    string `json:"digest"`
	PinnedRef string `json:"pinned_ref"`
	UpdatedAt string `json:"updated_at"`
}

// PokkumLockfile represents the structure of pokkum.lock files.
type PokkumLockfile struct {
	Version   int                      `json:"version"`
	UpdatedAt string                   `json:"updated_at"`
	Bases     map[string]BaseLockEntry `json:"bases"`
}

// BaseImage is a resolved, digest-pinned base image, decomposed into the
// per-platform images the packager needs.
type BaseImage struct {
	// Ref is the reference that was resolved, as supplied (tag or digest).
	Ref string

	// PinnedRef is Ref rewritten to its digest form, "repo@sha256:…". It is
	// what gets recorded in the image labels and in the build summary so that
	// a build can be reproduced exactly.
	PinnedRef string

	// Digest is the digest of the resolved manifest or index.
	Digest v1.Hash

	// Images maps each requested platform to its base image. It contains an
	// entry for every platform in BaseImageRequest.Platforms and no others; a
	// missing entry is a contract violation, not a signal.
	Images map[Platform]v1.Image

	// IsIndex reports whether Ref resolved to a multi-platform index rather
	// than a single image. Informational.
	IsIndex bool
}

// BaseImageResolver fetches the base image and proves it can actually run a
// Bun-compiled binary. It is implemented by internal/adapters/registry.
//
// The libc check is the point of this port. Resolving is otherwise a thin
// wrapper over a remote pull; what earns it a place in the hexagon is that it
// is the single gate preventing the most likely user-facing failure in the
// whole tool — a beautifully built image that dies on start with
// "no such file or directory" because the loader is missing.
//
// Error expectations:
//   - core.ErrBaseImageIncompatible when the resolved image lacks a dynamic
//     loader / libc (scratch, distroless static, a musl-only base), or when it
//     does not cover every requested platform. The error message must name the
//     offending reference and say what was missing.
//   - core.ErrInvalidBaseImage when Ref is unparseable or Preset is unknown.
//   - core.ErrRegistryAuth when the pull is rejected for credentials.
//
// Implementations must be safe for concurrent use.
type BaseImageResolver interface {
	// Resolve pulls the base image and returns one v1.Image per requested
	// platform. It must not mutate the returned images.
	Resolve(ctx context.Context, req BaseImageRequest) (*BaseImage, error)
}
