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
	return watchAndRunDevContainer(ctx, logger, flags, absDir, repoName, dockerContainerRunner{}, dockerDevBuilder{}, devWatchPollInterval)
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

// devWatchPollInterval is the production polling interval used by
// watchAndRunDevContainer to check for source modifications. Tests inject a
// much shorter interval so multiple rebuild cycles can be exercised quickly.
const devWatchPollInterval = 2 * time.Second

// devGenerationStopTimeout bounds how long watchAndRunDevContainer waits for
// a superseded (or, on shutdown, the current) container generation to
// actually finish before moving on. It is a safety net around what is
// otherwise a real synchronization point (waiting for the generation's
// goroutine to report in), not an arbitrary guess like the fixed sleep it
// replaces.
const devGenerationStopTimeout = 5 * time.Second

// devContainerRunner abstracts the "run one container generation" step of
// the dev watch loop so it can be faked in tests without shelling out to a
// real Docker daemon. This mirrors the releaseFetcher seam used by
// runUpgrade in upgrade.go: the real implementation is a thin adapter over
// the existing free function, and tests inject a fake that records calls
// and is driven by context cancellation instead of a real subprocess.
type devContainerRunner interface {
	Run(ctx context.Context, logger *slog.Logger, flags *devFlags, repoName string) error
}

// dockerContainerRunner is the production devContainerRunner backed by the
// real `docker run` invocation.
type dockerContainerRunner struct{}

func (dockerContainerRunner) Run(ctx context.Context, logger *slog.Logger, flags *devFlags, repoName string) error {
	return runDevContainer(ctx, logger, flags, repoName)
}

// devBuilder abstracts the "rebuild and load the image" step for the same
// reason as devContainerRunner above: it lets tests drive multiple rebuild
// cycles without invoking the real build pipeline (which needs a real
// SvelteKit project, bun, and a Docker daemon).
type devBuilder interface {
	Build(ctx context.Context, logger *slog.Logger, flags *devFlags, absDir, repoName string) error
}

// dockerDevBuilder is the production devBuilder backed by the real build
// pipeline.
type dockerDevBuilder struct{}

func (dockerDevBuilder) Build(ctx context.Context, logger *slog.Logger, flags *devFlags, absDir, repoName string) error {
	return buildAndLoadDevContainer(ctx, logger, flags, absDir, repoName)
}

// watchAndRunDevContainer runs the container, watches the project's src/
// directory for modifications, and rebuilds+relaunches on change.
//
// Each container generation gets its own error channel (cmdErrChan is
// reassigned to a fresh channel every time a new generation is launched),
// rather than every generation sharing one buffered channel. This is the
// crux of the fix for a bug where a superseded generation's stale exit
// result (e.g. "signal: killed", written when cancelContainer stops the old
// container for a rebuild) could be read by a later loop iteration and
// misreported as the *current* generation crashing, ending the whole watch
// session after a single rebuild. With a fresh channel per generation, a
// stale write from an old generation lands in a channel nobody is listening
// on anymore -- it is silently discarded, and the goroutine that wrote it
// still exits normally (the send never blocks, since each channel is
// created fresh and used by exactly one generation), so nothing leaks.
func watchAndRunDevContainer(ctx context.Context, logger *slog.Logger, flags *devFlags, absDir, repoName string, runner devContainerRunner, builder devBuilder, pollInterval time.Duration) error {
	containerCtx, cancelContainer := context.WithCancel(ctx)
	defer func() { cancelContainer() }()

	// Launch initial container generation in the background, using a
	// channel dedicated to this generation only.
	cmdErrChan := make(chan error, 1)
	go func(cCtx context.Context, resultCh chan error) {
		resultCh <- runner.Run(cCtx, logger, flags, repoName)
	}(containerCtx, cmdErrChan)

	logger.Info("dev container running; watching for source changes (press Ctrl+C to exit)...")

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	var lastMod time.Time
	srcDir := filepath.Join(absDir, "src")
	if stat, err := os.Stat(srcDir); err == nil {
		lastMod = stat.ModTime()
	}

	for {
		select {
		case <-ctx.Done():
			// Case (c): outer context cancelled (user Ctrl-C). Stop the
			// current generation and wait for it to actually report in
			// before returning, so the command doesn't exit while the
			// container is still in the middle of stopping. Bounded by a
			// timeout so shutdown can never hang indefinitely.
			cancelContainer()
			select {
			case <-cmdErrChan:
			case <-time.After(devGenerationStopTimeout):
				logger.Warn("timed out waiting for dev container to stop during shutdown")
			}
			return ctx.Err()
		case err := <-cmdErrChan:
			cancelContainer()
			if ctx.Err() != nil {
				// The outer context was cancelled at roughly the same time
				// this generation's container stopped in response to it.
				// Report this as the same clean shutdown as the ctx.Done()
				// case above, regardless of which select case the runtime
				// happened to pick -- never let this race surface as a
				// crash (e.g. a raw "signal: killed" error).
				return ctx.Err()
			}
			// Case (a): the current generation genuinely exited on its
			// own (not superseded by a rebuild, not cancelled by us) --
			// report and stop.
			if err != nil {
				logger.Error("container exited with error", "error", err)
			}
			return err
		case <-ticker.C:
			currentMod := getLatestModTime(srcDir)
			if currentMod.After(lastMod) {
				logger.Info("detected source modifications; re-building container...", "dir", srcDir)
				lastMod = currentMod

				// Case (b): deliberately killing the current generation
				// for a rebuild is expected, not a crash. Cancel it, then
				// wait for it to actually finish (bounded by a timeout)
				// instead of guessing with a fixed sleep -- this is a real
				// synchronization point, not an arbitrary wait.
				cancelContainer()
				select {
				case oldErr := <-cmdErrChan:
					logger.Debug("previous dev container generation stopped", "error", oldErr)
				case <-time.After(devGenerationStopTimeout):
					logger.Warn("timed out waiting for previous dev container generation to stop; proceeding with rebuild anyway")
				}

				if err := builder.Build(ctx, logger, flags, absDir, repoName); err != nil {
					logger.Error("re-build failed", "error", err)
					continue
				}

				// Start the next generation with its own context AND its
				// own fresh result channel. Even if the wait above timed
				// out and the old generation is still shutting down, its
				// eventual write now lands in the old (now-unreferenced)
				// channel instead of this one -- it can never be confused
				// with this generation's result, and the goroutine that
				// writes it still exits normally without leaking.
				var nextCtx context.Context
				nextCtx, cancelContainer = context.WithCancel(ctx)
				cmdErrChan = make(chan error, 1)
				go func(cCtx context.Context, resultCh chan error) {
					resultCh <- runner.Run(cCtx, logger, flags, repoName)
				}(nextCtx, cmdErrChan)
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
