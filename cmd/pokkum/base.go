package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/baseimage"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/lockfileutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

type baseUpdateFlags struct {
	preset         string
	mirrorRegistry string
}

func newBaseCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "base",
		Short: "Manage base image lockfile (pokkum.lock)",
		Long: `The base command group provides operations for maintaining and updating
base image digest locks recorded in pokkum.lock.`,
	}

	cmd.AddCommand(newBaseUpdateCommand(ctx, logger))
	cmd.AddCommand(newBaseCheckCommand(ctx, logger))

	return cmd
}

func newBaseUpdateCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	flags := &baseUpdateFlags{}

	cmd := &cobra.Command{
		Use:   "update [dir]",
		Short: "Update locked base image digests in pokkum.lock",
		Long: `Queries remote registries for the latest upstream digests of configured base image presets
(e.g., distroless, chainguard), updates pokkum.lock in the project directory, and prints the updated digests.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runBaseUpdate(ctx, logger, flags, args)
		},
	}

	cmd.Flags().StringVar(&flags.preset, "preset", "", "Preset to update (distroless, chainguard, or empty for all)")
	cmd.Flags().StringVar(&flags.mirrorRegistry, "mirror-registry", "", "Mirror repository for base image escrow (e.g. ghcr.io/myorg/base-mirror)")

	return cmd
}

func newBaseCheckCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "check [dir]",
		Short: "Check for upstream base image updates without modifying pokkum.lock",
		Long: `Compares locked base image digests in pokkum.lock against upstream registries
and reports whether newer base image digests are available.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runBaseCheck(ctx, logger, args)
		},
	}

	return cmd
}

func runBaseUpdate(ctx context.Context, logger *slog.Logger, flags *baseUpdateFlags, args []string) error {
	projectDir := "."
	if len(args) > 0 {
		projectDir = args[0]
	}

	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("base update: %w", err)
	}

	lockPath := filepath.Join(absDir, ports.PokkumLockfileName)
	resolver := baseimage.NewResolver(logger)

	presetsToUpdate := []ports.BaseImagePreset{
		ports.BaseImageDistroless,
		ports.BaseImageChainguard,
	}

	if flags.preset != "" {
		p, err := core.ParseBaseImagePreset(flags.preset)
		if err != nil {
			return fmt.Errorf("base update: %w", err)
		}
		presetsToUpdate = []ports.BaseImagePreset{p}
	}

	for _, preset := range presetsToUpdate {
		if preset == ports.BaseImageCustom {
			continue
		}
		logger.Info("updating base image lock", "preset", preset)
		res, err := resolver.Resolve(ctx, ports.BaseImageRequest{
			Preset:         preset,
			Platforms:      core.SupportedPlatforms,
			LockfilePath:   lockPath,
			UpdateBase:     true,
			MirrorRegistry: flags.mirrorRegistry,
		})
		if err != nil {
			logger.Warn("failed to update base image preset", "preset", preset, "err", err)
			continue
		}
		fmt.Fprintf(os.Stdout, "Updated base preset %q -> %s\n", preset, res.PinnedRef)
	}

	return nil
}

func runBaseCheck(ctx context.Context, logger *slog.Logger, args []string) error {
	projectDir := "."
	if len(args) > 0 {
		projectDir = args[0]
	}

	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("base check: %w", err)
	}

	lockPath := filepath.Join(absDir, ports.PokkumLockfileName)
	lf, err := lockfileutils.LoadLockfile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stdout, "No lockfile found at %s. Run 'pokkum base update' or 'pokkum build' to create one.\n", lockPath)
			return nil
		}
		return fmt.Errorf("base check: %w", err)
	}

	resolver := baseimage.NewResolver(logger)
	for name, entry := range lf.Bases {
		preset, err := core.ParseBaseImagePreset(name)
		if err != nil {
			continue
		}

		res, err := resolver.Resolve(ctx, ports.BaseImageRequest{
			Preset:    preset,
			Ref:       entry.Ref,
			Platforms: core.SupportedPlatforms,
		})
		if err != nil {
			logger.Warn("failed to check upstream base image", "preset", name, "err", err)
			continue
		}

		if res.Digest.String() != entry.Digest {
			fmt.Fprintf(os.Stdout, "[UPDATE AVAILABLE] %s: locked=%s upstream=%s\n", name, entry.Digest, res.Digest.String())
		} else {
			fmt.Fprintf(os.Stdout, "[UP TO DATE] %s: %s\n", name, entry.Digest)
		}
	}

	return nil
}
