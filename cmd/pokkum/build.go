package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/baseimage"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunexec"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/config"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registry"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sbom"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/supervisor"
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

	// Execution-mode switches. They are not part of the request — both
	// describe how far to get, not what to build — so they travel alongside it
	// as core.BuildOptions.
	if flags.dryRun && flags.printManifest {
		return fmt.Errorf("cannot specify both --dry-run and --print-manifest")
	}
	opts := core.BuildOptions{
		DryRun:        flags.dryRun,
		PrintManifest: flags.printManifest,
	}

	// Resolve SOURCE_DATE_EPOCH
	timestamp, err := cfg.ResolveBuildTimestamp()
	if err != nil {
		return fmt.Errorf("source date epoch: %w", err)
	}
	req.SourceDateEpoch = timestamp

	// Normalize and validate here as well as inside core.Build, so that a bad
	// flag combination is reported before the composition root builds
	// anything. core.Build repeats both; they are idempotent.
	req.Normalize()
	if err := req.Validate(); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// The result carries the full summary, but the reference has already gone
	// to stdout by the time Build returns; everything here is a log line.
	res, err := core.Build(ctx, buildDeps(logger), req, opts)
	if err != nil {
		return err
	}

	logger.Info("build finished",
		"ref", res.Image.Ref,
		"digest", res.Image.Digest.String(),
		"platforms", core.PlatformList(res.Image.Platforms),
		"base", res.BaseImage.PinnedRef,
		"duration", res.Duration.String())
	return nil
}

// buildDeps is the composition root: the one place in the program where the
// concrete adapters are named. core.Build sees only the ports, which is what
// keeps internal/core free of any adapter import.
//
// Every adapter is constructed unconditionally. They are all trivial value
// types holding a logger — the registry adapter does not open a connection and
// the supervisor provider does not touch its embedded binaries until asked —
// so there is nothing to gain from building them lazily per output mode, and
// something to lose in a branch that could get the mapping wrong.
func buildDeps(logger *slog.Logger) core.Deps {
	reg := registry.NewAdapter(logger)
	return core.Deps{
		Compiler:   bunexec.NewCompiler(logger),
		BaseImages: baseimage.NewResolver(logger),
		Supervisor: supervisor.New(logger),
		Packager:   packager.NewPackager(logger),

		// One adapter satisfies all three publishing ports: pushing, loading
		// into the daemon and writing a tarball are the same
		// go-containerregistry machinery pointed at different sinks.
		Registry: reg,
		Daemon:   reg,
		Tarballs: reg,

		SBOM: sbom.NewGenerator(logger),

		Logger:    logger,
		Stdout:    os.Stdout,
		Version:   version,
		UserAgent: "pokkum/" + version,
	}
}
