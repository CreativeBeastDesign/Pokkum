package remotecacheutils

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/ignoreutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registryutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// IsUtilityPackage marks this as a reusable utility.
const IsUtilityPackage = true

var _ ports.RemoteCacher = (*Cacher)(nil)

// Cacher implements ports.RemoteCacher for composite OCI input caching.
type Cacher struct{}

// New returns a new Cacher instance.
func New() *Cacher {
	return &Cacher{}
}

// IgnoredBuildDirs lists directory basenames skipped when hashing project source trees.
var IgnoredBuildDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	".pokkum":      true,
	"build":        true,
	".svelte-kit":  true,
	".vercel":      true,
	".output":      true,
	"dist":         true,
}

// InputParams defines the complete set of inputs that determine a container build's output.
type InputParams struct {
	ProjectDir      string   `json:"-"`
	SourceTreeHash  string   `json:"source_tree_hash"`
	LockfileHash    string   `json:"lockfile_hash"`
	BaseImageDigest string   `json:"base_image_digest"`
	BunVersion      string   `json:"bun_version"`
	BunVariant      string   `json:"bun_variant"`
	Platforms       []string `json:"platforms"`
	Strategy        string   `json:"strategy"`
	Compression     string   `json:"compression"`
	NoPrune         bool     `json:"no_prune"`
	KeepVendor      []string `json:"keep_vendor"`
	NoPrecompress   bool     `json:"no_precompress"`
	NoStrip         bool     `json:"no_strip"`
	Sourcemap       bool     `json:"sourcemap"`
	RequireEnv      []string `json:"require_env"`
}

// ComputeSourceTreeHash walks projectDir and computes a deterministic SHA-256 tree hash.
func ComputeSourceTreeHash(projectDir string) (string, error) {
	ignorer, _ := ignoreutils.Load(projectDir)

	var files []string
	err := filepath.WalkDir(projectDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if IgnoredBuildDirs[name] {
				return filepath.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(projectDir, p)
		if err != nil {
			return err
		}
		slashPath := filepath.ToSlash(rel)
		if ignorer != nil && ignorer.Match(slashPath, false) {
			return nil
		}

		files = append(files, rel)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walking project source tree: %w", err)
	}

	slices.Sort(files)

	h := sha256.New()
	for _, rel := range files {
		h.Write([]byte(filepath.ToSlash(rel)))
		h.Write([]byte{0})

		f, err := os.Open(filepath.Join(projectDir, rel))
		if err != nil {
			return "", fmt.Errorf("reading project file %q: %w", rel, err)
		}
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return "", fmt.Errorf("hashing project file %q: %w", rel, err)
		}
		f.Close()
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// ComputeLockfileHash hashes existing lockfiles in projectDir.
func ComputeLockfileHash(projectDir string) (string, error) {
	lockfiles := []string{
		"bun.lock",
		"bun.lockb",
		"package-lock.json",
		"pnpm-lock.yaml",
		"yarn.lock",
	}

	h := sha256.New()
	for _, name := range lockfiles {
		p := filepath.Join(projectDir, name)
		if data, err := os.ReadFile(p); err == nil {
			h.Write([]byte(name))
			h.Write([]byte{0})
			h.Write(data)
		}
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// ComputeInputHash generates a composite 64-character hex SHA-256 hash representing the build inputs.
func ComputeInputHash(params InputParams) (string, error) {
	if params.SourceTreeHash == "" && params.ProjectDir != "" {
		stHash, err := ComputeSourceTreeHash(params.ProjectDir)
		if err != nil {
			return "", err
		}
		params.SourceTreeHash = stHash
	}

	if params.LockfileHash == "" && params.ProjectDir != "" {
		lfHash, err := ComputeLockfileHash(params.ProjectDir)
		if err != nil {
			return "", err
		}
		params.LockfileHash = lfHash
	}

	slices.Sort(params.Platforms)
	slices.Sort(params.KeepVendor)
	slices.Sort(params.RequireEnv)

	data, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("marshaling input params: %w", err)
	}

	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), nil
}

// ComputeInputHash implements ports.RemoteCacher.
func (c *Cacher) ComputeInputHash(_ context.Context, req ports.RemoteCacheInputRequest) (string, error) {
	return ComputeInputHash(InputParams{
		ProjectDir:      req.ProjectDir,
		BaseImageDigest: req.BaseImageDigest,
		BunVersion:      req.BunVersion,
		BunVariant:      req.BunVariant,
		Platforms:       slices.Clone(req.Platforms),
		Strategy:        req.Strategy,
		Compression:     req.Compression,
		NoPrune:         req.NoPrune,
		KeepVendor:      slices.Clone(req.KeepVendor),
		NoPrecompress:   req.NoPrecompress,
		NoStrip:         req.NoStrip,
		Sourcemap:       req.Sourcemap,
		RequireEnv:      slices.Clone(req.RequireEnv),
	})
}

// CacheTag formats an input hash into a registry cache tag.
func CacheTag(inputHash string) string {
	return "cache-" + inputHash
}

// Check queries whether an image matching req.InputHash exists in req.Repo.
// If present, it reconciles req.Tags and returns the result.
func (c *Cacher) Check(ctx context.Context, req ports.RemoteCacheRequest) (ports.RemoteCacheResult, error) {
	cacheTag := CacheTag(req.InputHash)
	digest, hit, err := CheckRemoteCache(ctx, req.Repo, cacheTag, req.Insecure, req.UserAgent, req.RegistryConfigPath)
	if err != nil || !hit {
		return ports.RemoteCacheResult{Hit: false}, err
	}

	// On hit, reconcile tags
	if len(req.Tags) > 0 {
		_ = ReconcileTags(ctx, req.Repo, digest, req.Tags, req.Insecure, req.UserAgent, req.RegistryConfigPath)
	}

	return ports.RemoteCacheResult{
		Hit:    true,
		Digest: digest,
		Ref:    req.Repo + "@" + digest.String(),
		Tags:   slices.Clone(req.Tags),
	}, nil
}

// CheckRemoteCache queries repo for cacheTag, returning the cached digest if present.
func CheckRemoteCache(ctx context.Context, repo string, cacheTag string, insecure bool, userAgent string, registryConfig string) (v1.Hash, bool, error) {
	nameOpts := []name.Option{name.WeakValidation}
	if insecure {
		nameOpts = append(nameOpts, name.Insecure)
	}
	ref, err := name.ParseReference(repo+":"+cacheTag, nameOpts...)
	if err != nil {
		return v1.Hash{}, false, err
	}

	kc, err := registryutils.ResolveKeychain(registryConfig)
	if err != nil {
		return v1.Hash{}, false, err
	}

	remoteOpts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(kc),
	}
	if userAgent != "" {
		remoteOpts = append(remoteOpts, remote.WithUserAgent(userAgent))
	}
	if insecure {
		remoteOpts = append(remoteOpts, remote.WithTransport(&http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		}))
	}

	desc, err := remote.Get(ref, remoteOpts...)
	if err != nil {
		var terr *transport.Error
		if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
			return v1.Hash{}, false, nil
		}
		return v1.Hash{}, false, err
	}

	return desc.Digest, true, nil
}

// ReconcileTags points target tags to an existing digest in the remote registry.
func ReconcileTags(ctx context.Context, repo string, digest v1.Hash, tags []string, insecure bool, userAgent string, registryConfig string) error {
	nameOpts := []name.Option{name.WeakValidation}
	if insecure {
		nameOpts = append(nameOpts, name.Insecure)
	}
	parsedRepo, err := name.NewRepository(repo, nameOpts...)
	if err != nil {
		return err
	}

	kc, err := registryutils.ResolveKeychain(registryConfig)
	if err != nil {
		return err
	}

	remoteOpts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(kc),
	}
	if userAgent != "" {
		remoteOpts = append(remoteOpts, remote.WithUserAgent(userAgent))
	}
	if insecure {
		remoteOpts = append(remoteOpts, remote.WithTransport(&http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		}))
	}

	digestRef := parsedRepo.Digest(digest.String())
	desc, err := remote.Get(digestRef, remoteOpts...)
	if err != nil {
		return err
	}

	for _, tag := range tags {
		tagRef := parsedRepo.Tag(tag)
		if err := remote.Tag(tagRef, desc, remoteOpts...); err != nil {
			return err
		}
	}
	return nil
}
