package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

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
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sbom"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/scanner"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/secretguard"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/slsa"
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
	bunBinary   string
	bunVariant  string
	strategy    string
	compression string

	imageLabels []string

	noVerifyBase        bool
	baseVerifyMode      string
	baseKeylessIdentity string
	baseKeylessIssuer   string
	sigstoreTrustedRoot string
	allowSecretPatterns []string
	hermetic            bool
	registryConfig      string
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
		RunE: func(_ *cobra.Command, args []string) error {
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
	cmd.Flags().StringVar(&flags.strategy, "strategy", "layered",
		"Packaging strategy: layered (5-layer arch-independent layout [default]) or exe (single executable)")
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

	// Base image options
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
	req.BaseImage.UpdateBase = flags.updateBase
	req.BaseImage.Offline = flags.offline

	// SBOM format and attachment mode
	sbomFmt, err := core.ParseSBOMFormat(flags.sbom)
	if err != nil {
		return fmt.Errorf("invalid sbom format: %w", err)
	}
	req.SBOM.Format = sbomFmt

	sbomAttachMode, err := core.ParseSBOMAttachMode(flags.sbomAttach)
	if err != nil {
		return fmt.Errorf("invalid sbom attach mode: %w", err)
	}
	req.SBOM.AttachMode = sbomAttachMode

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

	// Compile options
	if flags.strategy == "exe" {
		logger.Warn("packaging strategy 'exe' is deprecated and will be removed in a future release; migrate to '--strategy=layered' (default)")
	}
	req.Compile = core.CompileOptions{
		Strategy:    core.BuildStrategy(flags.strategy),
		Compression: core.CompressionAlgorithm(flags.compression),
		NoInject:    flags.noInject || !flags.inject,
	}

	// Telemetry options
	req.Telemetry = core.TelemetryOptions{
		Enabled:         flags.telemetry && !flags.noTelemetry,
		TracesEndpoint:  flags.otelExport,
		MetricsEndpoint: flags.otelExport,
		SampleRate:      flags.traceSampleRate,
		MetricsOnly:     flags.metricsOnly,
		Environment:     flags.telemetryEnv,
		WithSidecar:     flags.withOtelSidecar,
	}

	// Signing options
	req.Sign = flags.sign && !flags.noSign

	// Bun runtime options
	req.BunRuntime = core.BunRuntimeOptions{
		CustomBinaryPath: flags.bunBinary,
		Variant:          core.BunVariant(flags.bunVariant),
	}

	// Resolve SOURCE_DATE_EPOCH before label discovery: the "created" label
	// must report exactly the timestamp the rest of the build actually uses
	// (layer mtimes, image config, history entries), not a second,
	// independently-resolved value that could disagree with it.
	timestamp, err := cfg.ResolveBuildTimestamp()
	if err != nil {
		return fmt.Errorf("source date epoch: %w", err)
	}
	req.SourceDateEpoch = timestamp

	// Parse image labels
	if req.Labels == nil {
		req.Labels = make(map[string]string)
	}
	if len(flags.imageLabels) > 0 {
		for _, lbl := range flags.imageLabels {
			k, v, ok := strings.Cut(lbl, "=")
			if !ok {
				return fmt.Errorf("invalid --image-label %q: expected key=value", lbl)
			}
			req.Labels[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	req.Labels = discoverGitMetadata(ctx, projectDir, req.Labels, timestamp)

	req.BaseImage.NoVerifyBase = flags.noVerifyBase
	if flags.baseVerifyMode != "" {
		verifyMode, err := core.ParseBaseImageVerifyMode(flags.baseVerifyMode)
		if err != nil {
			return fmt.Errorf("invalid base verify mode: %w", err)
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
	req.Hermetic = flags.hermetic
	req.RegistryConfigPath = flags.registryConfig

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
	res, err := core.Build(ctx, buildDeps(logger, os.Stdout), req, opts)
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
		Compiler:   bunexec.NewCompiler(logger),
		BaseImages: baseimage.NewResolver(logger),
		Supervisor: supervisor.New(logger),
		Packager:   packager.NewPackager(logger),
		BunRuntime: bunruntime.NewResolver("", nil),

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

		Logger:    logger,
		Stdout:    stdout,
		Version:   version,
		UserAgent: "pokkum/" + version,
	}
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
