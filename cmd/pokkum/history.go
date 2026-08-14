package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
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

	historyOut, err := fetchImageHistory(ctx, imageRef, flags.registryConfig)
	if err != nil {
		msg := fmt.Sprintf("failed to read image annotations for %s: %v", imageRef, err)
		if outputFormat == ports.FormatJSON {
			return jsonutils.WriteError(os.Stdout, "history", "ERR_HISTORY_FAILED", msg, "")
		}
		return fmt.Errorf("%s", msg)
	}

	if outputFormat == ports.FormatJSON {
		return jsonutils.WriteSuccess(os.Stdout, "history", historyOut)
	}

	fmt.Println("=== Image Provenance Annotations ===")
	fmt.Printf("Image:         %s\n", historyOut.ImageRef)
	fmt.Printf("Digest:        %s\n", historyOut.ImageDigest)
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

// fetchImageHistory pulls imageRef's real manifest and reports the standard
// OCI annotations Pokkum writes at build time. It deliberately does not
// populate SignatureValid/SLSAProvenance/Builder/BaseImage/BunVersion —
// this command reads what the registry actually says about the image, it
// does not perform (or claim to perform) cryptographic verification; that
// is "pokkum verify"'s job, via the real cosign/sigstore verifiers already
// wired there.
func fetchImageHistory(ctx context.Context, imageRef, registryConfigPath string) (ports.HistoryOutput, error) {
	ref, err := name.ParseReference(imageRef, name.WeakValidation)
	if err != nil {
		return ports.HistoryOutput{}, fmt.Errorf("parse image reference %q: %w", imageRef, err)
	}

	kc, err := registryutils.ResolveKeychain(registryConfigPath)
	if err != nil {
		return ports.HistoryOutput{}, err
	}

	desc, err := remote.Get(ref, remote.WithContext(ctx), remote.WithAuthFromKeychain(kc))
	if err != nil {
		return ports.HistoryOutput{}, fmt.Errorf("pull manifest for %s: %w", imageRef, err)
	}

	var annotations map[string]string
	switch {
	case desc.MediaType.IsIndex():
		idx, ierr := desc.ImageIndex()
		if ierr != nil {
			return ports.HistoryOutput{}, fmt.Errorf("read index for %s: %w", imageRef, ierr)
		}
		im, ierr := idx.IndexManifest()
		if ierr != nil {
			return ports.HistoryOutput{}, fmt.Errorf("read index manifest for %s: %w", imageRef, ierr)
		}
		annotations = im.Annotations
	default:
		img, ierr := desc.Image()
		if ierr != nil {
			return ports.HistoryOutput{}, fmt.Errorf("read image for %s: %w", imageRef, ierr)
		}
		m, ierr := img.Manifest()
		if ierr != nil {
			return ports.HistoryOutput{}, fmt.Errorf("read manifest for %s: %w", imageRef, ierr)
		}
		annotations = m.Annotations
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
	}, nil
}
