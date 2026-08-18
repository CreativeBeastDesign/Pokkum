// Package assetoverlay implements the --asset-overlay rolling-deploy feature:
// resolving which prior generations of an image were pushed to a given
// target, pulling their /app/client immutable asset content by digest, and
// merging non-conflicting files into a scratch directory the packager stages
// as a new, separate OCI layer. See internal/ports/packager.go's
// PackageRequest.AssetOverlayDir doc comment for the packaging half.
//
// Lineage discovery is entirely registry-side and Kubernetes-independent:
// every image pokkum build pushes carries a ports.AnnotationPredecessor
// manifest annotation naming the digest it replaced at the same push target.
// ResolvePredecessorChain walks that chain by repeatedly pulling manifests
// (not full images) starting from the target's current tag. This was a
// deliberate design choice over reading the pre-existing
// pokkum.dev/image-history annotation: that annotation is written and read
// exclusively by internal/adapters/k8s (resolve/apply/rollback) — pokkum
// build has no code path to parse a Kubernetes manifest, so a build-time
// flag structurally cannot depend on it without either coupling build to
// Kubernetes or requiring every caller to already be using resolve/apply.
package assetoverlay

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registryutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/transportutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// clientLayerCreatedBy is the exact v1.History.CreatedBy string the packager
// stamps on the layer built from AppClientDirPrefix (see
// internal/adapters/packager/packager.go). Used to locate that specific
// layer inside a pulled predecessor image without assuming a fixed layer
// index or count — the layered strategy's own layer set varies per build.
const clientLayerCreatedBy = "pokkum: add " + ports.AppClientDirPrefix

// clientRootPrefix is AppClientDirPrefix's own tar-relative root (no leading
// or trailing slash asymmetry — see BuildDirectoryTreeLayer's path.Clean/
// TrimPrefix convention). extractImmutableAssets strips exactly this much
// off each tar entry, NOT immutableAssetPrefix, so that "_app/immutable/..."
// survives into destDir's relative path — see immutableAssetPrefix's doc
// comment for why that distinction is load-bearing.
var clientRootPrefix = strings.TrimPrefix(ports.AppClientDirPrefix, "/") + "/"

// immutableAssetPrefix is the path, relative to the tar root (which itself
// has no leading slash — see BuildDirectoryTreeLayer's own path.Clean/
// TrimPrefix convention), under which SvelteKit's content-hashed,
// safe-to-cache-forever build output lives. Only paths under this prefix are
// ever extracted — anything else in the client layer (non-hashed HTML,
// service-worker.js) is deliberately excluded, since it isn't safe to serve
// from a stale generation.
var immutableAssetPrefix = clientRootPrefix + "_app/immutable/"

var _ ports.AssetOverlayResolver = (*Resolver)(nil)

// Resolver implements ports.AssetOverlayResolver. The zero value is ready to
// use — this package holds no mutable state; every method derives
// everything it needs from its arguments.
type Resolver struct{}

// NewResolver returns a ready-to-use Resolver.
func NewResolver() *Resolver { return &Resolver{} }

// ResolvePredecessorChain implements ports.AssetOverlayResolver.
func (r *Resolver) ResolvePredecessorChain(ctx context.Context, repo, tag, registryConfigPath string, insecure bool, maxDepth int) ([]string, error) {
	return ResolvePredecessorChain(ctx, repo, tag, registryConfigPath, insecure, maxDepth)
}

// BuildOverlayDir implements ports.AssetOverlayResolver.
func (r *Resolver) BuildOverlayDir(ctx context.Context, repo string, digests []string, registryConfigPath string, insecure bool) (string, error) {
	return BuildOverlayDir(ctx, repo, digests, registryConfigPath, insecure)
}

// ResolveDigest implements ports.AssetOverlayResolver: resolves an arbitrary
// ref (as supplied via --asset-overlay-from) to its FULLY-QUALIFIED
// "repo@digest" form, not just the bare digest.
//
// This is deliberate, not cosmetic: --asset-overlay-from's whole documented
// purpose is naming "arbitrary external images, not this build's own push
// target" (see pipeline.go's Stage 4.4 comment) — a ref may resolve in a
// completely different repository than req.Repo. Returning only the bare
// digest here was a real bug found by adversarial review: BuildOverlayDir
// downstream always pulled every resolved digest from req.Repo regardless
// of where ResolveDigest actually found it, so any cross-repo
// --asset-overlay-from ref 404'd on pull and hard-failed the whole build —
// directly contradicting the flag's own contract. Returning the qualified
// ref here lets resolveOverlaySourceRef (below) carry the correct source
// repository all the way through to the pull.
func (r *Resolver) ResolveDigest(ctx context.Context, ref, registryConfigPath string, insecure bool) (string, error) {
	opts, err := remoteOptions(ctx, insecure, registryConfigPath)
	if err != nil {
		return "", err
	}
	parsed, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		return "", fmt.Errorf("asset overlay: parse %q: %w: %w", ref, err, core.ErrAssetOverlayResolutionFailed)
	}
	desc, err := remote.Get(parsed, opts...)
	if err != nil {
		return "", fmt.Errorf("asset overlay: resolve %q: %w: %w", ref, err, core.ErrAssetOverlayResolutionFailed)
	}
	return parsed.Context().Name() + "@" + desc.Digest.String(), nil
}

// resolveOverlaySourceRef returns the fully-qualified "repo@digest" ref to
// pull for one resolved overlay source. source is either a bare digest
// (as ResolvePredecessorChain produces — always within defaultRepo, this
// build's own push target, since chain-walking never leaves req.Repo) or an
// already-qualified "repo@digest" ref (as ResolveDigest produces for an
// explicit --asset-overlay-from entry, which may name a different
// repository entirely). The two are unambiguous: a bare digest string
// ("sha256:<64 hex>") never contains "@"; a qualified ref always does.
func resolveOverlaySourceRef(defaultRepo, source string) string {
	if strings.Contains(source, "@") {
		return source
	}
	return defaultRepo + "@" + source
}

func remoteOptions(ctx context.Context, insecure bool, registryConfigPath string) ([]remote.Option, error) {
	kc, err := registryutils.ResolveKeychain(registryConfigPath)
	if err != nil {
		return nil, err
	}
	opts := []remote.Option{
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(kc),
	}
	if insecure {
		opts = append(opts, remote.WithTransport(transportutils.InsecureTransport()))
	}
	return opts, nil
}

// manifestAnnotations reads the manifest-level annotations off a resolved
// remote.Descriptor, handling both a multi-platform index (whose own
// annotations are what pokkum build stamps predecessor/overlay-source info
// onto — see indexAnnotations's doc comment in
// internal/adapters/packager/config.go) and a single-platform image, mirrors
// cmd/pokkum/history.go's fetchImageHistory.
func manifestAnnotations(desc *remote.Descriptor) (map[string]string, error) {
	var annotations map[string]string
	switch {
	case desc.MediaType.IsIndex():
		idx, err := desc.ImageIndex()
		if err != nil {
			return nil, fmt.Errorf("read index: %w", err)
		}
		im, err := idx.IndexManifest()
		if err != nil {
			return nil, fmt.Errorf("read index manifest: %w", err)
		}
		annotations = im.Annotations
	default:
		img, err := desc.Image()
		if err != nil {
			return nil, fmt.Errorf("read image: %w", err)
		}
		m, err := img.Manifest()
		if err != nil {
			return nil, fmt.Errorf("read manifest: %w", err)
		}
		annotations = m.Annotations
	}
	if annotations == nil {
		annotations = map[string]string{}
	}
	return annotations, nil
}

// ResolvePredecessorChain resolves repo:tag's current digest and walks its
// ports.AnnotationPredecessor chain up to maxDepth entries deep, returning
// the resolved digests most-recent-first (index 0 is the image repo:tag
// currently points to, the immediate predecessor of the build about to
// happen).
//
// Returns (nil, nil) — not an error — when repo:tag does not resolve at all:
// this is the expected, self-bootstrapping state for the first image ever
// pushed to a target with --asset-overlay set. A genuine registry problem
// (auth failure, network error) on that same first lookup IS a real error,
// deliberately not swallowed the same way, since silently producing zero
// generations there would look identical to "no prior build" while actually
// meaning "the configured overlay feature can't work right now" — a
// meaningfully different situation for the caller to know about.
//
// A predecessor digest found via the chain that itself fails to resolve
// (garbage collected by the registry, most likely) truncates the chain at
// that point rather than erroring — registry GC of old generations is
// ordinary hygiene, not a build-breaking problem.
func ResolvePredecessorChain(ctx context.Context, repo, tag, registryConfigPath string, insecure bool, maxDepth int) ([]string, error) {
	if maxDepth <= 0 {
		return nil, nil
	}
	opts, err := remoteOptions(ctx, insecure, registryConfigPath)
	if err != nil {
		return nil, err
	}

	digests := make([]string, 0, maxDepth)
	seen := make(map[string]bool, maxDepth)
	currentRef := repo + ":" + tag

	for len(digests) < maxDepth {
		parsed, perr := name.ParseReference(currentRef, name.WeakValidation)
		if perr != nil {
			// currentRef on the FIRST iteration is repo+":"+tag — the
			// caller's own, trusted input, so a parse failure there is a
			// real, immediate error. On every LATER iteration, currentRef
			// is built from ports.AnnotationPredecessor, a value read off a
			// remote manifest — reachable by anyone with push access to
			// repo (auto-discovery) or entirely attacker-chosen (an
			// --asset-overlay-from ref pointed at a crafted image). A
			// single malformed predecessor annotation must truncate the
			// chain here, exactly like the GC'd/not-found case just above,
			// not hard-fail the whole build — otherwise one bad annotation
			// permanently wedges every future --asset-overlay build against
			// this repo, which is a self-inflicted denial of service this
			// feature has no business causing.
			if len(digests) == 0 {
				return nil, fmt.Errorf("asset overlay: parse %q: %w: %w", currentRef, perr, core.ErrAssetOverlayResolutionFailed)
			}
			break
		}

		desc, gerr := remote.Get(parsed, opts...)
		if gerr != nil {
			if isNotFound(gerr) {
				// The starting tag has never been pushed (first-ever build to
				// this target), or a found predecessor was since garbage
				// collected — either way, stop here with what we have.
				break
			}
			if len(digests) == 0 {
				return nil, fmt.Errorf("asset overlay: resolve %q: %w: %w", currentRef, gerr, core.ErrAssetOverlayResolutionFailed)
			}
			break
		}

		digestStr := desc.Digest.String()
		if seen[digestStr] {
			break // defends against a corrupted/cyclic predecessor chain
		}
		seen[digestStr] = true
		digests = append(digests, digestStr)

		annotations, aerr := manifestAnnotations(desc)
		if aerr != nil {
			break
		}
		predecessor, ok := annotations[ports.AnnotationPredecessor]
		if !ok || predecessor == "" {
			break
		}
		currentRef = repo + "@" + predecessor
	}

	return digests, nil
}

func isNotFound(err error) bool {
	var terr *transport.Error
	if errors.As(err, &terr) {
		return terr.StatusCode == http.StatusNotFound
	}
	return false
}

// ExtractClientImmutableAssets pulls one resolved overlay source (source is
// either a bare digest, resolved via resolveOverlaySourceRef against
// defaultRepo, or an already-qualified "repo@digest" ref that may name a
// different repository entirely — see resolveOverlaySourceRef's doc
// comment), locates its AppClientDirPrefix layer (via the exact CreatedBy
// history convention the packager stamps, cross-checked against
// RootFS.DiffIDs length the same way cmd/pokkum/explain.go's layerPurposes
// does before trusting the mapping — a config whose History/DiffIDs
// disagree in length yields no match rather than a possibly-misaligned
// one), and extracts every tar entry under immutableAssetPrefix into
// destDir, preserving the relative path structure so the extracted tree can
// be handed directly to packager.BuildDirectoryTreeLayerWithPruning.
//
// For a multi-platform digest (an index), the first platform child is used:
// client assets are pure build output (JS/CSS/HTML) and do not vary by
// target platform, so any one child's copy is equally valid.
func ExtractClientImmutableAssets(ctx context.Context, defaultRepo, source, registryConfigPath string, insecure bool, destDir string) error {
	opts, err := remoteOptions(ctx, insecure, registryConfigPath)
	if err != nil {
		return err
	}

	ref := resolveOverlaySourceRef(defaultRepo, source)
	parsed, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		return fmt.Errorf("asset overlay: parse %q: %w: %w", ref, err, core.ErrAssetOverlayResolutionFailed)
	}
	desc, err := remote.Get(parsed, opts...)
	if err != nil {
		return fmt.Errorf("asset overlay: pull %q: %w: %w", ref, err, core.ErrAssetOverlayResolutionFailed)
	}

	img, err := selectAnyPlatform(desc, ref)
	if err != nil {
		return err
	}

	cfg, err := img.ConfigFile()
	if err != nil {
		return fmt.Errorf("asset overlay: %s: read config: %w: %w", ref, err, core.ErrAssetOverlayResolutionFailed)
	}
	layers, err := img.Layers()
	if err != nil {
		return fmt.Errorf("asset overlay: %s: read layers: %w: %w", ref, err, core.ErrAssetOverlayResolutionFailed)
	}

	idx, ok := clientLayerIndex(cfg, len(layers))
	if !ok {
		// No /app/client layer in this generation (e.g. it was a
		// --strategy=exe build, or genuinely had no client assets) —
		// nothing to extract, not an error.
		return nil
	}

	rc, err := layers[idx].Uncompressed()
	if err != nil {
		return fmt.Errorf("asset overlay: %s: read client layer: %w: %w", ref, err, core.ErrAssetOverlayResolutionFailed)
	}
	defer rc.Close()

	return extractImmutableAssets(rc, destDir, ref)
}

// selectAnyPlatform returns a concrete v1.Image for desc, picking the first
// child of a multi-platform index — client assets are pure build output and
// identical across platforms, so which child is used does not matter.
func selectAnyPlatform(desc *remote.Descriptor, ref string) (v1.Image, error) {
	if !desc.MediaType.IsIndex() {
		img, err := desc.Image()
		if err != nil {
			return nil, fmt.Errorf("asset overlay: %s: read image: %w: %w", ref, err, core.ErrAssetOverlayResolutionFailed)
		}
		return img, nil
	}
	idx, err := desc.ImageIndex()
	if err != nil {
		return nil, fmt.Errorf("asset overlay: %s: read index: %w: %w", ref, err, core.ErrAssetOverlayResolutionFailed)
	}
	im, err := idx.IndexManifest()
	if err != nil {
		return nil, fmt.Errorf("asset overlay: %s: read index manifest: %w: %w", ref, err, core.ErrAssetOverlayResolutionFailed)
	}
	if len(im.Manifests) == 0 {
		return nil, fmt.Errorf("asset overlay: %s: index has no child manifests: %w", ref, core.ErrAssetOverlayResolutionFailed)
	}
	img, err := idx.Image(im.Manifests[0].Digest)
	if err != nil {
		return nil, fmt.Errorf("asset overlay: %s: fetch child %s: %w: %w", ref, im.Manifests[0].Digest, err, core.ErrAssetOverlayResolutionFailed)
	}
	return img, nil
}

// clientLayerIndex locates the layer index whose History entry matches
// clientLayerCreatedBy, cross-checking History (EmptyLayer-filtered) against
// RootFS.DiffIDs length and layerCount before trusting the mapping — same
// discipline as cmd/pokkum/explain.go's layerPurposes, so a config whose
// History/DiffIDs disagree in length (a malformed or foreign image) yields
// no match rather than a possibly-misaligned one.
func clientLayerIndex(cfg *v1.ConfigFile, layerCount int) (int, bool) {
	if cfg == nil {
		return 0, false
	}
	var nonEmptyHistoryCount int
	for _, h := range cfg.History {
		if !h.EmptyLayer {
			nonEmptyHistoryCount++
		}
	}
	if nonEmptyHistoryCount != len(cfg.RootFS.DiffIDs) || nonEmptyHistoryCount != layerCount {
		return 0, false
	}

	i := 0
	for _, h := range cfg.History {
		if h.EmptyLayer {
			continue
		}
		if strings.TrimSpace(h.CreatedBy) == clientLayerCreatedBy {
			return i, true
		}
		i++
	}
	return 0, false
}

// extractImmutableAssets reads a tar stream and writes every regular file
// under immutableAssetPrefix into destDir, preserving the path relative to
// AppClientDirPrefix's own root (so destDir's tree is directly consumable by
// BuildDirectoryTreeLayerWithPruning targeting AppClientDirPrefix again).
// Only regular files are extracted — directory entries are implicit in the
// relative paths written, and any other tar entry type (symlink, etc.) is
// skipped rather than followed, since the source of this content is a
// previously-built Pokkum image, not user-controlled input, but there is no
// reason for the immutable asset tree to ever legitimately contain one.
//
// destDir accumulates content across multiple calls (one per resolved
// generation, oldest generation last — see BuildOverlayDir): if a path this
// call would write already exists from an earlier call, its bytes are
// compared, not blindly overwritten. Identical content is silently skipped
// (expected — the whole point of a content-hashed filename is that it never
// changes); different content at the same path is core.ErrAssetOverlayConflict,
// a hard failure, per this feature's stated conflict policy — a genuine
// collision here means an upstream invariant broke (a hash collision, or a
// non-immutable file wrongly living under the immutable prefix) and needs a
// human, not a silent pick of either generation's bytes.
// maxOverlayEntryBytes caps a single extracted asset's size. The content
// this bounds comes from a remote registry's layer tar stream — reachable
// via an automatically-walked pokkum.dev/predecessor chain or an arbitrary
// --asset-overlay-from ref — not a locally-produced, inherently-bounded
// artifact, so an unbounded io.ReadAll here would let a crafted layer OOM
// or fill the disk of the machine running the build. 256MiB comfortably
// exceeds any real SvelteKit-generated immutable chunk while still bounding
// the damage a single malicious/corrupted entry can do; mirrors the same
// defensive pattern already used elsewhere in this codebase for
// registry/network-sourced content (e.g. layerdiffutils.diff.go's
// io.LimitReader(tr, 500<<20), bunruntime.resolver.go's capped archive
// reads).
const maxOverlayEntryBytes = 256 << 20

// safeJoinOverlayPath joins rel onto destDir, rejecting anything that would
// resolve outside destDir. rel has already passed extractImmutableAssets'
// own path.Clean + prefix-based rejection of ".."/absolute entries before
// reaching here, but this is a second, independent check at the actual
// write boundary — the same defense-in-depth pattern as
// tests/integration/harness_test.go's safeJoin, which this mirrors. Belt
// and suspenders is deliberate: the first check can only be as good as
// every code path that reaches it, and this function is the one place that
// actually turns a path string into a filesystem write.
func safeJoinOverlayPath(destDir, rel string) (string, error) {
	joined := filepath.Join(destDir, filepath.FromSlash(rel))
	destDirClean := filepath.Clean(destDir)
	if joined != destDirClean && !strings.HasPrefix(joined, destDirClean+string(filepath.Separator)) {
		return "", fmt.Errorf("path traversal rejected: %q", rel)
	}
	return joined, nil
}

func extractImmutableAssets(r io.Reader, destDir, sourceRef string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("asset overlay: %s: read client layer tar: %w: %w", sourceRef, err, core.ErrAssetOverlayResolutionFailed)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		// path.Clean (POSIX-slash-aware — tar entry names are always "/"
		// separated regardless of host OS) BEFORE the prefix check, not
		// after: the source of this tar stream is a remote registry's
		// manifest/layer content, reachable either via a repo the caller
		// doesn't fully trust (a pokkum.dev/predecessor chain walked
		// automatically) or an arbitrary --asset-overlay-from ref the
		// caller supplies directly — not a locally-built, inherently-safe
		// artifact, however the doc comment above once assumed. An
		// uncleaned entry like "app/client/_app/immutable/../../../../pwned.txt"
		// would literally start with immutableAssetPrefix as a raw string
		// and pass the filter below, only to have filepath.Join silently
		// resolve the ".." segments and escape destDir once used as a
		// write target. Cleaning first makes such an entry collapse to
		// "pwned.txt", which no longer matches the prefix at all — the
		// filter itself becomes the containment check for this class of
		// entry. entryName is deliberately NOT filepath.Clean'd (host-OS
		// separator rules) — path.Clean is used because tar names are
		// POSIX paths, not host paths.
		entryName := path.Clean(strings.TrimPrefix(hdr.Name, "./"))
		if path.IsAbs(entryName) || entryName == ".." || strings.HasPrefix(entryName, "../") {
			continue // never a legitimate immutable-asset entry regardless of prefix
		}
		if !strings.HasPrefix(entryName, immutableAssetPrefix) {
			continue
		}
		// Strip only the client root ("app/client/"), NOT the immutable
		// prefix — rel must retain "_app/immutable/..." so that repackaging
		// destDir under AppClientDirPrefix again (BuildDirectoryTreeLayerWithPruning,
		// called from packager.go's appendAssetOverlayLayer) reproduces the
		// exact in-image path a browser actually requests. Stripping
		// immutableAssetPrefix here instead was a real bug caught by
		// TestRealBuild_AssetOverlay_TwoGenerations: it silently repackaged
		// every overlay asset one directory level too high
		// (/app/client/chunks/... instead of
		// /app/client/_app/immutable/chunks/...), which 404s in exactly the
		// scenario this feature exists to prevent.
		rel := strings.TrimPrefix(entryName, clientRootPrefix)
		if rel == "" {
			continue
		}

		destPath, err := safeJoinOverlayPath(destDir, rel)
		if err != nil {
			return fmt.Errorf("asset overlay: %s: %s: %w: %w", sourceRef, entryName, err, core.ErrAssetOverlayResolutionFailed)
		}

		content, err := io.ReadAll(io.LimitReader(tr, maxOverlayEntryBytes+1))
		if err != nil {
			return fmt.Errorf("asset overlay: %s: read %s: %w: %w", sourceRef, entryName, err, core.ErrAssetOverlayResolutionFailed)
		}
		if int64(len(content)) > maxOverlayEntryBytes {
			return fmt.Errorf("asset overlay: %s: %s: entry exceeds %d byte cap: %w", sourceRef, entryName, maxOverlayEntryBytes, core.ErrAssetOverlayResolutionFailed)
		}
		if existing, err := os.ReadFile(destPath); err == nil {
			if !bytes.Equal(existing, content) {
				return fmt.Errorf("asset overlay: %s: %s: content differs from an already-merged generation at the same hashed path — a content-hash collision or a non-immutable file under %s should be impossible: %w", sourceRef, rel, immutableAssetPrefix, core.ErrAssetOverlayConflict)
			}
			continue // identical — already correct from an earlier generation
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("asset overlay: read existing %s: %w: %w", destPath, err, core.ErrAssetOverlayResolutionFailed)
		}

		if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
			return fmt.Errorf("asset overlay: create %s: %w: %w", filepath.Dir(destPath), err, core.ErrAssetOverlayResolutionFailed)
		}
		if err := os.WriteFile(destPath, content, 0o644); err != nil {
			return fmt.Errorf("asset overlay: write %s: %w: %w", destPath, err, core.ErrAssetOverlayResolutionFailed)
		}
	}
}

// BuildOverlayDir resolves sources' /app/client immutable asset content (via
// ExtractClientImmutableAssets, most-recent generation first) and merges it
// into one new temp directory, hard-failing per extractImmutableAssets's
// conflict policy on any genuine collision. Returns an empty string (not an
// error) when sources is empty — the caller's signal that --asset-overlay
// resolved zero generations and no overlay layer should be added.
//
// defaultRepo is the fallback repository for any entry in sources that is a
// bare digest (auto-discovery via ResolvePredecessorChain, always within
// the caller's own req.Repo); an entry that is already a fully-qualified
// "repo@digest" ref (as ResolveDigest produces for --asset-overlay-from)
// carries its own repository and ignores defaultRepo entirely — see
// resolveOverlaySourceRef. Passing every source through defaultRepo
// unconditionally, as an earlier version of this function did, silently
// mispulled every cross-repo --asset-overlay-from entry from the wrong
// repository and hard-failed the build; this is why the distinction matters.
//
// Callers own cleanup of the returned directory (os.RemoveAll) once
// packaging has consumed it.
func BuildOverlayDir(ctx context.Context, defaultRepo string, sources []string, registryConfigPath string, insecure bool) (string, error) {
	if len(sources) == 0 {
		return "", nil
	}
	destDir, err := os.MkdirTemp("", "pokkum-asset-overlay-*")
	if err != nil {
		return "", fmt.Errorf("asset overlay: create scratch directory: %w: %w", err, core.ErrAssetOverlayResolutionFailed)
	}
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			_ = os.RemoveAll(destDir)
			return "", err
		}
		if err := ExtractClientImmutableAssets(ctx, defaultRepo, source, registryConfigPath, insecure, destDir); err != nil {
			_ = os.RemoveAll(destDir)
			return "", err
		}
	}
	return destDir, nil
}
