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

// BaseImageVerifyMode selects how base image signature verification is
// performed. The zero value (BaseImageVerifyAuto) defers to the preset's
// BaseImagePreset.DefaultVerifyMode().
type BaseImageVerifyMode string

const (
	// BaseImageVerifyAuto lets the preset decide (distroless/chainguard ->
	// keyless; custom -> static-key). This is the zero value.
	BaseImageVerifyAuto BaseImageVerifyMode = ""

	// BaseImageVerifyKeyless verifies a keyless Sigstore signature (Fulcio +
	// Rekor) against a KeylessIdentity. Never falls back to static-key
	// verification if no keyless signature material is found — that would be
	// a silent downgrade.
	BaseImageVerifyKeyless BaseImageVerifyMode = "keyless"

	// BaseImageVerifyStaticKey verifies a Cosign signature against a static
	// public key (POKKUM_BASE_IMAGE_PUBKEY or the embedded default),
	// unchanged from the pre-existing behavior.
	BaseImageVerifyStaticKey BaseImageVerifyMode = "static-key"
)

// Valid reports whether m is a known verification mode. The zero value IS
// valid (it means "auto").
func (m BaseImageVerifyMode) Valid() bool {
	switch m {
	case BaseImageVerifyAuto, BaseImageVerifyKeyless, BaseImageVerifyStaticKey:
		return true
	default:
		return false
	}
}

// String implements fmt.Stringer.
func (m BaseImageVerifyMode) String() string { return string(m) }

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

// StaticBaseRef is the default base image for --strategy=static builds: a
// fully static, libc-free image that can run the statically linked pokkum-static
// PID-1 server. It is used whenever a static build does not pin an explicit
// base via --base; see internal/ports/packager.go for the runtime contract the
// static server satisfies on such a base.
const StaticBaseRef = "cgr.dev/chainguard/static:latest"

// Keyless Sigstore identity constants for the stock distroless/chainguard
// presets. Verified on 2026-08-12 by pulling live Cosign signature artifacts
// and decoding their Fulcio leaf certificates; see the doc comment on
// DefaultKeylessIdentity for the re-verification procedure. These are
// security policy, not incidental values — do not "fix" a build failure by
// loosening them without re-deriving from a fresh pull first.
//
// Evidence behind these values (all certs chained to O=sigstore.dev,
// CN=sigstore-intermediate under the sigstore.dev public-good root, with
// ~10-minute validity windows, i.e. genuine ephemeral Fulcio keyless certs):
//
//   - gcr.io/distroless/cc-debian12:nonroot @ sha256:adcd20c7b4c988b73cbfbdd
//     b26d2eee574571e6d7c9ffea29b3821e0690efb77 — SAN
//     "email:keyless@distroless.iam.gserviceaccount.com", OIDC issuer
//     extension (both OID .1.1 and .1.8) "https://accounts.google.com".
//     Cross-checked against gcr.io/distroless/base-debian12:nonroot and two
//     historical cc-debian12 signature tags (certs issued 2024-10-25 and
//     2025-10-13). Those historical signature tags carry many signature
//     layers each (re-signings); all 27 leaf certs decoded agree exactly on
//     both the SAN and the issuer.
//
//   - cgr.dev/chainguard/glibc-dynamic:latest @ sha256:df4e22a4b5dcd8e15a51f
//     e9b04e16717d411dd9f4fe4b3844c1bf425b14be303 — SAN
//     "URI:https://github.com/chainguard-images/images/.github/workflows/rel
//     ease.yaml@refs/heads/main", issuer extension
//     "https://token.actions.githubusercontent.com".
const (
	DistrolessKeylessIssuer = "https://accounts.google.com"
	DistrolessKeylessSAN    = "keyless@distroless.iam.gserviceaccount.com"

	ChainguardKeylessIssuer = "https://token.actions.githubusercontent.com"

	// ChainguardKeylessSAN is matched exactly, not by regex, and that is a
	// deliberate, evidence-backed choice rather than an oversight.
	//
	// The obvious worry with a GitHub Actions signer is that the SAN embeds
	// run-specific detail, which would force a regex anchored on the stable
	// workflow path. That worry does not hold here. The GitHub Actions Fulcio
	// SAN is the *workflow ref* — "<repo URL>/<workflow path>@<git ref>" — and
	// carries no run identity at all. Everything that varies per build lives in
	// other certificate extensions, not the SAN: the build-signer digest
	// (OID 1.3.6.1.4.1.57264.1.3/.1.10/.1.13), the run invocation URI
	// (.1.21, e.g. ".../actions/runs/31639200520/attempts/1"), and the run ID
	// (.1.15).
	//
	// Confirmed empirically by decoding glibc-dynamic signature certificates
	// from three builds spanning three years — leaf certs issued 2023-03-10,
	// 2025-08-08 and 2026-08-12 — plus cgr.dev/chainguard/static:latest. The
	// per-run extensions differ in every one of them; this SAN string is
	// byte-identical in all four. An exact match is therefore strictly
	// stronger than a regex here and costs nothing.
	//
	// If Chainguard ever renames the workflow file or releases from a
	// non-main ref, this constant will stop matching and builds will fail
	// closed. That is the intended behavior: re-run the procedure documented
	// on DefaultKeylessIdentity and update the constant to the newly observed
	// value. Do not replace it with a loose regex to make the failure go away.
	ChainguardKeylessSAN = "https://github.com/chainguard-images/images/.github/workflows/release.yaml@refs/heads/main"
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

// DefaultVerifyMode returns the verification mode a preset uses when the
// caller leaves BaseImageRequest.VerifyMode at BaseImageVerifyAuto.
// distroless and chainguard verify keyless by default, since neither project
// signs with a static key; custom defaults to static-key, the pre-existing
// behavior for a self-signed base image.
func (p BaseImagePreset) DefaultVerifyMode() BaseImageVerifyMode {
	switch p {
	case BaseImageDistroless, BaseImageChainguard:
		return BaseImageVerifyKeyless
	default:
		return BaseImageVerifyStaticKey
	}
}

// DefaultKeylessIdentity returns the expected Fulcio certificate identity for
// a preset's upstream signer, and reports false for presets with no known
// default (BaseImageCustom, or an unrecognized preset — the caller must
// supply BaseImageRequest.KeylessIdentity explicitly in that case).
//
// Re-verification procedure: resolve the image to a digest, fetch its
// "<repo>:<alg>-<hex>.sig" signature tag manifest, and read the
// dev.sigstore.cosign/certificate annotation off each signature layer (it is
// plain PEM text, not base64, for both presets today — handle either). Decode
// it as an X.509 certificate and inspect its Subject Alternative Name and the
// Fulcio OIDC-issuer extension (OID 1.3.6.1.4.1.57264.1.8, or the older
// 1.3.6.1.4.1.57264.1.1):
//
//	openssl x509 -in cert.pem -noout -text
//
// A signature tag may carry more than one signature layer (older distroless
// tags carry many, from repeated re-signings); decode every layer and confirm
// they agree before trusting any one of them. Sanity-check that the leaf is
// short-lived (~10 minutes) and chains to O=sigstore.dev,
// CN=sigstore-intermediate — that is what distinguishes a real keyless Fulcio
// cert from an unrelated CA.
//
// Re-run this before ever changing the values below — they are the sole gate
// on whether Pokkum trusts an upstream base image signature.
func (p BaseImagePreset) DefaultKeylessIdentity() (KeylessIdentity, bool) {
	switch p {
	case BaseImageDistroless:
		return KeylessIdentity{
			Issuer: DistrolessKeylessIssuer,
			SAN:    DistrolessKeylessSAN,
		}, true
	case BaseImageChainguard:
		return KeylessIdentity{
			Issuer: ChainguardKeylessIssuer,
			SAN:    ChainguardKeylessSAN,
		}, true
	default:
		return KeylessIdentity{}, false
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

	// VerifySignature verifies Cosign signatures on upstream base images when pulling. True by default.
	VerifySignature bool

	// VerifyMode selects how VerifySignature is performed. Empty
	// (BaseImageVerifyAuto) defers to Preset.DefaultVerifyMode(). Ignored
	// when VerifySignature is false.
	VerifyMode BaseImageVerifyMode

	// KeylessIdentity is the expected certificate identity for keyless
	// verification. Empty means "use Preset.DefaultKeylessIdentity()"; if
	// that also has no default (e.g. BaseImageCustom) and the effective
	// VerifyMode is keyless, resolution must fail rather than verify against
	// an empty identity.
	KeylessIdentity KeylessIdentity

	// TrustedRootPath is an optional path to a Sigstore trusted-root JSON
	// file overriding the embedded public-good default. Empty means use the
	// embedded default.
	TrustedRootPath string

	// RegistryConfigPath is the optional custom OCI config.json path for authentication.
	RegistryConfigPath string

	// MirrorRegistry is the optional mirror registry repository for base image escrow (e.g. ghcr.io/myorg/base-mirror).
	MirrorRegistry string

	// AllowStatic permits base classes that carry no dynamic loader or libc
	// (distroless/static, scratch) to pass the resolver's static-base gate.
	// It must only be set when the built image runs a fully static payload —
	// i.e. --strategy=static, whose pokkum-static PID-1 binary has zero dynamic
	// dependencies. It must be false for the layered and exe strategies, whose
	// Bun-compiled server binaries are dynamically linked against glibc.
	AllowStatic bool
}

// PokkumLockfileName is the canonical lockfile name.
const PokkumLockfileName = "pokkum.lock"

// BaseLockEntry records a locked base image digest in pokkum.lock.
type BaseLockEntry struct {
	Ref                  string `json:"ref"`
	Digest               string `json:"digest"`
	PinnedRef            string `json:"pinned_ref"`
	MirrorRef            string `json:"mirror_ref,omitempty"`
	UpdatedAt            string `json:"updated_at"`
	LastScannedAt        string `json:"last_scanned_at,omitempty"`
	VulnerabilitiesCount int    `json:"vulnerabilities_count,omitempty"`
	MaxSeverity          string `json:"max_severity,omitempty"`
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

	// UpstreamRef is the pristine reference naming the image's true upstream
	// repository — unlike Ref, it is never rebound to a mirror or a locked
	// pinned-digest form. It exists for the docker-reference claims check
	// during signature verification, which must always be evaluated against
	// the name a real upstream signature actually embeds, never against
	// whatever mirror the bytes happened to be fetched through.
	UpstreamRef string

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

	// LastScannedAt is the RFC3339 timestamp when the base image was last audited for vulnerabilities.
	LastScannedAt string

	// VulnerabilitiesCount is the number of vulnerabilities found in the last scan.
	VulnerabilitiesCount int

	// MaxSeverity is the maximum severity level detected in the last scan.
	MaxSeverity string
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

	// RecordScanResult updates the locked base image entry in pokkum.lock with the latest scan findings.
	RecordScanResult(ctx context.Context, lockfilePath string, preset BaseImagePreset, scan ScanResult) error

	// VerifyBaseImage verifies the Cosign/Sigstore signature of an
	// already-resolved base image. resolved must be the exact value a prior
	// Resolve(ctx, req) call returned for this req (same Ref/Preset/
	// Platforms/Insecure/LockfilePath/UpdateBase/RegistryConfigPath/
	// MirrorRegistry) — implementations verify resolved.Ref/resolved.Digest
	// directly rather than re-deriving them from req, so this never re-reads
	// the lockfile and never re-runs mirror/escrow logic. req.VerifySignature
	// is ignored — verification always runs when this method is called. If
	// resolved.Digest no longer matches what a fresh pull of resolved.Ref
	// returns (e.g. a floating tag moved between the two calls),
	// implementations must fail closed with core.ErrBaseSignatureInvalid
	// rather than silently verifying a different manifest than the one
	// resolved. Safe to call concurrently with other pipeline stages for the
	// same build (it does not mutate shared state visible to the caller).
	VerifyBaseImage(ctx context.Context, resolved *BaseImage, req BaseImageRequest) error
}
