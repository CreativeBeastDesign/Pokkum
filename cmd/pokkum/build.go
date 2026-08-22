package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/assetoverlay"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/baseimage"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunexec"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunruntime"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/config"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/dsse"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/envbake"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/keymaterialutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/nativeinspect"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/provenance"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registry"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/remotecacheutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/routefilter"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sbom"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/scanner"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/secretguard"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sigstore"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/slsa"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/staticserver"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/supervisor"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/vexutils"
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
	toOCILayout   string
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
	sign          bool
	noSign        bool
	signingKey    string
	requireSigned bool

	// Injection flags
	inject   bool
	noInject bool

	// Bun runtime & strategy flags
	bunBinary  string
	bunVariant string
	bunVersion string
	runtime    string
	// runtimeExplicit records whether --runtime was actually passed on the
	// command line (vs. sitting at its "bun" default), so config/profile
	// values can fill in only when the flag was untouched — the same
	// pattern as strategyExplicit below.
	runtimeExplicit bool
	// explicitBunFlags lists which of the embedded-Bun-selection flags
	// (bun-version, bun-variant, bun-binary, stub-launcher) were explicitly
	// passed on the command line — needed to reject their use with an
	// effective --runtime=node (from flag OR config), since silently
	// ignoring an explicitly-passed flag is checklist row 16's exact
	// failure mode, and two of them (--bun-version/--bun-variant) have
	// non-empty normalized defaults core's Validate cannot tell apart from
	// an explicit choice.
	explicitBunFlags []string
	strategy         string
	// strategyExplicit records whether --strategy was actually passed on the
	// command line, as opposed to sitting at its "layered" default — needed
	// to detect a genuine --strategy=layered --static conflict, since
	// "layered" can't be distinguished from "unset" by value alone.
	strategyExplicit bool
	static           bool
	compression      string
	sourcemap        bool

	imageLabels []string

	tags []string

	profile              string
	platformExplicit     bool
	baseExplicit         bool
	sbomExplicit         bool
	sbomAttachExplicit   bool
	sourcemapExplicit    bool
	stubLauncherExplicit bool
	tagsExplicit         bool

	noVerifyBase           bool
	baseVerifyMode         string
	baseKeylessIdentity    string
	baseKeylessIssuer      string
	sigstoreTrustedRoot    string
	allowSecretPatterns    []string
	excludeRoutes          []string
	showSecretValues       bool
	hermetic               bool
	hermeticMountIsolation bool
	registryConfig         string
	pushConcurrency        int
	requireEnv             []string

	// adapter-node reverse-proxy contract flags (see ports.EnvOrigin's doc
	// comment for the full rationale)
	origin         string
	protocolHeader string
	hostHeader     string
	addressHeader  string
	xffDepth       int
	bodySizeLimit  string

	failOnCVE        string
	allowIncomplete  bool
	vexOutput        string
	noPrune          bool
	keepVendor       []string
	noPrecompress    bool
	noStrip          bool
	noCache          bool
	stubLauncher     bool
	assetOverlay     int
	assetOverlayFrom []string

	// Cache verification flags
	noCacheVerify        bool
	cacheVerifyMode      string
	cacheVerifyKey       string
	cacheKeylessIdentity string
	cacheKeylessIssuer   string
	cacheVerifyStrict    bool
	sigstoreTUFRefresh   bool
}

func newBuildCommand(ctx context.Context, logger *slog.Logger) *cobra.Command {
	flags := &buildFlags{}

	cmd := &cobra.Command{
		Use:   "build [dir]",
		Short: "Build a SvelteKit application into a container image",
		Long: `Build compiles a SvelteKit application and assembles it into a reproducible
container image with a hardened base. It handles multi-platform builds, SBOM generation,
and multiple output modes (push to registry, load into Docker daemon, export to a docker-save
tarball, or export to an OCI image layout directory).

The project directory defaults to the current working directory.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			flags.strategyExplicit = cmd.Flags().Changed("strategy")
			flags.runtimeExplicit = cmd.Flags().Changed("runtime")
			for _, bunFlag := range []string{"bun-version", "bun-variant", "bun-binary", "stub-launcher"} {
				if cmd.Flags().Changed(bunFlag) {
					flags.explicitBunFlags = append(flags.explicitBunFlags, bunFlag)
				}
			}
			flags.platformExplicit = cmd.Flags().Changed("platform")
			flags.baseExplicit = cmd.Flags().Changed("base")
			flags.sbomExplicit = cmd.Flags().Changed("sbom")
			flags.sbomAttachExplicit = cmd.Flags().Changed("sbom-attach")
			flags.sourcemapExplicit = cmd.Flags().Changed("sourcemap")
			flags.stubLauncherExplicit = cmd.Flags().Changed("stub-launcher")
			flags.tagsExplicit = cmd.Flags().Changed("tag")
			return runBuild(ctx, logger, flags, args)
		},
	}

	// Flag definitions with defaults from spec
	cmd.Flags().StringVarP(&flags.profile, "profile", "P", "",
		"Activate a named build profile defined in .pokkum.yaml (e.g. --profile local or --profile production)")
	cmd.Flags().StringSliceVarP(&flags.platforms, "platform", "p", []string{"linux/amd64", "linux/arm64"},
		"Target platform(s); repeatable, e.g. --platform linux/amd64 --platform linux/arm64. Use 'all' for all supported platforms")
	cmd.Flags().StringSliceVarP(&flags.tags, "tag", "t", nil,
		"Image tag(s) to apply, without the repository prefix; repeatable or comma-separated, e.g. --tag v1.2.3 --tag latest. Defaults to \"latest\" if unset here, in POKKUM_DOCKER_TAGS, and in docker.tags")
	cmd.Flags().StringVar(&flags.base, "base", "",
		"Base image preset (distroless [default], chainguard, distroless-node) or a full custom image reference (e.g. registry/repo:tag or repo@sha256:...); a custom reference defaults to static-key signature verification and requires POKKUM_BASE_IMAGE_PUBKEY/--base-verify-mode or --no-verify-base")
	cmd.Flags().BoolVar(&flags.hardened, "hardened", false,
		"Select the Chainguard base preset (shorthand for --base chainguard)")
	cmd.Flags().StringVar(&flags.sbom, "sbom", "spdx-json",
		"SBOM format (spdx-json [default], cyclonedx-json, or none)")
	cmd.Flags().StringVar(&flags.sbomAttach, "sbom-attach", "auto",
		"SBOM attachment mode: auto [default] probes OCI 1.1 referrers support and falls back to tag mode if unsupported (ECR, older Harbor/Artifactory), referrer forces OCI 1.1 with no fallback, tag always uses the legacy .sbom tag convention")
	cmd.Flags().BoolVar(&flags.local, "local", false,
		"Load the image into the local Docker daemon instead of pushing to a registry")
	cmd.Flags().StringVar(&flags.tarball, "tarball", "",
		"Export the image as a docker-save archive to the specified path (e.g., image.tar). Loadable with docker load, but the format has no annotations field and cannot hold a multi-platform index — use --to-oci-layout to keep either")
	cmd.Flags().StringVar(&flags.toOCILayout, "to-oci-layout", "",
		"Export the image as an OCI image layout into the specified directory (e.g., ./oci-out). Daemonless: needs no Docker/Podman, and unlike --tarball it preserves every OCI annotation and the full multi-platform index, so it can be imported straight into kind/k3d/minikube or read by crane/skopeo")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false,
		"Resolve everything and report what would be built and pushed, but perform no writes")
	cmd.Flags().BoolVar(&flags.printManifest, "print-manifest", false,
		"Emit the computed OCI manifest/config without pushing")
	cmd.Flags().StringVar(&flags.logLevel, "log-level", "INFO",
		"Log level (DEBUG, INFO, WARN, ERROR)")
	cmd.Flags().StringVar(&flags.logFormat, "log-format", "auto",
		"Log format: auto (human-readable on a terminal, text otherwise), console, text, or json")

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
	cmd.Flags().StringVar(&flags.signingKey, "signing-key", "",
		"Private signing key (ECDSA P-256 or Ed25519): a path to a PEM file, or the PEM text itself. Defaults to POKKUM_SIGNING_KEY. Without a key, a signing-enabled build pushes UNSIGNED with a loud warning")
	cmd.Flags().BoolVar(&flags.requireSigned, "require-signed", false,
		"Fail the build unless the pushed image is signed, attested, and self-verified against the registry (CI gate; requires a signing key and push output)")

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
	cmd.Flags().StringVar(&flags.bunVersion, "bun-version", "",
		"Bun release version to embed (default: the pinned "+core.DefaultBunVersion+"). Any version is checksum-verified against Bun's GPG-signed release manifest before use")
	cmd.Flags().BoolVar(&flags.stubLauncher, "stub-launcher", false,
		"Compile a minimal entrypoint launcher stub instead of embedding stock Bun runtime (layered strategy hardening)")
	cmd.Flags().StringVar(&flags.runtime, "runtime", "bun",
		"Application runtime the built image executes under: bun (default, embedded and checksum-pinned) or node (provided by the base image; defaults the base to the distroless-node preset, keyless-verified like distroless). node supports --strategy=layered only")
	cmd.Flags().StringVar(&flags.strategy, "strategy", "layered",
		"Packaging strategy: layered (multi-layer arch-independent layout [default] — see 'pokkum explain' for a given build's real breakdown), exe (single executable, deprecated), or static (purely static site served by an embedded Go file server, no Bun runtime)")
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
	cmd.Flags().StringSliceVar(&flags.excludeRoutes, "exclude-route", nil,
		"Drop a prerendered route from the image, repeatable (e.g. --exclude-route=/storybook). A bare path covers its subtree; '*' matches within a segment and '**' across segments. Merged with build.exclude_routes in .pokkum.yaml. Filters prerendered files only: a server-rendered route is compiled into the server bundle and is reported as unmatched rather than silently kept")
	cmd.Flags().BoolVar(&flags.showSecretValues, "show-secret-values", false,
		"Reveal the matched text of secret-guard findings instead of redacting it. For local triage of a false positive in minified output; never set this in CI, where it would copy real credentials into build logs")

	cmd.Flags().BoolVar(&flags.hermetic, "hermetic", false,
		"Enforce strict hermetic build mode (zero network egress, cached base images and node_modules required)")
	cmd.Flags().BoolVar(&flags.hermeticMountIsolation, "hermetic-mount-isolation", false,
		"With --hermetic on Linux, additionally block path-based Unix domain socket access (e.g. a bind-mounted /var/run/docker.sock) for the build subprocess. Opt-in: new raw-syscall code, ignored (with a warning) on non-Linux hosts or without --hermetic")
	cmd.Flags().StringVar(&flags.registryConfig, "registry-config", "",
		"Path to custom OCI registry auth config file (config.json)")
	cmd.Flags().IntVar(&flags.pushConcurrency, "push-concurrency", 0,
		"Number of concurrent layer uploads during registry push (0 = registry adapter's default)")
	cmd.Flags().StringSliceVar(&flags.requireEnv, "require-env", nil,
		"Declare required runtime environment variables (comma-separated or repeatable)")
	cmd.Flags().StringVar(&flags.origin, "origin", "",
		"Canonical origin (e.g. https://example.com) written to ORIGIN, adapter-node's reverse-proxy contract. Strongly recommended for any deployment behind an ingress/reverse proxy — without it, form-action POSTs fail with \"403 Cross-site POST form submissions are forbidden\"")
	cmd.Flags().StringVar(&flags.protocolHeader, "protocol-header", "",
		"Proxy header adapter-node trusts for the original request protocol (e.g. x-forwarded-proto), written to PROTOCOL_HEADER")
	cmd.Flags().StringVar(&flags.hostHeader, "host-header", "",
		"Proxy header adapter-node trusts for the original request host (e.g. x-forwarded-host), written to HOST_HEADER")
	cmd.Flags().StringVar(&flags.addressHeader, "address-header", "",
		"Proxy header adapter-node trusts for the real client IP (e.g. x-forwarded-for), written to ADDRESS_HEADER")
	cmd.Flags().IntVar(&flags.xffDepth, "xff-depth", 0,
		"Number of trusted proxy hops when parsing --address-header (0 = adapter-node's own default of 1), written to XFF_DEPTH")
	cmd.Flags().StringVar(&flags.bodySizeLimit, "body-size-limit", "",
		"Request body size cap in adapter-node's size-string format (e.g. 512K, 10M, Infinity; empty = adapter-node's own default of 512K), written to BODY_SIZE_LIMIT")
	cmd.Flags().StringVar(&flags.failOnCVE, "fail-on-cve", "",
		"Fail build if base image vulnerabilities exceed threshold (low, medium, high, critical; default warn-only)")
	cmd.Flags().BoolVar(&flags.allowIncomplete, "allow-incomplete", false,
		"Allow build to succeed even if base image vulnerability database lookups fail (default: fail closed when --fail-on-cve is active)")
	cmd.Flags().StringVar(&flags.vexOutput, "vex-output", "",
		"Write a real OpenVEX document (https://openvex.dev) covering this build's active security.vex_exemptions (.pokkum.yaml) to the given path. No-op if there are no active exemptions.")
	cmd.Flags().BoolVar(&flags.noPrune, "no-prune", false,
		"Disable build-time stripping of non-runtime files (*.d.ts, *.map, tests, docs) from the vendor layer")
	cmd.Flags().StringSliceVar(&flags.keepVendor, "keep-vendor", nil,
		"Custom glob pattern(s) of vendor files to preserve during pruning, repeatable (e.g. --keep-vendor='*.md')")
	cmd.Flags().IntVar(&flags.assetOverlay, "asset-overlay", 0,
		"Rolling-deploy asset overlay: merge up to <n> prior generations' immutable client assets into a new layer, so browsers holding stale HTML can still fetch old hashed chunks during a rolling update. 0 (default) disables the feature. Auto-discovers predecessors via the pushed image's own lineage annotation (registry pushes only, i.e. not --local/--tarball/--to-oci-layout); use --asset-overlay-from to override")
	cmd.Flags().StringSliceVar(&flags.assetOverlayFrom, "asset-overlay-from", nil,
		"Explicit image ref(s) to pull --asset-overlay content from instead of auto-discovering the predecessor chain, repeatable. Truncated to --asset-overlay's <n> if longer. Ignored unless --asset-overlay is also set")
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
	cmd.Flags().BoolVar(&flags.sigstoreTUFRefresh, "sigstore-tuf-refresh", false,
		"Opt-in: refresh the Sigstore trust root from the live TUF repository for cache-signature verification, falling back to the snapshot embedded in this binary (with a warning) if the refresh fails. Ignored when --sigstore-trusted-root is set, which always wins. Bound to --hermetic: a hermetic build never attempts this network fetch and uses the embedded snapshot instead, so hermetic builds cannot reach the network even with this flag set")

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

	if flags.vexOutput != "" {
		if err := writeVEXDocument(flags.vexOutput, req.VEXExemptions, res.Image.Ref); err != nil {
			return fmt.Errorf("write vex document: %w", err)
		}
	}

	logger.Info("build finished",
		"ref", res.Image.Ref,
		"digest", res.Image.Digest.String(),
		"platforms", core.PlatformList(res.Image.Platforms),
		"base", res.BaseImage.PinnedRef,
		"duration", res.Duration.String())
	return nil
}

// writeVEXDocument writes a real OpenVEX document covering exemptions to
// path, or does nothing (not even creating an empty file) if there are no
// active exemptions — a --vex-output flag on a build with no
// security.vex_exemptions configured is a no-op, not an empty/misleading
// document.
func writeVEXDocument(path string, exemptions []core.VEXExemption, imageRef string) error {
	if len(exemptions) == 0 {
		return nil
	}
	now := time.Now()
	id := "https://pokkum.dev/vex/" + strings.ReplaceAll(imageRef, "/", "-")
	doc := vexutils.BuildDocument(exemptions, now, id, imageRef)

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal openvex document: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
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
		BaseImages:   newBaseImageResolver(logger),
		Supervisor:   supervisor.New(logger),
		StaticServer: staticserver.New(logger),
		Packager:     packager.NewPackager(logger),
		BunRuntime:   bunruntime.NewResolver("", nil),

		Registry:   reg,
		Daemon:     reg,
		Tarballs:   reg,
		OCILayouts: reg,

		SBOM:            sbom.NewGenerator(logger),
		NativeInspector: nativeinspect.NewClosuredAdapter(),
		SLSAGenerator:   slsa.NewGenerator(logger),
		CosignSigner:    cosign.NewSigner(logger),
		DSSESigner:      dsse.NewSigner(logger),
		Scanner:         scanner.NewAdapter(logger),
		SecretGuard:     secretguard.NewAdapter(),
		EnvBakeDetector: envbake.NewAdapter(),
		RouteFilter:     routefilter.NewAdapter(),
		RemoteCache: remotecacheutils.New(
			remotecacheutils.WithLogger(logger),
			remotecacheutils.WithCosignSigner(cosign.NewSigner(logger)),
			remotecacheutils.WithKeylessVerifier(sigstore.NewVerifier(logger)),
		),
		AssetOverlay: assetoverlay.NewResolver(),

		Logger:    logger,
		Stdout:    stdout,
		Version:   version,
		UserAgent: "pokkum/" + version,
	}
}

// newBaseImageResolver builds a fully-wired base-image resolver.
//
// It exists so there is exactly ONE place in the program that knows which
// concrete verifiers the base-image resolver needs. baseimage.NewResolver has
// no defaults on purpose — it used to construct cosign.NewSigner /
// sigstore.NewVerifier itself, which meant an adapter importing two of its
// peers — and an un-injected verifier makes the matching --base-verify-mode
// fail closed rather than silently skip verification. Every command that
// resolves base images therefore goes through here instead of calling
// baseimage.NewResolver directly, so adding a command cannot quietly produce a
// resolver that refuses to verify.
func newBaseImageResolver(logger *slog.Logger) *baseimage.Resolver {
	return baseimage.NewResolver(logger,
		baseimage.WithCosignSigner(cosign.NewSigner(logger)),
		baseimage.WithKeylessVerifier(sigstore.NewVerifier(logger)),
	)
}

// newProvenanceResolver builds a fully-wired provenance resolver, for the same
// reason and with the same discipline as newBaseImageResolver: provenance's
// three verifier dependencies have no defaults, and a missing one is refused
// with provenance.ErrVerifierNotInjected rather than reported as an unsigned or
// unverified image.
func newProvenanceResolver(logger *slog.Logger) *provenance.Resolver {
	return provenance.NewResolver(logger,
		provenance.WithCosignSigner(cosign.NewSigner(logger)),
		provenance.WithKeylessVerifier(sigstore.NewVerifier(logger)),
		provenance.WithDSSESigner(dsse.NewSigner(logger)),
	)
}

// sigstoreTUFOptionsFactory constructs the base TUFOptions for an opt-in
// --sigstore-tuf-refresh, before Offline is bound to --hermetic (build.go) or
// hardcoded false (verify.go, which has no hermetic mode of its own). A
// package-level var, mirroring exitFunc's pattern in verify.go, so a test can
// point it at a local request-counting server instead of the real Sigstore
// TUF CDN and prove that a hermetic build makes zero network attempts even
// with the refresh flag set -- something a struct-field assertion alone
// cannot distinguish from "attempted and silently fell back". The network
// safety guarantee itself (Offline refuses before any TUF client is even
// constructed) lives in and is already tested by internal/adapters/sigstore;
// this seam only proves this package's own wiring reaches it correctly.
var sigstoreTUFOptionsFactory = sigstore.DefaultTUFOptions

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

	// Tags: explicit CLI flag > POKKUM_DOCKER_TAGS env > project config /
	// profile > default (core.DefaultTag "latest", applied by
	// BuildRequest.Normalize). projCfg has already had any active profile's
	// docker.tags merged in above (cfg.ApplyProfile), so "profile" and
	// "project config" collapse into the single projCfg.Docker.Tags read
	// here, mirroring how Repo's own profile/project-config precedence works.
	var tagArgs []string
	switch {
	case flags.tagsExplicit:
		tagArgs = flags.tags
	case strings.TrimSpace(os.Getenv("POKKUM_DOCKER_TAGS")) != "":
		tagArgs = splitCommaSeparated(os.Getenv("POKKUM_DOCKER_TAGS"))
	case projCfg != nil && len(projCfg.Docker.Tags) > 0:
		tagArgs = projCfg.Docker.Tags
	}
	req.Tags = tagArgs

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
		preset, ref, err := core.ParseBaseImageSpec(basePreset)
		if err != nil {
			return nil, fmt.Errorf("invalid base image: %w", err)
		}
		req.BaseImage.Preset = preset
		if ref != "" {
			req.BaseImage.Ref = ref
		}
	}
	req.BaseImage.UpdateBase = flags.updateBase
	req.BaseImage.Offline = flags.offline

	// Strategy: explicit flag / static > project config / profile > default
	strategy := flags.strategy
	if !flags.strategyExplicit && projCfg != nil && projCfg.Strategy != "" {
		strategy = projCfg.Strategy
	}

	// Runtime: explicit flag > project config / profile > default (bun, via
	// Normalize). projCfg has already had any active profile's runtime
	// merged in above (cfg.ApplyProfile), same as Strategy.
	runtimeSetting := flags.runtime
	if !flags.runtimeExplicit && projCfg != nil && projCfg.Runtime != "" {
		runtimeSetting = projCfg.Runtime
	}
	appRuntime, err := core.ParseAppRuntime(runtimeSetting)
	if err != nil {
		return nil, fmt.Errorf("invalid runtime: %w", err)
	}
	req.AppRuntime = appRuntime
	if appRuntime == core.RuntimeNode && len(flags.explicitBunFlags) > 0 {
		return nil, fmt.Errorf("--%s configures the embedded Bun runtime, which a --runtime=node image does not contain", flags.explicitBunFlags[0])
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

	// Output mode: explicit flag > active profile output > default push.
	// The three destination flags are mutually exclusive — each names a
	// different place the finished image goes, and silently preferring one
	// would build something the user did not ask for.
	switch {
	case flags.local && flags.tarball != "":
		return nil, fmt.Errorf("cannot specify both --local and --tarball")
	case flags.local && flags.toOCILayout != "":
		return nil, fmt.Errorf("cannot specify both --local and --to-oci-layout")
	case flags.tarball != "" && flags.toOCILayout != "":
		return nil, fmt.Errorf("cannot specify both --tarball and --to-oci-layout")
	}
	if flags.local {
		req.Output.Mode = core.OutputLocal
	} else if flags.tarball != "" {
		req.Output.Mode = core.OutputTarball
		req.Output.TarballPath = flags.tarball
	} else if flags.toOCILayout != "" {
		req.Output.Mode = core.OutputOCILayout
		req.Output.OCILayoutPath = flags.toOCILayout
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
	}

	// Unless the user pinned an explicit base (--base/--hardened), a static
	// build gets a fully static, libc-free image: the whole point of the
	// strategy is a server with no dynamic dependencies, and pokkum-static is
	// built CGO_ENABLED=0 precisely so it needs none.
	//
	// This is keyed on the resolved STRATEGY, not on the --static flag. It used
	// to be inside the `if flags.static` branch above, so `--strategy=static`
	// — the documented spelling, and the one the strategy flag exists for —
	// silently fell through to the distroless/cc default and shipped libssl,
	// libstdc++ and libgomp that nothing in the image can even load. Measured on
	// a real project: 44.3MB, of which roughly 34MB was base the server never
	// touches, for a feature whose entire pitch is not shipping a runtime.
	if strategy == "static" && basePreset == "" {
		// Chainguard preset, not distroless: StaticBaseRef is chainguard/static,
		// and the preset is the pokkum.lock key. Keying it "distroless" makes a
		// project that already has a distroless entry resolve back to
		// cc-debian12 — verified by doing it.
		req.BaseImage.Preset = core.BaseImageChainguard
		req.BaseImage.Ref = core.StaticBaseRef
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

		AssetOverlayGenerations: flags.assetOverlay,
		AssetOverlayFrom:        flags.assetOverlayFrom,
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

	// Signing options. The key is resolved here, at the composition root,
	// and never logged: --signing-key wins over POKKUM_SIGNING_KEY; either
	// may hold a file path or the PEM text itself (matching
	// --cache-verify-key's convention). The public half is derived from the
	// private key so the pipeline's post-push self-verification always
	// checks against exactly the key that signed.
	req.Sign = flags.sign && !flags.noSign
	keyPEM, err := resolveSigningKey(flags.signingKey)
	if err != nil {
		return nil, err
	}
	if len(keyPEM) > 0 {
		pubPEM, err := deriveSigningPublicKey(keyPEM)
		if err != nil {
			return nil, fmt.Errorf("signing key: %w", err)
		}
		req.Signing.KeyPEM = keyPEM
		req.Signing.PublicKeyPEM = pubPEM
	}
	req.Signing.Require = flags.requireSigned

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
		Version:          flags.bunVersion,
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
		if projCfg.Image.Origin != "" {
			req.Runtime.Origin = projCfg.Image.Origin
		}
		if projCfg.Image.ProtocolHeader != "" {
			req.Runtime.ProtocolHeader = projCfg.Image.ProtocolHeader
		}
		if projCfg.Image.HostHeader != "" {
			req.Runtime.HostHeader = projCfg.Image.HostHeader
		}
		if projCfg.Image.AddressHeader != "" {
			req.Runtime.AddressHeader = projCfg.Image.AddressHeader
		}
		if projCfg.Image.XFFDepth != 0 {
			req.Runtime.XFFDepth = projCfg.Image.XFFDepth
		}
		if projCfg.Image.BodySizeLimit != "" {
			req.Runtime.BodySizeLimit = projCfg.Image.BodySizeLimit
		}
	}

	// CLI flags win over .pokkum.yaml, matching --image-label below and the
	// general flag-beats-config precedence used throughout this command.
	if flags.origin != "" {
		req.Runtime.Origin = flags.origin
	}
	if flags.protocolHeader != "" {
		req.Runtime.ProtocolHeader = flags.protocolHeader
	}
	if flags.hostHeader != "" {
		req.Runtime.HostHeader = flags.hostHeader
	}
	if flags.addressHeader != "" {
		req.Runtime.AddressHeader = flags.addressHeader
	}
	if flags.xffDepth != 0 {
		req.Runtime.XFFDepth = flags.xffDepth
	}
	if flags.bodySizeLimit != "" {
		req.Runtime.BodySizeLimit = flags.bodySizeLimit
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
	// --sigstore-trusted-root names a file, but every Sigstore trust-root
	// consumer inside Pokkum takes raw JSON bytes
	// (ports.BaseImageRequest.TrustedRootJSON, core.CacheVerifyOptions'
	// TrustedRootJSON, and sigstore.Verifier's own trustedRootJSON). Reading it
	// exactly once here, in the composition root, keeps the adapters off the
	// filesystem and guarantees both consumers verify against the same bytes.
	//
	// Fail closed on an unreadable file: silently degrading to the embedded
	// snapshot would verify against a different trust root than the operator
	// explicitly asked for, which is precisely the substitution they passed the
	// flag to avoid (`pokkum verify` has always handled this flag that way).
	// core.ErrBaseSignatureInvalid is carried over from when the base-image
	// resolver performed this read itself, so `errors.Is` on that sentinel
	// still matches.
	var explicitTrustedRootJSON []byte
	if flags.sigstoreTrustedRoot != "" {
		data, rerr := os.ReadFile(flags.sigstoreTrustedRoot)
		if rerr != nil {
			return nil, fmt.Errorf("read --sigstore-trusted-root %s: %w: %w", flags.sigstoreTrustedRoot, rerr, core.ErrBaseSignatureInvalid)
		}
		explicitTrustedRootJSON = data
		req.BaseImage.TrustedRootJSON = data
	}

	req.AllowSecretPatterns = flags.allowSecretPatterns
	req.ShowSecretValues = flags.showSecretValues
	if projCfg != nil && len(projCfg.Security.AllowSecretPatterns) > 0 {
		req.AllowSecretPatterns = append(req.AllowSecretPatterns, projCfg.Security.AllowSecretPatterns...)
	}

	req.ExcludeRoutes = flags.excludeRoutes
	if projCfg != nil && len(projCfg.Build.ExcludeRoutes) > 0 {
		req.ExcludeRoutes = append(req.ExcludeRoutes, projCfg.Build.ExcludeRoutes...)
	}

	req.Hermetic = flags.hermetic
	req.HermeticMountIsolation = flags.hermeticMountIsolation
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

	if projCfg != nil && len(projCfg.Security.VEXExemptions) > 0 {
		exemptions, err := core.ParseVEXExemptions(projCfg.Security.VEXExemptions, time.Now())
		if err != nil {
			return nil, fmt.Errorf(".pokkum.yaml security.vex_exemptions: %w", err)
		}
		req.VEXExemptions = exemptions
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
		// Resolved through the shared helper so --cache-verify-key,
		// POKKUM_CACHE_PUBKEY and .pokkum.yaml's cache.pubkey mean exactly
		// what the same values mean inside the adapters. This used to be a
		// local "try ReadFile, else treat the string as PEM", which turned a
		// mistyped path into nonsense key bytes and surfaced it as "signature
		// verification failed" rather than as a missing file.
		data, err := keymaterialutils.Resolve(verifyKeySetting, "--cache-verify-key/POKKUM_CACHE_PUBKEY")
		if err != nil {
			return nil, err
		}
		req.CacheVerify.PublicKeyPEM = data
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
		// Same bytes the base-image resolver gets, read once above. This used
		// to re-read the file and swallow the error (`if err == nil`), silently
		// falling back to the embedded snapshot for cache verification while
		// the operator believed their explicit trust root was in force.
		req.CacheVerify.TrustedRootJSON = explicitTrustedRootJSON
	} else if flags.sigstoreTUFRefresh {
		// Only reached when --sigstore-trusted-root was not given, so the
		// explicit file always wins over the refresh flag. Offline is bound
		// to the same value as --hermetic (req.Hermetic, set just above from
		// flags.hermetic): a hermetic build must never reach the network for
		// this either, so FetchTrustedRootJSON refuses before constructing a
		// TUF client and ResolveTrustedRootJSON goes straight to the
		// embedded snapshot. A live TUF fetch failure never fails the
		// build; it only warns and falls back to the embedded snapshot.
		tufOpts := sigstoreTUFOptionsFactory()
		tufOpts.Offline = flags.hermetic
		data, origin, err := sigstore.ResolveTrustedRootJSON(ctx, logger, tufOpts, time.Now())
		if err != nil {
			// Only fails if the embedded snapshot itself cannot be assessed,
			// meaning this binary was built wrong -- fail closed rather than
			// build against material that could not be characterized.
			return nil, fmt.Errorf("resolve sigstore trust root for cache verification: %w", err)
		}
		req.CacheVerify.TrustedRootJSON = data
		logger.Info("resolved Sigstore trust root for cache-signature verification", "origin", string(origin))
	}

	return &req, nil
}

// splitCommaSeparated splits a comma-separated environment variable value
// into trimmed, non-empty tokens, mirroring pflag's StringSlice
// comma-splitting behavior for repeatable CLI flags (e.g. --tag, --platform)
// so POKKUM_DOCKER_TAGS="v1.2.3, latest" behaves the same as
// --tag v1.2.3 --tag latest.
func splitCommaSeparated(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// resolveSigningKey resolves the private signing key: --signing-key wins,
// then POKKUM_SIGNING_KEY. The value may be a filesystem path or the PEM
// text itself. Returns nil (no error) when no key is configured — that is a
// legal state the pipeline warns about, not a CLI error. Error messages
// never echo the value, since it may be key material.
func resolveSigningKey(flagValue string) ([]byte, error) {
	v := strings.TrimSpace(flagValue)
	if v == "" {
		v = strings.TrimSpace(os.Getenv("POKKUM_SIGNING_KEY"))
	}
	if v == "" {
		return nil, nil
	}
	if strings.Contains(v, "-----BEGIN") {
		return []byte(v), nil
	}
	data, err := os.ReadFile(v)
	if err != nil {
		return nil, fmt.Errorf("signing key (--signing-key/POKKUM_SIGNING_KEY) is neither PEM text nor a readable file: %w", err)
	}
	return data, nil
}

// deriveSigningPublicKey derives the PEM-encoded public half of a private
// signing key. Only the key types the cosign/dsse signers can actually sign
// with are accepted (ECDSA and Ed25519) — rejecting anything else here means
// an unusable key fails at flag time, not after a two-minute build.
func deriveSigningPublicKey(privPEM []byte) ([]byte, error) {
	block, _ := pem.Decode(privPEM)
	if block == nil {
		return nil, errors.New("no PEM block found in signing key")
	}

	var priv any
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		priv = k
	} else if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		priv = k
	} else {
		return nil, errors.New("unsupported signing key format (PKCS#8 or SEC1; ECDSA P-256 or Ed25519)")
	}

	var pub crypto.PublicKey
	switch k := priv.(type) {
	case *ecdsa.PrivateKey:
		pub = k.Public()
	case ed25519.PrivateKey:
		pub = k.Public()
	default:
		return nil, fmt.Errorf("unsupported signing key type %T (ECDSA P-256 or Ed25519 required)", priv)
	}

	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, fmt.Errorf("marshal signing public key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
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
