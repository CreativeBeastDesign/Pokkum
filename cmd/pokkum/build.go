package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/baseimage"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunexec"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunruntime"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/config"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/dsse"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/nativeinspect"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registry"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/remotecacheutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sbom"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/scanner"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/secretguard"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sigstore"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/slsa"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/staticserver"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/supervisor"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// buildFlags holds all command-line flags for the build command.
type buildFlags struct {
	platforms     []string
	base          string
	hardened      bool
	sbom          string
	sbomAttach    string
	local         bool
	tarball       string
	dryRun        bool
	printManifest bool
	logLevel      string
	logFormat     string
	updateBase    bool
	offline       bool

	// Telemetry flags
	telemetry       bool
	noTelemetry     bool
	otelExport      string
	telemetryEnv    string
	traceSampleRate float64
	metricsOnly     bool
	withOtelSidecar bool

	// Signing flags
	sign   bool
	noSign bool

	// Injection flags
	inject   bool
	noInject bool

	// Bun runtime & strategy flags
	bunBinary  string
	bunVariant string
	strategy   string
	// strategyExplicit records whether --strategy was actually passed on the
	// command line, as opposed to sitting at its "layered" default — needed
	// to detect a genuine --strategy=layered --static conflict, since
	// "layered" can't be distinguished from "unset" by value alone.
	strategyExplicit bool
	static           bool
	compression      string
	sourcemap        bool

	imageLabels []string

	profile              string
	platformExplicit     bool
	baseExplicit         bool
	sbomExplicit         bool
	sbomAttachExplicit   bool
	sourcemapExplicit    bool
	stubLauncherExplicit bool

	noVerifyBase        bool
	baseVerifyMode      string
	baseKeylessIdentity string
	baseKeylessIssuer   string
	sigstoreTrustedRoot string
	allowSecretPatterns []string
	hermetic            bool
	registryConfig      string
	pushConcurrency     int
	requireEnv          []string
	failOnCVE           string
	allowIncomplete     bool
	noPrune             bool
	keepVendor          []string
	noPrecompress       bool
	noStrip             bool
	noCache             bool
	stubLauncher        bool

	// Cache verification flags
	noCacheVerify        bool
	cacheVerifyMode      string
	cacheVerifyKey       string
	cacheKeylessIdentity string
	cacheKeylessIssuer   string
	cacheVerifyStrict    bool
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
			flags.strategyExplicit = cmd.Flags().Changed("strategy")
			flags.platformExplicit = cmd.Flags().Changed("platform")
			flags.baseExplicit = cmd.Flags().Changed("base")
			flags.sbomExplicit = cmd.Flags().Changed("sbom")
			flags.sbomAttachExplicit = cmd.Flags().Changed("sbom-attach")
			flags.sourcemapExplicit = cmd.Flags().Changed("sourcemap")
			flags.stubLauncherExplicit = cmd.Flags().Changed("stub-launcher")
			return runBuild(ctx, logger, flags, args)
		},
	}

	// Flag definitions with defaults from spec
	cmd.Flags().StringVarP(&flags.profile, "profile", "P", "",
		"Activate a named build profile defined in .pokkum.yaml (e.g. --profile local or --profile production)")
	cmd.Flags().StringSliceVarP(&flags.platforms, "platform", "p", []string{"linux/amd64", "linux/arm64"},
		"Target platform(s); repeatable, e.g. --platform linux/amd64 --platform linux/arm64. Use 'all' for all supported platforms")
	cmd.Flags().StringVar(&flags.base, "base", "",
		"Base image preset (distroless [default], chainguard, or custom reference)")
	cmd.Flags().BoolVar(&flags.hardened, "hardened", false,
		"Select the Chainguard base preset (shorthand for --base chainguard)")
	cmd.Flags().StringVar(&flags.sbom, "sbom", "spdx-json",
		"SBOM format (spdx-json [default], cyclonedx-json, or none)")
	cmd.Flags().StringVar(&flags.sbomAttach, "sbom-attach", "referrer",
		"SBOM attachment mode (referrer [default, OCI 1.1] or tag)")
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

	// Telemetry flags
	cmd.Flags().BoolVar(&flags.telemetry, "telemetry", false,
		"Enable OpenTelemetry auto-instrumentation and metrics export")
	cmd.Flags().BoolVar(&flags.noTelemetry, "no-telemetry", false,
		"Explicitly disable OpenTelemetry auto-instrumentation and metrics export")
	cmd.Flags().StringVar(&flags.otelExport, "otel-export", "",
		"Override OTLP exporter endpoint URL (e.g. http://collector:4318)")
	cmd.Flags().StringVar(&flags.telemetryEnv, "telemetry-env", "",
		"Target environment for telemetry (dev, preview, production)")
	cmd.Flags().Float64Var(&flags.traceSampleRate, "trace-sample-rate", 1.0,
		"Sampling ratio for trace spans (0.0 to 1.0)")
	cmd.Flags().BoolVar(&flags.metricsOnly, "metrics-only", false,
		"Disable trace span generation while keeping OTEL metrics active")
	cmd.Flags().BoolVar(&flags.withOtelSidecar, "with-otel-sidecar", false,
		"Inject OTEL Collector sidecar spec into Kubernetes manifests")

	// Signing flags
	cmd.Flags().BoolVar(&flags.sign, "sign", true,
		"Enable SLSA, Cosign, and DSSE signing (default true)")
	cmd.Flags().BoolVar(&flags.noSign, "no-sign", false,
		"Explicitly disable signing")

	cmd.Flags().BoolVar(&flags.updateBase, "update-base", false,
		"Force re-resolving base image tags against remote registry and update pokkum.lock")
	cmd.Flags().BoolVar(&flags.offline, "offline", false,
		"Strictly enforce using pokkum.lock and local cache without remote registry calls")

	// Injection flags
	cmd.Flags().BoolVar(&flags.inject, "inject", true,
		"Enable zero-config auto-injection for svelte.config.js (default true)")
	cmd.Flags().BoolVar(&flags.noInject, "no-inject", false,
		"Explicitly disable auto-injection")

	// Bun runtime & strategy flags
	cmd.Flags().StringVar(&flags.bunBinary, "bun-binary", "",
		"Local path to a bun executable escape hatch (skips download/resolution)")
	cmd.Flags().StringVar(&flags.bunVariant, "bun-variant", "standard",
		"Bun CPU variant (standard [AVX2 required on x86-64] or baseline)")
	cmd.Flags().BoolVar(&flags.stubLauncher, "stub-launcher", false,
		"Compile a minimal entrypoint launcher stub instead of embedding stock Bun runtime (layered strategy hardening)")
	cmd.Flags().StringVar(&flags.strategy, "strategy", "layered",
		"Packaging strategy: layered (5-layer arch-independent layout [default]), exe (single executable, deprecated), or static (purely static site served by an embedded Go file server, no Bun runtime)")
	cmd.Flags().BoolVar(&flags.static, "static", false,
		"Shorthand for --strategy=static: compile a purely static site onto a minimal libc-free base image, served by an embedded Go file server (ETag/Range/Content-Encoding). Cannot be combined with --strategy.")
	cmd.Flags().StringVar(&flags.compression, "compression", "gzip",
		"Layer compression algorithm: gzip (default) or zstd")
	cmd.Flags().StringSliceVar(&flags.imageLabels, "image-label", nil,
		"Custom image labels (key=value), repeatable")

	cmd.Flags().BoolVar(&flags.noVerifyBase, "no-verify-base", false,
		"Suppress Cosign signature verification on upstream base images")
	cmd.Flags().StringVar(&flags.baseVerifyMode, "base-verify-mode", "",
		"Base image signature verification mode: auto (preset decides), keyless (Fulcio/Rekor, the default for distroless/chainguard), or static-key (Cosign key-based verification)")
	cmd.Flags().StringVar(&flags.baseKeylessIdentity, "base-keyless-identity", "",
		"Expected Fulcio certificate Subject Alternative Name for keyless base image verification (overrides the preset default)")
	cmd.Flags().StringVar(&flags.baseKeylessIssuer, "base-keyless-issuer", "",
		"Expected OIDC issuer for keyless base image verification (overrides the preset default)")
	cmd.Flags().StringVar(&flags.sigstoreTrustedRoot, "sigstore-trusted-root", "",
		"Path to a Sigstore trusted-root JSON file overriding the embedded public-good default")
	cmd.Flags().StringSliceVar(&flags.allowSecretPatterns, "allow-secret-pattern", nil,
		"Regex pattern to ignore during build-time secret scanning, repeatable")

	cmd.Flags().BoolVar(&flags.hermetic, "hermetic", false,
		"Enforce strict hermetic build mode (zero network egress, cached base images and node_modules required)")
	cmd.Flags().StringVar(&flags.registryConfig, "registry-config", "",
		"Path to custom OCI registry auth config file (config.json)")
	cmd.Flags().IntVar(&flags.pushConcurrency, "push-concurrency", 0,
		"Number of concurrent layer uploads during registry push (0 = registry adapter's default)")
	cmd.Flags().StringSliceVar(&flags.requireEnv, "require-env", nil,
		"Declare required runtime environment variables (comma-separated or repeatable)")
	cmd.Flags().StringVar(&flags.failOnCVE, "fail-on-cve", "",
		"Fail build if base image vulnerabilities exceed threshold (low, medium, high, critical; default warn-only)")
	cmd.Flags().BoolVar(&flags.allowIncomplete, "allow-incomplete", false,
		"Allow build to succeed even if base image vulnerability database lookups fail (default: fail closed when --fail-on-cve is active)")
	cmd.Flags().BoolVar(&flags.noPrune, "no-prune", false,
		"Disable build-time stripping of non-runtime files (*.d.ts, *.map, tests, docs) from the vendor layer")
	cmd.Flags().StringSliceVar(&flags.keepVendor, "keep-vendor", nil,
		"Custom glob pattern(s) of vendor files to preserve during pruning, repeatable (e.g. --keep-vendor='*.md')")
	cmd.Flags().BoolVar(&flags.noPrecompress, "no-precompress", false,
		"Disable build-time static asset pre-compression (.gz, .br, .zst)")
	cmd.Flags().BoolVar(&flags.noStrip, "no-strip", false,
		"Disable build-time stripping of unneeded debug symbols from native .node ELF addons")
	cmd.Flags().BoolVar(&flags.noCache, "no-cache", false,
		"Disable remote OCI composite input caching and force full rebuild")
	cmd.Flags().BoolVar(&flags.sourcemap, "sourcemap", false,
		"Generate and preserve source maps in compiled bundles and vendor layers")

	// Cache verification flags
	cmd.Flags().BoolVar(&flags.noCacheVerify, "no-cache-verify", false,
		"Disable cryptographic signature verification on remote cache-hit images")
	cmd.Flags().StringVar(&flags.cacheVerifyMode, "cache-verify-mode", "",
		"Cache image signature verification mode: auto (default), static-key, or keyless")
	cmd.Flags().StringVar(&flags.cacheVerifyKey, "cache-verify-key", "",
		"Path or PEM string for static Cosign public key to verify remote cache hits")
	cmd.Flags().StringVar(&flags.cacheKeylessIdentity, "cache-keyless-identity", "",
		"Expected Fulcio certificate Subject Alternative Name for keyless cache verification")
	cmd.Flags().StringVar(&flags.cacheKeylessIssuer, "cache-keyless-issuer", "",
		"Expected OIDC issuer for keyless cache verification")
	cmd.Flags().BoolVar(&flags.cacheVerifyStrict, "cache-verify-strict", false,
		"Strict cache verification: fail build if candidate cache tag has invalid signature")

	return cmd
}

func runBuild(ctx context.Context, logger *slog.Logger, flags *buildFlags, args []string) error {
	// Determine project directory
	projectDir := "."
	if len(args) > 0 {
		projectDir = args[0]
	}

	logger.Debug("build command started", "project_dir", projectDir)

	req, err := buildRequestFromConfigAndFlags(ctx, logger, flags, projectDir)
	if err != nil {
		return err
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

	// Normalize and validate here as well as inside core.Build, so that a bad
	// flag combination is reported before the composition root builds
	// anything. core.Build repeats both; they are idempotent.
	req.Normalize()
	if err := req.Validate(); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	// The result carries the full summary, but the reference has already gone
	// to stdout by the time Build returns; everything here is a log line.
	res, err := runCoreBuild(ctx, buildDeps(logger, os.Stdout), *req, opts)
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

// runCoreBuild calls core.Build wrapped in a packager.NewBuildContext, so
// every intermediate layer temp file the packager adapter creates for this
// one build is removed deterministically once Build returns — success or
// error — rather than left for the packager's runtime.SetFinalizer backstop,
// which a normal process exit does not reliably run. This is the only place
// that call belongs: it is the one composition-root site that calls
// core.Build and therefore the one place that knows the entire build,
// including publish (registry push, daemon load, or tarball write), has
// finished — core.Build itself must not know about packager's temp files,
// and Packager.Build returning is not that point, since the returned
// v1.Image's layers still read from those files until publish drains them.
//
// Every cmd/pokkum call site that invokes core.Build must go through this
// wrapper rather than calling it directly, or its build leaks the packager's
// temp files exactly as before.
func runCoreBuild(ctx context.Context, deps core.Deps, req core.BuildRequest, opts core.BuildOptions) (core.BuildResult, error) {
	bctx, cleanup := packager.NewBuildContext(ctx)
	defer cleanup()
	return core.Build(bctx, deps, req, opts)
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
//
// stdout is threaded through explicitly rather than hard-coded to os.Stdout:
// `pokkum build` wants the published reference on the real stdout, but
// resolve/apply reserve stdout for the rewritten manifest (piped into
// `kubectl apply -f -`) and must not let a nested build's own "repo@sha256:…"
// line leak onto it. Passing nil is deliberate there — Deps.Stdout documents
// nil as io.Discard.
func buildDeps(logger *slog.Logger, stdout io.Writer) core.Deps {
	reg := registry.NewAdapter(logger)
	return core.Deps{
		Compiler:     bunexec.NewCompiler(logger),
		BaseImages:   baseimage.NewResolver(logger),
		Supervisor:   supervisor.New(logger),
		StaticServer: staticserver.New(logger),
		Packager:     packager.NewPackager(logger),
		BunRuntime:   bunruntime.NewResolver("", nil),

		Registry: reg,
		Daemon:   reg,
		Tarballs: reg,

		SBOM:            sbom.NewGenerator(logger),
		NativeInspector: nativeinspect.NewClosuredAdapter(),
		SLSAGenerator:   slsa.NewGenerator(logger),
		CosignSigner:    cosign.NewSigner(logger),
		DSSESigner:      dsse.NewSigner(logger),
		Scanner:         scanner.NewAdapter(logger),
		SecretGuard:     secretguard.NewAdapter(),
		RemoteCache: remotecacheutils.New(
			remotecacheutils.WithLogger(logger),
			remotecacheutils.WithCosignSigner(cosign.NewSigner(logger)),
			remotecacheutils.WithKeylessVerifier(sigstore.NewVerifier(logger)),
		),

		Logger:    logger,
		Stdout:    stdout,
		Version:   version,
		UserAgent: "pokkum/" + version,
	}
}

func buildRequestFromConfigAndFlags(ctx context.Context, logger *slog.Logger, flags *buildFlags, projectDir string) (*core.BuildRequest, error) {
	// Load configuration
	cfg, err := config.New(projectDir, logger)
	if err != nil {
		return nil, fmt.Errorf("config loader: %w", err)
	}

	projCfg, err := cfg.Load(projectDir)
	if err != nil && !os.IsNotExist(err) {
		logger.Warn("failed to load .pokkum.yaml", "error", err)
	}

	// Active profile resolution: --profile takes precedence; if unset and --local is set, look for 'local' profile
	activeProfile := strings.TrimSpace(flags.profile)
	if activeProfile != "" && projCfg == nil {
		return nil, fmt.Errorf("profile %q requested but no %s found in project", activeProfile, config.ConfigFilename)
	}
	if activeProfile == "" && flags.local && projCfg != nil {
		if _, ok := projCfg.Profiles["local"]; ok {
			activeProfile = "local"
		}
	}
	if activeProfile != "" && projCfg != nil {
		merged, err := cfg.ApplyProfile(projCfg, activeProfile)
		if err != nil {
			return nil, fmt.Errorf("apply profile %q: %w", activeProfile, err)
		}
		projCfg = merged
	}

	// Build the request from flags, config, and environment
	req := core.BuildRequest{
		ProjectDir: projectDir,
	}

	// Repo: from env, then project config / profile, then error if missing (for push mode)
	repo := os.Getenv("POKKUM_DOCKER_REPO")
	if repo == "" && projCfg != nil {
		repo = projCfg.Docker.Repo
	}
	req.Repo = repo

	// Platforms: explicit CLI flag > project config / profile > default flags
	platformArgs := flags.platforms
	if !flags.platformExplicit && projCfg != nil && len(projCfg.Platforms) > 0 {
		platformArgs = projCfg.Platforms
	}
	platforms, err := core.ParsePlatforms(platformArgs)
	if err != nil {
		return nil, fmt.Errorf("invalid platforms: %w", err)
	}
	req.Platforms = platforms

	// Base image options: explicit flag > hardened > project config / profile > default
	basePreset := flags.base
	if !flags.baseExplicit && basePreset == "" && projCfg != nil && projCfg.Base != "" {
		basePreset = projCfg.Base
	}
	if flags.hardened {
		basePreset = "chainguard"
	}
	if basePreset != "" {
		parsed, err := core.ParseBaseImagePreset(basePreset)
		if err != nil {
			return nil, fmt.Errorf("invalid base image preset: %w", err)
		}
		req.BaseImage.Preset = parsed
	}
	req.BaseImage.UpdateBase = flags.updateBase
	req.BaseImage.Offline = flags.offline

	// Strategy: explicit flag / static > project config / profile > default
	strategy := flags.strategy
	if !flags.strategyExplicit && projCfg != nil && projCfg.Strategy != "" {
		strategy = projCfg.Strategy
	}

	// SBOM format and attachment mode: explicit flag > project config / profile > default
	sbomFmtStr := flags.sbom
	if !flags.sbomExplicit && projCfg != nil && projCfg.SBOM.Format != "" {
		sbomFmtStr = projCfg.SBOM.Format
	}
	if sbomFmtStr != "" {
		sbomFmt, err := core.ParseSBOMFormat(sbomFmtStr)
		if err != nil {
			return nil, fmt.Errorf("invalid sbom format: %w", err)
		}
		req.SBOM.Format = sbomFmt
	}

	sbomAttachStr := flags.sbomAttach
	if !flags.sbomAttachExplicit && projCfg != nil && projCfg.SBOM.Attach != "" {
		sbomAttachStr = projCfg.SBOM.Attach
	}
	if sbomAttachStr != "" {
		sbomAttachMode, err := core.ParseSBOMAttachMode(sbomAttachStr)
		if err != nil {
			return nil, fmt.Errorf("invalid sbom attach mode: %w", err)
		}
		req.SBOM.AttachMode = sbomAttachMode
	}

	// Output mode: explicit flag > active profile output > default push
	if flags.local && flags.tarball != "" {
		return nil, fmt.Errorf("cannot specify both --local and --tarball")
	}
	if flags.local {
		req.Output.Mode = core.OutputLocal
	} else if flags.tarball != "" {
		req.Output.Mode = core.OutputTarball
		req.Output.TarballPath = flags.tarball
	} else if activeProfile != "" && projCfg != nil && projCfg.Profiles[activeProfile].Output != "" {
		outMode, err := core.ParseOutputMode(projCfg.Profiles[activeProfile].Output)
		if err != nil {
			return nil, fmt.Errorf("profile %q invalid output: %w", activeProfile, err)
		}
		req.Output.Mode = outMode
	} else {
		req.Output.Mode = core.OutputPush
	}

	// Reconcile the --static shorthand with an explicit --strategy flag. A
	// static build compiles a purely static site (no Bun runtime, no bundled
	// executable) onto a minimal libc-free base, served by the embedded
	// pokkum-static Go file server.
	if flags.static && flags.strategyExplicit && strategy != "static" {
		return nil, fmt.Errorf("--static cannot be combined with --strategy=%s (layered, static, or nothing must be used)", flags.strategy)
	}
	if flags.static {
		strategy = "static"
		// Unless the user pinned an explicit base (--base/--hardened), default
		// to a fully static, libc-free image: the entire point of --static is a
		// server with no dynamic dependencies. StaticBaseRef (chainguard/static)
		// is signed with Chainguard's identity, not Distroless's — Preset must
		// be BaseImageChainguard so signature verification and the pokkum.lock
		// cache key both agree with the image actually being pulled, and so a
		// plain build's "distroless"-keyed lock entry never collides with a
		// --static build's base in the same project.
		if basePreset == "" {
			req.BaseImage.Preset = core.BaseImageChainguard
			req.BaseImage.Ref = core.StaticBaseRef
		}
	}

	// Compile options
	if strategy == "exe" {
		logger.Warn("packaging strategy 'exe' is deprecated and will be removed in a future release; migrate to '--strategy=layered' (default)")
	}

	sourcemapSetting := flags.sourcemap
	if !flags.sourcemapExplicit {
		if envVal := os.Getenv("POKKUM_SOURCEMAP"); envVal != "" {
			sourcemapSetting = envVal == "1" || strings.EqualFold(envVal, "true") || strings.EqualFold(envVal, "yes")
		} else if activeProfile != "" && projCfg != nil && projCfg.Profiles[activeProfile].Sourcemap != nil {
			sourcemapSetting = *projCfg.Profiles[activeProfile].Sourcemap
		}
	}

	noCacheSetting := flags.noCache
	if !noCacheSetting && projCfg != nil && projCfg.Cache.Enabled != nil && !*projCfg.Cache.Enabled {
		noCacheSetting = true
	}

	req.Compile = core.CompileOptions{
		Strategy:      core.BuildStrategy(strategy),
		Compression:   core.CompressionAlgorithm(flags.compression),
		Sourcemap:     sourcemapSetting,
		NoInject:      flags.noInject || !flags.inject,
		NoPrune:       flags.noPrune,
		KeepVendor:    flags.keepVendor,
		NoPrecompress: flags.noPrecompress,
		NoStrip:       flags.noStrip,
		NoCache:       noCacheSetting,
	}

	// Runtime options
	if len(flags.requireEnv) > 0 {
		var reqEnvs []string
		for _, re := range flags.requireEnv {
			for _, part := range strings.Split(re, ",") {
				if p := strings.TrimSpace(part); p != "" {
					reqEnvs = append(reqEnvs, p)
				}
			}
		}
		req.Runtime.RequireEnv = reqEnvs
	}

	// Telemetry options
	withSidecar := flags.withOtelSidecar
	if !withSidecar && projCfg != nil && projCfg.OTel.Sidecar != nil {
		withSidecar = *projCfg.OTel.Sidecar
	}
	telemetryEnabled := flags.telemetry && !flags.noTelemetry
	if !flags.telemetry && !flags.noTelemetry && projCfg != nil {
		if (projCfg.OTel.Tracing != nil && *projCfg.OTel.Tracing) || (projCfg.OTel.Metrics != nil && *projCfg.OTel.Metrics) || (projCfg.OTel.Sidecar != nil && *projCfg.OTel.Sidecar) {
			telemetryEnabled = true
		}
	}
	metricsOnly := flags.metricsOnly
	if !metricsOnly && projCfg != nil && projCfg.OTel.Tracing != nil && !*projCfg.OTel.Tracing && projCfg.OTel.Metrics != nil && *projCfg.OTel.Metrics {
		metricsOnly = true
	}

	req.Telemetry = core.TelemetryOptions{
		Enabled:         telemetryEnabled,
		TracesEndpoint:  flags.otelExport,
		MetricsEndpoint: flags.otelExport,
		SampleRate:      flags.traceSampleRate,
		MetricsOnly:     metricsOnly,
		Environment:     flags.telemetryEnv,
		WithSidecar:     withSidecar,
	}

	// Signing options
	req.Sign = flags.sign && !flags.noSign

	// Bun runtime options
	stubLauncherSetting := flags.stubLauncher
	if !flags.stubLauncherExplicit {
		if envVal := os.Getenv("POKKUM_STUB_LAUNCHER"); envVal != "" {
			stubLauncherSetting = envVal == "1" || strings.EqualFold(envVal, "true") || strings.EqualFold(envVal, "yes")
		} else if activeProfile != "" && projCfg != nil && projCfg.Profiles[activeProfile].StubLauncher != nil {
			stubLauncherSetting = *projCfg.Profiles[activeProfile].StubLauncher
		} else if projCfg != nil && projCfg.StubLauncher != nil {
			stubLauncherSetting = *projCfg.StubLauncher
		}
	}

	req.BunRuntime = core.BunRuntimeOptions{
		CustomBinaryPath: flags.bunBinary,
		Variant:          core.BunVariant(flags.bunVariant),
		StubLauncher:     stubLauncherSetting,
	}

	// Resolve SOURCE_DATE_EPOCH before label discovery: the "created" label
	// must report exactly the timestamp the rest of the build actually uses
	// (layer mtimes, image config, history entries), not a second,
	// independently-resolved value that could disagree with it.
	timestamp, err := cfg.ResolveBuildTimestamp()
	if err != nil {
		return nil, fmt.Errorf("source date epoch: %w", err)
	}
	req.SourceDateEpoch = timestamp

	// Parse image labels
	if req.Labels == nil {
		req.Labels = make(map[string]string)
	}

	// Apply image metadata & runtime configuration from .pokkum.yaml
	if projCfg != nil {
		for k, v := range projCfg.Image.Labels {
			req.Labels[k] = v
		}
		if len(projCfg.Image.Annotations) > 0 {
			req.Annotations = make(map[string]string)
			for k, v := range projCfg.Image.Annotations {
				req.Annotations[k] = v
			}
		}
		if len(projCfg.Image.Env) > 0 {
			req.Runtime.Env = make(map[string]string)
			for k, v := range projCfg.Image.Env {
				req.Runtime.Env[k] = v
			}
		}
		if len(projCfg.Image.RequireEnv) > 0 {
			req.Runtime.RequireEnv = append(req.Runtime.RequireEnv, projCfg.Image.RequireEnv...)
		}
		if len(projCfg.Image.Ports) > 0 {
			req.Runtime.ExposedPorts = append(req.Runtime.ExposedPorts, projCfg.Image.Ports...)
		}
		if projCfg.Image.Port != 0 {
			req.Runtime.Port = projCfg.Image.Port
		}
		if projCfg.Image.ProbePort != 0 {
			req.Runtime.ProbePort = projCfg.Image.ProbePort
		}
		if projCfg.Image.User != "" {
			req.Runtime.User = projCfg.Image.User
		}
		if projCfg.Image.WorkingDir != "" {
			req.Runtime.WorkingDir = projCfg.Image.WorkingDir
		}
		if projCfg.Image.ShutdownTimeout != "" {
			d, err := time.ParseDuration(projCfg.Image.ShutdownTimeout)
			if err != nil {
				return nil, fmt.Errorf("invalid shutdown_timeout %q: %w", projCfg.Image.ShutdownTimeout, err)
			}
			req.Runtime.ShutdownTimeout = d
		}
	}

	if len(flags.imageLabels) > 0 {
		for _, lbl := range flags.imageLabels {
			k, v, ok := strings.Cut(lbl, "=")
			if !ok {
				return nil, fmt.Errorf("invalid --image-label %q: expected key=value", lbl)
			}
			req.Labels[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	req.Labels = discoverGitMetadata(ctx, projectDir, req.Labels, timestamp)

	if !flags.noVerifyBase && projCfg != nil && projCfg.Security.VerifyBase != nil && !*projCfg.Security.VerifyBase {
		req.BaseImage.NoVerifyBase = true
	} else {
		req.BaseImage.NoVerifyBase = flags.noVerifyBase
	}

	if flags.baseVerifyMode != "" {
		verifyMode, err := core.ParseBaseImageVerifyMode(flags.baseVerifyMode)
		if err != nil {
			return nil, fmt.Errorf("invalid base verify mode: %w", err)
		}
		req.BaseImage.VerifyMode = verifyMode
	}
	if flags.baseKeylessIdentity != "" {
		req.BaseImage.KeylessSAN = flags.baseKeylessIdentity
	}
	if flags.baseKeylessIssuer != "" {
		req.BaseImage.KeylessIssuer = flags.baseKeylessIssuer
	}
	if flags.sigstoreTrustedRoot != "" {
		req.BaseImage.TrustedRootPath = flags.sigstoreTrustedRoot
	}

	req.AllowSecretPatterns = flags.allowSecretPatterns
	if projCfg != nil && len(projCfg.Security.AllowSecretPatterns) > 0 {
		req.AllowSecretPatterns = append(req.AllowSecretPatterns, projCfg.Security.AllowSecretPatterns...)
	}

	req.Hermetic = flags.hermetic
	req.RegistryConfigPath = flags.registryConfig
	req.PushConcurrency = flags.pushConcurrency

	// FailOnCVE: flag > env > profile/config
	failOnCVESetting := flags.failOnCVE
	if failOnCVESetting == "" {
		failOnCVESetting = os.Getenv("POKKUM_FAIL_ON_CVE")
	}
	if failOnCVESetting == "" && projCfg != nil {
		failOnCVESetting = projCfg.Security.FailOnCVE
	}
	if failOnCVESetting != "" {
		if failOnCVESetting == "1" || strings.EqualFold(failOnCVESetting, "true") || strings.EqualFold(failOnCVESetting, "yes") {
			req.FailOnCVE = core.SeverityCritical
		} else {
			sev, err := core.ParseSeverity(failOnCVESetting)
			if err != nil {
				return nil, fmt.Errorf("invalid fail-on-cve severity %q: %w", failOnCVESetting, err)
			}
			req.FailOnCVE = sev
		}
	}

	if !flags.allowIncomplete && projCfg != nil && projCfg.Security.AllowIncompleteScans != nil && *projCfg.Security.AllowIncompleteScans {
		req.AllowIncompleteScan = true
	} else {
		req.AllowIncompleteScan = flags.allowIncomplete
	}

	// Cache verification configuration
	if flags.noCacheVerify {
		req.CacheVerify.VerifyMode = core.CacheVerifyNone
		req.CacheVerify.VerifySignature = false
	} else {
		req.CacheVerify.VerifySignature = true
		verifyModeSetting := flags.cacheVerifyMode
		if verifyModeSetting == "" {
			verifyModeSetting = os.Getenv("POKKUM_CACHE_VERIFY_MODE")
		}
		if verifyModeSetting == "" && projCfg != nil {
			verifyModeSetting = projCfg.Cache.VerifyMode
		}
		if verifyModeSetting != "" {
			mode, err := core.ParseCacheVerifyMode(verifyModeSetting)
			if err != nil {
				return nil, fmt.Errorf("invalid cache verify mode: %w", err)
			}
			req.CacheVerify.VerifyMode = mode
		}
	}

	verifyKeySetting := flags.cacheVerifyKey
	if verifyKeySetting == "" {
		verifyKeySetting = os.Getenv("POKKUM_CACHE_PUBKEY")
	}
	if verifyKeySetting == "" && projCfg != nil {
		verifyKeySetting = projCfg.Cache.Pubkey
	}
	if verifyKeySetting != "" {
		if data, err := os.ReadFile(verifyKeySetting); err == nil {
			req.CacheVerify.PublicKeyPEM = data
		} else {
			req.CacheVerify.PublicKeyPEM = []byte(verifyKeySetting)
		}
	}

	keylessSANSetting := flags.cacheKeylessIdentity
	if keylessSANSetting == "" {
		keylessSANSetting = os.Getenv("POKKUM_CACHE_KEYLESS_IDENTITY")
	}
	if keylessSANSetting == "" && projCfg != nil {
		keylessSANSetting = projCfg.Cache.KeylessIdentity
	}
	if keylessSANSetting != "" {
		req.CacheVerify.KeylessIdentity.SAN = keylessSANSetting
	}

	keylessIssuerSetting := flags.cacheKeylessIssuer
	if keylessIssuerSetting == "" {
		keylessIssuerSetting = os.Getenv("POKKUM_CACHE_KEYLESS_ISSUER")
	}
	if keylessIssuerSetting == "" && projCfg != nil {
		keylessIssuerSetting = projCfg.Cache.KeylessIssuer
	}
	if keylessIssuerSetting != "" {
		req.CacheVerify.KeylessIdentity.Issuer = keylessIssuerSetting
	}

	req.CacheVerify.Strict = flags.cacheVerifyStrict
	if flags.sigstoreTrustedRoot != "" {
		if data, err := os.ReadFile(flags.sigstoreTrustedRoot); err == nil {
			req.CacheVerify.TrustedRootJSON = data
		}
	}

	return &req, nil
}

// buildRequestForPath constructs a push-mode BuildRequest for a SvelteKit
// project referenced by a pokkum:// manifest entry. Unlike runBuild, it has
// no CLI flags to layer on top — resolve and apply expose no per-project
// build flags, only --security-context — so every optional field is left at
// its documented Normalize default: multi-platform, distroless base,
// SPDX-JSON SBOM. The only inputs that vary per reference are the project
// directory (resolved from the pokkum:// path against the manifest's base
// directory) and the destination repository (derived from
// POKKUM_DOCKER_REPO; see deriveRepo in k8s.go).
func buildRequestForPath(projectDir, repo string, logger *slog.Logger) (core.BuildRequest, error) {
	cfg, err := config.New(projectDir, logger)
	if err != nil {
		return core.BuildRequest{}, fmt.Errorf("config loader for %s: %w", projectDir, err)
	}

	req := core.BuildRequest{
		ProjectDir: projectDir,
		Repo:       repo,
	}

	timestamp, err := cfg.ResolveBuildTimestamp()
	if err != nil {
		return core.BuildRequest{}, fmt.Errorf("source date epoch for %s: %w", projectDir, err)
	}
	req.SourceDateEpoch = timestamp

	req.Normalize()
	if err := req.Validate(); err != nil {
		return core.BuildRequest{}, fmt.Errorf("validation failed for %s: %w", projectDir, err)
	}
	return req, nil
}
