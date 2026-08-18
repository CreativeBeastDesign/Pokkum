package ports

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// DefaultTag is the tag applied when a BuildRequest names none. It matches ko's
// behaviour and the near-universal expectation of `docker run repo`.
const DefaultTag = "latest"

// SBOMTagSuffix is the tag suffix under which an SBOM is attached to an image,
// following the cosign/ko convention: the subject's digest with ':' replaced by
// '-', then this suffix. Using a tag rather than a referrers API entry keeps the
// attachment readable by every registry, including ones that predate OCI 1.1.
const SBOMTagSuffix = ".sbom"

// SBOMTag returns the tag under which an SBOM for the image with digest h is
// attached, e.g. "sha256-abc123….sbom". This is the cosign/ko convention and is
// what `cosign download sbom` expects to find.
func SBOMTag(h v1.Hash) string {
	return h.Algorithm + "-" + h.Hex + SBOMTagSuffix
}

// SigTagSuffix is the tag suffix under which a Cosign signature is attached to
// an image: the subject's digest with ':' replaced by '-', then this suffix.
// This is the cosign tag convention — the default read path of `cosign
// verify`, of `pokkum verify`'s provenance resolver, and of the remote build
// cache's cache-hit verification — so it is the compatibility contract for
// signatures, not a legacy fallback the way SBOMTagSuffix is for SBOMs.
const SigTagSuffix = ".sig"

// AttTagSuffix is the tag suffix under which a DSSE-enveloped in-toto
// attestation (SLSA provenance) is attached to an image, following the same
// cosign tag convention as SigTagSuffix. It is what `cosign
// verify-attestation` and `pokkum verify`'s provenance resolver read.
const AttTagSuffix = ".att"

// SigTag returns the tag under which a Cosign signature for the image with
// digest h is attached, e.g. "sha256-abc123….sig".
func SigTag(h v1.Hash) string {
	return h.Algorithm + "-" + h.Hex + SigTagSuffix
}

// AttTag returns the tag under which a DSSE attestation for the image with
// digest h is attached, e.g. "sha256-abc123….att".
func AttTag(h v1.Hash) string {
	return h.Algorithm + "-" + h.Hex + AttTagSuffix
}

// CosignSignatureLayerAnnotation is the layer annotation carrying the base64
// signature over a signature image's Simple Signing payload layer. Fixed by
// cosign's wire format; internal/adapters/sigstore declares the same string
// (CosignSignatureAnnotation) for its verification-side reading — the value is
// spec-pinned, so the two cannot drift without breaking against real cosign.
const CosignSignatureLayerAnnotation = "dev.cosignproject.cosign/signature"

// MediaTypeSimpleSigning is the layer media type of a Cosign Simple Signing
// payload inside a .sig signature image, per cosign's wire format.
const MediaTypeSimpleSigning = "application/vnd.dev.cosign.simplesigning.v1+json"

// MediaTypeDSSEEnvelope is the layer media type of a DSSE envelope inside a
// .att attestation image, per cosign's wire format.
const MediaTypeDSSEEnvelope = "application/vnd.dsse.envelope.v1+json"

// Payload is the image content to publish: exactly one of Image or Index must
// be non-nil. It is shared by every publishing port so that core can build the
// payload once and hand it to whichever destination the output mode selected.
type Payload struct {
	// Image is a single-platform image. Mutually exclusive with Index.
	Image v1.Image

	// Index is a multi-platform image index. Mutually exclusive with Image.
	// Preferred whenever more than one platform was built.
	Index v1.ImageIndex
}

// IsZero reports whether neither an image nor an index was set, which is always
// a caller bug.
func (p Payload) IsZero() bool { return p.Image == nil && p.Index == nil }

// PublishResult is the outcome of sending an image somewhere: a registry, the
// local Docker daemon, or a tarball on disk. All three publishing ports return
// this same type so that core can normalise them into a single result without a
// type switch, and so that adding a fourth destination does not change core's
// shape.
type PublishResult struct {
	// Ref is the primary, immutable reference to the published image. For a
	// registry push it is "repo@sha256:…" — the digest form, because that is
	// what k8s manifests must contain and what a tag can never guarantee. For a
	// daemon load it is the tag the daemon knows the image by. For a tarball it
	// is the reference recorded inside the archive.
	Ref string

	// Digest is the digest of the published manifest or index. Always set.
	Digest v1.Hash

	// Tags are the tags that were actually written, without the repository
	// prefix. Empty for a tarball write that recorded no tag.
	Tags []string

	// Path is the filesystem path written, for the tarball destination only.
	// Empty otherwise.
	Path string

	// Size is the total transferred or written size in bytes, or zero if the
	// destination cannot report one. Informational.
	Size int64
}

// PushRequest publishes an image to a remote registry.
type PushRequest struct {
	// Repo is the destination repository without a tag or digest, e.g.
	// "ghcr.io/acme/app". Required — an empty Repo is core.ErrNoDockerRepo.
	//
	// It is a plain string rather than a name.Reference because parsing
	// registry references is the adapter's job; leaking name.Reference into the
	// port would drag go-containerregistry's naming rules into core, where they
	// cannot be tested without a registry.
	Repo string

	// Tags are the tags to apply, without the repository prefix. Empty means
	// DefaultTag. The digest is always published regardless of tags.
	Tags []string

	// Payload is the image or index to push. Required.
	Payload Payload

	// Insecure permits plain HTTP and skips TLS verification. False by default;
	// intended for local test registries.
	Insecure bool

	// UserAgent is appended to the User-Agent header sent to the registry.
	// Empty means the adapter's default.
	UserAgent string

	// RegistryConfigPath is the optional custom OCI config.json path for authentication.
	RegistryConfigPath string

	// Concurrency caps how many layers are uploaded to the registry in parallel.
	// Zero means the registry adapter uses its own default.
	Concurrency int
}

// AttachSBOMRequest publishes an SBOM alongside an already-pushed image, under
// the OCI 1.1 referrers API or cosign/ko tag convention.
type AttachSBOMRequest struct {
	// Repo is the repository holding the subject image. Required, and must be
	// the same repository the subject was pushed to — the attachment is
	// repository-local and an SBOM in a different repository is invisible to
	// every tool that looks for it.
	Repo string

	// Subject is the digest of the image or index the SBOM describes. Required.
	Subject v1.Hash

	// Document is the SBOM to attach. Required. Its MediaType determines the
	// layer media type of the attachment.
	Document SBOMDocument

	// AttachMode selects between OCI 1.1 referrer attachment (default) or tag convention.
	AttachMode SBOMAttachMode

	// Insecure permits plain HTTP and skips TLS verification.
	Insecure bool

	// RegistryConfigPath is the optional custom OCI config.json path for authentication.
	RegistryConfigPath string
}

// AttachSignatureRequest publishes a Cosign signature alongside an
// already-pushed image.
//
// Attachment is dual-published, deliberately differing from AttachSBOM's
// three-mode design: the .sig tag convention (SigTag) is ALWAYS written,
// because it is what `cosign verify` (by default), `pokkum verify`, and the
// remote build cache's cache-hit verification all read — a referrer-only
// signature would be invisible to every one of them. When the registry
// supports the OCI 1.1 Referrers API, the same signature image is
// additionally attached as a referrer of the subject (probed exactly like
// AttachSBOM's auto mode, via the same referrers-unsupported detection); a
// registry without referrers support gets the tag alone, silently, because
// the tag is the load-bearing artifact and the referrer is additive
// discoverability for OCI 1.1-aware policy engines.
type AttachSignatureRequest struct {
	// Repo is the repository holding the subject image. Required, and must be
	// the same repository the subject was pushed to.
	Repo string

	// Subject is the digest of the image manifest or index the signature
	// covers. Required.
	Subject v1.Hash

	// Bundle carries the Simple Signing payload and the signature over it.
	// PayloadBytes and Base64Signature are required.
	Bundle CosignSignatureBundle

	// Insecure permits plain HTTP and skips TLS verification.
	Insecure bool

	// RegistryConfigPath is the optional custom OCI config.json path for authentication.
	RegistryConfigPath string
}

// AttachAttestationRequest publishes a DSSE-enveloped in-toto attestation
// (SLSA provenance) alongside an already-pushed image, under the .att tag
// convention (AttTag) plus an additive OCI 1.1 referrer — the same
// dual-publication contract as AttachSignatureRequest.
type AttachAttestationRequest struct {
	// Repo is the repository holding the subject image. Required.
	Repo string

	// Subject is the digest of the image manifest or index the attestation
	// describes. Required, and must match the statement's own subject digest —
	// verifiers cross-check the two.
	Subject v1.Hash

	// Envelope is the signed DSSE envelope to attach. Required.
	Envelope DSSEEnvelope

	// Insecure permits plain HTTP and skips TLS verification.
	Insecure bool

	// RegistryConfigPath is the optional custom OCI config.json path for authentication.
	RegistryConfigPath string
}

// FetchAttachmentRequest reads back a signature or attestation previously
// attached to Subject in Repo, for post-push self-verification: the pipeline
// re-fetches what it just attached and verifies it before reporting a signed
// build as successful, so a broken attach path cannot silently ship.
type FetchAttachmentRequest struct {
	// Repo is the repository holding the subject image. Required.
	Repo string

	// Subject is the digest whose SigTag/AttTag attachment to fetch. Required.
	Subject v1.Hash

	// Insecure permits plain HTTP and skips TLS verification.
	Insecure bool

	// RegistryConfigPath is the optional custom OCI config.json path for authentication.
	RegistryConfigPath string
}

// LoadRequest imports an image into the local Docker daemon, the `--local`
// output mode.
type LoadRequest struct {
	// Repo is the repository name to register the image under in the daemon,
	// e.g. "acme/app" or "ko.local/app". Required.
	Repo string

	// Tags are the tags to apply. Empty means DefaultTag.
	Tags []string

	// Payload is the image or index to load. Required.
	//
	// The Docker daemon cannot store a multi-platform index in its classic
	// image store. When Payload carries an Index, the loader must select the
	// child image matching Platform and load that alone, rather than failing —
	// `--local` on a laptop means "give me something I can run here".
	Payload Payload

	// Platform selects which child of an index to load. Ignored when Payload
	// carries a single image. The zero value means "the daemon's own
	// platform", which the adapter discovers by inspecting the daemon.
	Platform Platform
}

// TarballRequest writes an OCI archive to disk, the `--tarball` output mode.
type TarballRequest struct {
	// Path is the destination file path. Required — core owns the choice of
	// location. An existing file is overwritten. Parent directories are created
	// if missing.
	Path string

	// Repo is the repository name recorded inside the archive so that a later
	// `docker load` knows what to call the image. Required.
	Repo string

	// Tags are the tags recorded in the archive. Empty means DefaultTag.
	Tags []string

	// Payload is the image or index to write. Required. Unlike LoadRequest, an
	// index is written faithfully: the OCI layout format represents one.
	Payload Payload
}

// Registry publishes images and their attestations to a remote registry. It is
// implemented by internal/adapters/registry.
//
// Authentication is entirely the adapter's business: it composes the ambient
// keychain (docker config, ECR/GCR/ACR helpers, environment) and none of that
// appears here. core never sees a credential.
//
// Error expectations:
//   - core.ErrNoDockerRepo when Repo is empty.
//   - core.ErrRegistryAuth when the registry rejects the credentials. This is
//     the single most common user-facing failure and deserves an error message
//     naming the registry host and the credential source that was tried.
//   - core.ErrPushFailed for any other transport or registry error.
//
// Implementations must be safe for concurrent use.
type Registry interface {
	// Push uploads the payload and every tag, returning the digest reference.
	Push(ctx context.Context, req PushRequest) (PublishResult, error)

	// AttachSBOM uploads an SBOM as a separate image tagged per SBOMTag. It
	// must be called only after the subject has been pushed; publishing an
	// attachment for a digest the registry does not have produces a dangling
	// reference that no tool will ever look at twice.
	AttachSBOM(ctx context.Context, req AttachSBOMRequest) (PublishResult, error)

	// AttachSignature uploads a Cosign signature as a separate image tagged
	// per SigTag (plus an additive OCI 1.1 referrer where supported — see
	// AttachSignatureRequest). Like AttachSBOM, it must be called only after
	// the subject has been pushed.
	AttachSignature(ctx context.Context, req AttachSignatureRequest) (PublishResult, error)

	// AttachAttestation uploads a DSSE-enveloped attestation as a separate
	// image tagged per AttTag, under the same contract as AttachSignature.
	AttachAttestation(ctx context.Context, req AttachAttestationRequest) (PublishResult, error)

	// FetchSignature reads back the signature attached under SigTag(Subject)
	// and reconstructs the bundle (payload bytes plus base64 signature) for
	// verification. It is the read half of the post-push self-verification
	// stage; an absent or malformed attachment is an error, never an empty
	// bundle.
	FetchSignature(ctx context.Context, req FetchAttachmentRequest) (CosignSignatureBundle, error)

	// FetchAttestation reads back the DSSE envelope attached under
	// AttTag(Subject), under the same contract as FetchSignature.
	FetchAttestation(ctx context.Context, req FetchAttachmentRequest) (DSSEEnvelope, error)
}

// LocalLoader imports an image into the local Docker daemon. It is a separate
// interface from Registry, not a mode flag on it, because it has a genuinely
// different failure surface (a missing daemon, not a missing credential) and
// because keeping it separate means a build that never uses `--local` cannot
// accidentally take a dependency on the daemon client.
//
// Error expectations: core.ErrDaemonUnavailable when no Docker daemon can be
// reached, core.ErrPushFailed for a load that the daemon rejected.
type LocalLoader interface {
	// Load imports the payload into the daemon under the requested tags.
	Load(ctx context.Context, req LoadRequest) (PublishResult, error)
}

// TarballWriter writes an image to a local OCI archive. This is the only output
// mode that requires neither a network nor a daemon, which makes it the mode
// air-gapped and CI-sandboxed builds use.
//
// Error expectations: core.ErrTarballFailed for any filesystem or serialisation
// failure.
type TarballWriter interface {
	// Write serialises the payload to req.Path.
	Write(ctx context.Context, req TarballRequest) (PublishResult, error)
}
