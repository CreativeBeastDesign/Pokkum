package comparator

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/layerdiffutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registryutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

var _ ports.ImageComparator = (*Comparator)(nil)

// Comparator implements ports.ImageComparator by comparing remote container images
// against locally rebuilt OCI tarballs across L1 (exact), L2 (semantic diffIDs), and L3 (file-level layer diffs).
type Comparator struct {
	log *slog.Logger

	// assetOverlay and layerBuilder, when both set, let CompareImages
	// reconstruct a --asset-overlay image's merged layer from its own
	// ports.AnnotationAssetOverlaySources annotation before comparing — see
	// reconcileAssetOverlay. Nil unless constructed via
	// NewComparatorWithAssetOverlay: an image carrying that annotation with
	// either dependency unset is a hard error (fail closed), never a
	// silent skip of the overlay comparison.
	assetOverlay ports.AssetOverlayResolver
	layerBuilder ports.LayerBuilder
}

// NewComparator constructs an ImageComparator adapter instance with no
// --asset-overlay reconstruction support. Comparing an image that carries
// ports.AnnotationAssetOverlaySources with a Comparator built this way is a
// hard error — use NewComparatorWithAssetOverlay to support it.
func NewComparator(log *slog.Logger) *Comparator {
	return NewComparatorWithAssetOverlay(log, nil, nil)
}

// NewComparatorWithAssetOverlay constructs an ImageComparator adapter
// instance able to reconstruct a --asset-overlay image's merged layer from
// its own annotation before comparing (see reconcileAssetOverlay). Pass the
// real internal/adapters/assetoverlay.Resolver and
// internal/adapters/packager.LayerBuilderAdapter from cmd/pokkum, the sole
// composition root permitted to construct concrete adapters — this
// comparator package itself may only depend on the ports.AssetOverlayResolver
// and ports.LayerBuilder interfaces, per the hexagonal architecture's ban on
// adapter-to-adapter imports.
func NewComparatorWithAssetOverlay(log *slog.Logger, assetOverlay ports.AssetOverlayResolver, layerBuilder ports.LayerBuilder) *Comparator {
	if log == nil {
		log = slog.Default()
	}
	return &Comparator{log: log, assetOverlay: assetOverlay, layerBuilder: layerBuilder}
}

// CompareImages performs L1 (exact), L2 (semantic diffIDs), and L3 (file-level layerdiff) comparisons.
func (c *Comparator) CompareImages(ctx context.Context, req ports.ImageComparatorRequest) (ports.ImageComparatorResult, error) {
	if strings.TrimSpace(req.RemoteImageRef) == "" {
		return ports.ImageComparatorResult{}, fmt.Errorf("comparator: remote image ref is required: %w", core.ErrInvalidRequest)
	}
	if strings.TrimSpace(req.LocalTarball) == "" {
		return ports.ImageComparatorResult{}, fmt.Errorf("comparator: local tarball path is required: %w", core.ErrInvalidRequest)
	}

	c.log.DebugContext(ctx, "comparing remote image against local rebuild tarball",
		"remote", req.RemoteImageRef, "local", req.LocalTarball)

	// Load local rebuild image from tarball
	localImg, err := tarball.ImageFromPath(req.LocalTarball, nil)
	if err != nil {
		return ports.ImageComparatorResult{}, fmt.Errorf("comparator: load local tarball %s: %w", req.LocalTarball, err)
	}

	// Load remote image (or remote tarball if local path provided)
	var remoteImg v1.Image
	var remoteRepo string
	if strings.HasSuffix(req.RemoteImageRef, ".tar") {
		remoteImg, err = tarball.ImageFromPath(req.RemoteImageRef, nil)
		if err != nil {
			return ports.ImageComparatorResult{}, fmt.Errorf("comparator: load remote tarball %s: %w", req.RemoteImageRef, err)
		}
	} else {
		parsedRef, perr := name.ParseReference(req.RemoteImageRef, name.WeakValidation)
		if perr != nil {
			return ports.ImageComparatorResult{}, fmt.Errorf("comparator: parse remote ref %s: %w: %w", req.RemoteImageRef, perr, core.ErrInvalidRequest)
		}
		kc, kerr := registryutils.ResolveKeychain(req.RegistryConfigPath)
		if kerr != nil {
			return ports.ImageComparatorResult{}, fmt.Errorf("comparator: resolve auth for %s: %w", req.RemoteImageRef, kerr)
		}
		desc, gerr := remote.Get(parsedRef, remote.WithContext(ctx), remote.WithAuthFromKeychain(kc))
		if gerr != nil {
			return ports.ImageComparatorResult{}, fmt.Errorf("comparator: pull remote image %s: %w", req.RemoteImageRef, gerr)
		}
		remoteImg, err = desc.Image()
		if err != nil {
			return ports.ImageComparatorResult{}, fmt.Errorf("comparator: extract image from descriptor for %s: %w", req.RemoteImageRef, err)
		}
		remoteRepo = parsedRef.Context().Name()
	}

	// --asset-overlay reconstruction. An image built with --asset-overlay
	// carries ports.AnnotationAssetOverlaySources naming the exact
	// predecessor digests its merged overlay layer was built from (see that
	// constant's doc comment). pokkum verify has no --asset-overlay flags
	// of its own (Option A, deliberately not chosen — see
	// docs/items/asset-overlay-verify-gap.md), so an ordinary local rebuild
	// supplied via --against legitimately has no idea the overlay was ever
	// used and is missing that layer entirely. Left unhandled, that makes
	// EVERY --asset-overlay image fail verification with a permanent
	// false-positive mismatch, regardless of whether it is otherwise
	// perfectly legitimate. reconcileAssetOverlay reconstructs the layer
	// from the annotation and splices it into the local comparison view so
	// the L2/L3 checks below see what a rebuild that DID reproduce the
	// overlay would have produced.
	//
	// This must fail CLOSED: an absent annotation is the only condition
	// that skips reconciliation outright (nothing to reconstruct). A
	// present-but-malformed annotation, an unreachable predecessor digest,
	// or a reconstruction whose content does not match the image's own
	// overlay layer are all hard errors from reconcileAssetOverlay, not
	// silent skips — see its doc comment.
	remoteManifest, err := remoteImg.Manifest()
	if err != nil {
		return ports.ImageComparatorResult{}, fmt.Errorf("comparator: read remote manifest for %s: %w", req.RemoteImageRef, err)
	}
	localLayers, err := localImg.Layers()
	if err != nil {
		return ports.ImageComparatorResult{}, fmt.Errorf("comparator: read local layers: %w", err)
	}
	localConfig, err := localImg.ConfigFile()
	if err != nil {
		return ports.ImageComparatorResult{}, fmt.Errorf("comparator: read local config: %w", err)
	}
	localDiffIDs := localConfig.RootFS.DiffIDs
	if overlaySources, ok := remoteManifest.Annotations[ports.AnnotationAssetOverlaySources]; ok && strings.TrimSpace(overlaySources) != "" {
		localLayers, localDiffIDs, err = c.reconcileAssetOverlay(ctx, req, remoteImg, remoteRepo, overlaySources, localLayers, localDiffIDs, localConfig)
		if err != nil {
			return ports.ImageComparatorResult{}, err
		}
	}

	// 1. Level 1 (L1) Exact Comparison: Manifest digests
	remoteDigest, rErr := remoteImg.Digest()
	localDigest, lErr := localImg.Digest()
	if rErr == nil && lErr == nil && remoteDigest == localDigest {
		return ports.ImageComparatorResult{
			Level:           "L1",
			L1ExactMatch:    true,
			L2SemanticMatch: true,
			Summary:         fmt.Sprintf("Rebuilt container exactly matches remote image (bit-for-bit L1 match; digest %s).", localDigest),
		}, nil
	}

	// 2. Level 2 (L2) Semantic Comparison: Config and uncompressed DiffIDs
	remoteConfig, rcErr := remoteImg.ConfigFile()

	var remoteDiffIDs []v1.Hash
	if rcErr == nil && remoteConfig != nil {
		remoteDiffIDs = remoteConfig.RootFS.DiffIDs
	}

	l2Match := false
	if rcErr == nil {
		if configsMatch(remoteConfig, localConfig) && diffIDsMatch(remoteDiffIDs, localDiffIDs) {
			l2Match = true
		}
	}

	if l2Match {
		return ports.ImageComparatorResult{
			Level:           "L2",
			L1ExactMatch:    false,
			L2SemanticMatch: true,
			Summary:         "Rebuilt container matches remote image (L2 content-identical; uncompressed diffIDs and configuration match, compressed layer framing differs).",
		}, nil
	}

	// 3. Level 3 (L3) Explained Mismatch: Layer-by-layer file diffs
	remoteLayers, _ := remoteImg.Layers()

	var diffDetails []string
	var probableCauses []string

	maxLayers := len(remoteLayers)
	if len(localLayers) > maxLayers {
		maxLayers = len(localLayers)
	}

	for i := 0; i < maxLayers; i++ {
		if i >= len(remoteLayers) {
			diffDetails = append(diffDetails, fmt.Sprintf("layer %d: layer added in rebuilt image", i))
			continue
		}
		if i >= len(localLayers) {
			diffDetails = append(diffDetails, fmt.Sprintf("layer %d: layer missing in rebuilt image", i))
			continue
		}

		if i < len(remoteDiffIDs) && i < len(localDiffIDs) && remoteDiffIDs[i] == localDiffIDs[i] {
			continue
		}

		// DiffIDs differ on this layer — inspect tar streams
		rR, rErr := remoteLayers[i].Uncompressed()
		lR, lErr := localLayers[i].Uncompressed()
		if rErr != nil || lErr != nil {
			var msg string
			if i < len(remoteDiffIDs) && i < len(localDiffIDs) {
				msg = fmt.Sprintf("layer %d: failed to read uncompressed layer streams (diffID %s vs %s)", i, remoteDiffIDs[i], localDiffIDs[i])
			} else {
				msg = fmt.Sprintf("layer %d: failed to read uncompressed layer streams (diffIDs unavailable)", i)
			}
			diffDetails = append(diffDetails, msg)
			continue
		}

		diffRes, dErr := layerdiffutils.CompareTarStreams(rR, lR)
		_ = rR.Close()
		_ = lR.Close()

		if dErr != nil {
			diffDetails = append(diffDetails, fmt.Sprintf("layer %d: error comparing tar streams: %v", i, dErr))
			continue
		}

		for _, hd := range diffRes.HeaderDiffs {
			diffDetails = append(diffDetails, fmt.Sprintf("layer %d: metadata mismatch on %s: %s (%s vs %s)", i, hd.Path, hd.Field, hd.Value1, hd.Value2))
		}
		for _, cd := range diffRes.ContentDiffs {
			diffDetails = append(diffDetails, fmt.Sprintf("layer %d: content mismatch on %s (sha256 %s vs %s)", i, cd.Path, truncateHash(cd.Sha256First), truncateHash(cd.Sha256Second)))
		}
		for _, af := range diffRes.AddedFiles {
			diffDetails = append(diffDetails, fmt.Sprintf("layer %d: added file %s", i, af))
		}
		for _, rf := range diffRes.RemovedFiles {
			diffDetails = append(diffDetails, fmt.Sprintf("layer %d: removed file %s", i, rf))
		}

		if diffRes.ProbableCause != "" {
			probableCauses = append(probableCauses, diffRes.ProbableCause)
		}
	}

	summary := "Rebuilt container does NOT match remote image: layer content differs."
	if len(probableCauses) > 0 {
		summary = fmt.Sprintf("Rebuilt container does NOT match remote image: %s", strings.Join(probableCauses, "; "))
	}

	return ports.ImageComparatorResult{
		Level:           "L3",
		L1ExactMatch:    false,
		L2SemanticMatch: false,
		L3FileDiffs:     diffDetails,
		Summary:         summary,
	}, nil
}

func configsMatch(c1, c2 *v1.ConfigFile) bool {
	if c1 == nil || c2 == nil {
		return c1 == c2
	}
	if c1.Architecture != c2.Architecture || c1.OS != c2.OS {
		return false
	}
	if c1.Config.User != c2.Config.User || c1.Config.WorkingDir != c2.Config.WorkingDir {
		return false
	}
	if len(c1.Config.Entrypoint) != len(c2.Config.Entrypoint) {
		return false
	}
	for i := range c1.Config.Entrypoint {
		if c1.Config.Entrypoint[i] != c2.Config.Entrypoint[i] {
			return false
		}
	}
	return true
}

func diffIDsMatch(d1, d2 []v1.Hash) bool {
	if len(d1) != len(d2) {
		return false
	}
	for i := range d1 {
		if d1[i] != d2[i] {
			return false
		}
	}
	return true
}

func truncateHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// reconcileAssetOverlay reconstructs the --asset-overlay merged layer from
// the digests named in overlaySourcesRaw (ports.AnnotationAssetOverlaySources
// — a comma-joined list stamped at build time, see that constant's doc
// comment) and splices it into localLayers/localDiffIDs at the position the
// remote image's own history places it, unless the local rebuild already
// carries its own asset-overlay layer (in which case it is already a
// like-for-like entry in the comparison and must not be duplicated).
//
// Every failure path here is deliberate and hard, per the fail-closed
// requirement this feature exists under: c.assetOverlay/c.layerBuilder being
// unset, a malformed annotation entry, a remote image whose history has no
// matching overlay layer despite carrying the annotation, an unresolvable
// predecessor digest, or a reconstruction whose content does not match the
// image's own overlay layer all return an error rather than silently
// falling back to the un-reconciled comparison — any of those silently
// skipped would let a genuinely mismatched or falsely-labeled
// --asset-overlay image pass verification by omission, which is exactly the
// false-negative this reconstruction exists to prevent, not merely the
// false-positive it also happens to fix.
func (c *Comparator) reconcileAssetOverlay(
	ctx context.Context,
	req ports.ImageComparatorRequest,
	remoteImg v1.Image,
	remoteRepo string,
	overlaySourcesRaw string,
	localLayers []v1.Layer,
	localDiffIDs []v1.Hash,
	localConfig *v1.ConfigFile,
) ([]v1.Layer, []v1.Hash, error) {
	if c.assetOverlay == nil || c.layerBuilder == nil {
		return nil, nil, fmt.Errorf("comparator: remote image %s was built with --asset-overlay (%s annotation present) but this comparator has no asset-overlay reconstruction support configured: refusing to verify rather than silently skipping the overlay layer comparison", req.RemoteImageRef, ports.AnnotationAssetOverlaySources)
	}

	sources, err := parseAssetOverlaySources(overlaySourcesRaw)
	if err != nil {
		return nil, nil, fmt.Errorf("comparator: remote image %s has a malformed %s annotation: %w: %w", req.RemoteImageRef, ports.AnnotationAssetOverlaySources, err, core.ErrInvalidRequest)
	}

	// If the local rebuild already carries its own asset-overlay layer
	// (the caller reproduced --asset-overlay-from manually, or ran the
	// exact same --asset-overlay build), it is already a like-for-like
	// layer in the comparison; inserting a second one would duplicate it.
	if _, ok := layerIndexByCreatedBy(localConfig, len(localLayers), ports.HistoryCreatedByAssetOverlay); ok {
		return localLayers, localDiffIDs, nil
	}

	remoteConfig, err := remoteImg.ConfigFile()
	if err != nil {
		return nil, nil, fmt.Errorf("comparator: read remote config for asset-overlay reconstruction: %w", err)
	}
	remoteLayers, err := remoteImg.Layers()
	if err != nil {
		return nil, nil, fmt.Errorf("comparator: read remote layers for asset-overlay reconstruction: %w", err)
	}
	overlayIdx, ok := layerIndexByCreatedBy(remoteConfig, len(remoteLayers), ports.HistoryCreatedByAssetOverlay)
	if !ok {
		return nil, nil, fmt.Errorf("comparator: remote image %s carries %s but has no matching asset-overlay layer in its own history: %w", req.RemoteImageRef, ports.AnnotationAssetOverlaySources, core.ErrInvalidRequest)
	}
	remoteDiffIDs := remoteConfig.RootFS.DiffIDs
	if overlayIdx >= len(remoteDiffIDs) {
		return nil, nil, fmt.Errorf("comparator: remote image %s: asset-overlay layer index out of range in its own config: %w", req.RemoteImageRef, core.ErrInvalidRequest)
	}
	remoteOverlayDiffID := remoteDiffIDs[overlayIdx]

	overlayDir, err := c.assetOverlay.BuildOverlayDir(ctx, remoteRepo, sources, req.RegistryConfigPath, false)
	if err != nil {
		return nil, nil, fmt.Errorf("comparator: reconstruct asset-overlay content for %s: %w", req.RemoteImageRef, err)
	}
	if overlayDir == "" {
		// BuildOverlayDir's own contract returns "" only when sources is
		// empty, which parseAssetOverlaySources already rejected above — an
		// empty result here means that contract was violated, not that
		// there is legitimately nothing to reconstruct.
		return nil, nil, fmt.Errorf("comparator: remote image %s: asset-overlay annotation named sources but reconstruction produced no content: %w", req.RemoteImageRef, core.ErrInvalidRequest)
	}
	defer os.RemoveAll(overlayDir)

	// modTime MUST be the exact value the original build stamped into every
	// layered-strategy tar entry (see internal/adapters/packager's package
	// doc, "How determinism is achieved") — that value is the image
	// config's own Created field, so reusing it here (rather than, say,
	// time.Now()) is what makes the reconstructed layer's tar bytes,
	// and therefore its DiffID, reproducible at all. compression is
	// deliberately CompressionGzip regardless of what the original image
	// actually used: DiffID is the hash of the UNCOMPRESSED tar stream and
	// is provably compression-independent (see BuildLayer's doc comment),
	// so the comparison below never depends on guessing the original
	// compression algorithm correctly.
	platform := ports.Platform{OS: remoteConfig.OS, Arch: remoteConfig.Architecture}
	overlayLayer, err := c.layerBuilder.BuildLayer(ctx, platform, overlayDir, ports.AppClientDirPrefix, remoteConfig.Created.Time, ports.CompressionGzip)
	if err != nil {
		return nil, nil, fmt.Errorf("comparator: build reconstructed asset-overlay layer for %s: %w", req.RemoteImageRef, err)
	}
	reconstructedDiffID, err := overlayLayer.DiffID()
	if err != nil {
		return nil, nil, fmt.Errorf("comparator: compute reconstructed asset-overlay layer diffID for %s: %w", req.RemoteImageRef, err)
	}

	if reconstructedDiffID != remoteOverlayDiffID {
		// The image's own overlay layer bytes do not match what its own
		// annotation says produced them: either the annotation was
		// tampered with (names the wrong predecessors) or the overlay
		// layer itself was tampered with (real predecessors, wrong
		// bytes). Either way this is a genuine, unambiguous mismatch —
		// fail closed rather than silently falling back to a comparison
		// that omits the overlay layer, which would hide exactly this
		// class of tampering.
		return nil, nil, fmt.Errorf("comparator: remote image %s: reconstructed asset-overlay content (diffID %s) does not match the image's own asset-overlay layer (diffID %s) — the %s annotation does not describe the actual layer content: %w",
			req.RemoteImageRef, reconstructedDiffID, remoteOverlayDiffID, ports.AnnotationAssetOverlaySources, core.ErrAssetOverlayVerificationMismatch)
	}

	// The overlay layer is verified legitimate: splice it into the local
	// comparison view at the same position it occupies in the remote
	// image. Everything before that position (base image, runtime,
	// supervisor, server) is common to both a --asset-overlay build and an
	// ordinary rebuild of the same source, so the same numeric index
	// applies to both slices.
	spliceIdx := overlayIdx
	if spliceIdx > len(localLayers) {
		spliceIdx = len(localLayers)
	}
	diffSpliceIdx := overlayIdx
	if diffSpliceIdx > len(localDiffIDs) {
		diffSpliceIdx = len(localDiffIDs)
	}

	newLayers := make([]v1.Layer, 0, len(localLayers)+1)
	newLayers = append(newLayers, localLayers[:spliceIdx]...)
	newLayers = append(newLayers, overlayLayer)
	newLayers = append(newLayers, localLayers[spliceIdx:]...)

	newDiffIDs := make([]v1.Hash, 0, len(localDiffIDs)+1)
	newDiffIDs = append(newDiffIDs, localDiffIDs[:diffSpliceIdx]...)
	newDiffIDs = append(newDiffIDs, reconstructedDiffID)
	newDiffIDs = append(newDiffIDs, localDiffIDs[diffSpliceIdx:]...)

	c.log.InfoContext(ctx, "asset overlay: reconstructed merged layer from image's own annotation and included it in the comparison",
		"remote", req.RemoteImageRef, "sources", len(sources), "diffID", reconstructedDiffID.String())

	return newLayers, newDiffIDs, nil
}

// parseAssetOverlaySources splits and validates raw
// (ports.AnnotationAssetOverlaySources's comma-joined value) into its
// individual entries, each either a bare digest ("sha256:<64 hex>", as
// ResolvePredecessorChain's auto-discovery produces) or a fully-qualified
// "repo@sha256:<64 hex>" ref (as ResolveDigest produces for
// --asset-overlay-from) — see ports.AssetOverlayResolver.BuildOverlayDir's
// doc comment for why both forms are valid. Any entry that is neither, or an
// empty annotation/entry, is an error: a malformed annotation must never be
// silently treated as "no overlay to reconcile."
func parseAssetOverlaySources(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("empty entry in %q", raw)
		}
		if strings.Contains(p, "@") {
			if _, err := name.NewDigest(p, name.WeakValidation); err != nil {
				return nil, fmt.Errorf("invalid qualified digest ref %q: %w", p, err)
			}
		} else if _, err := v1.NewHash(p); err != nil {
			return nil, fmt.Errorf("invalid digest %q: %w", p, err)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no sources in %q", raw)
	}
	return out, nil
}

// layerIndexByCreatedBy locates the layer index whose History entry matches
// createdBy, cross-checking History (EmptyLayer-filtered) against
// RootFS.DiffIDs length and layerCount before trusting the mapping, so a
// config whose History/DiffIDs disagree in length (a malformed or foreign
// image) yields no match rather than a possibly-misaligned one. Mirrors
// internal/adapters/assetoverlay's unexported clientLayerIndex and
// cmd/pokkum/explain.go's layerPurposes, which apply the identical
// discipline for the same reason — duplicated here rather than imported
// because this is an adapter-to-adapter boundary the hexagonal architecture
// rules forbid crossing.
func layerIndexByCreatedBy(cfg *v1.ConfigFile, layerCount int, createdBy string) (int, bool) {
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
		if strings.TrimSpace(h.CreatedBy) == createdBy {
			return i, true
		}
		i++
	}
	return 0, false
}
