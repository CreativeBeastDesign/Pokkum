package comparator

import (
	"context"
	"fmt"
	"log/slog"
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
}

// NewComparator constructs an ImageComparator adapter instance.
func NewComparator(log *slog.Logger) *Comparator {
	if log == nil {
		log = slog.Default()
	}
	return &Comparator{log: log}
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
	localConfig, lcErr := localImg.ConfigFile()

	var remoteDiffIDs []v1.Hash
	if rcErr == nil && remoteConfig != nil {
		remoteDiffIDs = remoteConfig.RootFS.DiffIDs
	}
	var localDiffIDs []v1.Hash
	if lcErr == nil && localConfig != nil {
		localDiffIDs = localConfig.RootFS.DiffIDs
	}

	l2Match := false
	if rcErr == nil && lcErr == nil {
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
	localLayers, _ := localImg.Layers()

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
			diffDetails = append(diffDetails, fmt.Sprintf("layer %d: failed to read uncompressed layer streams (diffID %s vs %s)", i, remoteDiffIDs[i], localDiffIDs[i]))
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
