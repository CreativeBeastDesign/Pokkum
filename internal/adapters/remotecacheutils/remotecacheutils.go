package remotecacheutils

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/ignoreutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/poolutils"
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

// InputParams defines the complete set of inputs that determine a container
// build's output. See ports.RemoteCacheInputRequest's doc comment for why
// this must stay exhaustive, and why Sign is deliberately excluded.
type InputParams struct {
	ProjectDir          string                 `json:"-"`
	SourceTreeHash      string                 `json:"source_tree_hash"`
	LockfileHash        string                 `json:"lockfile_hash"`
	BaseImageDigest     string                 `json:"base_image_digest"`
	BunVersion          string                 `json:"bun_version"`
	BunVariant          string                 `json:"bun_variant"`
	BunCustomBinaryPath string                 `json:"bun_custom_binary_path"`
	Platforms           []string               `json:"platforms"`
	Strategy            string                 `json:"strategy"`
	Compression         string                 `json:"compression"`
	NoPrune             bool                   `json:"no_prune"`
	KeepVendor          []string               `json:"keep_vendor"`
	NoPrecompress       bool                   `json:"no_precompress"`
	NoStrip             bool                   `json:"no_strip"`
	NoInject            bool                   `json:"no_inject"`
	NoMinify            bool                   `json:"no_minify"`
	MinBunVersion       string                 `json:"min_bun_version"`
	CompileEnv          []string               `json:"compile_env"`
	Sourcemap           bool                   `json:"sourcemap"`
	Hermetic            bool                   `json:"hermetic"`
	SourceDateEpochUnix int64                  `json:"source_date_epoch_unix"`
	Runtime             ports.RuntimeConfig    `json:"runtime"`
	Telemetry           ports.TelemetryOptions `json:"telemetry"`
	Labels              map[string]string      `json:"labels"`
	Annotations         map[string]string      `json:"annotations"`
	SBOMFormat          string                 `json:"sbom_format"`
	SBOMAttachMode      string                 `json:"sbom_attach_mode"`
	SBOMNoAttach        bool                   `json:"sbom_no_attach"`
}

// ComputeSourceTreeHash walks projectDir and computes a deterministic SHA-256
// tree hash.
//
// Each entry is folded into the outer hash as path + NUL + kind + NUL +
// contentDigest + NUL — never as path + NUL + raw content directly. Raw
// content interleaved with NUL-delimited paths is forgeable: a file whose
// bytes happen to contain "<nextpath>\x00<nextcontent>" can be crafted so a
// tree hashes identically to a different tree with genuinely more files (a
// second-preimage attack on the framing, not on SHA-256 itself). Folding in
// a fixed-length (64 hex chars), NUL-free content digest instead removes the
// ambiguity: a digest can never be mistaken for a path/kind separator, so
// there is no boundary left to forge.
//
// kind also records whether the entry is a regular file, an executable file
// (owner-executable bit set — the one permission bit that changes what a
// build actually does with a file), or a symlink. Symlinks are hashed by
// their target string, never dereferenced: following a symlink would make
// the tree hash depend on state outside projectDir, which is neither
// reproducible across machines nor a real "build input" in the sense this
// hash is meant to capture.
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
		digestHex, kind, err := hashTreeEntry(filepath.Join(projectDir, rel))
		if err != nil {
			return "", fmt.Errorf("hashing project file %q: %w", rel, err)
		}
		h.Write([]byte(filepath.ToSlash(rel)))
		h.Write([]byte{0})
		h.Write([]byte(kind))
		h.Write([]byte{0})
		h.Write([]byte(digestHex))
		h.Write([]byte{0})
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashTreeEntry hashes one filesystem entry's identity for
// ComputeSourceTreeHash. It returns a fixed-length hex SHA-256 digest and a
// one-character kind tag: "f" for a regular file, "x" for a file with the
// owner-executable bit set, "l" for a symlink (hashed by its target path,
// never dereferenced).
func hashTreeEntry(fullPath string) (digestHex, kind string, err error) {
	info, err := os.Lstat(fullPath)
	if err != nil {
		return "", "", err
	}

	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(fullPath)
		if err != nil {
			return "", "", err
		}
		sum := sha256.Sum256([]byte(target))
		return hex.EncodeToString(sum[:]), "l", nil
	}

	f, err := os.Open(fullPath)
	if err != nil {
		return "", "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := poolutils.Copy(h, f); err != nil {
		return "", "", err
	}

	kind = "f"
	if info.Mode()&0o111 != 0 {
		kind = "x"
	}
	return hex.EncodeToString(h.Sum(nil)), kind, nil
}

// ComputeLockfileHash hashes existing lockfiles in projectDir.
//
// Like ComputeSourceTreeHash, each lockfile is folded in as
// name + NUL + fixed-length contentDigest + NUL rather than
// name + NUL + raw content, for the same reason: raw content directly
// following a NUL-delimited name is forgeable across the boundary, a
// fixed-length hex digest is not.
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
			sum := sha256.Sum256(data)
			h.Write([]byte(name))
			h.Write([]byte{0})
			h.Write([]byte(hex.EncodeToString(sum[:])))
			h.Write([]byte{0})
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
	slices.Sort(params.CompileEnv)
	slices.Sort(params.Runtime.RequireEnv)
	slices.Sort(params.Runtime.ExposedPorts)

	data, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("marshaling input params: %w", err)
	}

	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), nil
}

// ComputeInputHash implements ports.RemoteCacher.
func (c *Cacher) ComputeInputHash(_ context.Context, req ports.RemoteCacheInputRequest) (string, error) {
	// Runtime's slice fields are cloned before being handed to
	// ComputeInputHash, which sorts some of them in place — req.Runtime may
	// share its backing arrays with state the caller (internal/core/pipeline.go)
	// still uses after this call returns, and sorting must never mutate
	// caller-owned data as a side effect.
	runtime := req.Runtime
	runtime.Entrypoint = slices.Clone(req.Runtime.Entrypoint)
	runtime.Cmd = slices.Clone(req.Runtime.Cmd)
	runtime.RequireEnv = slices.Clone(req.Runtime.RequireEnv)
	runtime.ExposedPorts = slices.Clone(req.Runtime.ExposedPorts)

	return ComputeInputHash(InputParams{
		ProjectDir:          req.ProjectDir,
		BaseImageDigest:     req.BaseImageDigest,
		BunVersion:          req.BunVersion,
		BunVariant:          req.BunVariant,
		BunCustomBinaryPath: req.BunCustomBinaryPath,
		Platforms:           slices.Clone(req.Platforms),
		Strategy:            req.Strategy,
		Compression:         req.Compression,
		NoPrune:             req.NoPrune,
		KeepVendor:          slices.Clone(req.KeepVendor),
		NoPrecompress:       req.NoPrecompress,
		NoStrip:             req.NoStrip,
		NoInject:            req.NoInject,
		NoMinify:            req.NoMinify,
		MinBunVersion:       req.MinBunVersion,
		CompileEnv:          slices.Clone(req.CompileEnv),
		Sourcemap:           req.Sourcemap,
		Hermetic:            req.Hermetic,
		SourceDateEpochUnix: req.SourceDateEpochUnix,
		Runtime:             runtime,
		Telemetry:           req.Telemetry,
		Labels:              req.Labels,
		Annotations:         req.Annotations,
		SBOMFormat:          req.SBOMFormat,
		SBOMAttachMode:      req.SBOMAttachMode,
		SBOMNoAttach:        req.SBOMNoAttach,
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

	// On hit, reconcile tags. A reconciliation failure means the release
	// tags were NOT actually moved to the cache-hit digest, so this must not
	// be reported as a hit — doing so would tell the caller the build
	// succeeded while the requested tags silently still point at whatever
	// they pointed at before (or don't exist at all). Falling through to
	// Hit: false makes the caller run a real build instead, which publishes
	// the tags itself through the normal, already-correct publish path.
	if len(req.Tags) > 0 {
		if err := ReconcileTags(ctx, req.Repo, digest, req.Tags, req.Insecure, req.UserAgent, req.RegistryConfigPath); err != nil {
			return ports.RemoteCacheResult{Hit: false}, fmt.Errorf("remote cache hit but reconciling tags failed: %w", err)
		}
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
