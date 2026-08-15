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
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/ignoreutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/poolutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registryutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sigstore"
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
}

// Option configures a Cacher instance.
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
// If present and verified, it reconciles req.Tags and returns the result.
func (c *Cacher) Check(ctx context.Context, req ports.RemoteCacheRequest) (ports.RemoteCacheResult, error) {
	cacheTag := CacheTag(req.InputHash)
	digest, hit, err := CheckRemoteCache(ctx, req.Repo, cacheTag, req.Insecure, req.UserAgent, req.RegistryConfigPath)
	if err != nil || !hit {
		return ports.RemoteCacheResult{Hit: false}, err
	}

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
		v, id, vErr := c.verifyCandidate(ctx, req.Repo, digest, req)
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
		if err := ReconcileTags(ctx, req.Repo, digest, req.Tags, req.Insecure, req.UserAgent, req.RegistryConfigPath); err != nil {
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
func (c *Cacher) verifyCandidate(ctx context.Context, repo string, digest v1.Hash, req ports.RemoteCacheRequest) (bool, string, error) {
	nameOpts := []name.Option{name.WeakValidation}
	if req.Insecure {
		nameOpts = append(nameOpts, name.Insecure)
	}

	kc, err := registryutils.ResolveKeychain(req.RegistryConfigPath)
	if err != nil {
		return false, "", err
	}

	remoteOpts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(kc),
	}
	if req.UserAgent != "" {
		remoteOpts = append(remoteOpts, remote.WithUserAgent(req.UserAgent))
	}
	if req.Insecure {
		remoteOpts = append(remoteOpts, remote.WithTransport(transportutils.InsecureTransport()))
	}

	sigTagStr := fmt.Sprintf("%s:%s-%s.sig", repo, digest.Algorithm, digest.Hex)
	sigRef, err := name.ParseReference(sigTagStr, nameOpts...)
	if err != nil {
		return false, "", err
	}

	sigImg, err := remote.Image(sigRef, remoteOpts...)
	if err != nil {
		var terr *transport.Error
		if errors.As(err, &terr) && terr.StatusCode == http.StatusNotFound {
			if req.Verify.Strict {
				return false, "", fmt.Errorf("remote cache candidate %s@%s has no signature (%s)", repo, digest, sigTagStr)
			}
			if c.log != nil {
				c.log.InfoContext(ctx, "remote cache candidate has no signature; skipping cache hit", "repo", repo, "digest", digest.String())
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
			if s, ok := ann[sigstore.CosignSignatureAnnotation]; ok {
				sigStr = s
			} else if s, ok := ann["org.opencontainers.image.signature"]; ok {
				sigStr = s
			}
			if c, ok := ann[sigstore.CosignCertificateAnnotation]; ok {
				certPEM = []byte(c)
			}
			if ch, ok := ann[sigstore.CosignChainAnnotation]; ok {
				chainPEM = []byte(ch)
			}
			if b, ok := ann[sigstore.CosignBundleAnnotation]; ok {
				bundleJSON = []byte(b)
			}
		}
		if sigStr == "" && manifest.Annotations != nil {
			if s, ok := manifest.Annotations[sigstore.CosignSignatureAnnotation]; ok {
				sigStr = s
			}
			if c, ok := manifest.Annotations[sigstore.CosignCertificateAnnotation]; ok {
				certPEM = []byte(c)
			}
			if ch, ok := manifest.Annotations[sigstore.CosignChainAnnotation]; ok {
				chainPEM = []byte(ch)
			}
			if b, ok := manifest.Annotations[sigstore.CosignBundleAnnotation]; ok {
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
				signer = cosign.NewSigner(c.log)
			}
			pubKey := req.Verify.PublicKeyPEM
			if len(pubKey) == 0 {
				pubKey = []byte(os.Getenv("POKKUM_CACHE_PUBKEY"))
			}
			if len(pubKey) == 0 {
				pubKey = []byte(os.Getenv("POKKUM_SIGNING_PUBKEY"))
			}
			if len(pubKey) == 0 {
				pubKey = []byte(os.Getenv("POKKUM_BASE_IMAGE_PUBKEY"))
			}
			if len(pubKey) == 0 {
				pubKey = []byte(cosign.DefaultPublicKeyPEM)
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
				keyless = sigstore.NewVerifier(c.log)
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
		remoteOpts = append(remoteOpts, remote.WithTransport(transportutils.InsecureTransport()))
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
		remoteOpts = append(remoteOpts, remote.WithTransport(transportutils.InsecureTransport()))
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
