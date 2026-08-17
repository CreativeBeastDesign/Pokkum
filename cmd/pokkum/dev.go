package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/config"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

type devFlags struct {
	debug      bool
	port       string
	watch      bool
	envFile    string
	platform   string
	bunBinary  string
	bunVariant string
	bunVersion string
}

func newDevCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	flags := &devFlags{}

	cmd := &cobra.Command{
		Use:   "dev [dir]",
		Short: "Hot-reload containerized SvelteKit development environment",
		Long: `Dev compiles the SvelteKit application into a local container image, loads it into
the local Docker daemon, and runs it immediately.

When --watch is enabled (default), source file changes trigger automatic container image updates.
When --debug is enabled, the command drops into an interactive shell inside the container environment.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			return runDev(ctx, logger, flags, args)
		},
	}

	cmd.Flags().BoolVar(&flags.debug, "debug", false,
		"Drop into an interactive shell inside the container environment")
	cmd.Flags().StringVarP(&flags.port, "port", "p", "3000:3000",
		"Port mapping for container execution (e.g. 3000:3000)")
	cmd.Flags().BoolVar(&flags.watch, "watch", true,
		"Watch source directory and auto-rebuild container on file changes")
	cmd.Flags().StringVar(&flags.envFile, "env-file", "",
		"Path to an environment file to pass to container execution")
	cmd.Flags().StringVar(&flags.platform, "platform", "",
		"Target platform for local container build (defaults to local host architecture)")
	cmd.Flags().StringVar(&flags.bunBinary, "bun-binary", "",
		"Local path to a bun executable escape hatch")
	cmd.Flags().StringVar(&flags.bunVariant, "bun-variant", "standard",
		"Bun CPU variant (standard or baseline)")
	cmd.Flags().StringVar(&flags.bunVersion, "bun-version", "",
		"Bun release version to embed (default: the pinned "+core.DefaultBunVersion+")")

	return cmd
}

func runDev(ctx context.Context, logger *slog.Logger, flags *devFlags, args []string) error {
	projectDir := "."
	if len(args) > 0 {
		projectDir = args[0]
	}

	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("dev: resolve project directory %q: %w", projectDir, err)
	}

	logger.Info("starting hot-reload dev environment", "project_dir", absDir, "debug", flags.debug, "port", flags.port)

	// Step 1: Initial build and daemon load
	repoName := "pokkum.local/" + strings.ToLower(filepath.Base(absDir)) + ":dev"
	if err := buildAndLoadDevContainer(ctx, logger, flags, absDir, repoName); err != nil {
		return fmt.Errorf("dev build failed: %w", err)
	}

	// Step 2: Container execution loop
	if !flags.watch || flags.debug {
		// Single-shot or debug shell run
		return runDevContainer(ctx, logger, flags, repoName)
	}

	// Hot-reload loop with watching
	return watchAndRunDevContainer(ctx, logger, flags, absDir, repoName)
}

func buildAndLoadDevContainer(ctx context.Context, logger *slog.Logger, flags *devFlags, absDir, repoName string) error {
	cfg, err := config.New(absDir, logger)
	if err != nil {
		return fmt.Errorf("config loader: %w", err)
	}

	platformsStr := []string{}
	if flags.platform != "" {
		platformsStr = append(platformsStr, flags.platform)
	}
	platforms, err := core.ParsePlatforms(platformsStr)
	if err != nil {
		return fmt.Errorf("invalid platform: %w", err)
	}

	req := core.BuildRequest{
		ProjectDir: absDir,
		Repo:       repoName,
		Platforms:  platforms,
		Output: core.OutputOptions{
			Mode: core.OutputLocal,
		},
		SBOM: core.SBOMOptions{
			Format:   core.SBOMFormatNone,
			NoAttach: true,
		},
		Sign: false,
		BunRuntime: core.BunRuntimeOptions{
			Version:          flags.bunVersion,
			CustomBinaryPath: flags.bunBinary,
			Variant:          core.BunVariant(flags.bunVariant),
		},
	}

	timestamp, err := cfg.ResolveBuildTimestamp()
	if err != nil {
		timestamp = time.Now().UTC()
	}
	req.SourceDateEpoch = timestamp

	req.Normalize()
	if err := req.Validate(); err != nil {
		return fmt.Errorf("request validation failed: %w", err)
	}

	opts := core.BuildOptions{}
	res, err := runCoreBuild(ctx, buildDeps(logger, os.Stdout), req, opts)
	if err != nil {
		return err
	}

	logger.Info("dev container image built and loaded", "ref", res.Image.Ref, "digest", res.Image.Digest.String())
	return nil
}

func runDevContainer(ctx context.Context, logger *slog.Logger, flags *devFlags, repoName string) error {
	args := []string{"run", "--rm"}
	if flags.port != "" {
		args = append(args, "-p", flags.port)
	}
	if flags.envFile != "" {
		args = append(args, "--env-file", flags.envFile)
	}

	if flags.debug {
		args = append(args, "-it", "--entrypoint", "/bin/sh", repoName)
	} else {
		args = append(args, repoName)
	}

	logger.Info("launching local container", "cmd", "docker "+strings.Join(args, " "))

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("container execution failed: %w", err)
	}
	return nil
}

func watchAndRunDevContainer(ctx context.Context, logger *slog.Logger, flags *devFlags, absDir, repoName string) error {
	containerCtx, cancelContainer := context.WithCancel(ctx)
	defer func() { cancelContainer() }()

	// Launch initial container in background
	cmdErrChan := make(chan error, 1)
	go func() {
		cmdErrChan <- runDevContainer(containerCtx, logger, flags, repoName)
	}()

	logger.Info("dev container running; watching for source changes (press Ctrl+C to exit)...")

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var lastMod time.Time
	srcDir := filepath.Join(absDir, "src")
	if stat, err := os.Stat(srcDir); err == nil {
		lastMod = stat.ModTime()
	}

	for {
		select {
		case <-ctx.Done():
			cancelContainer()
			return ctx.Err()
		case err := <-cmdErrChan:
			cancelContainer()
			if err != nil && ctx.Err() == nil {
				logger.Error("container exited with error", "error", err)
			}
			return err
		case <-ticker.C:
			currentMod := getLatestModTime(srcDir)
			if currentMod.After(lastMod) {
				logger.Info("detected source modifications; re-building container...", "dir", srcDir)
				lastMod = currentMod

				cancelContainer()
				// Wait briefly for process cleanup
				time.Sleep(500 * time.Millisecond)

				if err := buildAndLoadDevContainer(ctx, logger, flags, absDir, repoName); err != nil {
					logger.Error("re-build failed", "error", err)
					continue
				}

				var nextCtx context.Context
				nextCtx, cancelContainer = context.WithCancel(ctx)
				go func(cCtx context.Context) {
					cmdErrChan <- runDevContainer(cCtx, logger, flags, repoName)
				}(nextCtx)
			}
		}
	}
}

func getLatestModTime(dir string) time.Time {
	var latest time.Time
	_ = filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.ModTime().After(latest) {
			latest = info.ModTime()
		}
		return nil
	})
	return latest
}
