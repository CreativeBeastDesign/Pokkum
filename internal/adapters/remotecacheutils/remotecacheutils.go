package remotecacheutils

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/ignoreutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/keymaterialutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/poolutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registryutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/transportutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// IsUtilityPackage marks this as a reusable utility.
const IsUtilityPackage = true

var _ ports.RemoteCacher = (*Cacher)(nil)

// Cacher implements ports.RemoteCacher for composite OCI input caching.
type Cacher struct {
	log     *slog.Logger
	signer  ports.CosignSigner
	keyless ports.KeylessVerifier

	// sessions memoises one authenticated registry session per distinct
	// remote option set, so that the three network phases of a single Check —
	// the cache-tag probe, the signature fetch, and the tag reconciliation —
	// share one session instead of opening one each. See
	// registryutils.SessionCache.
	sessions registryutils.SessionCache
}

// sessionKey identifies one distinct remote option set. It carries every
// dimension sessionOptions varies on, so a probe against an insecure registry
// can never borrow the secure session's transport and a probe using a custom
// config.json can never borrow another config's credentials.
type sessionKey struct {
	insecure           bool
	userAgent          string
	registryConfigPath string
}

// sessionOptions builds the option set every registry operation in this
// package runs on. It deliberately carries no remote.WithContext: every
// registryutils.Session method takes the caller's context per call, which is
// what makes a session safe to memoise across operations.
func sessionOptions(insecure bool, userAgent, registryConfig string) ([]remote.Option, error) {
	kc, err := registryutils.ResolveKeychain(registryConfig)
	if err != nil {
		return nil, err
	}
	opts := []remote.Option{remote.WithAuthFromKeychain(kc)}
	if userAgent != "" {
		opts = append(opts, remote.WithUserAgent(userAgent))
	}
	if insecure {
		opts = append(opts, remote.WithTransport(transportutils.InsecureTransport()))
	}
	return opts, nil
}

// remoteSession returns the Cacher's memoised session for this option set.
func (c *Cacher) remoteSession(insecure bool, userAgent, registryConfig string) (*registryutils.Session, error) {
	opts, err := sessionOptions(insecure, userAgent, registryConfig)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return registryutils.NewSession(opts...), nil
	}
	return c.sessions.Session(sessionKey{insecure: insecure, userAgent: userAgent, registryConfigPath: registryConfig}, opts...), nil
}

// nameOptions builds the reference-parsing options every reference in this
// package is parsed with.
func nameOptions(insecure bool) []name.Option {
	opts := []name.Option{name.WeakValidation}
	if insecure {
		opts = append(opts, name.Insecure)
	}
	return opts
}

// Option configures a Cacher instance.
//
// Neither verifier dependency has a default. Both were previously defaulted at
// point of use by constructing cosign.NewSigner(c.log) / sigstore.NewVerifier(
// c.log) directly, which made this package import two concrete port adapters
// and wire them behind the composition root's back. Injection is now the only
// source; cmd/pokkum supplies both. A Cacher built without them still caches —
// but verifyCandidate refuses the candidate outright rather than promoting an
// unverified cache hit, exactly as it does for a missing key or an
// unrecognized verify mode.
type Option func(*Cacher)

// WithLogger sets the logger for the Cacher.
func WithLogger(log *slog.Logger) Option {
	return func(c *Cacher) {
		if log != nil {
			c.log = log
		}
	}
}

// WithCosignSigner sets the Cosign signer/verifier for the Cacher.
func WithCosignSigner(signer ports.CosignSigner) Option {
	return func(c *Cacher) {
		if signer != nil {
			c.signer = signer
		}
	}
}

// WithKeylessVerifier sets the Sigstore keyless verifier for the Cacher.
func WithKeylessVerifier(verifier ports.KeylessVerifier) Option {
	return func(c *Cacher) {
		if verifier != nil {
			c.keyless = verifier
		}
	}
}

// New returns a new Cacher instance.
func New(opts ...Option) *Cacher {
	c := &Cacher{
		log: slog.Default(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(c)
		}
	}
	return c
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
	ProjectDir      string `json:"-"`
	SourceTreeHash  string `json:"source_tree_hash"`
	LockfileHash    string `json:"lockfile_hash"`
	BaseImageDigest string `json:"base_image_digest"`

	// AppRuntime is the image's application runtime ("bun" or "node"). A
	// correctness-critical key dimension, not metadata: two builds
	// differing only in runtime produce different images (runtime layer,
	// entrypoint), so omitting it would let one serve a cache hit for the
	// other. The json tag has NO omitempty deliberately — an empty value
	// must still hash differently from a populated one, and adding the
	// field changes every pre-existing hash exactly once (a universal cache
	// miss on upgrade, the safe direction).
	AppRuntime string `json:"app_runtime"`

	BunVersion                string                 `json:"bun_version"`
	BunVariant                string                 `json:"bun_variant"`
	BunCustomBinaryPath       string                 `json:"bun_custom_binary_path"`
	StubLauncher              bool                   `json:"stub_launcher"`
	Platforms                 []string               `json:"platforms"`
	Strategy                  string                 `json:"strategy"`
	Compression               string                 `json:"compression"`
	NoPrune                   bool                   `json:"no_prune"`
	KeepVendor                []string               `json:"keep_vendor"`
	NoPrecompress             bool                   `json:"no_precompress"`
	NoStrip                   bool                   `json:"no_strip"`
	NoInject                  bool                   `json:"no_inject"`
	NoMinify                  bool                   `json:"no_minify"`
	MinBunVersion             string                 `json:"min_bun_version"`
	CompileEnv                []string               `json:"compile_env"`
	Sourcemap                 bool                   `json:"sourcemap"`
	Hermetic                  bool                   `json:"hermetic"`
	SourceDateEpochUnix       int64                  `json:"source_date_epoch_unix"`
	Runtime                   ports.RuntimeConfig    `json:"runtime"`
	Telemetry                 ports.TelemetryOptions `json:"telemetry"`
	Labels                    map[string]string      `json:"labels"`
	Annotations               map[string]string      `json:"annotations"`
	SBOMFormat                string                 `json:"sbom_format"`
	SBOMAttachMode            string                 `json:"sbom_attach_mode"`
	SBOMNoAttach              bool                   `json:"sbom_no_attach"`
	AssetOverlaySourceDigests []string               `json:"asset_overlay_source_digests"`
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

	entries, err := hashTreeEntries(projectDir, files)
	if err != nil {
		return "", err
	}

	// The fold below is the cache key's wire format and MUST NOT change: it
	// runs over `files` in its already-sorted order, reading the per-entry
	// results positionally, and writes path\0kind\0digest\0 exactly as the
	// original single-threaded loop did. Parallelism above changes only *when*
	// each entry's digest is computed, never the order or framing in which the
	// results are folded — anything else silently invalidates every remote
	// cache tag that exists in the wild. TestComputeSourceTreeHash_Golden
	// pins the resulting digest for a fixed fixture tree.
	h := sha256.New()
	for i, rel := range files {
		h.Write([]byte(filepath.ToSlash(rel)))
		h.Write([]byte{0})
		h.Write([]byte(entries[i].kind))
		h.Write([]byte{0})
		h.Write([]byte(entries[i].digestHex))
		h.Write([]byte{0})
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// treeEntry is one file's contribution to the source tree hash, held in the
// caller's sorted-file-list position.
type treeEntry struct {
	digestHex string
	kind      string
	err       error
}

// hashTreeEntries computes the per-entry digest and kind for every path in
// files, in parallel, and returns them indexed by files' own (already sorted)
// order.
//
// Hashing a ~1000-file / 6MB SvelteKit source tree is a pure Lstat + read +
// SHA-256 workload with no cross-entry dependencies, and it sits on the
// critical path of every build: nothing else can start until the remote cache
// key is known. Running it one file at a time left the machine's other cores
// idle for the whole of it.
//
// Concurrency is bounded by GOMAXPROCS, and work is handed out by an atomic
// cursor rather than by pre-slicing the list, so the handful of large files in
// a typical tree do not strand one worker while the others finish early.
//
// Ordering is not a concern here by construction: nothing is appended, every
// worker writes only to out[i] for the i it claimed, and the caller folds
// out in index order. The error returned is the one belonging to the
// lowest-indexed failing file, which is the same error the previous serial
// loop would have returned — a tree with two unreadable files reports the same
// one every time rather than whichever goroutine happened to lose the race.
func hashTreeEntries(projectDir string, files []string) ([]treeEntry, error) {
	out := make([]treeEntry, len(files))

	workers := runtime.GOMAXPROCS(0)
	if workers > len(files) {
		workers = len(files)
	}

	if workers <= 1 {
		for i, rel := range files {
			out[i].digestHex, out[i].kind, out[i].err = hashTreeEntry(filepath.Join(projectDir, rel))
		}
	} else {
		var (
			next atomic.Int64
			wg   sync.WaitGroup
		)
		wg.Add(workers)
		for w := 0; w < workers; w++ {
			go func() {
				defer wg.Done()
				for {
					i := int(next.Add(1)) - 1
					if i >= len(files) {
						return
					}
					out[i].digestHex, out[i].kind, out[i].err = hashTreeEntry(filepath.Join(projectDir, files[i]))
				}
			}()
		}
		// Every worker is joined before out is read: no goroutine outlives
		// this call, and there is no path that returns while one is still
		// writing into out.
		wg.Wait()
	}

	for i := range out {
		if out[i].err != nil {
			return nil, fmt.Errorf("hashing project file %q: %w", files[i], out[i].err)
		}
	}
	return out, nil
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
	slices.Sort(params.AssetOverlaySourceDigests)

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
		ProjectDir:                req.ProjectDir,
		BaseImageDigest:           req.BaseImageDigest,
		AppRuntime:                req.AppRuntime,
		BunVersion:                req.BunVersion,
		BunVariant:                req.BunVariant,
		BunCustomBinaryPath:       req.BunCustomBinaryPath,
		StubLauncher:              req.StubLauncher,
		Platforms:                 slices.Clone(req.Platforms),
		Strategy:                  req.Strategy,
		Compression:               req.Compression,
		NoPrune:                   req.NoPrune,
		KeepVendor:                slices.Clone(req.KeepVendor),
		NoPrecompress:             req.NoPrecompress,
		NoStrip:                   req.NoStrip,
		NoInject:                  req.NoInject,
		NoMinify:                  req.NoMinify,
		MinBunVersion:             req.MinBunVersion,
		CompileEnv:                slices.Clone(req.CompileEnv),
		Sourcemap:                 req.Sourcemap,
		Hermetic:                  req.Hermetic,
		SourceDateEpochUnix:       req.SourceDateEpochUnix,
		Runtime:                   runtime,
		Telemetry:                 req.Telemetry,
		Labels:                    req.Labels,
		Annotations:               req.Annotations,
		SBOMFormat:                req.SBOMFormat,
		SBOMAttachMode:            req.SBOMAttachMode,
		SBOMNoAttach:              req.SBOMNoAttach,
		AssetOverlaySourceDigests: slices.Clone(req.AssetOverlaySourceDigests),
	})
}

// CacheTag formats an input hash into a registry cache tag.
func CacheTag(inputHash string) string {
	return "cache-" + inputHash
}

// Check queries whether an image matching req.InputHash exists in req.Repo.
// If present and verified, it reconciles req.Tags and returns the result.
func (c *Cacher) Check(ctx context.Context, req ports.RemoteCacheRequest) (ports.RemoteCacheResult, error) {
	cacheTag := CacheTag(req.InputHash)

	// One session for all three network phases below. Each phase still issues
	// its own requests — a Puller memoises the authenticated fetcher, never a
	// manifest — but they no longer re-authenticate against the same registry
	// three times over.
	sess, err := c.remoteSession(req.Insecure, req.UserAgent, req.RegistryConfigPath)
	if err != nil {
		return ports.RemoteCacheResult{Hit: false}, err
	}

	cacheRef, err := name.ParseReference(req.Repo+":"+cacheTag, nameOptions(req.Insecure)...)
	if err != nil {
		return ports.RemoteCacheResult{Hit: false}, err
	}

	desc, hit, err := checkRemoteCache(ctx, sess, cacheRef)
	if err != nil || !hit {
		return ports.RemoteCacheResult{Hit: false}, err
	}
	digest := desc.Digest

	var (
		verified       bool
		signerIdentity string
	)

	// Cryptographic signature verification before tag promotion:
	// A cache hit is only safe when the candidate digest carries a valid
	// signature matching the trusted verification criteria. If verification
	// is active and fails, the cache hit must be rejected so release tags
	// are never promoted to unverified or poisoned bytes.
	if req.Verify.VerifySignature || (req.Verify.VerifyMode != "" && req.Verify.VerifyMode != ports.CacheVerifyNone) {
		v, id, vErr := c.verifyCandidate(ctx, sess, req.Repo, digest, req)
		if vErr != nil {
			return ports.RemoteCacheResult{Hit: false}, vErr
		}
		if !v {
			return ports.RemoteCacheResult{Hit: false}, nil
		}
		verified = true
		signerIdentity = id
	}

	// On verified hit, reconcile tags. A reconciliation failure means the release
	// tags were NOT actually moved to the cache-hit digest, so this must not
	// be reported as a hit — doing so would tell the caller the build
	// succeeded while the requested tags silently still point at whatever
	// they pointed at before (or don't exist at all). Falling through to
	// Hit: false makes the caller run a real build instead, which publishes
	// the tags itself through the normal, already-correct publish path.
	if len(req.Tags) > 0 {
		// desc is the descriptor checkRemoteCache already fetched, and its
		// digest is the digest just verified — so the tags are pointed at
		// exactly the manifest this Check validated, and the redundant
		// re-fetch of that same manifest by digest is gone.
		if err := reconcileTags(ctx, sess, cacheRef.Context(), desc, req.Tags); err != nil {
			return ports.RemoteCacheResult{Hit: false}, fmt.Errorf("remote cache hit but reconciling tags failed: %w", err)
		}
	}

	return ports.RemoteCacheResult{
		Hit:            true,
		Digest:         digest,
		Ref:            req.Repo + "@" + digest.String(),
		Tags:           slices.Clone(req.Tags),
		Verified:       verified,
		SignerIdentity: signerIdentity,
	}, nil
}

// verifyCandidate fetches and verifies Cosign static-key or Sigstore keyless signatures
// on a remote cache candidate digest before tag promotion.
//
// The <alg>-<hex>.sig tag this requires is genuinely produced by pokkum's
// own build pipeline: core.Build's signing stage (internal/core/pipeline.go,
// signAndSelfVerify) attaches a Cosign signature under ports.SigTag for the
// published digest of every signed push — and the cache-<hash> tag is
// appended to that same push's tag list, so the digest it resolves to here
// is exactly the digest the .sig was attached to. A builder configured with
// a signing key (POKKUM_SIGNING_KEY / --signing-key) therefore gets verified
// sub-100ms cache hits out of the box; a builder without one pushes unsigned
// images whose cache entries can never pass this check (core.Build logs that
// state explicitly at build time rather than letting the misses look like
// mysterious cache churn).
func (c *Cacher) verifyCandidate(ctx context.Context, sess *registryutils.Session, repo string, digest v1.Hash, req ports.RemoteCacheRequest) (bool, string, error) {
	sigTagStr := fmt.Sprintf("%s:%s-%s.sig", repo, digest.Algorithm, digest.Hex)
	sigRef, err := name.ParseReference(sigTagStr, nameOptions(req.Insecure)...)
	if err != nil {
		return false, "", err
	}

	sigImg, err := sess.Image(ctx, sigRef)
	if err != nil {
		var terr *transport.Error
		if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
			if req.Verify.Strict {
				return false, "", fmt.Errorf("remote cache candidate %s@%s has no signature (%s)", repo, digest, sigTagStr)
			}
			if c.log != nil {
				c.log.InfoContext(ctx, "remote cache candidate has no signature; skipping cache hit (was the candidate pushed by a builder with a signing key? unsigned pushes create cache entries that can never verify)", "repo", repo, "digest", digest.String())
			}
			return false, "", nil
		}
		if req.Verify.Strict {
			return false, "", fmt.Errorf("fetching remote cache signature for %s: %w", sigTagStr, err)
		}
		if c.log != nil {
			c.log.WarnContext(ctx, "fetching remote cache signature failed; bypassing cache", "sig_ref", sigTagStr, "err", err)
		}
		return false, "", nil
	}

	layers, err := sigImg.Layers()
	if err != nil || len(layers) == 0 {
		if req.Verify.Strict {
			return false, "", fmt.Errorf("signature image %s has no layers", sigTagStr)
		}
		return false, "", nil
	}

	manifest, err := sigImg.Manifest()
	if err != nil {
		if req.Verify.Strict {
			return false, "", fmt.Errorf("reading signature manifest %s: %w", sigTagStr, err)
		}
		return false, "", nil
	}

	var errs []error
	// One line per Check, not per signature layer: a signature image can
	// carry several layers and the derived-key notice is a property of this
	// build's configuration, not of any individual layer.
	loggedSigningKeyFallback := false
	for i, layer := range layers {
		rc, rerr := layer.Uncompressed()
		if rerr != nil {
			rc, rerr = layer.Compressed()
			if rerr != nil {
				errs = append(errs, fmt.Errorf("layer %d: read content: %w", i, rerr))
				continue
			}
		}
		rawBytes, rerr := io.ReadAll(rc)
		_ = rc.Close()
		if rerr != nil {
			errs = append(errs, fmt.Errorf("layer %d: read bytes: %w", i, rerr))
			continue
		}

		payloadBytes := extractPayload(rawBytes)

		var (
			sigStr     string
			certPEM    []byte
			chainPEM   []byte
			bundleJSON []byte
		)

		if i < len(manifest.Layers) && manifest.Layers[i].Annotations != nil {
			ann := manifest.Layers[i].Annotations
			if s, ok := ann[ports.CosignSignatureAnnotation]; ok {
				sigStr = s
			} else if s, ok := ann["org.opencontainers.image.signature"]; ok {
				sigStr = s
			}
			if c, ok := ann[ports.CosignCertificateAnnotation]; ok {
				certPEM = []byte(c)
			}
			if ch, ok := ann[ports.CosignChainAnnotation]; ok {
				chainPEM = []byte(ch)
			}
			if b, ok := ann[ports.CosignBundleAnnotation]; ok {
				bundleJSON = []byte(b)
			}
		}
		if sigStr == "" && manifest.Annotations != nil {
			if s, ok := manifest.Annotations[ports.CosignSignatureAnnotation]; ok {
				sigStr = s
			}
			if c, ok := manifest.Annotations[ports.CosignCertificateAnnotation]; ok {
				certPEM = []byte(c)
			}
			if ch, ok := manifest.Annotations[ports.CosignChainAnnotation]; ok {
				chainPEM = []byte(ch)
			}
			if b, ok := manifest.Annotations[ports.CosignBundleAnnotation]; ok {
				bundleJSON = []byte(b)
			}
		}

		if sigStr == "" {
			errs = append(errs, fmt.Errorf("layer %d: no signature annotation found", i))
			continue
		}

		if claimErr := checkSimpleSigningClaims(payloadBytes, repo, digest); claimErr != nil {
			errs = append(errs, fmt.Errorf("layer %d: invalid payload claims: %w", i, claimErr))
			continue
		}

		sigBytes, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(sigStr))
		if decErr != nil {
			errs = append(errs, fmt.Errorf("layer %d: decode base64 signature: %w", i, decErr))
			continue
		}

		mode := req.Verify.VerifyMode
		if mode == "" || mode == ports.CacheVerifyAuto {
			if !req.Verify.KeylessIdentity.Empty() {
				mode = ports.CacheVerifyKeyless
			} else {
				mode = ports.CacheVerifyStaticKey
			}
		}

		switch mode {
		case ports.CacheVerifyStaticKey:
			signer := c.signer
			if signer == nil {
				// Fail closed on a composition-root wiring gap, on the same
				// path as every other rejection in this loop. This branch used
				// to construct cosign.NewSigner(c.log) here — an adapter
				// wiring its own peer — so "not injected" was unreachable.
				// With that default gone, silently continuing would promote an
				// unverified (possibly poisoned) cache hit, which is precisely
				// the fail-open 812662e closed on the default arm below.
				errs = append(errs, fmt.Errorf(
					"layer %d: static-key verification requested but no ports.CosignSigner was injected into the cacher "+
						"(pass remotecacheutils.WithCosignSigner from the composition root)", i))
				continue
			}
			pubKey := req.Verify.PublicKeyPEM
			if len(pubKey) == 0 {
				// Each variable may hold PEM text or a path to a PEM file, and
				// resolution goes through the shared helper so it means the same
				// here as it does at the composition root. These used to be bare
				// []byte(os.Getenv(...)) conversions, accepting literal PEM
				// only, while cmd/pokkum resolved a path for the same variable —
				// so POKKUM_CACHE_PUBKEY meant two different things depending on
				// which path consumed it (checklist row 41).
				//
				// A value that is set but unresolvable is an error rather than a
				// reason to try the next variable: continuing would verify
				// against a different key than the operator named.
				resolved, source, rerr := keymaterialutils.ResolveFirst(
					keymaterialutils.Candidate{Source: "POKKUM_CACHE_PUBKEY", Setting: os.Getenv("POKKUM_CACHE_PUBKEY")},
					keymaterialutils.Candidate{Source: "POKKUM_SIGNING_PUBKEY", Setting: os.Getenv("POKKUM_SIGNING_PUBKEY")},
					keymaterialutils.Candidate{Source: "POKKUM_BASE_IMAGE_PUBKEY", Setting: os.Getenv("POKKUM_BASE_IMAGE_PUBKEY")},
				)
				if rerr != nil {
					errs = append(errs, fmt.Errorf("layer %d: %w", i, rerr))
					continue
				}
				if len(resolved) > 0 {
					pubKey = resolved
					if c.log != nil {
						c.log.DebugContext(ctx, "remote cache: resolved cache-verification key", "source", source, "repo", repo)
					}
				}
			}
			if len(pubKey) == 0 && len(req.Verify.SigningPublicKeyPEM) > 0 {
				// Last resort, and deliberately last: every explicit source
				// above wins, so an operator who configured a distinct
				// cache-verification key (their CI builder's, say) never has
				// it silently replaced by a locally derived one.
				//
				// This narrows trust rather than widening it — see
				// ports.RemoteCacheVerifyOptions.SigningPublicKeyPEM for the
				// full argument. In short: this arm accepts a candidate only
				// when its signature verifies against this single key, so
				// using the operator's own signing public key means "trust
				// only cache entries I signed myself", and the alternative
				// (no key at all) is to refuse every candidate outright.
				//
				// Logged, not silent: an implicitly derived trust anchor that
				// nobody is told about is the shape mem:self_review_checklist
				// rows 38/41 warn against, so say once, plainly, which key is
				// actually being verified against.
				pubKey = req.Verify.SigningPublicKeyPEM
				if c.log != nil && !loggedSigningKeyFallback {
					loggedSigningKeyFallback = true
					c.log.InfoContext(ctx, "remote cache: no cache-verification key configured; verifying cache-hit signatures against the signing key's public half (--signing-key), so only cache entries signed by this builder's own key will be accepted; set --cache-verify-key or POKKUM_CACHE_PUBKEY to verify against a different key", "repo", repo, "digest", digest.String())
				}
			}
			if len(pubKey) == 0 {
				// No fallback key. A shared, unattributed placeholder public
				// key used to live here (cosign.DefaultPublicKeyPEM) — no
				// real signer ever held its private half, so it could never
				// actually verify anything; it has been deleted (docs/archive/Roadmap.md
				// item 2h). Recorded as its own distinct failure — "nothing
				// configured to check against" — rather than falling through
				// to signer.Verify, which would report a generic "signature
				// verification failed" indistinguishable from a genuinely
				// wrong key or a tampered signature.
				errs = append(errs, fmt.Errorf(
					"layer %d: static-key verification requested but no key is configured; set --cache-verify-key, POKKUM_CACHE_PUBKEY, POKKUM_SIGNING_PUBKEY, or POKKUM_BASE_IMAGE_PUBKEY (a build's own --signing-key public half is used as a last resort, but this build has no signing key either)", i))
				continue
			}

			bundle := ports.CosignSignatureBundle{
				PayloadBytes:    payloadBytes,
				SignatureBytes:  sigBytes,
				Base64Signature: sigStr,
				Repo:            repo,
				Digest:          digest,
			}
			// Named distinctly from the function-level err (used above for
			// keychain/registry lookups): an if-scoped `err` here would fall
			// out of scope after this if-block, silently making a
			// following `errs = append(..., err)` refer to that stale outer
			// err instead of this verification's own failure.
			verifyErr := signer.Verify(ctx, bundle, pubKey, repo, digest)
			if verifyErr == nil {
				return true, "static-key", nil
			}
			errs = append(errs, fmt.Errorf("layer %d static-key verify: %w", i, verifyErr))

		case ports.CacheVerifyKeyless:
			keyless := c.keyless
			if keyless == nil {
				// Same fail-closed rule as the static-key arm above.
				errs = append(errs, fmt.Errorf(
					"layer %d: keyless verification requested but no ports.KeylessVerifier was injected into the cacher "+
						"(pass remotecacheutils.WithKeylessVerifier from the composition root)", i))
				continue
			}
			if len(certPEM) == 0 || len(bundleJSON) == 0 {
				errs = append(errs, fmt.Errorf("layer %d: keyless verification requires certificate and rekor bundle annotations", i))
				continue
			}

			identity := req.Verify.KeylessIdentity
			if identity.Empty() {
				errs = append(errs, fmt.Errorf("layer %d: keyless identity is required for keyless cache verification", i))
				continue
			}

			kres, kerr := keyless.Verify(ctx, ports.KeylessVerifyRequest{
				PayloadBytes:    payloadBytes,
				SignatureBytes:  sigBytes,
				CertificatePEM:  certPEM,
				ChainPEM:        chainPEM,
				RekorBundleJSON: bundleJSON,
				Identity:        identity,
				TrustedRootJSON: req.Verify.TrustedRootJSON,
			})
			if kerr == nil {
				return true, fmt.Sprintf("keyless:%s", kres.SAN), nil
			}
			errs = append(errs, fmt.Errorf("layer %d keyless verify: %w", i, kerr))

		default:
			// mode is normalized to CacheVerifyKeyless/CacheVerifyStaticKey
			// just above this switch whenever it starts out empty or
			// ports.CacheVerifyAuto — so this default is NOT dead code: it is
			// reached whenever req.Verify.VerifyMode explicitly carries
			// ports.CacheVerifyNone (or any future/unrecognized value)
			// alongside req.Verify.VerifySignature == true, e.g.
			// "--cache-verify-mode=none" set without the paired
			// "--no-cache-verify" flag that's the only path keeping those two
			// fields consistent (cmd/pokkum/build.go). Check() only calls
			// verifyCandidate at all because it already decided this
			// candidate must be cryptographically verified before promoting
			// release tags — silently treating an unhandled mode here as
			// "skip, no error" would resolve that contradiction in the
			// fail-open direction: an unverified (possibly poisoned) image
			// gets promoted anyway. This is a security control, so it fails
			// closed instead, exactly like every other rejection path in this
			// loop: record it as a verification failure for this layer, which
			// errors under --strict and otherwise safely bypasses the cache
			// (falls through to a real build) rather than accepting the hit.
			errs = append(errs, fmt.Errorf("layer %d: cache verify mode %q is not a recognized signature verification mode", i, mode))
		}
	}

	joinedErr := errors.Join(errs...)
	if req.Verify.Strict {
		return false, "", fmt.Errorf("remote cache candidate %s signature verification failed: %w", sigTagStr, joinedErr)
	}
	if c.log != nil {
		c.log.WarnContext(ctx, "remote cache candidate signature verification failed; bypassing cache", "sig_ref", sigTagStr, "err", joinedErr)
	}
	return false, "", nil
}

// extractPayload returns the canonical Simple Signing payload JSON, inspecting tar archive wrappers if necessary.
func extractPayload(payloadBytes []byte) []byte {
	var probe map[string]any
	if err := json.Unmarshal(payloadBytes, &probe); err == nil {
		return payloadBytes
	}
	tr := tar.NewReader(bytes.NewReader(payloadBytes))
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		if hdr.Typeflag == tar.TypeReg || hdr.Typeflag == 0 {
			data, rerr := io.ReadAll(tr)
			if rerr == nil && json.Unmarshal(data, &probe) == nil {
				return data
			}
		}
	}
	return payloadBytes
}

// checkSimpleSigningClaims verifies that the Simple Signing payload matches the expected repo and digest.
func checkSimpleSigningClaims(payloadBytes []byte, expectedRepo string, expectedDigest v1.Hash) error {
	var payload ports.CosignSimpleSigningPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return fmt.Errorf("unmarshal Simple Signing payload: %w", err)
	}

	switch payload.Critical.Type {
	case ports.CosignSimpleSigningType, ports.CosignContainerImageSignatureType:
		// Valid type
	default:
		return fmt.Errorf("unexpected payload type %q", payload.Critical.Type)
	}

	if payload.Critical.Image.DockerManifestDigest != expectedDigest.String() {
		return fmt.Errorf("payload digest %q != expected %q", payload.Critical.Image.DockerManifestDigest, expectedDigest.String())
	}

	// Exact match only. A suffix-based comparison here previously accepted
	// e.g. claimRepo = "evil.com/" + expectedRepo as matching expectedRepo —
	// for the keyless path, this check is the ONLY thing standing between a
	// validly-signed-by-the-trusted-identity payload for a *different* repo
	// and a false accept here (sigstore-go's Verify proves identity + payload
	// integrity, not which repo the payload claims to describe), so a fuzzy
	// match is a real cross-repo signature confusion, not just a cosmetic
	// looseness. Matches the exact-match convention already established in
	// internal/adapters/baseimage/resolver.go's checkSimpleSigningClaims and
	// internal/adapters/cosign/signer.go's Verify.
	claimRepo := payload.Critical.Identity.DockerReference
	if claimRepo != expectedRepo {
		return fmt.Errorf("payload repo %q does not match expected %q", claimRepo, expectedRepo)
	}

	return nil
}

// CheckRemoteCache queries repo for cacheTag, returning the cached digest if present.
//
// Cacher.Check does not go through this wrapper: it needs the full descriptor
// so that the reconcile phase does not re-fetch the manifest this probe
// already has. This entry point remains for callers that only want the digest.
func CheckRemoteCache(ctx context.Context, repo string, cacheTag string, insecure bool, userAgent string, registryConfig string) (v1.Hash, bool, error) {
	ref, err := name.ParseReference(repo+":"+cacheTag, nameOptions(insecure)...)
	if err != nil {
		return v1.Hash{}, false, err
	}

	opts, err := sessionOptions(insecure, userAgent, registryConfig)
	if err != nil {
		return v1.Hash{}, false, err
	}

	desc, hit, err := checkRemoteCache(ctx, registryutils.NewSession(opts...), ref)
	if err != nil || !hit {
		return v1.Hash{}, false, err
	}
	return desc.Digest, true, nil
}

// checkRemoteCache issues the cache-tag manifest GET and returns the full
// descriptor, so its caller can reuse the already-fetched manifest instead of
// re-fetching it by digest a moment later.
//
// A 404 is "no cache entry", not an error. Every other failure is surfaced:
// a probe that could not reach the registry must never be reported as a miss
// that a build then silently proceeds past.
func checkRemoteCache(ctx context.Context, sess *registryutils.Session, ref name.Reference) (*remote.Descriptor, bool, error) {
	desc, err := sess.Get(ctx, ref)
	if err != nil {
		var terr *transport.Error
		if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
			return nil, false, nil
		}
		return nil, false, err
	}
	return desc, true, nil
}

// ReconcileTags points target tags to an existing digest in the remote registry.
//
// Cacher.Check does not go through this wrapper either: it already holds the
// descriptor for the digest it verified and threads that straight into
// reconcileTags, which removes this function's manifest GET entirely.
func ReconcileTags(ctx context.Context, repo string, digest v1.Hash, tags []string, insecure bool, userAgent string, registryConfig string) error {
	parsedRepo, err := name.NewRepository(repo, nameOptions(insecure)...)
	if err != nil {
		return err
	}

	opts, err := sessionOptions(insecure, userAgent, registryConfig)
	if err != nil {
		return err
	}
	sess := registryutils.NewSession(opts...)

	desc, err := sess.Get(ctx, parsedRepo.Digest(digest.String()))
	if err != nil {
		return err
	}

	return reconcileTags(ctx, sess, parsedRepo, desc, tags)
}

// reconcileTags PUTs desc's manifest under every tag. desc must be the
// descriptor of the manifest the caller intends the tags to resolve to — it is
// the sole source of the bytes written, so pointing tags somewhere other than
// the verified digest is not expressible here.
func reconcileTags(ctx context.Context, sess *registryutils.Session, repo name.Repository, desc *remote.Descriptor, tags []string) error {
	for _, tag := range tags {
		if err := sess.Put(ctx, repo.Tag(tag), desc); err != nil {
			return err
		}
	}
	return nil
}
