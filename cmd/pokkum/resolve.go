package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// resolveFlags holds all command-line flags for the resolve command.
type resolveFlags struct {
	file      string
	recursive bool
}

func newResolveCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	flags := &resolveFlags{}

	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Resolve pokkum:// references in Kubernetes manifests",
		Long: `Resolve processes Kubernetes manifests and resolves pokkum:// image references
to their concrete digest form, validating that the images have been built and are available.

This is useful for generating deployment manifests with pinned image digests for
reproducibility and auditability.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runResolve(ctx, logger, flags)
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

func runResolve(ctx context.Context, logger *slog.Logger, flags *resolveFlags) error {
	logger.Debug("resolve command started",
		"file", flags.file,
		"recursive", flags.recursive)

	// Validation
	if flags.file == "" {
		return fmt.Errorf("--file is required: %w", core.ErrInvalidRequest)
	}

	logger.Info("resolve request constructed",
		"file", flags.file,
		"recursive", flags.recursive)

	// Return "not yet wired" error as per spec
	return fmt.Errorf("resolve not yet implemented: %w", core.ErrInvalidRequest)
}
