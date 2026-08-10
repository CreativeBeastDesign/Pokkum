package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// applyFlags holds all command-line flags for the apply command.
type applyFlags struct {
	file      string
	recursive bool
}

func newApplyCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	flags := &applyFlags{}

	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Apply Kubernetes manifests with pokkum:// image references",
		Long: `Apply processes Kubernetes manifests with pokkum:// image references,
resolves them to concrete images, and applies the resolved manifests to the cluster.

This combines resolution and Kubernetes application in a single step, automatically
updating deployment manifests with the latest built images.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runApply(ctx, logger, flags)
		},
	}

	cmd.Flags().StringVarP(&flags.file, "file", "f", "",
		"File or directory containing Kubernetes manifests")
	cmd.Flags().BoolVar(&flags.recursive, "recursive", false,
		"Process all YAML files in the directory recursively")

	// file is required
	cmd.MarkFlagRequired("file")

	return cmd
}

func runApply(ctx context.Context, logger *slog.Logger, flags *applyFlags) error {
	logger.Debug("apply command started",
		"file", flags.file,
		"recursive", flags.recursive)

	// Validation
	if flags.file == "" {
		return fmt.Errorf("--file is required: %w", core.ErrInvalidRequest)
	}

	logger.Info("apply request constructed",
		"file", flags.file,
		"recursive", flags.recursive)

	// Return "not yet wired" error as per spec
	return fmt.Errorf("apply not yet implemented: %w", core.ErrInvalidRequest)
}
