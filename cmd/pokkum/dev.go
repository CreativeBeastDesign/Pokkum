package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/config"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

type devFlags struct {
	debug       bool
	port        string
	watch       bool
	envFile     string
	platform    string
	bunBinary   string
	bunVariant  string
	bunVersion  string
	noContainer bool
	cluster     bool
	namespace   string
	selector    string
	container   string
}

func newDevCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	flags := &devFlags{}

	cmd := &cobra.Command{
		Use:   "dev [dir]",
		Short: "Hot-reload SvelteKit development environment",
		Long: `Dev runs a local development loop for your SvelteKit project.

By default (container-parity mode), Dev compiles the SvelteKit application into a local container
image, loads it into the local Docker daemon, and runs it immediately -- exercising the same runtime
environment (supervisor, probes, non-root user) a real Pokkum image ships with. When --watch is
enabled (default), source file changes trigger automatic container image rebuilds. When --debug is
enabled, the command drops into an interactive shell inside the container environment.

--no-container skips image construction entirely and instead runs the project's own dev server
("bun run dev") directly on the host, relying on its native hot-module-reloading (e.g. Vite HMR)
rather than Pokkum's rebuild loop. This is the fast path for everyday iteration, but it does NOT
reproduce the runtime guarantees a real Pokkum image provides -- no supervisor, no startup
attestation, no health/readiness probes, no base image, no non-root user. Use the default
container-parity mode whenever a real environment check matters.

--cluster is the third mode: it builds the SvelteKit project and streams /app/server and
/app/client straight into an already-running pod over the Kubernetes API, then asks the in-pod
pokkum-init to restart the application process. No image is built, nothing is pushed to a
registry, and the pod is never recreated. It requires kubectl on PATH, --namespace and
--selector, and a workload that opts in by setting POKKUM_DEV_MODE=1 on the container. The
patched pod no longer matches the image it was started from, and every change is lost the moment
the pod is replaced -- this is a development loop, not a deployment.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateDevFlags(cmd); err != nil {
				return err
			}
			warnIneffectiveNoContainerFlags(cmd, logger)
			warnIneffectiveClusterFlags(cmd, logger)
			return runDev(ctx, logger, flags, args)
		},
	}

	cmd.Flags().BoolVar(&flags.debug, "debug", false,
		"Drop into an interactive shell inside the container environment (not supported with --no-container)")
	cmd.Flags().StringVarP(&flags.port, "port", "p", "3000:3000",
		"Port mapping for container execution (e.g. 3000:3000); ignored with --no-container, where the project's own dev server picks its own port")
	cmd.Flags().BoolVar(&flags.watch, "watch", true,
		"Watch source directory and auto-rebuild container on file changes (has no effect with --no-container, which relies on the dev server's own hot reload)")
	cmd.Flags().StringVar(&flags.envFile, "env-file", "",
		"Path to an environment file to pass to container execution, or to merge into the local dev server's environment with --no-container")
	cmd.Flags().StringVar(&flags.platform, "platform", "",
		"Target platform for local container build (defaults to local host architecture); not supported with --no-container")
	cmd.Flags().StringVar(&flags.bunBinary, "bun-binary", "",
		"Local path to a bun executable escape hatch; also the executable --no-container runs \"run dev\" with (default: \"bun\" on PATH)")
	cmd.Flags().StringVar(&flags.bunVariant, "bun-variant", "standard",
		"Bun CPU variant (standard or baseline); not supported with --no-container")
	cmd.Flags().StringVar(&flags.bunVersion, "bun-version", "",
		"Bun release version to embed (default: the pinned "+core.DefaultBunVersion+"); not supported with --no-container")
	cmd.Flags().BoolVar(&flags.noContainer, "no-container", false,
		"Skip image construction entirely and run the project's own dev server directly on the host -- no daemon, no supervisor/probes/non-root guarantees; use for fast local iteration, not production parity")
	cmd.Flags().BoolVar(&flags.cluster, "cluster", false,
		"Build and sync /app/server and /app/client straight into a running pod via kubectl, restarting the in-pod application process -- no image build, no registry round-trip; requires --namespace, --selector and POKKUM_DEV_MODE=1 on the target container")
	cmd.Flags().StringVar(&flags.namespace, "namespace", "",
		"Kubernetes namespace holding the pod to sync into (required with --cluster)")
	cmd.Flags().StringVar(&flags.selector, "selector", "",
		"Label selector identifying the pod to sync into, in kubectl -l syntax, e.g. app=storefront (required with --cluster)")
	cmd.Flags().StringVar(&flags.container, "container", "",
		"Container within the matched pod to sync into; required only when the pod runs more than one container")

	return cmd
}

// validateDevFlags rejects flag combinations that request behavior
// --no-container fundamentally cannot deliver: a shell inside a container
// that is never built, a platform for an image that is never built, or a
// specific Bun release/variant to embed when nothing is embedded. These all
// describe a property of the image-build path with no local-process
// equivalent, so accepting them would mean the flag parses fine and
// silently does nothing -- exactly the failure mode this repo has hit
// before (see Lessons.md's --bun-version entry). Reject outright instead,
// so the mistake surfaces immediately as a usage error.
//
// Only cmd.Flags() is consulted (not *devFlags) so this is testable against
// a bare *cobra.Command without needing a matching flags pointer, and so it
// can distinguish "explicitly set" from "left at its default" via
// Changed().
func validateDevFlags(cmd *cobra.Command) error {
	fs := cmd.Flags()
	noContainer, _ := fs.GetBool("no-container")
	cluster, _ := fs.GetBool("cluster")

	if err := validateDevClusterFlags(fs, cluster, noContainer); err != nil {
		return err
	}
	if !noContainer {
		return nil
	}

	debug, _ := fs.GetBool("debug")
	var rejected []string
	if debug {
		rejected = append(rejected, "--debug (no container exists to open a shell in)")
	}
	if fs.Changed("platform") {
		rejected = append(rejected, "--platform (no image is built, so there is no platform to target)")
	}
	if fs.Changed("bun-version") {
		rejected = append(rejected, "--bun-version (no Bun runtime is embedded; the host's bun, or --bun-binary, is used directly)")
	}
	if fs.Changed("bun-variant") {
		rejected = append(rejected, "--bun-variant (no Bun runtime is embedded; the host's bun, or --bun-binary, is used directly)")
	}
	if len(rejected) == 0 {
		return nil
	}
	return fmt.Errorf("dev: --no-container is incompatible with: %s: %w", strings.Join(rejected, "; "), core.ErrInvalidRequest)
}

// validateDevClusterFlags enforces --cluster's own flag contract, in both
// directions.
//
// The forward direction is ordinary: --cluster needs a namespace and a
// selector, and it cannot be combined with --no-container (two different
// places to run the application) or with the image-build flags, since it
// builds no image at all.
//
// The reverse direction is the one this repo has been bitten by before: a
// --namespace, --selector or --container given WITHOUT --cluster parses
// perfectly and does nothing whatsoever. That is the exact "flag parsed fine,
// silently did nothing" shape Lessons.md's --bun-version entry records, so it
// is a usage error rather than a warning.
func validateDevClusterFlags(fs *pflag.FlagSet, cluster, noContainer bool) error {
	if !cluster {
		var orphaned []string
		for _, name := range []string{"namespace", "selector", "container"} {
			if fs.Changed(name) {
				orphaned = append(orphaned, "--"+name)
			}
		}
		if len(orphaned) > 0 {
			return fmt.Errorf("dev: %s only apply to --cluster, and --cluster was not given: %w",
				strings.Join(orphaned, ", "), core.ErrInvalidRequest)
		}
		return nil
	}

	if noContainer {
		return fmt.Errorf("dev: --cluster and --no-container are mutually exclusive: one syncs into a running pod, the other runs a dev server on this machine: %w", core.ErrInvalidRequest)
	}

	var missing []string
	if strings.TrimSpace(mustGetString(fs, "namespace")) == "" {
		missing = append(missing, "--namespace")
	}
	if strings.TrimSpace(mustGetString(fs, "selector")) == "" {
		missing = append(missing, "--selector")
	}
	if len(missing) > 0 {
		return fmt.Errorf("dev: --cluster requires %s: a loop that hot-patches a running pod must never guess which one: %w",
			strings.Join(missing, " and "), core.ErrInvalidRequest)
	}

	debug, _ := fs.GetBool("debug")
	var rejected []string
	if debug {
		rejected = append(rejected, "--debug (there is no local container to open a shell in; use kubectl exec)")
	}
	if fs.Changed("platform") {
		rejected = append(rejected, "--platform (no image is built; the pod's own image decides its platform)")
	}
	if fs.Changed("bun-version") {
		rejected = append(rejected, "--bun-version (no Bun runtime is embedded; the pod runs the one already in its image)")
	}
	if fs.Changed("bun-variant") {
		rejected = append(rejected, "--bun-variant (no Bun runtime is embedded; the pod runs the one already in its image)")
	}
	if len(rejected) > 0 {
		return fmt.Errorf("dev: --cluster is incompatible with: %s: %w", strings.Join(rejected, "; "), core.ErrInvalidRequest)
	}
	return nil
}

// mustGetString reads a string flag that this file registered itself. The
// error can only be a programming mistake (wrong name, wrong type), never
// user input, so it is folded into the empty string rather than plumbed
// through every caller.
func mustGetString(fs *pflag.FlagSet, name string) string {
	v, err := fs.GetString(name)
	if err != nil {
		return ""
	}
	return v
}

// warnIneffectiveClusterFlags is --cluster's counterpart to
// warnIneffectiveNoContainerFlags: the flags that have a plausible but
// different meaning here rather than none at all, and so warn instead of
// failing.
func warnIneffectiveClusterFlags(cmd *cobra.Command, logger *slog.Logger) {
	fs := cmd.Flags()
	cluster, _ := fs.GetBool("cluster")
	if !cluster {
		return
	}
	if fs.Changed("port") {
		logger.Warn("--port has no effect with --cluster; the pod keeps the ports its own manifest declares. Use kubectl port-forward to reach it.", "port", mustGetString(fs, "port"))
	}
	if fs.Changed("watch") {
		watch, _ := fs.GetBool("watch")
		logger.Warn("--watch has no effect with --cluster; the sync loop always watches for source changes, and there is no single-shot mode to fall back to", "watch", watch)
	}
	if fs.Changed("env-file") {
		logger.Warn("--env-file has no effect with --cluster; the running pod's environment comes from its own manifest and cannot be changed by syncing files into it. Use kubectl set env instead.", "env_file", mustGetString(fs, "env-file"))
	}
	if fs.Changed("bun-binary") {
		logger.Warn("--bun-binary points at the bun used for the local SvelteKit build only; the pod runs the Bun already embedded in its own image", "bun_binary", mustGetString(fs, "bun-binary"))
	}
}

// warnIneffectiveNoContainerFlags logs an explicit warning for flags that
// --no-container reinterprets rather than rejects outright. Unlike
// validateDevFlags's rejections, --port and --watch each have a plausible
// (if different) real behavior in no-container mode, so failing the command
// outright would be more surprising than proceeding with a clear
// explanation of what actually happens -- but staying silent would repeat
// the same "flag parsed fine, did nothing" mistake, just as a warning
// instead of an error.
func warnIneffectiveNoContainerFlags(cmd *cobra.Command, logger *slog.Logger) {
	fs := cmd.Flags()
	noContainer, _ := fs.GetBool("no-container")
	if !noContainer {
		return
	}

	if fs.Changed("port") {
		port, _ := fs.GetString("port")
		logger.Warn("--port's HOST:CONTAINER mapping does not apply with --no-container; the project's own dev server picks its own port -- see its own startup output", "port", port)
	}
	if fs.Changed("watch") {
		watch, _ := fs.GetBool("watch")
		logger.Warn("--watch has no effect with --no-container; hot reload is handled by the project's own dev server (e.g. Vite HMR), not by Pokkum's rebuild loop", "watch", watch)
	}
}

func runDev(ctx context.Context, logger *slog.Logger, flags *devFlags, args []string) error {
	return runDevWithDeps(ctx, logger, flags, args, dockerContainerRunner{}, dockerDevBuilder{}, hostDevProcessRunner{}, kubectlClusterStarter{})
}

// runDevWithDeps is runDev's real implementation, parameterized over the
// container-parity seams (devContainerRunner, devBuilder -- pre-existing,
// see the type docs below) and the no-container seam (devLocalRunner, new)
// so tests can drive every branch without a real Docker daemon, a real
// SvelteKit project, or a real bun binary. Critically, this also makes the
// "--no-container never touches the container seams" property directly
// assertable: a test can inject a devContainerRunner/devBuilder that fails
// the test if Run/Build is ever called, then run the --no-container path
// and confirm it wasn't.
func runDevWithDeps(ctx context.Context, logger *slog.Logger, flags *devFlags, args []string, containerRunner devContainerRunner, containerBuilder devBuilder, localRunner devLocalRunner, clusterStarter devClusterStarter) error {
	projectDir := "."
	if len(args) > 0 {
		projectDir = args[0]
	}

	absDir, err := filepath.Abs(projectDir)
	if err != nil {
		return fmt.Errorf("dev: resolve project directory %q: %w", projectDir, err)
	}

	if flags.noContainer {
		return runNoContainerDev(ctx, logger, flags, absDir, localRunner)
	}

	if flags.cluster {
		// Like --no-container above, this branch deliberately never
		// references containerRunner or containerBuilder: nothing is built
		// into an image and no daemon is contacted. The tests' "never
		// invoked" assertion on those fakes is what proves it.
		return clusterStarter.Start(ctx, logger, flags, absDir)
	}

	logger.Info("starting hot-reload dev environment", "project_dir", absDir, "debug", flags.debug, "port", flags.port)

	// Step 1: Initial build and daemon load
	repoName := "pokkum.local/" + strings.ToLower(filepath.Base(absDir)) + ":dev"
	if err := containerBuilder.Build(ctx, logger, flags, absDir, repoName); err != nil {
		return fmt.Errorf("dev build failed: %w", err)
	}

	// Step 2: Container execution loop
	if !flags.watch || flags.debug {
		// Single-shot or debug shell run
		return containerRunner.Run(ctx, logger, flags, repoName)
	}

	// Hot-reload loop with watching
	return watchAndRunDevContainer(ctx, logger, flags, absDir, repoName, containerRunner, containerBuilder, devWatchPollInterval)
}

// runNoContainerDev is the entire --no-container execution path: no image
// build, no packaging, no daemon -- just the project's own dev server,
// supervised directly. It deliberately never references containerRunner or
// containerBuilder; the tests' "never invoked" assertion on those fakes is
// what actually proves that, not this comment.
//
// The startup warning below is logged exactly once per invocation (dev
// itself is a single long-running command, so "once at startup" and "once
// per process" coincide here) -- deliberately at Warn level, since silently
// debugging a production discrepancy against a mode that was never meant to
// reproduce production is the exact mistake this exists to head off.
func runNoContainerDev(ctx context.Context, logger *slog.Logger, flags *devFlags, absDir string, runner devLocalRunner) error {
	logger.Warn("--no-container: running the project's dev server directly on the host, skipping image construction entirely. " +
		"This mode has none of the runtime guarantees a real Pokkum image provides: no supervisor, no startup attestation, " +
		"no health/readiness probes, no base image, no non-root user. It is for fast local iteration only -- it does not " +
		"reproduce production. Use the default container-parity dev mode (no --no-container) when a real environment check matters.")

	logger.Info("starting local dev server (no container)", "project_dir", absDir)

	return runner.Run(ctx, logger, flags, absDir)
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

// devLocalRunner abstracts the --no-container execution step (run the
// project's own dev server directly on the host) for the same testability
// reason as devContainerRunner/devBuilder above: it lets tests drive a
// clean-shutdown-on-cancel scenario without a real bun binary or a real
// SvelteKit project. Its shape deliberately differs from devContainerRunner
// (absDir instead of repoName) rather than being forced to fit that
// interface -- there is no image name here, and reusing repoName for a
// filesystem path would read as a bug to the next person touching this
// file. It is a genuinely different execution mode (a supervised local
// process, not a container), not a second watch loop: unlike
// watchAndRunDevContainer, there is no rebuild-on-change logic here at all,
// because the project's own dev server (Vite HMR) already handles hot
// reload -- reimplementing that would be exactly the "second, subtly
// different loop" this package's history warns against.
type devLocalRunner interface {
	Run(ctx context.Context, logger *slog.Logger, flags *devFlags, absDir string) error
}

// hostDevProcessRunner is the production devLocalRunner backed by the real
// project dev server subprocess.
type hostDevProcessRunner struct{}

func (hostDevProcessRunner) Run(ctx context.Context, logger *slog.Logger, flags *devFlags, absDir string) error {
	return runLocalDevProcess(ctx, logger, flags, absDir)
}

// runLocalDevProcess runs the project's own "dev" package.json script (via
// bun, or flags.bunBinary as an escape hatch) directly on the host and
// supervises it for the lifetime of ctx. There is no rebuild loop: Vite (or
// whatever the script wraps) owns hot reload internally, so this function's
// only jobs are starting the process, wiring its stdio through, merging in
// an optional --env-file, and shutting it down cleanly on cancellation.
func runLocalDevProcess(ctx context.Context, logger *slog.Logger, flags *devFlags, absDir string) error {
	pkg, err := sveltekitutils.ReadPackageJSON(absDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("dev: %s: package.json not found: %w", absDir, core.ErrProjectNotFound)
		}
		return fmt.Errorf("dev: %s: read package.json: %w: %w", absDir, err, core.ErrProjectNotFound)
	}
	if _, ok := pkg.Scripts["dev"]; !ok {
		return fmt.Errorf(`dev: %s: package.json has no "dev" script for --no-container to run: %w`, absDir, core.ErrInvalidRequest)
	}

	bunExe := flags.bunBinary
	if bunExe == "" {
		bunExe = "bun"
	}
	runArgs := []string{"run", "dev"}

	cmd := exec.CommandContext(ctx, bunExe, runArgs...)
	cmd.Dir = absDir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()

	if flags.envFile != "" {
		envPairs, err := parseSimpleEnvFile(flags.envFile)
		if err != nil {
			return fmt.Errorf("dev: read env file %q: %w", flags.envFile, err)
		}
		cmd.Env = append(cmd.Env, envPairs...)
	}

	// Graceful-then-bounded shutdown: on ctx cancellation, ask the dev
	// server to exit the way Ctrl+C would (SIGINT) rather than killing it
	// outright, so Vite/bun get to print their own shutdown message and
	// release the port promptly. WaitDelay bounds how long that is allowed
	// to take before Go escalates to a hard Kill, so shutdown can never
	// hang indefinitely -- reusing devGenerationStopTimeout's bound, the
	// same shutdown-grace philosophy watchAndRunDevContainer already uses
	// for container generations.
	cmd.Cancel = func() error {
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = devGenerationStopTimeout

	logger.Info("running project dev server", "cmd", bunExe+" "+strings.Join(runArgs, " "), "dir", absDir)

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			// The context was cancelled (outer shutdown) at roughly the
			// same time the dev server exited in response to the SIGINT
			// above -- report this as the same clean shutdown regardless
			// of which happened first, never as a raw "signal: interrupt"
			// crash.
			return ctx.Err()
		}
		return fmt.Errorf("dev: local dev server exited: %w", err)
	}
	return nil
}

// parseSimpleEnvFile reads path in the same simple format Docker's own
// --env-file uses: one KEY=VALUE pair per line, blank lines and lines
// starting with '#' ignored, no quoting or variable expansion. --no-container
// mode has no daemon to hand the file to, so it is parsed here instead and
// merged directly into the local dev server's own environment.
func parseSimpleEnvFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var pairs []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "=") {
			return nil, fmt.Errorf("invalid line %q: expected KEY=VALUE", line)
		}
		pairs = append(pairs, line)
	}
	return pairs, nil
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
