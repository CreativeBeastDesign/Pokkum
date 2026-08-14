// Package baseimage implements ports.BaseImageResolver by pulling the
// distroless, chainguard or custom base image over
// github.com/google/go-containerregistry and pinning it to a digest.
//
// # The libc check
//
// The whole point of this package is one cheap guard: a Bun-compiled
// executable is dynamically linked against libc.so.6, libstdc++.so.6,
// libgcc_s.so.1 and libm.so.6, so a base image that provides none of them
// (distroless/static, scratch) produces a container that builds fine and dies
// on boot with an inscrutable loader error. Resolve detects the obviously
// broken refs before ever touching the network and returns
// core.ErrBaseImageIncompatible with a message that explains why. It cannot
// fully verify glibc presence for an arbitrary custom ref — that would mean
// pulling and inspecting every layer — so this is a best-effort filter for the
// mistake users actually make, not a guarantee.
//
// # Signature verification
//
// When BaseImageRequest.VerifySignature is set, Resolve verifies the base
// image's Cosign signature by one of two paths — a static public key, or a
// keyless Sigstore signature (Fulcio certificate + Rekor transparency log,
// delegated to internal/adapters/sigstore). Which path runs is decided from
// the request and the preset before any signature material is fetched, never
// from what happens to be published alongside the image; see verifyBaseImage.
//
// # Caching
//
// A Resolver also caches successful signature verifications per (ref, digest,
// mode, identity, trusted root, key fingerprint, insecure, registry config),
// so re-resolving the same image does not re-fetch and re-verify signature
// material it has already proved. Failures are never cached.
//
// A Resolver caches its network round-trips per (ref, insecure) for the
// top-level pull (index or single image plus its digest), and per (ref,
// insecure, platform) for the resolved per-platform image. This means calling
// Resolve twice for the same ref and platform set — the normal case, since one
// BuildRequest resolves every platform in a single call — only pulls the
// manifest once. The cache is unbounded and lives for the lifetime of the
// Resolver; callers that build continuously should create a fresh Resolver per
// process or accept the memory, since a base image's manifest is small.
//
// Caveat: a cached entry's underlying go-containerregistry objects (the
// v1.ImageIndex or v1.Image the top-level pull produced) remain bound to the
// context.Context that was live when they were first fetched, because that
// library resolves layers and config lazily against the context captured at
// construction time. In the intended usage — one Resolve call per build,
// carrying that build's full platform set — this is invisible: every network
// operation for the call happens under that call's own context. It only
// surfaces if a Resolver outlives the context of the call that first warmed a
// given ref's cache entry, and a later call against the same ref (under a
// different context) asks for a platform not seen before, triggering a
// first-time child fetch bound to the now-stale original context. Pokkum's
// core never does this — BuildRequest.Platforms is fixed for the life of one
// Resolve call — so this is a documented limitation of sharing a Resolver
// across independently-cancelled contexts, not a bug in the common path.
package baseimage

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/lockfileutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registryutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sigstore"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// insecureTransport is used for requests against BaseImageRequest.Insecure
// targets: local or self-signed test registries only. It is package-level
// because http.Transport is meant to be reused, not built per call.
var insecureTransport http.RoundTripper = &http.Transport{
	TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // opt-in via BaseImageRequest.Insecure
}

var _ ports.BaseImageResolver = (*Resolver)(nil)

// Resolver implements ports.BaseImageResolver against a real container
// registry. The zero value is not usable; construct with NewResolver.
//
// Resolver is safe for concurrent use: all mutable state lives behind mu.
type Resolver struct {
	log *slog.Logger

	// signer verifies static-key Cosign signatures (the custom / self-signed
	// base image case); keyless verifies Sigstore keyless signatures (Fulcio
	// certificate + Rekor entry), which is how the stock distroless and
	// chainguard presets are signed. Which one runs for a given Resolve call
	// is decided by verifyBaseImage from the request, never from the bytes
	// found on the registry.
	signer  ports.CosignSigner
	keyless ports.KeylessVerifier

	mu            sync.Mutex
	pulls         map[pullKey]pullEntry
	images        map[imageKey]imageEntry
	verifications map[verifyKey]struct{}
}

// pullKey identifies one top-level manifest pull.
type pullKey struct {
	ref                string
	insecure           bool
	registryConfigPath string
}

// pullEntry caches the outcome of resolving ref to an index or a single
// image, before any per-platform selection.
type pullEntry struct {
	pull *pulledManifest
	err  error
}

// pulledManifest is the digest-pinned top-level manifest, either an index or
// a single image. Exactly one of index or image is set, matching isIndex.
type pulledManifest struct {
	digest  v1.Hash
	isIndex bool
	index   v1.ImageIndex
	image   v1.Image
}

// imageKey identifies one resolved (ref, platform) pair.
type imageKey struct {
	ref      string
	insecure bool
	platform ports.Platform
}

// imageEntry caches the outcome of selecting a platform's child image out of
// a pulledManifest.
type imageEntry struct {
	image v1.Image
	err   error
}

// verifyKey identifies one signature verification that already succeeded. It
// deliberately carries every input that can change the verdict — including a
// fingerprint of the static public key actually used, so that changing
// POKKUM_BASE_IMAGE_PUBKEY mid-process cannot be satisfied by a cache entry
// proved against the previous key.
type verifyKey struct {
	ref                string
	digest             string
	mode               ports.BaseImageVerifyMode
	identity           ports.KeylessIdentity
	trustedRootPath    string
	pubKeyFingerprint  string
	insecure           bool
	registryConfigPath string
}

// Option supplies an optional Resolver dependency to NewResolver.
//
// Go permits only one variadic parameter, so the Resolver's two optional
// adapters — the static-key Cosign signer and the keyless Sigstore verifier —
// are passed as options rather than as two variadic dependency lists. A nil
// Option is accepted and ignored, so NewResolver(log, nil) still means "all
// defaults", exactly as the previous `signer ...ports.CosignSigner` form did.
type Option func(*Resolver)

// WithCosignSigner overrides the static-key Cosign signer used by the
// static-key verification path. A nil signer is ignored (the default stands).
func WithCosignSigner(signer ports.CosignSigner) Option {
	return func(r *Resolver) {
		if signer != nil {
			r.signer = signer
		}
	}
}

// WithKeylessVerifier overrides the keyless Sigstore verifier used by the
// keyless verification path. A nil verifier is ignored (the default stands).
func WithKeylessVerifier(verifier ports.KeylessVerifier) Option {
	return func(r *Resolver) {
		if verifier != nil {
			r.keyless = verifier
		}
	}
}

// NewResolver constructs a Resolver. A nil logger defaults to slog.Default().
// The optional dependencies default to cosign.NewSigner(log) for static-key
// signature verification and sigstore.NewVerifier(log) for keyless
// verification; pass WithCosignSigner / WithKeylessVerifier to override
// either.
func NewResolver(log *slog.Logger, opts ...Option) *Resolver {
	if log == nil {
		log = slog.Default()
	}
	r := &Resolver{
		log:           log,
		signer:        cosign.NewSigner(log),
		keyless:       sigstore.NewVerifier(log),
		pulls:         make(map[pullKey]pullEntry),
		images:        make(map[imageKey]imageEntry),
		verifications: make(map[verifyKey]struct{}),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

// Resolve implements ports.BaseImageResolver.
func (r *Resolver) Resolve(ctx context.Context, req ports.BaseImageRequest) (*ports.BaseImage, error) {
	if len(req.Platforms) == 0 {
		return nil, fmt.Errorf("baseimage: no platforms requested: %w", core.ErrInvalidBaseImage)
	}

	ref, err := effectiveRef(req)
	if err != nil {
		return nil, err
	}

	nameOpts := []name.Option{name.WeakValidation}
	if req.Insecure {
		nameOpts = append(nameOpts, name.Insecure)
	}

	// Handle pokkum.lock lookup
	var (
		lf          *ports.PokkumLockfile
		lockedFound bool
		lockKey     = string(req.Preset)
	)

	if req.LockfilePath != "" {
		loaded, lerr := lockfileutils.LoadLockfile(req.LockfilePath)
		if lerr == nil {
			lf = loaded
			if entry, ok := lockfileutils.GetLockedBase(lf, lockKey); ok && !req.UpdateBase {
				lockedFound = true
				if entry.MirrorRef != "" {
					// Attempt to resolve from mirrored escrow registry first
					if mParsed, mErr := name.ParseReference(entry.MirrorRef, nameOpts...); mErr == nil {
						if _, mPullErr := r.pull(ctx, mParsed, entry.MirrorRef, req.Insecure, req.RegistryConfigPath); mPullErr == nil {
							ref = entry.MirrorRef
							r.logger().Info("using mirrored base image from escrow registry", "mirror_ref", ref)
						} else {
							r.logger().Warn("failed to pull base image from escrow mirror, falling back to locked pinned ref", "mirror_ref", entry.MirrorRef, "err", mPullErr)
						}
					}
				}
				if ref != entry.MirrorRef && entry.PinnedRef != "" {
					ref = entry.PinnedRef
					r.logger().Info("using locked base image from lockfile", "lockfile", req.LockfilePath, "key", lockKey, "ref", ref)
				}
			}
		} else if !os.IsNotExist(lerr) {
			r.logger().Warn("failed to load base image lockfile", "path", req.LockfilePath, "err", lerr)
		}
	}

	if req.Offline && !lockedFound {
		return nil, fmt.Errorf("baseimage: offline mode enabled but base %q is not locked in %s: %w", req.Preset, req.LockfilePath, core.ErrInvalidBaseImage)
	}

	if reason, bad := staticBaseReason(ref); bad {
		return nil, fmt.Errorf("baseimage: %s: %s: %w", ref, reason, core.ErrBaseImageIncompatible)
	}

	parsedRef, err := name.ParseReference(ref, nameOpts...)
	if err != nil {
		return nil, fmt.Errorf("baseimage: parse %q: %w: %w", ref, err, core.ErrInvalidBaseImage)
	}

	pull, err := r.pull(ctx, parsedRef, ref, req.Insecure, req.RegistryConfigPath)
	if err != nil {
		return nil, err
	}

	out := &ports.BaseImage{
		Ref:       ref,
		PinnedRef: pinnedRef(parsedRef, pull.digest),
		Digest:    pull.digest,
		IsIndex:   pull.isIndex,
		Images:    make(map[ports.Platform]v1.Image, len(req.Platforms)),
	}

	for _, p := range req.Platforms {
		img, err := r.imageForPlatform(ref, req.Insecure, pull, p)
		if err != nil {
			return nil, err
		}
		out.Images[p] = img
	}

	if _, pinned := parsedRef.(name.Digest); !pinned {
		r.logger().Warn("base image reference is a tag; build reproducibility relies on PinnedRef being recorded",
			"ref", ref, "pinned_ref", out.PinnedRef)
	}

	// Escrow mirroring: if MirrorRegistry is requested, copy the image and signature to mirror
	var mirrorRef string
	if req.MirrorRegistry != "" {
		mirrorTarget := fmt.Sprintf("%s:sha256-%s", strings.TrimRight(req.MirrorRegistry, "/"), pull.digest.Hex)
		mirrorParsed, mErr := name.ParseReference(mirrorTarget, nameOpts...)
		if mErr != nil {
			return nil, fmt.Errorf("baseimage: parse mirror reference %q: %w: %w", mirrorTarget, mErr, core.ErrInvalidBaseImage)
		}

		remoteOpts, rErr := r.remoteOptions(ctx, req.Insecure, req.RegistryConfigPath)
		if rErr != nil {
			return nil, fmt.Errorf("baseimage: resolve mirror remote options: %w", rErr)
		}

		if pull.isIndex {
			if err := remote.WriteIndex(mirrorParsed, pull.index, remoteOpts...); err != nil {
				return nil, classifyMirrorErr(mirrorTarget, err)
			}
		} else {
			if err := remote.Write(mirrorParsed, pull.image, remoteOpts...); err != nil {
				return nil, classifyMirrorErr(mirrorTarget, err)
			}
		}

		// Also mirror the Cosign signature tag if present upstream
		sigTag := fmt.Sprintf("sha256-%s.sig", pull.digest.Hex)
		upstreamSigRef := parsedRef.Context().Tag(sigTag)
		mirrorSigRef := mirrorParsed.Context().Tag(sigTag)
		if sigDesc, sErr := remote.Get(upstreamSigRef, remoteOpts...); sErr == nil {
			sigImg, iErr := sigDesc.Image()
			if iErr != nil {
				return nil, fmt.Errorf("baseimage: escrow mirror get signature image %s: %w: %w", upstreamSigRef.Name(), iErr, core.ErrPushFailed)
			}
			if wErr := remote.Write(mirrorSigRef, sigImg, remoteOpts...); wErr != nil {
				return nil, classifyMirrorErr(mirrorSigRef.Name(), wErr)
			}
			r.logger().Info("escrow mirrored base image and signatures", "mirror_ref", mirrorTarget, "sig_ref", mirrorSigRef.Name())
		} else {
			r.logger().Info("escrow mirrored base image", "mirror_ref", mirrorTarget)
		}
		mirrorRef = mirrorTarget
	}

	// Update pokkum.lock if requested or if lockfile path specified and base was unpinned/newly resolved
	if req.LockfilePath != "" && (!lockedFound || req.UpdateBase) {
		if lf == nil {
			lf = &ports.PokkumLockfile{
				Version:   lockfileutils.LockfileSchemaVersion,
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
				Bases:     make(map[string]ports.BaseLockEntry),
			}
		}
		origRef, _ := req.Preset.DefaultRef()
		if req.Ref != "" {
			origRef = req.Ref
		}
		entry := ports.BaseLockEntry{
			Ref:       origRef,
			Digest:    pull.digest.String(),
			PinnedRef: out.PinnedRef,
			MirrorRef: mirrorRef,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if existing, ok := lockfileutils.GetLockedBase(lf, lockKey); ok {
			if mirrorRef == "" && existing.MirrorRef != "" && existing.Digest == entry.Digest {
				entry.MirrorRef = existing.MirrorRef
			}
			entry.LastScannedAt = existing.LastScannedAt
			entry.VulnerabilitiesCount = existing.VulnerabilitiesCount
			entry.MaxSeverity = existing.MaxSeverity
		}
		lockfileutils.SetLockedBase(lf, lockKey, entry)
		if serr := lockfileutils.SaveLockfile(req.LockfilePath, lf); serr != nil {
			r.logger().Warn("failed to save updated base image lockfile", "path", req.LockfilePath, "err", serr)
		} else {
			r.logger().Info("updated base image lockfile", "path", req.LockfilePath, "preset", req.Preset, "pinned_ref", out.PinnedRef)
		}
	}

	if req.VerifySignature {
		if err := r.verifyBaseImage(ctx, ref, pull, req); err != nil {
			return nil, err
		}
	}

	r.logger().Info("base image resolved", "preset", req.Preset, "ref", out.Ref, "pinned_ref", out.PinnedRef,
		"is_index", pull.isIndex, "platforms", platformList(req.Platforms))

	return out, nil
}

// effectiveRef validates the preset and returns the reference to resolve:
// req.Ref when set, otherwise req.Preset.DefaultRef(). It is split out of
// Resolve so the fallback logic can be unit-tested without a network call.
func effectiveRef(req ports.BaseImageRequest) (string, error) {
	if !req.Preset.Valid() {
		return "", fmt.Errorf("baseimage: preset %q: %w", req.Preset, core.ErrInvalidBaseImage)
	}
	ref := strings.TrimSpace(req.Ref)
	if ref != "" {
		return ref, nil
	}
	defRef, ok := req.Preset.DefaultRef()
	if !ok {
		return "", fmt.Errorf("baseimage: preset %q has no default reference and none was supplied: %w", req.Preset, core.ErrInvalidBaseImage)
	}
	return defRef, nil
}

// pull resolves ref to its top-level manifest, index or image, using the
// cache when available.
func (r *Resolver) pull(ctx context.Context, parsedRef name.Reference, rawRef string, insecure bool, registryConfigPath string) (*pulledManifest, error) {
	key := pullKey{ref: rawRef, insecure: insecure, registryConfigPath: registryConfigPath}

	r.mu.Lock()
	if e, ok := r.pulls[key]; ok {
		r.mu.Unlock()
		r.logger().Debug("base image pull cache hit", "ref", rawRef)
		return e.pull, e.err
	}
	r.mu.Unlock()

	r.logger().Debug("pulling base image manifest", "ref", rawRef, "insecure", insecure)
	opts, err := r.remoteOptions(ctx, insecure, registryConfigPath)
	if err != nil {
		return nil, err
	}
	desc, err := remote.Get(parsedRef, opts...)

	var (
		pulled *pulledManifest
		rerr   error
	)
	switch {
	case err != nil:
		rerr = classifyPullErr(rawRef, err)
	case desc.MediaType.IsIndex():
		idx, ierr := desc.ImageIndex()
		if ierr != nil {
			rerr = fmt.Errorf("baseimage: %s: read index: %w: %w", rawRef, ierr, core.ErrInvalidBaseImage)
		} else {
			pulled = &pulledManifest{digest: desc.Digest, isIndex: true, index: idx}
		}
	default:
		img, ierr := desc.Image()
		if ierr != nil {
			rerr = fmt.Errorf("baseimage: %s: read image: %w: %w", rawRef, ierr, core.ErrInvalidBaseImage)
		} else {
			pulled = &pulledManifest{digest: desc.Digest, isIndex: false, image: img}
		}
	}

	r.mu.Lock()
	r.pulls[key] = pullEntry{pull: pulled, err: rerr}
	r.mu.Unlock()

	return pulled, rerr
}

// imageForPlatform selects the child image for platform p out of pull, using
// the cache when available. It never touches the network itself — everything
// it needs was already fetched by pull — so it takes no context.
func (r *Resolver) imageForPlatform(rawRef string, insecure bool, pull *pulledManifest, p ports.Platform) (v1.Image, error) {
	key := imageKey{ref: rawRef, insecure: insecure, platform: p}

	r.mu.Lock()
	if e, ok := r.images[key]; ok {
		r.mu.Unlock()
		r.logger().Debug("base image platform cache hit", "ref", rawRef, "platform", p.String())
		return e.image, e.err
	}
	r.mu.Unlock()

	img, err := selectPlatform(pull, p, rawRef)

	r.mu.Lock()
	r.images[key] = imageEntry{image: img, err: err}
	r.mu.Unlock()

	return img, err
}

// selectPlatform picks the manifest child matching p out of an index, or
// verifies that a single image's own config matches p.
func selectPlatform(pull *pulledManifest, p ports.Platform, rawRef string) (v1.Image, error) {
	if pull.isIndex {
		im, err := pull.index.IndexManifest()
		if err != nil {
			return nil, fmt.Errorf("baseimage: %s: read index manifest: %w: %w", rawRef, err, core.ErrInvalidBaseImage)
		}
		for _, m := range im.Manifests {
			if !platformMatches(m.Platform, p) {
				continue
			}
			img, err := pull.index.Image(m.Digest)
			if err != nil {
				return nil, fmt.Errorf("baseimage: %s: fetch %s child %s: %w: %w", rawRef, p, m.Digest, err, core.ErrInvalidBaseImage)
			}
			return img, nil
		}
		return nil, fmt.Errorf("baseimage: %s: index has no manifest for platform %s: %w", rawRef, p, core.ErrBaseImageIncompatible)
	}

	cf, err := pull.image.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("baseimage: %s: read config file: %w: %w", rawRef, err, core.ErrInvalidBaseImage)
	}
	if cf.OS != p.OS || cf.Architecture != p.Arch {
		got := ports.Platform{OS: cf.OS, Arch: cf.Architecture, Variant: cf.Variant}
		return nil, fmt.Errorf("baseimage: %s: single-platform image is %s, requested %s covers only one platform: %w", rawRef, got, p, core.ErrBaseImageIncompatible)
	}
	return pull.image, nil
}

// platformMatches reports whether an index child's platform descriptor
// satisfies the requested platform. It compares OS and architecture only:
// Pokkum's requested platforms never carry a Variant (see ports.Platform),
// while registries commonly stamp arm64 children with variant "v8", so
// requiring an exact Variant match would reject every real multi-arch arm64
// image. It also skips the "unknown/unknown" placeholder platform that
// registries use for attestation and provenance manifests co-located in the
// same index.
func platformMatches(cp *v1.Platform, want ports.Platform) bool {
	if cp == nil {
		return false
	}
	if cp.OS == "unknown" || cp.Architecture == "unknown" {
		return false
	}
	return cp.OS == want.OS && cp.Architecture == want.Arch
}

// staticBaseReason reports whether ref obviously cannot run a dynamically
// linked Bun binary, and if so, why. It recognises distroless/static (any
// -debianNN suffix) and scratch, the two mistakes users actually make; it is
// not a general glibc-presence check, which would require pulling and
// inspecting layers.
func staticBaseReason(ref string) (string, bool) {
	const (
		staticReason  = "distroless/static ships no dynamic loader or libc; Bun binaries are dynamically linked against libc.so.6, libstdc++.so.6, libgcc_s.so.1 and libm.so.6 and cannot execute without them — use the distroless/cc-debian12 default or the chainguard glibc-dynamic base instead"
		scratchReason = "scratch is an empty filesystem with no dynamic loader or libc; Bun binaries are dynamically linked against libc.so.6, libstdc++.so.6, libgcc_s.so.1 and libm.so.6 and cannot execute in it — use the distroless/cc-debian12 default or the chainguard glibc-dynamic base instead"
	)

	lower := strings.ToLower(strings.TrimSpace(ref))
	if strings.Contains(lower, "distroless/static") {
		return staticReason, true
	}

	repo := lower
	if i := strings.IndexByte(repo, '@'); i >= 0 {
		repo = repo[:i]
	}
	if i := strings.LastIndex(repo, ":"); i >= 0 {
		repo = repo[:i]
	}
	seg := repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		seg = repo[i+1:]
	}
	if seg == "scratch" {
		return scratchReason, true
	}
	return "", false
}

// remoteOptions builds the go-containerregistry options common to every pull:
// context threading (so a cancelled build stops pulling mid-transfer) and keychain resolution.
func (r *Resolver) remoteOptions(ctx context.Context, insecure bool, registryConfigPath string) ([]remote.Option, error) {
	kc, err := registryutils.ResolveKeychain(registryConfigPath)
	if err != nil {
		return nil, err
	}
	opts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(kc),
	}
	if insecure {
		opts = append(opts, remote.WithTransport(insecureTransport))
	}
	return opts, nil
}

// classifyPullErr maps a go-containerregistry transport error onto a core
// sentinel. Context cancellation is preserved as-is rather than reclassified,
// per the core/errors.go wrapping convention. A 401/403 becomes
// core.ErrRegistryAuth; anything else becomes core.ErrInvalidBaseImage, since
// ports.BaseImageResolver declares no sentinel for "the registry is
// unreachable" or "the tag does not exist" — the closest fit in the current
// contract is "the reference could not be resolved".
func classifyPullErr(rawRef string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("baseimage: %s: %w", rawRef, err)
	}

	var terr *transport.Error
	if errors.As(err, &terr) {
		switch terr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("baseimage: %s: registry rejected credentials: %w: %w", rawRef, err, core.ErrRegistryAuth)
		}
	}
	return fmt.Errorf("baseimage: %s: pull failed: %w: %w", rawRef, err, core.ErrInvalidBaseImage)
}

// classifyMirrorErr maps a go-containerregistry transport or network error
// encountered while writing to an escrow mirror registry onto a core sentinel.
func classifyMirrorErr(target string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("baseimage: escrow mirror %s: %w", target, err)
	}
	var terr *transport.Error
	if errors.As(err, &terr) {
		switch terr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("baseimage: escrow mirror %s: registry rejected credentials: %w: %w", target, err, core.ErrRegistryAuth)
		}
	}
	return fmt.Errorf("baseimage: escrow mirror write %s: %w: %w", target, err, core.ErrPushFailed)
}

// pinnedRef renders the "repo@sha256:…" form of parsedRef at digest d,
// regardless of whether parsedRef was originally a tag or already a digest.
func pinnedRef(parsedRef name.Reference, d v1.Hash) string {
	return parsedRef.Context().Name() + "@" + d.String()
}

// platformList renders platforms for a single log field, avoiding a slice
// value (which slog would print via %v rather than a stable delimited form).
func platformList(ps []ports.Platform) string {
	ss := make([]string, len(ps))
	for i, p := range ps {
		ss[i] = p.String()
	}
	return strings.Join(ss, ",")
}

// logger returns the effective logger, defensively covering a Resolver built
// without NewResolver (e.g. a zero-value Resolver in a test).
func (r *Resolver) logger() *slog.Logger {
	if r.log == nil {
		return slog.Default()
	}
	return r.log
}

// DefaultBaseImagePublicKeyPEM is the fallback Cosign public key for
// verifying base images when POKKUM_BASE_IMAGE_PUBKEY is not set. It is a
// real, valid P-256 PKIX public key so parsePublicKeyPEM always succeeds,
// but it does not correspond to any key that actually signs upstream
// distroless or Chainguard images — see the scope note below.
const DefaultBaseImagePublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEbCr5pcxh+/q57RngLq9ycZmUDjZd
3KhRlVmB3LMAG1HtAwVdDIsKATtrP020TSVXeBVvOy1TntoxpA3ijNvHcA==
-----END PUBLIC KEY-----`

// verifyBaseImage verifies the base image's Cosign signature. It is the entry
// point for BaseImageRequest.VerifySignature and the single place where the
// choice of verification path is made.
//
// # Two verification paths
//
//   - static key: a Cosign "Simple Signing" signature verified against a
//     static ECDSA/Ed25519 public key (POKKUM_BASE_IMAGE_PUBKEY, falling back
//     to DefaultBaseImagePublicKeyPEM). This is how Pokkum itself signs a
//     custom or self-hosted base, and it is the default for
//     ports.BaseImageCustom.
//
//   - keyless: a Sigstore keyless signature — a short-lived Fulcio
//     certificate plus its Rekor transparency-log entry — verified by
//     internal/adapters/sigstore against the expected certificate identity
//     (issuer + SAN). This is how the stock distroless and chainguard images
//     are actually signed, so it is the default for those two presets; the
//     expected identities come from ports.BaseImagePreset.DefaultKeylessIdentity.
//
// # Why the mode is decided before anything is fetched
//
// The mode comes from BaseImageRequest.VerifyMode, or from the preset when
// that is BaseImageVerifyAuto. It is never inferred from which annotations
// happen to be present on the registry's signature manifest. Inferring it
// ("there is a certificate annotation, so verify keyless; otherwise verify
// with the static key") would let whoever controls the bytes on the wire —
// a compromised registry mirror, a MITM on an insecure pull — choose which
// control runs, by stripping the real keyless material and substituting
// something the weaker path would accept. So: decide the mode, then fetch,
// then dispatch. There is deliberately no fallback from one path to the other
// for a given Resolve call.
func (r *Resolver) verifyBaseImage(ctx context.Context, ref string, pull *pulledManifest, req ports.BaseImageRequest) error {
	mode := req.VerifyMode
	if !mode.Valid() {
		return fmt.Errorf("baseimage: %s: unknown verify mode %q for preset %q: %w", ref, mode, req.Preset, core.ErrBaseSignatureInvalid)
	}
	if mode == ports.BaseImageVerifyAuto {
		mode = req.Preset.DefaultVerifyMode()
	}

	var (
		identity        ports.KeylessIdentity
		trustedRootJSON []byte
		pubKeyPEM       []byte
	)

	switch mode {
	case ports.BaseImageVerifyStaticKey:
		if r.signer == nil {
			return fmt.Errorf("baseimage: no Cosign signer configured, refusing to treat %s as verified: %w", ref, core.ErrBaseSignatureInvalid)
		}
		pubKeyPEM = []byte(os.Getenv("POKKUM_BASE_IMAGE_PUBKEY"))
		if len(pubKeyPEM) == 0 {
			pubKeyPEM = []byte(DefaultBaseImagePublicKeyPEM)
		}

	case ports.BaseImageVerifyKeyless:
		if r.keyless == nil {
			return fmt.Errorf("baseimage: no keyless verifier configured, refusing to treat %s as verified: %w", ref, core.ErrBaseSignatureInvalid)
		}

		// An operator who sets POKKUM_BASE_IMAGE_PUBKEY on a preset that
		// verifies keyless by default almost certainly means "verify against
		// my key", not "ignore my key" and certainly not "downgrade this
		// preset's security model". Neither guess is ours to make silently,
		// so fail loudly and name the flag that expresses the intent.
		if os.Getenv("POKKUM_BASE_IMAGE_PUBKEY") != "" {
			return fmt.Errorf(
				"baseimage: %s: POKKUM_BASE_IMAGE_PUBKEY is set but preset %q verifies keyless by default; "+
					"pass --base-verify-mode=static-key (or the equivalent BaseImageRequest.VerifyMode) if you "+
					"intend to verify against that key instead of the upstream keyless signature: %w",
				ref, req.Preset, core.ErrBaseSignatureInvalid)
		}

		identity = req.KeylessIdentity
		if identity.Empty() {
			if def, ok := req.Preset.DefaultKeylessIdentity(); ok {
				identity = def
			}
		}
		if identity.Empty() {
			return fmt.Errorf(
				"baseimage: %s: preset %q has no default keyless identity and none was supplied "+
					"(BaseImageRequest.KeylessIdentity / --base-keyless-identity + --base-keyless-issuer); "+
					"refusing to verify against an unconstrained identity: %w",
				ref, req.Preset, core.ErrBaseSignatureInvalid)
		}

		if req.TrustedRootPath != "" {
			b, err := os.ReadFile(req.TrustedRootPath)
			if err != nil {
				return fmt.Errorf("baseimage: %s: read trusted root %s: %w: %w", ref, req.TrustedRootPath, err, core.ErrBaseSignatureInvalid)
			}
			trustedRootJSON = b
		}

	default:
		return fmt.Errorf("baseimage: %s: verify mode %q not implemented: %w", ref, mode, core.ErrBaseSignatureInvalid)
	}

	key := verifyKey{
		ref:                ref,
		digest:             pull.digest.String(),
		mode:               mode,
		identity:           identity,
		trustedRootPath:    req.TrustedRootPath,
		pubKeyFingerprint:  fingerprint(pubKeyPEM),
		insecure:           req.Insecure,
		registryConfigPath: req.RegistryConfigPath,
	}
	r.mu.Lock()
	_, cached := r.verifications[key]
	r.mu.Unlock()
	if cached {
		r.logger().Debug("base image signature verification cache hit", "ref", ref, "mode", mode.String())
		return nil
	}

	if err := r.runVerification(ctx, ref, pull, req, mode, identity, trustedRootJSON, pubKeyPEM); err != nil {
		// Only successes are cached. A failure aborts the build anyway, so
		// there is nothing to save by remembering it, and caching it would
		// mean caching transient causes (a registry timeout mid-fetch) as if
		// they were verification verdicts.
		return err
	}

	r.mu.Lock()
	r.verifications[key] = struct{}{}
	r.mu.Unlock()
	return nil
}

// runVerification performs the fetch-then-dispatch half of verifyBaseImage,
// after the mode and all of its inputs have been settled.
func (r *Resolver) runVerification(
	ctx context.Context,
	ref string,
	pull *pulledManifest,
	req ports.BaseImageRequest,
	mode ports.BaseImageVerifyMode,
	identity ports.KeylessIdentity,
	trustedRootJSON []byte,
	pubKeyPEM []byte,
) error {
	repo, sigRefStr, layers, err := r.fetchCosignSigLayers(ctx, ref, pull, req.Insecure, req.RegistryConfigPath)
	if err != nil {
		return err
	}

	switch mode {
	case ports.BaseImageVerifyStaticKey:
		return r.verifyStaticKeySignature(ctx, ref, repo, sigRefStr, layers, pull.digest, pubKeyPEM)
	case ports.BaseImageVerifyKeyless:
		return r.verifyKeylessSignature(ctx, ref, repo, sigRefStr, layers, pull.digest, identity, trustedRootJSON)
	default:
		return fmt.Errorf("baseimage: %s: verify mode %q not implemented: %w", ref, mode, core.ErrBaseSignatureInvalid)
	}
}

// cosignSigLayer is one signature layer of a Cosign "<repo>:<alg>-<hex>.sig"
// manifest: the signed payload blob plus the annotations on its manifest
// descriptor. A signature tag may legitimately carry several of these — an
// image re-signed over the years accumulates one layer per signing — so every
// layer is read and every layer is a verification candidate.
type cosignSigLayer struct {
	// index is the layer's position in the signature manifest, used only to
	// make per-layer error messages locatable.
	index int

	payloadBytes []byte
	b64Sig       string
	certPEM      []byte // sigstore.CosignCertificateAnnotation, nil if absent
	chainPEM     []byte // sigstore.CosignChainAnnotation, nil if absent
	bundleJSON   []byte // sigstore.CosignBundleAnnotation, nil if absent
}

// fetchCosignSigLayers pulls the Cosign signature manifest for pull's digest
// and returns every signature layer it carries, along with the repository name
// and the signature reference (both wanted for error messages and payload
// claim checks). It is mode-agnostic on purpose: the same bytes feed whichever
// verification path verifyBaseImage already chose.
func (r *Resolver) fetchCosignSigLayers(ctx context.Context, ref string, pull *pulledManifest, insecure bool, registryConfigPath string) (repo string, sigRefStr string, out []cosignSigLayer, err error) {
	// Same name options Resolve used for the base pull itself: without them an
	// insecure non-loopback registry parses for the image and fails for its
	// signature.
	nameOpts := []name.Option{name.WeakValidation}
	if insecure {
		nameOpts = append(nameOpts, name.Insecure)
	}

	parsedRef, err := name.ParseReference(ref, nameOpts...)
	if err != nil {
		return "", "", nil, fmt.Errorf("baseimage: parse reference %s: %w: %w", ref, err, core.ErrBaseSignatureInvalid)
	}
	repo = parsedRef.Context().Name()
	digest := pull.digest

	// Cosign signature tag convention: <digest-alg>-<digest-hex>.sig
	sigRefStr = repo + ":" + digest.Algorithm + "-" + digest.Hex + ".sig"

	sigRef, err := name.ParseReference(sigRefStr, nameOpts...)
	if err != nil {
		return repo, sigRefStr, nil, fmt.Errorf("baseimage: parse signature reference %s: %w: %w", sigRefStr, err, core.ErrBaseSignatureInvalid)
	}

	opts, err := r.remoteOptions(ctx, insecure, registryConfigPath)
	if err != nil {
		return repo, sigRefStr, nil, fmt.Errorf("baseimage: resolve auth for signature %s: %w: %w", ref, err, core.ErrBaseSignatureInvalid)
	}
	sigImg, err := remote.Image(sigRef, opts...)
	if err != nil {
		return repo, sigRefStr, nil, fmt.Errorf("baseimage: fetch Cosign signature for %s (%s): %w: %w", ref, sigRefStr, err, core.ErrBaseSignatureInvalid)
	}

	layers, err := sigImg.Layers()
	if err != nil || len(layers) == 0 {
		return repo, sigRefStr, nil, fmt.Errorf("baseimage: no signature layers found in %s: %w", sigRefStr, core.ErrBaseSignatureInvalid)
	}

	manifest, err := sigImg.Manifest()
	if err != nil {
		return repo, sigRefStr, nil, fmt.Errorf("baseimage: read signature manifest in %s: %w: %w", sigRefStr, err, core.ErrBaseSignatureInvalid)
	}

	out = make([]cosignSigLayer, 0, len(layers))
	for i, l := range layers {
		payloadBytes, rerr := readLayer(l)
		if rerr != nil {
			return repo, sigRefStr, nil, fmt.Errorf("baseimage: read signature layer %d in %s: %w: %w", i, sigRefStr, rerr, core.ErrBaseSignatureInvalid)
		}

		sl := cosignSigLayer{index: i, payloadBytes: payloadBytes}

		var ann map[string]string
		if i < len(manifest.Layers) {
			ann = manifest.Layers[i].Annotations
		}
		if ann != nil {
			// dev.cosignproject.cosign/signature is Cosign's real
			// static-signature annotation key (see sigstore/cosign
			// pkg/oci/static); the OCI one is a defensive fallback.
			if sig, ok := ann[sigstore.CosignSignatureAnnotation]; ok {
				sl.b64Sig = sig
			} else if sig, ok := ann["org.opencontainers.image.signature"]; ok {
				sl.b64Sig = sig
			}
			sl.certPEM = annotationBytes(ann, sigstore.CosignCertificateAnnotation)
			sl.chainPEM = annotationBytes(ann, sigstore.CosignChainAnnotation)
			sl.bundleJSON = annotationBytes(ann, sigstore.CosignBundleAnnotation)
		}
		// Manifest-level fallback, kept from the original single-layer
		// implementation for signature artifacts that annotate the manifest
		// rather than the layer descriptor.
		if sl.b64Sig == "" && manifest.Annotations != nil {
			if sig, ok := manifest.Annotations[sigstore.CosignSignatureAnnotation]; ok {
				sl.b64Sig = sig
			}
		}

		out = append(out, sl)
	}

	return repo, sigRefStr, out, nil
}

// readLayer reads one signature layer's blob. Split out so the reader is
// closed per iteration rather than deferred to the end of a loop.
func readLayer(l v1.Layer) ([]byte, error) {
	rc, err := l.Compressed()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// annotationBytes returns the named annotation's value with surrounding
// whitespace trimmed, or nil when it is absent or empty.
func annotationBytes(ann map[string]string, key string) []byte {
	v := strings.TrimSpace(ann[key])
	if v == "" {
		return nil
	}
	return []byte(v)
}

// verifyStaticKeySignature verifies the signature layers against a static
// public key. Any one layer verifying is enough — a signature tag carrying
// several layers is signed several times, and one good signature by the
// expected key is proof. cosign.Signer.Verify checks the payload's
// docker-reference and docker-manifest-digest claims against repo/digest, so
// a valid signature over some *other* image is not accepted here.
func (r *Resolver) verifyStaticKeySignature(ctx context.Context, ref, repo, sigRefStr string, layers []cosignSigLayer, digest v1.Hash, pubKeyPEM []byte) error {
	var errs []error

	for _, l := range layers {
		if l.b64Sig == "" {
			errs = append(errs, fmt.Errorf("layer %d: no %s annotation found on signature manifest %s", l.index, sigstore.CosignSignatureAnnotation, sigRefStr))
			continue
		}
		sigBytes, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(l.b64Sig))
		if decErr != nil || len(sigBytes) == 0 {
			errs = append(errs, fmt.Errorf("layer %d: decode base64 signature from %s: %w", l.index, sigRefStr, decErr))
			continue
		}

		bundle := ports.CosignSignatureBundle{
			PayloadBytes:    l.payloadBytes,
			SignatureBytes:  sigBytes,
			Base64Signature: l.b64Sig,
			Repo:            repo,
			Digest:          digest,
		}
		if err := r.signer.Verify(ctx, bundle, pubKeyPEM, repo, digest); err != nil {
			errs = append(errs, fmt.Errorf("layer %d: %w", l.index, err))
			continue
		}

		r.logger().Info("base image Cosign signature verified", "ref", ref, "sig_ref", sigRefStr)
		return nil
	}

	if len(errs) == 0 {
		return fmt.Errorf("baseimage: no signature layers found in %s: %w", sigRefStr, core.ErrBaseSignatureInvalid)
	}
	return fmt.Errorf("baseimage: Cosign signature verification failed for %s: %w: %w", ref, errors.Join(errs...), core.ErrBaseSignatureInvalid)
}

// verifyKeylessSignature verifies the signature layers as Sigstore keyless
// signatures against the expected certificate identity. Any one layer
// verifying is enough, for the same reason as in the static-key path.
//
// Two things this does that the port's Verify cannot do for us:
//
//   - It refuses to run at all if no layer carries both the certificate and
//     Rekor bundle annotations, rather than falling back to another mode. A
//     missing keyless signature on a preset configured to verify keyless is a
//     failure, not an invitation to try something weaker.
//
//   - It re-checks the payload's own claims after a successful verification.
//     sigstore-go proves "this identity signed exactly these payload bytes";
//     it says nothing about whether those bytes describe *this* image. Without
//     the claim check, a genuine distroless signature over a different
//     distroless digest would satisfy this function.
func (r *Resolver) verifyKeylessSignature(
	ctx context.Context,
	ref, repo, sigRefStr string,
	layers []cosignSigLayer,
	digest v1.Hash,
	identity ports.KeylessIdentity,
	trustedRootJSON []byte,
) error {
	var errs []error

	for _, l := range layers {
		// Mode is fixed for the whole Resolve call, so a layer without
		// keyless material is skipped, never verified some other way.
		if len(l.certPEM) == 0 || len(l.bundleJSON) == 0 {
			continue
		}

		sigBytes, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(l.b64Sig))
		if decErr != nil || len(sigBytes) == 0 {
			errs = append(errs, fmt.Errorf("layer %d: decode base64 %s annotation from %s: %w", l.index, sigstore.CosignSignatureAnnotation, sigRefStr, decErr))
			continue
		}

		result, err := r.keyless.Verify(ctx, ports.KeylessVerifyRequest{
			PayloadBytes:    l.payloadBytes,
			SignatureBytes:  sigBytes,
			CertificatePEM:  l.certPEM,
			ChainPEM:        l.chainPEM,
			RekorBundleJSON: l.bundleJSON,
			Identity:        identity,
			TrustedRootJSON: trustedRootJSON,
		})
		if err != nil {
			// ErrNoBundle means this particular layer is not keyless material
			// after all (it should have been filtered out above; the verifier
			// is the authority on what counts). Skip it without recording a
			// verification failure — and without downgrading it.
			if errors.Is(err, sigstore.ErrNoBundle) {
				r.logger().Debug("skipping signature layer without keyless material", "ref", ref, "sig_ref", sigRefStr, "layer", l.index)
				continue
			}
			errs = append(errs, fmt.Errorf("layer %d: %w", l.index, err))
			continue
		}

		if err := checkSimpleSigningClaims(l.payloadBytes, repo, digest); err != nil {
			errs = append(errs, fmt.Errorf("layer %d: keyless signature is valid but %w", l.index, err))
			continue
		}

		r.logger().Info("base image keyless signature verified",
			"ref", ref,
			"sig_ref", sigRefStr,
			"issuer", result.Issuer,
			"san", result.SAN,
			"rekor_log_id", result.RekorLogID,
			"rekor_log_index", result.RekorLogIndex,
			"integrated_time", result.IntegratedTime)
		return nil
	}

	if len(errs) == 0 {
		return fmt.Errorf(
			"baseimage: %s: signature manifest %s carries no keyless signature material — no layer has both the %s and %s "+
				"annotations, so there is no Fulcio certificate or Rekor entry to verify; if this image is signed with a "+
				"static key instead, pass --base-verify-mode=static-key (or the equivalent BaseImageRequest.VerifyMode) "+
				"and set POKKUM_BASE_IMAGE_PUBKEY to that key: %w",
			ref, sigRefStr, sigstore.CosignCertificateAnnotation, sigstore.CosignBundleAnnotation, core.ErrBaseSignatureInvalid)
	}
	return fmt.Errorf("baseimage: keyless signature verification failed for %s (%s): %w: %w", ref, sigRefStr, errors.Join(errs...), core.ErrBaseSignatureInvalid)
}

// checkSimpleSigningClaims validates that a Simple Signing payload actually
// names repo at digest. The static-key path gets this from
// cosign.Signer.Verify; the keyless path goes through ports.KeylessVerifier
// instead, which proves only that the payload bytes were signed by the
// expected identity, so the claims have to be checked here.
//
// Both payload type strings are accepted: real upstream Cosign signatures
// (distroless, chainguard) write ports.CosignContainerImageSignatureType,
// while Pokkum's own static-key signer writes ports.CosignSimpleSigningType.
func checkSimpleSigningClaims(payloadBytes []byte, repo string, digest v1.Hash) error {
	var payload ports.CosignSimpleSigningPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return fmt.Errorf("its signed payload is not valid Simple Signing JSON: %w", err)
	}

	switch payload.Critical.Type {
	case ports.CosignSimpleSigningType, ports.CosignContainerImageSignatureType:
	default:
		return fmt.Errorf("its signed payload type is %q, expected %q or %q",
			payload.Critical.Type, ports.CosignContainerImageSignatureType, ports.CosignSimpleSigningType)
	}

	if got := payload.Critical.Identity.DockerReference; got != repo {
		return fmt.Errorf("it is a signature for repository %q, not %q", got, repo)
	}
	if got := payload.Critical.Image.DockerManifestDigest; got != digest.String() {
		return fmt.Errorf("it is a signature for digest %q, not %q", got, digest.String())
	}
	return nil
}

// fingerprint hashes key material so it can be part of a comparable cache key
// without the key itself being retained. Empty input yields "".
func fingerprint(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}
