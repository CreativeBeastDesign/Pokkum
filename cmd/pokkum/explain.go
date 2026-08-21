package main

import (
	"archive/tar"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/jsonutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/layerdiffutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registryutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

type explainOptions struct {
	output         string
	registryConfig string
	platform       string
}

func newExplainCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	opts := &explainOptions{}

	cmd := &cobra.Command{
		Use:   "explain <image>",
		Short: "Explain container layer composition and file origins",
		Long: `Explain reads a real OCI image manifest — from a registry, or a local
"pokkum build --tarball <path>" result whose path ends in .tar — and reports
each layer's actual digest, actual compressed size, actual file count, and,
for layers Pokkum itself built, the layer's real purpose taken from the
image's own history metadata.

The layer count printed is whatever the image actually has: it varies by
strategy and by which optional layers (client assets, vendor dependencies,
native addons, prerendered pages) a given build produced, so there is no
single fixed number to expect.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			outFlag, _ := cmd.Flags().GetString("output")
			if outFlag != "" {
				opts.output = outFlag
			}
			return runExplain(ctx, opts, args[0])
		},
	}

	registerExplainFlags(cmd, opts, "Platform to inspect when the image is a multi-arch index, e.g. linux/amd64")

	cmd.AddCommand(newWhyCommand(ctx, logger))
	cmd.AddCommand(newDiffCommand(ctx, logger))

	return cmd
}

func registerExplainFlags(cmd *cobra.Command, opts *explainOptions, platformHelp string) {
	cmd.Flags().StringVar(&opts.registryConfig, "registry-config", "", "Path to a docker config.json-style auth file")
	cmd.Flags().StringVarP(&opts.platform, "platform", "p", ports.LocalPlatform().String(), platformHelp)
}

func runExplain(ctx context.Context, opts *explainOptions, target string) error {
	outputFormat := ports.OutputFormat(opts.output)

	platform, err := core.ParsePlatform(opts.platform)
	if err != nil {
		return explainFail(outputFormat, "explain", fmt.Sprintf("invalid --platform: %v", err))
	}

	img, err := resolveImage(ctx, target, platform, opts.registryConfig)
	if err != nil {
		return explainFail(outputFormat, "explain", fmt.Sprintf("failed to read image %s: %v", target, err))
	}

	layers, err := img.Layers()
	if err != nil {
		return explainFail(outputFormat, "explain", fmt.Sprintf("read layers for %s: %v", target, err))
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return explainFail(outputFormat, "explain", fmt.Sprintf("read config for %s: %v", target, err))
	}

	purposes := layerPurposes(cfg, len(layers))
	resolvedPlatform := imageConfigPlatform(cfg)

	explainLayers := make([]ports.LayerExplain, 0, len(layers))
	var totalSize int64
	var unknownSizeCount int
	for i, l := range layers {
		digestStr := "(digest unknown)"
		if d, derr := l.Digest(); derr == nil {
			digestStr = d.String()
		}

		// A real byte count is never negative, so -1 is an unambiguous
		// "could not be determined" sentinel — unlike 0, which is a
		// plausible size for a genuinely tiny layer and would silently look
		// like a real answer.
		size := int64(-1)
		if s, serr := l.Size(); serr == nil {
			size = s
			totalSize += s
		} else {
			unknownSizeCount++
		}

		fileCount := layerFileCount(l)

		purpose := "(no history metadata)"
		if i < len(purposes) {
			purpose = purposes[i]
		}

		explainLayers = append(explainLayers, ports.LayerExplain{
			LayerIndex: i,
			Digest:     digestStr,
			SizeBytes:  size,
			Purpose:    purpose,
			FileCount:  fileCount,
		})
	}

	payload := ports.ExplainOutput{
		Target:    target,
		TotalSize: totalSize,
		Layers:    explainLayers,
		Platform:  resolvedPlatform,
	}

	if outputFormat == ports.FormatJSON {
		return jsonutils.WriteSuccess(os.Stdout, "explain", payload)
	}

	fmt.Println("=== Pokkum Container Image Composition & Layer Breakdown ===")
	fmt.Printf("Target:   %s\n", target)
	fmt.Printf("Platform: %s\n", resolvedPlatform)
	if unknownSizeCount > 0 {
		fmt.Printf("Size:     %.2f MB (%d bytes) — excludes %d layer(s) of unknown size\n", float64(totalSize)/(1024*1024), totalSize, unknownSizeCount)
	} else {
		fmt.Printf("Size:     %.2f MB (%d bytes)\n", float64(totalSize)/(1024*1024), totalSize)
	}
	fmt.Printf("Layers:   %d\n", len(explainLayers))
	fmt.Println()

	for _, l := range explainLayers {
		fmt.Printf("Layer #%d [%s]\n", l.LayerIndex, shortDigest(l.Digest))
		fmt.Printf("  Purpose:    %s\n", l.Purpose)
		if l.SizeBytes < 0 {
			fmt.Println("  Size:       (unknown)")
		} else {
			fmt.Printf("  Size:       %.2f MB (%d bytes)\n", float64(l.SizeBytes)/(1024*1024), l.SizeBytes)
		}
		if l.FileCount < 0 {
			fmt.Println("  Files:      (unknown)")
		} else {
			fmt.Printf("  Files:      %d\n", l.FileCount)
		}
		fmt.Println()
	}

	return nil
}

// layerFileCount counts the real, non-directory, non-whiteout entries in a
// layer's uncompressed tar stream. It returns -1 (not 0) when the stream
// couldn't be read at all, so a genuinely empty/near-empty layer is never
// confused with a read failure.
func layerFileCount(l v1.Layer) int {
	rc, err := l.Uncompressed()
	if err != nil {
		return -1
	}
	defer rc.Close()

	entries, err := layerdiffutils.ListTarPaths(rc)
	if err != nil {
		return -1
	}

	count := 0
	for _, e := range entries {
		if e.Typeflag == tar.TypeDir || e.IsWhiteout() || e.IsOpaqueWhiteout() {
			continue
		}
		count++
	}
	return count
}

func shortDigest(d string) string {
	const n = 19
	if len(d) > n {
		return d[:n]
	}
	return d
}

func explainFail(outputFormat ports.OutputFormat, command, msg string) error {
	if outputFormat == ports.FormatJSON {
		return jsonutils.WriteError(os.Stdout, command, "ERR_EXPLAIN_FAILED", msg, "")
	}
	return fmt.Errorf("%s", msg)
}

// resolveImage reads target as a real image: a local tarball (a path ending
// in ".tar", mirroring comparator.go's own convention) or a remote registry
// reference, resolving a multi-arch index down to platform's child image
// exactly the way baseimage/resolver.go's selectPlatform does for base
// images. It never mutates anything — a single read-only GET at most.
func resolveImage(ctx context.Context, target string, platform ports.Platform, registryConfigPath string) (v1.Image, error) {
	if strings.HasSuffix(target, ".tar") {
		img, err := tarball.ImageFromPath(target, nil)
		if err != nil {
			return nil, fmt.Errorf("load local tarball %s: %w", target, err)
		}
		return img, nil
	}

	parsedRef, err := name.ParseReference(target, name.WeakValidation)
	if err != nil {
		return nil, fmt.Errorf("parse image reference %q: %w: %w", target, err, core.ErrInvalidRequest)
	}

	kc, err := registryutils.ResolveKeychain(registryConfigPath)
	if err != nil {
		return nil, err
	}

	desc, err := remote.Get(parsedRef, remote.WithContext(ctx), remote.WithAuthFromKeychain(kc))
	if err != nil {
		return nil, fmt.Errorf("pull manifest for %s: %w", target, err)
	}

	if !desc.MediaType.IsIndex() {
		img, ierr := desc.Image()
		if ierr != nil {
			return nil, fmt.Errorf("read image for %s: %w", target, ierr)
		}
		return img, nil
	}

	idx, err := desc.ImageIndex()
	if err != nil {
		return nil, fmt.Errorf("read index for %s: %w", target, err)
	}
	im, err := idx.IndexManifest()
	if err != nil {
		return nil, fmt.Errorf("read index manifest for %s: %w", target, err)
	}
	for _, m := range im.Manifests {
		if !platformMatchesManifest(m.Platform, platform) {
			continue
		}
		img, ierr := idx.Image(m.Digest)
		if ierr != nil {
			return nil, fmt.Errorf("fetch %s child %s: %w", platform, m.Digest, ierr)
		}
		return img, nil
	}
	return nil, fmt.Errorf("index %s has no manifest for platform %s: %w", target, platform, core.ErrInvalidRequest)
}

// platformMatchesManifest mirrors baseimage/resolver.go's platformMatches:
// OS/architecture only (registries stamp arm64 children with a Variant this
// codebase never requests), and it rejects the "unknown/unknown" placeholder
// platform some registries use for attestation/provenance manifests
// co-located in the same index.
func platformMatchesManifest(cp *v1.Platform, want ports.Platform) bool {
	if cp == nil {
		return false
	}
	if cp.OS == "unknown" || cp.Architecture == "unknown" {
		return false
	}
	return cp.OS == want.OS && cp.Architecture == want.Arch
}

// imageConfigPlatform reports the real OS/architecture the resolved image
// actually is, read from its own config — not the requested --platform flag.
// The two normally agree, but a local .tar target or a remote single-image
// (non-index) ref never actually uses --platform to select anything (see
// resolveImage), so echoing the flag there would print a platform that was
// never verified against the image at all — a fabricated-looking value for
// an unrelated input.
func imageConfigPlatform(cfg *v1.ConfigFile) string {
	if cfg == nil {
		return "(unknown)"
	}
	p := ports.Platform{OS: cfg.OS, Arch: cfg.Architecture, Variant: cfg.Variant}
	if s := p.String(); s != "" {
		return s
	}
	return "(unknown)"
}

const pokkumHistoryPrefix = "pokkum: add "

// layerPurposes derives a real, per-layer purpose string from the image's
// own config history — no static index→name table, so the result is always
// as many entries as the image actually has (see runExplain's Long text).
//
// mutate.Append (what packager.go uses to assemble every Pokkum-built image)
// appends each new layer's History entry, with EmptyLayer left false,
// directly onto whatever History array the base image already carried; it
// also keeps RootFS.DiffIDs in exact one-to-one sync with real (non-empty)
// layers. Cross-checking the EmptyLayer-filtered History count against both
// DiffIDs length and the real layer count guards against a base image whose
// History has the right length but a misplaced EmptyLayer flag earlier in
// the array — trusting that blindly would silently shift every later
// index's purpose, including Pokkum's own layers, without ever tripping an
// out-of-range error. On any disagreement this returns an honest "unknown"
// for every index rather than a confidently wrong one.
func layerPurposes(cfg *v1.ConfigFile, layerCount int) []string {
	unknown := make([]string, layerCount)
	for i := range unknown {
		unknown[i] = "(no history metadata)"
	}
	if cfg == nil {
		return unknown
	}

	var nonEmptyHistoryCount int
	for _, h := range cfg.History {
		if !h.EmptyLayer {
			nonEmptyHistoryCount++
		}
	}
	if nonEmptyHistoryCount != len(cfg.RootFS.DiffIDs) || nonEmptyHistoryCount != layerCount {
		return unknown
	}

	purposes := make([]string, 0, layerCount)
	for _, h := range cfg.History {
		if h.EmptyLayer {
			continue
		}
		purposes = append(purposes, formatPurpose(h.CreatedBy))
	}
	return purposes
}

func formatPurpose(createdBy string) string {
	createdBy = strings.TrimSpace(createdBy)
	switch {
	case createdBy == "":
		return "(no history metadata)"
	case strings.HasPrefix(createdBy, pokkumHistoryPrefix):
		return "Adds " + strings.TrimPrefix(createdBy, pokkumHistoryPrefix)
	default:
		const maxLen = 100
		if len(createdBy) > maxLen {
			return createdBy[:maxLen] + "…"
		}
		return createdBy
	}
}

// archiveName normalizes a user-supplied file path into the slash-separated,
// no-leading-slash form every OCI layer's tar member names use — mirroring
// packager/layer.go's own archiveName helper (unexported there, so this is a
// small local equivalent rather than a cross-package reach for one line).
func archiveName(p string) string {
	clean := strings.TrimPrefix(path.Clean("/"+p), "/")
	if clean == "." {
		return ""
	}
	return clean
}

func newWhyCommand(ctx context.Context, _ *slog.Logger) *cobra.Command {
	opts := &explainOptions{}

	cmd := &cobra.Command{
		Use:   "why <image> <file-path>",
		Short: "Trace which real layer a file came from, or was deleted in",
		Long: `Why reads a real OCI image and reports which layer actually last touched a
given file: the layer that added it, the layer whose whiteout entry deleted
it, or — if the path was never present in any layer at all — says so
plainly instead of guessing.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			outFlag, _ := cmd.Flags().GetString("output")
			if outFlag != "" {
				opts.output = outFlag
			}
			return runWhy(ctx, opts, args[0], args[1])
		},
	}

	registerExplainFlags(cmd, opts, "Platform to inspect when the image is a multi-arch index, e.g. linux/amd64")

	return cmd
}

func runWhy(ctx context.Context, opts *explainOptions, target, filePath string) error {
	outputFormat := ports.OutputFormat(opts.output)

	platform, err := core.ParsePlatform(opts.platform)
	if err != nil {
		return explainFail(outputFormat, "explain why", fmt.Sprintf("invalid --platform: %v", err))
	}

	img, err := resolveImage(ctx, target, platform, opts.registryConfig)
	if err != nil {
		return explainFail(outputFormat, "explain why", fmt.Sprintf("failed to read image %s: %v", target, err))
	}

	layers, err := img.Layers()
	if err != nil {
		return explainFail(outputFormat, "explain why", fmt.Sprintf("read layers for %s: %v", target, err))
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return explainFail(outputFormat, "explain why", fmt.Sprintf("read config for %s: %v", target, err))
	}
	purposes := layerPurposes(cfg, len(layers))

	normalized := archiveName(filePath)
	if normalized == "" {
		return explainFail(outputFormat, "explain why", fmt.Sprintf("%q is not a valid file path", filePath))
	}

	result, err := locateFileInLayers(layers, normalized)
	if err != nil {
		return explainFail(outputFormat, "explain why", fmt.Sprintf("inspect layers of %s: %v", target, err))
	}

	payload := map[string]interface{}{
		"image": target,
		"file":  filePath,
	}

	var reason string
	switch result.status {
	case whyFound:
		purpose := "(no history metadata)"
		if result.layerIndex < len(purposes) {
			purpose = purposes[result.layerIndex]
		}
		payload["found"] = true
		payload["layer_index"] = result.layerIndex
		payload["layer"] = purpose
		reason = fmt.Sprintf("Included via layer %d (%s)", result.layerIndex, purpose)
	case whyDeleted:
		payload["found"] = false
		payload["deleted_at_layer"] = result.layerIndex
		reason = fmt.Sprintf("Deleted at layer %d (whiteout) — not present in the final image", result.layerIndex)
	case whyAmbiguous:
		payload["found"] = false
		payload["ambiguous_at_layer"] = result.layerIndex
		reason = fmt.Sprintf("Ambiguous — directory %q was opaquely reset at layer %d; cannot determine origin", path.Dir(normalized), result.layerIndex)
	default:
		payload["found"] = false
		reason = "Not found in any layer"
	}
	payload["reason"] = reason

	if outputFormat == ports.FormatJSON {
		return jsonutils.WriteSuccess(os.Stdout, "explain why", payload)
	}

	fmt.Printf("File %q in %s:\n", filePath, target)
	fmt.Println(reason)
	return nil
}

type whyStatus int

const (
	whyNotFound whyStatus = iota
	whyFound
	whyDeleted
	whyAmbiguous
)

type whyResult struct {
	status     whyStatus
	layerIndex int
}

// locateFileInLayers walks layers from last to first — the order a real
// overlay filesystem actually resolves a path in — looking for the first
// layer that settles the question: contains the path itself, deletes it via
// a per-file whiteout, or opaquely resets the directory the path lives in
// (which would make an earlier "found" in a lower layer an actively wrong
// answer, not just an incomplete one). Each layer's tar reader is closed
// immediately after use, so this never accumulates open readers across a
// long layer list.
func locateFileInLayers(layers []v1.Layer, filePath string) (whyResult, error) {
	for i := len(layers) - 1; i >= 0; i-- {
		rc, err := layers[i].Uncompressed()
		if err != nil {
			return whyResult{}, fmt.Errorf("read layer %d: %w", i, err)
		}
		entries, err := layerdiffutils.ListTarPaths(rc)
		_ = rc.Close()
		if err != nil {
			return whyResult{}, fmt.Errorf("list layer %d: %w", i, err)
		}

		found := false
		deleted := false
		ambiguous := false
		for _, e := range entries {
			switch {
			case e.Path == filePath:
				found = true
			case e.IsWhiteout() && e.WhitesOutPath() == filePath:
				deleted = true
			case e.IsOpaqueWhiteout() && isUnderDir(filePath, e.OpaqueWhiteoutDir()):
				ambiguous = true
			}
		}

		switch {
		case found:
			// This layer adds the file itself — true regardless of any
			// whiteout/opaque marker also present in this same layer (e.g.
			// "reset this directory, then repopulate it").
			return whyResult{status: whyFound, layerIndex: i}, nil
		case deleted:
			return whyResult{status: whyDeleted, layerIndex: i}, nil
		case ambiguous:
			return whyResult{status: whyAmbiguous, layerIndex: i}, nil
		}
	}
	return whyResult{status: whyNotFound}, nil
}

func isUnderDir(filePath, dir string) bool {
	if dir == "." || dir == "" {
		return true
	}
	return strings.HasPrefix(filePath, dir+"/")
}

const maxDiffFilesPerLayer = 20

func newDiffCommand(ctx context.Context, _ *slog.Logger) *cobra.Command {
	opts := &explainOptions{}

	cmd := &cobra.Command{
		Use:   "diff <image1> <image2>",
		Short: "Compare real layer structure and size between two images",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			outFlag, _ := cmd.Flags().GetString("output")
			if outFlag != "" {
				opts.output = outFlag
			}
			return runDiff(ctx, opts, args[0], args[1])
		},
	}

	registerExplainFlags(cmd, opts, "Platform to inspect when either image is a multi-arch index, e.g. linux/amd64")

	return cmd
}

func runDiff(ctx context.Context, opts *explainOptions, ref1, ref2 string) error {
	outputFormat := ports.OutputFormat(opts.output)

	platform, err := core.ParsePlatform(opts.platform)
	if err != nil {
		return explainFail(outputFormat, "explain diff", fmt.Sprintf("invalid --platform: %v", err))
	}

	img1, err := resolveImage(ctx, ref1, platform, opts.registryConfig)
	if err != nil {
		return explainFail(outputFormat, "explain diff", fmt.Sprintf("failed to read image %s: %v", ref1, err))
	}
	img2, err := resolveImage(ctx, ref2, platform, opts.registryConfig)
	if err != nil {
		return explainFail(outputFormat, "explain diff", fmt.Sprintf("failed to read image %s: %v", ref2, err))
	}

	if digest1, derr1 := img1.Digest(); derr1 == nil {
		if digest2, derr2 := img2.Digest(); derr2 == nil && digest1 == digest2 {
			return writeDiffResult(outputFormat, ref1, ref2, true, 0, nil)
		}
	}

	layers1, err := img1.Layers()
	if err != nil {
		return explainFail(outputFormat, "explain diff", fmt.Sprintf("read layers for %s: %v", ref1, err))
	}
	layers2, err := img2.Layers()
	if err != nil {
		return explainFail(outputFormat, "explain diff", fmt.Sprintf("read layers for %s: %v", ref2, err))
	}

	sizeDiff := layerSetSize(layers2) - layerSetSize(layers1)

	modified, err := diffLayerSets(layers1, layers2)
	if err != nil {
		return explainFail(outputFormat, "explain diff", fmt.Sprintf("compare layers of %s and %s: %v", ref1, ref2, err))
	}

	return writeDiffResult(outputFormat, ref1, ref2, false, sizeDiff, modified)
}

func layerSetSize(layers []v1.Layer) int64 {
	var total int64
	for _, l := range layers {
		if s, err := l.Size(); err == nil {
			total += s
		}
	}
	return total
}

func diffLayerSets(layers1, layers2 []v1.Layer) ([]string, error) {
	maxLayers := len(layers1)
	if len(layers2) > maxLayers {
		maxLayers = len(layers2)
	}

	var modified []string
	for i := 0; i < maxLayers; i++ {
		switch {
		case i >= len(layers1):
			modified = append(modified, fmt.Sprintf("layer %d: present only in the second image", i))
			continue
		case i >= len(layers2):
			modified = append(modified, fmt.Sprintf("layer %d: present only in the first image", i))
			continue
		}

		d1, e1 := layers1[i].Digest()
		d2, e2 := layers2[i].Digest()
		if e1 == nil && e2 == nil && d1 == d2 {
			continue
		}

		changes, err := diffOneLayer(i, layers1[i], layers2[i])
		if err != nil {
			return nil, err
		}
		modified = append(modified, changes...)
	}
	return modified, nil
}

func diffOneLayer(index int, l1, l2 v1.Layer) ([]string, error) {
	r1, err := l1.Uncompressed()
	if err != nil {
		return []string{fmt.Sprintf("layer %d: digest differs, could not read first image's layer: %v", index, err)}, nil
	}
	r2, err := l2.Uncompressed()
	if err != nil {
		_ = r1.Close()
		return []string{fmt.Sprintf("layer %d: digest differs, could not read second image's layer: %v", index, err)}, nil
	}

	diffRes, err := layerdiffutils.CompareTarStreams(r1, r2)
	_ = r1.Close()
	_ = r2.Close()
	if err != nil {
		return []string{fmt.Sprintf("layer %d: digest differs, error comparing tar streams: %v", index, err)}, nil
	}

	var changes []string
	for _, hd := range diffRes.HeaderDiffs {
		changes = append(changes, fmt.Sprintf("layer %d: metadata differs on %s: %s (%s vs %s)", index, hd.Path, hd.Field, hd.Value1, hd.Value2))
	}
	for _, cd := range diffRes.ContentDiffs {
		changes = append(changes, fmt.Sprintf("layer %d: modified %s", index, cd.Path))
	}
	for _, af := range diffRes.AddedFiles {
		changes = append(changes, fmt.Sprintf("layer %d: added %s", index, af))
	}
	for _, rf := range diffRes.RemovedFiles {
		changes = append(changes, fmt.Sprintf("layer %d: removed %s", index, rf))
	}
	if len(changes) == 0 {
		changes = append(changes, fmt.Sprintf("layer %d: digest differs but uncompressed content is byte-identical (compression framing differs)", index))
	}

	// CompareTarStreams walks entries via Go map iteration, which is not
	// order-stable — sort before this ever reaches output so two runs over
	// the same real layers always print the same order.
	sort.Strings(changes)

	if len(changes) > maxDiffFilesPerLayer {
		extra := len(changes) - maxDiffFilesPerLayer
		changes = changes[:maxDiffFilesPerLayer]
		changes = append(changes, fmt.Sprintf("layer %d: +%d more changed entr(y/ies) not shown", index, extra))
	}
	return changes, nil
}

func writeDiffResult(outputFormat ports.OutputFormat, ref1, ref2 string, identical bool, sizeDiff int64, modified []string) error {
	if modified == nil {
		modified = []string{}
	}

	payload := map[string]interface{}{
		"image1":    ref1,
		"image2":    ref2,
		"identical": identical,
		"size_diff": sizeDiff,
		"modified":  modified,
	}

	if outputFormat == ports.FormatJSON {
		return jsonutils.WriteSuccess(os.Stdout, "explain diff", payload)
	}

	fmt.Printf("=== Pokkum Layer Diff: %s vs %s ===\n", ref1, ref2)
	if identical {
		fmt.Println("Status: Identical (manifest digests match exactly)")
		return nil
	}
	fmt.Println("Status: Modified")
	fmt.Printf("Size delta (second image vs first): %+d bytes\n", sizeDiff)
	for _, m := range modified {
		fmt.Println(m)
	}
	return nil
}
