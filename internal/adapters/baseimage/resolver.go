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
// # Caching
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
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/lockfileutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// insecureTransport is used for requests against BaseImageRequest.Insecure
// targets: local or self-signed test registries only. It is package-level
// because http.Transport is meant to be reused, not built per call.
var insecureTransport http.RoundTripper = &http.Transport{
	TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // opt-in via BaseImageRequest.Insecure
}

// Resolver implements ports.BaseImageResolver against a real container
// registry. The zero value is not usable; construct with NewResolver.
//
// Resolver is safe for concurrent use: all mutable state lives behind mu.
type Resolver struct {
	log *slog.Logger

	mu     sync.Mutex
	pulls  map[pullKey]pullEntry
	images map[imageKey]imageEntry
}

// pullKey identifies one top-level manifest pull.
type pullKey struct {
	ref      string
	insecure bool
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

// NewResolver constructs a Resolver. A nil logger defaults to slog.Default().
func NewResolver(log *slog.Logger) *Resolver {
	if log == nil {
		log = slog.Default()
	}
	return &Resolver{
		log:    log,
		pulls:  make(map[pullKey]pullEntry),
		images: make(map[imageKey]imageEntry),
	}
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

	// Handle pokkum.lock lookup
	var (
		lf         *ports.PokkumLockfile
		lockedFound bool
		lockKey     = string(req.Preset)
	)

	if req.LockfilePath != "" {
		loaded, lerr := lockfileutils.LoadLockfile(req.LockfilePath)
		if lerr == nil {
			lf = loaded
			if entry, ok := lockfileutils.GetLockedBase(lf, lockKey); ok && !req.UpdateBase {
				lockedFound = true
				if entry.PinnedRef != "" {
					ref = entry.PinnedRef
				}
				r.logger().Info("using locked base image from lockfile", "lockfile", req.LockfilePath, "key", lockKey, "ref", ref)
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

	nameOpts := []name.Option{name.WeakValidation}
	if req.Insecure {
		nameOpts = append(nameOpts, name.Insecure)
	}
	parsedRef, err := name.ParseReference(ref, nameOpts...)
	if err != nil {
		return nil, fmt.Errorf("baseimage: parse %q: %w: %w", ref, err, core.ErrInvalidBaseImage)
	}

	pull, err := r.pull(ctx, parsedRef, ref, req.Insecure)
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
		lockfileutils.SetLockedBase(lf, lockKey, ports.BaseLockEntry{
			Ref:       origRef,
			Digest:    pull.digest.String(),
			PinnedRef: out.PinnedRef,
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		})
		if serr := lockfileutils.SaveLockfile(req.LockfilePath, lf); serr != nil {
			r.logger().Warn("failed to save updated base image lockfile", "path", req.LockfilePath, "err", serr)
		} else {
			r.logger().Info("updated base image lockfile", "path", req.LockfilePath, "preset", req.Preset, "pinned_ref", out.PinnedRef)
		}
	}

	r.logger().Info("resolved base image",
		"ref", ref, "pinned_ref", out.PinnedRef, "digest", pull.digest.String(),
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
func (r *Resolver) pull(ctx context.Context, parsedRef name.Reference, rawRef string, insecure bool) (*pulledManifest, error) {
	key := pullKey{ref: rawRef, insecure: insecure}

	r.mu.Lock()
	if e, ok := r.pulls[key]; ok {
		r.mu.Unlock()
		r.logger().Debug("base image pull cache hit", "ref", rawRef)
		return e.pull, e.err
	}
	r.mu.Unlock()

	r.logger().Debug("pulling base image manifest", "ref", rawRef, "insecure", insecure)
	desc, err := remote.Get(parsedRef, r.remoteOptions(ctx, insecure)...)

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
// context threading (so a cancelled build stops pulling mid-transfer) and the
// default keychain (so private and mirrored bases work without Pokkum ever
// handling a credential itself).
func (r *Resolver) remoteOptions(ctx context.Context, insecure bool) []remote.Option {
	opts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	}
	if insecure {
		opts = append(opts, remote.WithTransport(insecureTransport))
	}
	return opts
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
