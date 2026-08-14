package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/comparator"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/jsonutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/provenance"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
	"github.com/spf13/cobra"
)

type verifyOptions struct {
	noRebuild      bool
	expectSource   string
	against        string
	registryConfig string
	output         string
}

func newVerifyCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	opts := &verifyOptions{}

	cmd := &cobra.Command{
		Use:   "verify <image-ref>",
		Short: "Independently rebuild from source and verify container image provenance",
		Long:  `Verify proves that a remote registry image or container provably matches its source commit by checking SLSA attestations, Cosign signatures, and independently comparing against a rebuilt artifact.`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			outFlag, _ := cmd.Flags().GetString("output")
			if outFlag != "" {
				opts.output = outFlag
			}
			imageRef := args[0]
			return runVerify(ctx, logger, opts, imageRef)
		},
	}

	cmd.Flags().BoolVar(&opts.noRebuild, "no-rebuild", false, "Validate attestations and toolchain metadata without running a rebuild")
	cmd.Flags().StringVar(&opts.expectSource, "expect-source", "", "Assert expected source repository and commit (repo@commit)")
	cmd.Flags().StringVar(&opts.against, "against", "", "Explicit local tarball path to compare against instead of remote registry")
	cmd.Flags().StringVar(&opts.registryConfig, "registry-config", "", "Path to docker config.json-style auth file for private registries")

	return cmd
}

func runVerify(ctx context.Context, logger *slog.Logger, opts *verifyOptions, imageRef string) error {
	outputFormat := ports.OutputFormat(opts.output)

	provResolver := provenance.NewResolver(logger)
	provSummary, err := provResolver.ResolveProvenance(ctx, ports.ProvenanceResolverRequest{
		ImageRef:           imageRef,
		ExpectSource:       opts.expectSource,
		RegistryConfigPath: opts.registryConfig,
	})

	if err != nil {
		msg := fmt.Sprintf("failed to resolve provenance for %s: %v", imageRef, err)
		if outputFormat == ports.FormatJSON {
			_ = jsonutils.WriteError(os.Stdout, "verify", "ERR_PROVENANCE_FAILED", msg, "")
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
		os.Exit(2)
	}

	commitDisplay := provSummary.PinnedInputs.Commit
	if len(commitDisplay) > 8 {
		commitDisplay = commitDisplay[:8]
	}
	if commitDisplay == "" {
		commitDisplay = "(unrecorded)"
	}
	repoDisplay := provSummary.PinnedInputs.Repo
	if repoDisplay == "" {
		repoDisplay = "(unrecorded)"
	}

	if opts.noRebuild {
		verdict := "ATTESTATION_VALIDATED"
		if !provSummary.HasProvenance && provSummary.PinnedInputs.Commit == "" {
			verdict = "UNATTESTED"
		}

		out := ports.VerifyOutput{
			ImageRef:   imageRef,
			Verdict:    verdict,
			Level:      "NONE",
			Provenance: provSummary,
		}

		if outputFormat == ports.FormatJSON {
			return jsonutils.WriteSuccess(os.Stdout, "verify", out)
		}

		fmt.Println("=== Pokkum Rebuild Verification Report (No-Rebuild Mode) ===")
		fmt.Printf("Image:       %s\n", provSummary.ImageRef)
		fmt.Printf("Digest:      %s\n", provSummary.ImageDigest)
		fmt.Printf("Source Repo: %s @ %s\n", repoDisplay, commitDisplay)
		fmt.Printf("Attestation: %t\n", provSummary.HasProvenance)
		fmt.Printf("Signature:   %t (%s)\n", provSummary.SignatureValid, provSummary.SignerIdentity)
		if provSummary.PinnedInputs.BunVersion != "" {
			fmt.Printf("Toolchain:   Bun %s\n", provSummary.PinnedInputs.BunVersion)
		}
		if provSummary.ToolchainNotes != "" {
			fmt.Printf("Notes:       %s\n", provSummary.ToolchainNotes)
		}
		fmt.Printf("Verdict:     %s\n", verdict)
		return nil
	}

	// Full Rebuild / Comparison Mode
	if opts.against == "" {
		msg := fmt.Sprintf("rebuild comparison requires an artifact to compare against: specify --against <path/to/rebuilt.tar> or pass --no-rebuild for attestation check only (source commit: %s @ %s)", repoDisplay, commitDisplay)
		if outputFormat == ports.FormatJSON {
			_ = jsonutils.WriteError(os.Stdout, "verify", "ERR_REBUILD_REQUIRED", msg, "")
			os.Exit(2)
		}
		fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
		os.Exit(2)
	}

	comp := comparator.NewComparator(logger)
	compResult, err := comp.CompareImages(ctx, ports.ImageComparatorRequest{
		RemoteImageRef:     imageRef,
		LocalTarball:       opts.against,
		RegistryConfigPath: opts.registryConfig,
	})

	if err != nil {
		msg := fmt.Sprintf("rebuild comparison failed: %v", err)
		if outputFormat == ports.FormatJSON {
			_ = jsonutils.WriteError(os.Stdout, "verify", "ERR_COMPARISON_FAILED", msg, "")
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Error: %s\n", msg)
		os.Exit(1)
	}

	verdict := "VERIFIED_" + compResult.Level
	if compResult.Level == "L3" {
		verdict = "MISMATCH"
	}

	out := ports.VerifyOutput{
		ImageRef:        imageRef,
		Verdict:         verdict,
		Level:           compResult.Level,
		Provenance:      provSummary,
		MismatchDetails: compResult.L3FileDiffs,
	}

	if outputFormat == ports.FormatJSON {
		if compResult.Level == "L3" {
			_ = jsonutils.WriteError(os.Stdout, "verify", "ERR_COMPARISON_MISMATCH", compResult.Summary, "")
			os.Exit(1)
		}
		return jsonutils.WriteSuccess(os.Stdout, "verify", out)
	}

	fmt.Println("=== Pokkum Rebuild Verification Report ===")
	fmt.Printf("Image:       %s\n", provSummary.ImageRef)
	fmt.Printf("Digest:      %s\n", provSummary.ImageDigest)
	fmt.Printf("Source Repo: %s @ %s\n", repoDisplay, commitDisplay)
	fmt.Printf("Level:       %s\n", compResult.Level)
	fmt.Printf("Summary:     %s\n", compResult.Summary)
	fmt.Printf("Verdict:     %s\n", verdict)

	if len(compResult.L3FileDiffs) > 0 {
		fmt.Println("\nFile-Level Differences:")
		for _, d := range compResult.L3FileDiffs {
			fmt.Printf("  - %s\n", d)
		}
	}

	if compResult.Level == "L3" {
		os.Exit(1)
	}

	return nil
}
