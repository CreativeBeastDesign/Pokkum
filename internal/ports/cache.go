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
	ProjectDir          string
	BaseImageDigest     string
	BunVersion          string
	BunVariant          string
	BunCustomBinaryPath string
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
}

// RemoteCacheRequest describes a query to the remote composite OCI input cache.
type RemoteCacheRequest struct {
	Repo               string
	InputHash          string
	Tags               []string
	Insecure           bool
	UserAgent          string
	RegistryConfigPath string
}

// RemoteCacheResult is the outcome of a remote cache lookup.
type RemoteCacheResult struct {
	Hit    bool
	Digest v1.Hash
	Ref    string
	Tags   []string
}

// RemoteCacher defines the driven port for computing composite input hashes and
// querying/reconciling remote OCI container caches.
type RemoteCacher interface {
	// ComputeInputHash computes a deterministic 64-character hex SHA-256 hash of all build inputs.
	ComputeInputHash(ctx context.Context, req RemoteCacheInputRequest) (string, error)

	// Check queries whether an image matching req.InputHash exists in req.Repo.
	// On hit, it reconciles req.Tags to point to that digest and returns the result.
	Check(ctx context.Context, req RemoteCacheRequest) (RemoteCacheResult, error)
}
