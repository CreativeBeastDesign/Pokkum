package ports

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// RemoteCacheInputRequest holds the build parameters needed to calculate the deterministic input hash.
type RemoteCacheInputRequest struct {
	ProjectDir      string
	BaseImageDigest string
	BunVersion      string
	BunVariant      string
	Platforms       []string
	Strategy        string
	Compression     string
	NoPrune         bool
	KeepVendor      []string
	NoPrecompress   bool
	NoStrip         bool
	Sourcemap       bool
	RequireEnv      []string
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
