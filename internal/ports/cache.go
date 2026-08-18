package ports

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// RemoteCacheInputRequest holds the build parameters needed to calculate the
// deterministic input hash. It must carry every input that changes what
// bytes actually end up in the built image — the point of the hash is that
// two builds with the same hash are guaranteed interchangeable, so an
// omitted field here is a correctness bug: two builds that differ only in
// that field would collide on the same cache tag and one would silently
// serve the other's image.
//
// Runtime and Telemetry are embedded as whole structs rather than
// hand-flattened field by field, specifically so that a field added to
// either of those types in the future is automatically covered here too,
// instead of requiring every caller of this hash to remember to update it.
//
// Sign is deliberately NOT part of this hash. Whether a cache hit is even
// consulted for a signed build is decided by the caller before it ever
// reaches this struct (see internal/core/pipeline.go) — a signed build must
// always run a fresh build and a fresh sign, never adopt a previously-cached
// digest that may have been pushed by a different, less-trusted actor with
// only cache-tag push access. Including Sign in the hash would not fix that:
// an attacker computing a colliding hash can set Sign the same way.
type RemoteCacheInputRequest struct {
	ProjectDir      string
	BaseImageDigest string

	// AppRuntime is the image's application runtime ("bun" or "node") — the
	// second dimension on everything BunVersion already keys. Omitting it
	// would be exactly the correctness bug this struct's doc comment warns
	// about: a Bun-built and a Node-built image of the same source differ in
	// their runtime layer and entrypoint but could otherwise hash
	// identically whenever they happen to share a base digest, letting one
	// silently serve a cache hit for the other. (Today the two runtimes'
	// DEFAULT bases differ, so BaseImageDigest usually separates them too —
	// but a custom --base shared across both runtimes makes that protection
	// evaporate, so the runtime must be keyed in its own right.)
	AppRuntime string

	BunVersion          string
	BunVariant          string
	BunCustomBinaryPath string
	StubLauncher        bool
	Platforms           []string
	Strategy            string
	Compression         string
	NoPrune             bool
	KeepVendor          []string
	NoPrecompress       bool
	NoStrip             bool
	NoInject            bool
	NoMinify            bool
	MinBunVersion       string
	CompileEnv          []string
	Sourcemap           bool
	Hermetic            bool
	SourceDateEpochUnix int64
	Runtime             RuntimeConfig
	Telemetry           TelemetryOptions
	Labels              map[string]string
	Annotations         map[string]string
	SBOMFormat          string
	SBOMAttachMode      string
	SBOMNoAttach        bool

	// AssetOverlaySourceDigests is the resolved list of predecessor image
	// sources --asset-overlay pulled content from (empty when the flag is
	// unused or resolved zero generations). Included so two builds that
	// differ only in which predecessors were found — e.g. one run right
	// after a new generation was pushed, another before — never collide on
	// the same cache tag: a cache hit must only ever be served to a build
	// whose overlay content would be byte-identical to what's cached.
	//
	// Despite the field name, an entry is not always a bare digest: one
	// resolved via --asset-overlay's auto-discovery (predecessor-chain
	// walk) is a bare digest at this build's own repo, but one resolved via
	// an explicit --asset-overlay-from ref is a fully-qualified "repo@digest"
	// string, since that ref may name a different repository entirely (see
	// internal/adapters/assetoverlay.ResolveDigest). Both forms are equally
	// valid cache-key inputs — the field just needs to change whenever the
	// actual resolved source set changes, regardless of representation —
	// and using the qualified form where available is what lets two
	// same-digest sources in different repositories be told apart, rather
	// than colliding on the same cache key.
	AssetOverlaySourceDigests []string
}

// RemoteCacheVerifyMode specifies how signature verification on cache-hit images is performed.
type RemoteCacheVerifyMode string

const (
	// CacheVerifyAuto selects keyless if expected identity is set, or static-key if public key is set.
	CacheVerifyAuto RemoteCacheVerifyMode = "auto"
	// CacheVerifyStaticKey enforces Cosign Simple Signing verification with a static public key.
	CacheVerifyStaticKey RemoteCacheVerifyMode = "static-key"
	// CacheVerifyKeyless enforces Sigstore keyless verification (Fulcio+Rekor).
	CacheVerifyKeyless RemoteCacheVerifyMode = "keyless"
	// CacheVerifyNone disables signature verification on cache hits.
	CacheVerifyNone RemoteCacheVerifyMode = "none"
)

// Valid reports whether m is one of the recognized verification modes.
func (m RemoteCacheVerifyMode) Valid() bool {
	switch m {
	case CacheVerifyAuto, CacheVerifyStaticKey, CacheVerifyKeyless, CacheVerifyNone:
		return true
	default:
		return false
	}
}

// RemoteCacheVerifyOptions specifies criteria for cryptographically verifying a candidate cache hit
// before promoting release tags or accepting the hit.
type RemoteCacheVerifyOptions struct {
	// VerifySignature enables signature verification on candidate cache-hit images.
	VerifySignature bool

	// VerifyMode selects between auto, static-key, and keyless verification.
	VerifyMode RemoteCacheVerifyMode

	// PublicKeyPEM is the PEM-encoded public key bytes for static-key verification.
	PublicKeyPEM []byte

	// KeylessIdentity is the expected certificate identity for keyless Sigstore verification.
	KeylessIdentity KeylessIdentity

	// TrustedRootJSON is the optional custom Sigstore trusted root snapshot.
	TrustedRootJSON []byte

	// Strict causes verification failure to return an error rather than falling back to a fresh build.
	Strict bool
}

// RemoteCacheRequest describes a query to the remote composite OCI input cache.
type RemoteCacheRequest struct {
	Repo               string
	InputHash          string
	Tags               []string
	Insecure           bool
	UserAgent          string
	RegistryConfigPath string
	Verify             RemoteCacheVerifyOptions
}

// RemoteCacheResult is the outcome of a remote cache lookup.
type RemoteCacheResult struct {
	Hit            bool
	Digest         v1.Hash
	Ref            string
	Tags           []string
	Verified       bool
	SignerIdentity string
}

// RemoteCacher defines the driven port for computing composite input hashes and
// querying/reconciling remote OCI container caches.
type RemoteCacher interface {
	// ComputeInputHash computes a deterministic 64-character hex SHA-256 hash of all build inputs.
	ComputeInputHash(ctx context.Context, req RemoteCacheInputRequest) (string, error)

	// Check queries whether an image matching req.InputHash exists in req.Repo.
	// On hit and successful verification (if enabled), it reconciles req.Tags to point to that digest and returns the result.
	Check(ctx context.Context, req RemoteCacheRequest) (RemoteCacheResult, error)
}
