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
}

// AttachSBOMRequest publishes an SBOM alongside an already-pushed image, under
// the cosign/ko tag convention.
type AttachSBOMRequest struct {
	// Repo is the repository holding the subject image. Required, and must be
	// the same repository the subject was pushed to — the tag convention is
	// repository-local and an SBOM in a different repository is invisible to
	// every tool that looks for it.
	Repo string

	// Subject is the digest of the image or index the SBOM describes. Required.
	// The attachment is published at Repo:SBOMTag(Subject).
	Subject v1.Hash

	// Document is the SBOM to attach. Required. Its MediaType determines the
	// layer media type of the attachment.
	Document SBOMDocument

	// Insecure permits plain HTTP and skips TLS verification.
	Insecure bool
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
