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
	noRebuild    bool
	expectSource string
	against      string
	output       string
}

func newVerifyCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	opts := &verifyOptions{}

	cmd := &cobra.Command{
		Use:   "verify <image-ref>",
		Short: "Independently rebuild from source and verify container image provenance",
		Long:  `Verify proves that a remote registry image or cluster container provably matches its source commit by checking SLSA attestations and independently rebuilding from source.`,
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

	return cmd
}

func runVerify(ctx context.Context, logger *slog.Logger, opts *verifyOptions, imageRef string) error {
	outputFormat := ports.OutputFormat(opts.output)

	provResolver := provenance.NewResolver(logger)
	provSummary, err := provResolver.ResolveProvenance(ctx, ports.ProvenanceResolverRequest{
		ImageRef:     imageRef,
		ExpectSource: opts.expectSource,
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

	if opts.noRebuild {
		out := ports.VerifyOutput{
			ImageRef:   imageRef,
			Verdict:    "ATTESTATION_VALIDATED",
			Level:      "NONE",
			Provenance: provSummary,
		}

		if outputFormat == ports.FormatJSON {
			return jsonutils.WriteSuccess(os.Stdout, "verify", out)
		}

		fmt.Println("=== Pokkum Rebuild Verification Report (No-Rebuild Mode) ===")
		fmt.Printf("Image:       %s\n", provSummary.ImageRef)
		fmt.Printf("Source Repo: %s @ %s\n", provSummary.PinnedInputs.Repo, provSummary.PinnedInputs.Commit[:8])
		fmt.Printf("Signature:   %t (%s)\n", provSummary.SignatureValid, provSummary.SignerIdentity)
		fmt.Printf("Toolchain:   %s (%s)\n", provSummary.PinnedInputs.BunVersion, provSummary.ToolchainNotes)
		fmt.Println("Verdict:     Attestation & toolchain metadata validated successfully.")
		return nil
	}

	// Full Rebuild Mode
	comp := comparator.NewComparator(logger)
	compResult, err := comp.CompareImages(ctx, ports.ImageComparatorRequest{
		RemoteImageRef: imageRef,
		LocalTarball:   opts.against,
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
	out := ports.VerifyOutput{
		ImageRef:        imageRef,
		Verdict:         verdict,
		Level:           compResult.Level,
		Provenance:      provSummary,
		MismatchDetails: compResult.L3FileDiffs,
	}

	if outputFormat == ports.FormatJSON {
		return jsonutils.WriteSuccess(os.Stdout, "verify", out)
	}

	fmt.Println("=== Pokkum Rebuild Verification Report ===")
	fmt.Printf("Image:       %s\n", provSummary.ImageRef)
	fmt.Printf("Source Repo: %s @ %s\n", provSummary.PinnedInputs.Repo, provSummary.PinnedInputs.Commit[:8])
	fmt.Printf("Level:       %s\n", compResult.Level)
	fmt.Printf("Summary:     %s\n", compResult.Summary)
	fmt.Printf("Verdict:     %s\n", verdict)

	return nil
}
