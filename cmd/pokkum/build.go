package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/config"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// buildFlags holds all command-line flags for the build command.
type buildFlags struct {
	platforms     []string
	base          string
	hardened      bool
	sbom          string
	local         bool
	tarball       string
	dryRun        bool
	printManifest bool
	logLevel      string
	logFormat     string
}

func newBuildCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	flags := &buildFlags{}

	cmd := &cobra.Command{
		Use:   "build [dir]",
		Short: "Build a SvelteKit application into a container image",
		Long: `Build compiles a SvelteKit application and assembles it into a reproducible
container image with a hardened base. It handles multi-platform builds, SBOM generation,
and multiple output modes (push to registry, load into Docker daemon, or export to tarball).

The project directory defaults to the current working directory.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBuild(ctx, logger, flags, args)
		},
	}

	// Flag definitions with defaults from spec
	cmd.Flags().StringSliceVarP(&flags.platforms, "platform", "p", []string{"linux/amd64", "linux/arm64"},
		"Target platform(s); repeatable, e.g. --platform linux/amd64 --platform linux/arm64. Use 'all' for all supported platforms")
	cmd.Flags().StringVar(&flags.base, "base", "",
		"Base image preset (distroless [default], chainguard, or custom reference)")
	cmd.Flags().BoolVar(&flags.hardened, "hardened", false,
		"Select the Chainguard base preset (shorthand for --base chainguard)")
	cmd.Flags().StringVar(&flags.sbom, "sbom", "spdx-json",
		"SBOM format (spdx-json [default], cyclonedx-json, or none)")
	cmd.Flags().BoolVar(&flags.local, "local", false,
		"Load the image into the local Docker daemon instead of pushing to a registry")
	cmd.Flags().StringVar(&flags.tarball, "tarball", "",
		"Export the image as an OCI archive to the specified path (e.g., image.tar)")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false,
		"Resolve everything and report what would be built and pushed, but perform no writes")
	cmd.Flags().BoolVar(&flags.printManifest, "print-manifest", false,
		"Emit the computed OCI manifest/config without pushing")
	cmd.Flags().StringVar(&flags.logLevel, "log-level", "INFO",
		"Log level (DEBUG, INFO, WARN, ERROR)")
	cmd.Flags().StringVar(&flags.logFormat, "log-format", "text",
		"Log format (text or json)")

	return cmd
}

func runBuild(ctx context.Context, logger *slog.Logger, flags *buildFlags, args []string) error {
	// Determine project directory
	projectDir := "."
	if len(args) > 0 {
		projectDir = args[0]
	}

	logger.Debug("build command started", "project_dir", projectDir)

	// Load configuration
	cfg, err := config.New(projectDir, logger)
	if err != nil {
		return fmt.Errorf("config loader: %w", err)
	}

	// Build the request from flags, config, and environment
	req := core.BuildRequest{
		ProjectDir: projectDir,
	}

	// Repo: from env, then config, then error if missing (for push mode)
	repo := os.Getenv("POKKUM_DOCKER_REPO")
	if repo == "" {
		repo = cfg.GetString("docker.repo", "")
	}
	req.Repo = repo

	// Platforms: parse from flags
	platforms, err := core.ParsePlatforms(flags.platforms)
	if err != nil {
		return fmt.Errorf("invalid platforms: %w", err)
	}
	req.Platforms = platforms

	// Base image: handle --hardened shorthand and --base
	basePreset := flags.base
	if flags.hardened {
		basePreset = "chainguard"
	}
	if basePreset != "" {
		parsed, err := core.ParseBaseImagePreset(basePreset)
		if err != nil {
			return fmt.Errorf("invalid base image preset: %w", err)
		}
		req.BaseImage.Preset = parsed
	}

	// SBOM format
	sbomFmt, err := core.ParseSBOMFormat(flags.sbom)
	if err != nil {
		return fmt.Errorf("invalid sbom format: %w", err)
	}
	req.SBOM.Format = sbomFmt

	// Output mode
	if flags.local && flags.tarball != "" {
		return fmt.Errorf("cannot specify both --local and --tarball")
	}
	if flags.local {
		req.Output.Mode = core.OutputLocal
	} else if flags.tarball != "" {
		req.Output.Mode = core.OutputTarball
		req.Output.TarballPath = flags.tarball
	} else {
		req.Output.Mode = core.OutputPush
	}

	// Dry-run and print-manifest flags
	// Thread them into the request for W13 to honour
	_ = flags.dryRun // TODO: store in request when W13 wires them
	_ = flags.printManifest

	// Resolve SOURCE_DATE_EPOCH
	timestamp, err := cfg.ResolveBuildTimestamp()
	if err != nil {
		return fmt.Errorf("source date epoch: %w", err)
	}
	req.SourceDateEpoch = timestamp

	// Normalize and validate
	req.Normalize()
	if err := req.Validate(); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// Report what we would do
	logger.Info("build request constructed",
		"project_dir", req.ProjectDir,
		"platforms", core.PlatformList(req.Platforms),
		"output_mode", req.Output.Mode,
		"repo", req.Repo)

	// Return "not yet wired" error as per spec
	return fmt.Errorf("build pipeline not yet implemented: %w", core.ErrInvalidRequest)
}
