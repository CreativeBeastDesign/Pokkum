package main

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"sort"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/jsonutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registryutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
	"github.com/spf13/cobra"
)

type historyFlags struct {
	registryConfig string
	output         string
}

func newHistoryCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	flags := &historyFlags{}

	cmd := &cobra.Command{
		Use:   "history <image>",
		Short: "Inspect real OCI provenance annotations for a published image",
		Long: `History pulls a published image's real manifest and reports the standard
org.opencontainers.image.* annotations Pokkum writes at build time (revision,
source, version, created) — the actual git commit, repo, and build timestamp
baked into that specific image, not a template or a guess.

This does NOT verify the image's signature or SLSA provenance attestation —
use "pokkum verify <ref>" for a cryptographic verdict on those. History is
for reading what's there, not attesting to its authenticity.`,
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runHistory(ctx, logger, flags, args[0])
		},
	}

	cmd.Flags().StringVar(&flags.registryConfig, "registry-config", "", "Path to a docker config.json-style auth file")
	cmd.Flags().StringVar(&flags.output, "output", "text", "Output format (text or json)")

	return cmd
}

func runHistory(ctx context.Context, logger *slog.Logger, flags *historyFlags, imageRef string) error {
	outputFormat := ports.OutputFormat(flags.output)

	if strings.TrimSpace(imageRef) == "" {
		return fmt.Errorf("image reference is required: %w", core.ErrInvalidRequest)
	}

	historyOut, annSrc, err := fetchImageHistory(ctx, imageRef, flags.registryConfig)
	if err != nil {
		msg := fmt.Sprintf("failed to read image annotations for %s: %v", imageRef, err)
		if outputFormat == ports.FormatJSON {
			return jsonutils.WriteError(os.Stdout, "history", "ERR_HISTORY_FAILED", msg, "")
		}
		return fmt.Errorf("%s", msg)
	}

	if outputFormat == ports.FormatJSON {
		report := historyReport{
			HistoryOutput:            historyOut,
			AnnotationsSource:        annSrc.Kind,
			AnnotationsChildDigest:   annSrc.ChildDigest,
			AnnotationsChildPlatform: annSrc.ChildPlatform,
			AnnotationsNote:          annSrc.Note,
		}
		return jsonutils.WriteSuccess(os.Stdout, "history", report)
	}

	fmt.Println("=== Image Provenance Annotations ===")
	fmt.Printf("Image:         %s\n", historyOut.ImageRef)
	fmt.Printf("Digest:        %s\n", historyOut.ImageDigest)
	switch annSrc.Kind {
	case historySourceIndexAndChild:
		fmt.Printf("Note:          %s is a multi-platform index; the annotations below were read from its\n"+
			"               %s child manifest %s (the index's own manifest carries only a subset).\n",
			historyOut.ImageDigest, annSrc.ChildPlatform, annSrc.ChildDigest)
	case historySourceIndexOnly:
		fmt.Printf("Note:          %s is a multi-platform index; could not read a child manifest's\n"+
			"               annotations (%s). Showing index-level annotations only -- this is likely\n"+
			"               an incomplete view of what pokkum build actually wrote.\n",
			historyOut.ImageDigest, annSrc.Note)
	}
	if historyOut.GitRepo != "" || historyOut.GitCommit != "" {
		fmt.Printf("Git Source:    %s @ %s\n", historyOut.GitRepo, historyOut.GitCommit)
	} else {
		fmt.Println("Git Source:    (no org.opencontainers.image.source/.revision annotation found)")
	}
	if historyOut.BuildTimestamp != "" {
		fmt.Printf("Built:         %s\n", historyOut.BuildTimestamp)
	}
	if v, ok := historyOut.Annotations["org.opencontainers.image.version"]; ok {
		fmt.Printf("Version:       %s\n", v)
	}
	fmt.Println()
	fmt.Println("Signature / SLSA provenance are NOT verified by this command.")
	fmt.Printf("Run `pokkum verify %s` for a cryptographic verdict.\n", imageRef)

	return nil
}

// historyReport is the --output=json payload for `pokkum history`. It embeds
// ports.HistoryOutput unchanged -- that struct's contract belongs to
// internal/ports, which this package does not own -- and adds fields
// describing which manifest the embedded Annotations actually came from.
//
// This exists because of F7: for a multi-platform image, the ref
// `pokkum build` prints is the INDEX digest, and the index's own manifest
// carries only "created" plus a couple of build-scoped annotations
// (internal/adapters/packager/config.go's indexAnnotations) -- the git
// revision/source/base-image annotations that matter for provenance live on
// each per-platform child manifest instead (imageAnnotations, same file).
// fetchImageHistory now descends into a child manifest to recover them, but
// presenting a child manifest's annotations as if they were the index's own,
// with no indication of the substitution, would trade one silent gap for
// another -- so the descend is always disclosed here.
type historyReport struct {
	ports.HistoryOutput
	AnnotationsSource        string `json:"annotations_source"`
	AnnotationsChildDigest   string `json:"annotations_child_digest,omitempty"`
	AnnotationsChildPlatform string `json:"annotations_child_platform,omitempty"`
	AnnotationsNote          string `json:"annotations_note,omitempty"`
}

// historyAnnotationSource records which manifest fetchImageHistory actually
// read Annotations from, so callers (and runHistory's own output) never
// present a descended-into child manifest's annotations as if they belonged
// to the top-level ref. See historyReport's doc comment for the full F7
// background.
type historyAnnotationSource struct {
	// Kind is one of the historySource* constants below.
	Kind string
	// ChildDigest/ChildPlatform are set only for Kind == historySourceIndexAndChild.
	ChildDigest   string
	ChildPlatform string
	// Note explains a historySourceIndexOnly degradation (e.g. the
	// underlying error that prevented reading a child manifest); empty
	// otherwise.
	Note string
}

const (
	// historySourceManifest: imageRef resolved directly to a single-platform
	// manifest -- there is nothing to descend into.
	historySourceManifest = "manifest"
	// historySourceIndexAndChild: imageRef resolved to a multi-platform
	// index, and Annotations is the index's own annotations merged with a
	// selected child manifest's (child wins on overlapping keys).
	historySourceIndexAndChild = "index+child-manifest"
	// historySourceIndexOnly: imageRef resolved to an index, but no child
	// manifest's annotations could be read -- Annotations is the index's own
	// (impoverished) set only. This is the pre-fix behavior, so it must
	// remain visibly labeled rather than look identical to a full read.
	historySourceIndexOnly = "index-only"
)

// fetchImageHistory pulls imageRef's real manifest and reports the standard
// OCI annotations Pokkum writes at build time. It deliberately does not
// populate SignatureValid/SLSAProvenance/Builder/BaseImage/BunVersion —
// this command reads what the registry actually says about the image, it
// does not perform (or claim to perform) cryptographic verification; that
// is "pokkum verify"'s job, via the real cosign/sigstore verifiers already
// wired there.
//
// When imageRef resolves to a multi-platform index -- e.g. the exact ref
// `pokkum build` prints for a multi-platform build -- the index's own
// manifest carries only a handful of build-scoped annotations
// (packager.indexAnnotations: "created" plus caller-supplied ones). The
// annotations that matter for provenance (git revision, source, base image)
// live on each per-platform child manifest instead (packager.imageAnnotations).
// This descends into a deterministically-selected child to recover them,
// merging the two sets (child wins on overlap) so index-only keys like
// pokkum.dev/build-input-hash are not lost either. See F7 and
// selectHistoryChildManifest for the selection rule.
func fetchImageHistory(ctx context.Context, imageRef, registryConfigPath string) (ports.HistoryOutput, historyAnnotationSource, error) {
	ref, err := name.ParseReference(imageRef, name.WeakValidation)
	if err != nil {
		return ports.HistoryOutput{}, historyAnnotationSource{}, fmt.Errorf("parse image reference %q: %w", imageRef, err)
	}

	kc, err := registryutils.ResolveKeychain(registryConfigPath)
	if err != nil {
		return ports.HistoryOutput{}, historyAnnotationSource{}, err
	}

	desc, err := remote.Get(ref, remote.WithContext(ctx), remote.WithAuthFromKeychain(kc))
	if err != nil {
		return ports.HistoryOutput{}, historyAnnotationSource{}, fmt.Errorf("pull manifest for %s: %w", imageRef, err)
	}

	var (
		annotations map[string]string
		annSrc      historyAnnotationSource
	)
	switch {
	case desc.MediaType.IsIndex():
		idx, ierr := desc.ImageIndex()
		if ierr != nil {
			return ports.HistoryOutput{}, historyAnnotationSource{}, fmt.Errorf("read index for %s: %w", imageRef, ierr)
		}
		im, ierr := idx.IndexManifest()
		if ierr != nil {
			return ports.HistoryOutput{}, historyAnnotationSource{}, fmt.Errorf("read index manifest for %s: %w", imageRef, ierr)
		}
		annotations, annSrc = readIndexAnnotations(idx, im)
	default:
		img, ierr := desc.Image()
		if ierr != nil {
			return ports.HistoryOutput{}, historyAnnotationSource{}, fmt.Errorf("read image for %s: %w", imageRef, ierr)
		}
		m, ierr := img.Manifest()
		if ierr != nil {
			return ports.HistoryOutput{}, historyAnnotationSource{}, fmt.Errorf("read manifest for %s: %w", imageRef, ierr)
		}
		annotations = m.Annotations
		annSrc = historyAnnotationSource{Kind: historySourceManifest}
	}
	if annotations == nil {
		annotations = map[string]string{}
	}

	return ports.HistoryOutput{
		ImageRef:       imageRef,
		ImageDigest:    desc.Digest.String(),
		GitRepo:        annotations["org.opencontainers.image.source"],
		GitCommit:      annotations["org.opencontainers.image.revision"],
		BuildTimestamp: annotations["org.opencontainers.image.created"],
		Annotations:    annotations,
	}, annSrc, nil
}

// readIndexAnnotations recovers the fuller per-platform annotation set for a
// multi-platform index by descending into a deterministically-selected child
// manifest, and reports honestly (via the returned historyAnnotationSource)
// whether that descend actually happened. It never returns an error itself:
// a failure to read a child manifest degrades to the index's own annotations
// rather than failing the whole command, since the index-level annotations
// are still real, useful data (this mirrors --allow-unverified-source's
// "explicit, visible downgrade rather than a silent one" shape used
// elsewhere in this command family, though nothing here is a security
// verdict — it's just a narrower read).
func readIndexAnnotations(idx v1.ImageIndex, im *v1.IndexManifest) (map[string]string, historyAnnotationSource) {
	indexAnnotations := im.Annotations
	if indexAnnotations == nil {
		indexAnnotations = map[string]string{}
	}

	child, err := selectHistoryChildManifest(im.Manifests)
	if err != nil {
		return indexAnnotations, historyAnnotationSource{Kind: historySourceIndexOnly, Note: err.Error()}
	}

	childImg, err := idx.Image(child.Digest)
	if err != nil {
		return indexAnnotations, historyAnnotationSource{
			Kind: historySourceIndexOnly,
			Note: fmt.Sprintf("read child manifest %s: %v", child.Digest, err),
		}
	}
	childManifest, err := childImg.Manifest()
	if err != nil {
		return indexAnnotations, historyAnnotationSource{
			Kind: historySourceIndexOnly,
			Note: fmt.Sprintf("read child manifest %s: %v", child.Digest, err),
		}
	}

	merged := make(map[string]string, len(indexAnnotations)+len(childManifest.Annotations))
	maps.Copy(merged, indexAnnotations)
	maps.Copy(merged, childManifest.Annotations)

	return merged, historyAnnotationSource{
		Kind:          historySourceIndexAndChild,
		ChildDigest:   child.Digest.String(),
		ChildPlatform: platformString(child.Platform),
	}
}

// selectHistoryChildManifest picks which child of a multi-platform index to
// read the fuller annotation set from. Selection must be deterministic: the
// same index ref must report the same annotations regardless of registry
// response ordering or which machine runs the command (mem:core's
// determinism invariant, applied here to a read path rather than a build
// one).
//
// "unknown/unknown" is buildx's own convention for attestation-manifest
// index entries (SBOM/provenance payloads attached to the index alongside
// the real per-platform images) — those are skipped, since they are not
// image manifests and their "Annotations" would not answer what this
// command exists to answer. Pokkum's own packager.Index never emits such
// entries (every child it adds carries a real OS/Arch platform), but
// `pokkum history` also has to make sense of indexes it did not build.
func selectHistoryChildManifest(manifests []v1.Descriptor) (v1.Descriptor, error) {
	candidates := make([]v1.Descriptor, 0, len(manifests))
	for _, m := range manifests {
		if m.Platform != nil && m.Platform.OS == "unknown" && m.Platform.Architecture == "unknown" {
			continue
		}
		candidates = append(candidates, m)
	}
	if len(candidates) == 0 {
		return v1.Descriptor{}, fmt.Errorf("index carries no image manifest to descend into")
	}

	// Prefer linux/amd64: matches go-containerregistry's own default
	// platform selection (remote.Descriptor.Image()'s defaultPlatform) and
	// Pokkum's primary build target, so the common case matches what an
	// ordinary `desc.Image()` call would already have resolved to.
	for _, m := range candidates {
		if m.Platform != nil && m.Platform.OS == "linux" && m.Platform.Architecture == "amd64" {
			return m, nil
		}
	}

	// No linux/amd64 present (e.g. an arm64-only build): fall back to a
	// deterministic sort by platform string then digest, so the result never
	// depends on registry response order or which candidate happened to be
	// first in the index.
	sort.Slice(candidates, func(i, j int) bool {
		pi, pj := platformString(candidates[i].Platform), platformString(candidates[j].Platform)
		if pi != pj {
			return pi < pj
		}
		return candidates[i].Digest.String() < candidates[j].Digest.String()
	})
	return candidates[0], nil
}

func platformString(p *v1.Platform) string {
	if p == nil {
		return ""
	}
	return p.String()
}
