// Package ports defines the driven-side contract of Pokkum: the interfaces that
// the application core calls out through, together with every value type that
// crosses that boundary.
//
// # Type-placement convention
//
// ports is the LEAF package of the internal tree. It imports the standard
// library and github.com/google/go-containerregistry/pkg/v1 and nothing else —
// in particular it never imports internal/core or any adapter. internal/core
// imports ports; adapters import ports (and internal/core for the sentinel
// errors). The dependency graph is therefore acyclic in exactly one direction:
//
//	cmd ──▶ core ──▶ ports ◀── adapters
//	         └────────────────────┘  (adapters also import core for errors)
//
// The consequence is that shared boundary vocabulary (Platform, Artifact,
// SBOMFormat, BaseImagePreset, the per-port request/response structs and the
// runtime contract constants) is declared HERE, not in core. core re-exports the
// four shared value types as type aliases so that core-facing code may spell
// them core.Platform, core.Artifact, and so on; an alias is the same type, so
// there is never a conversion and never an ambiguity about which one to use.
//
// The split of responsibility is:
//
//   - ports holds pure vocabulary and behaviour that cannot fail: String, ToV1,
//     BunTarget, Valid. It declares no sentinel errors.
//   - core holds policy: parsing (ParsePlatform, ParseSBOMFormat, ...),
//     validation (BuildRequest.Validate) and every sentinel error.
//
// This inverts the naive "structs in core" arrangement, and deliberately so: the
// application service core.Build is the sole consumer of these interfaces, so
// core must be able to import ports. Had ports imported core for its request
// structs, core could not have imported ports back and the pipeline would have
// been forced to redeclare all seven interfaces locally. One import direction,
// one declaration of every type.
//
// # Contract rules that apply to every interface in this package
//
//   - Every method takes context.Context as its first parameter and must honour
//     cancellation. Long-running work (subprocesses, network I/O) must abort and
//     return a wrapped ctx.Err() when the context is done.
//   - Implementations must be safe for concurrent use by multiple goroutines
//     unless the interface's doc comment says otherwise. core fans out across
//     platforms and will call the same Compiler/Packager value in parallel.
//   - Implementations must be deterministic: given identical requests, including
//     an identical SourceDateEpoch, they must produce byte-identical output.
//     Never read the wall clock, a random source, the environment or the working
//     directory inside an adapter; take it from the request.
//   - Errors must wrap a sentinel from internal/core with %w. See core/errors.go
//     for the wrapping convention.
//   - A returned pointer or slice belongs to the caller; implementations must
//     not retain or mutate it after returning.
package ports

import (
	"context"
	"runtime"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// Platform identifies a build target. It is the fan-out key of the whole
// pipeline: one Platform yields one compiled Artifact, one supervisor binary,
// one base image and one entry in the final image index.
//
// Platform is comparable and is intended to be used as a map key
// (map[Platform]v1.Image is the canonical per-platform collection type).
//
// The zero Platform is invalid; use ParsePlatform in internal/core to build one
// from user input, or one of the LinuxAMD64 / LinuxARM64 package variables.
type Platform struct {
	// OS is the GOOS-style operating system name. Only "linux" is supported in
	// v0.1 — the produced image runs a distroless container.
	OS string

	// Arch is the GOARCH-style architecture name: "amd64" or "arm64".
	Arch string

	// Variant is the optional OCI platform variant ("v8" for arm64, "v7" for
	// arm). Empty for every platform supported in v0.1. It is carried so that
	// the type does not have to change when 32-bit arm is added; adapters that
	// do not understand a non-empty Variant must reject the platform rather
	// than silently ignore it.
	Variant string
}

// Supported build targets for v0.1. Bun publishes no other Linux compile
// targets that are useful here, and the distroless/Chainguard bases are only
// published for these two architectures.
var (
	LinuxAMD64 = Platform{OS: "linux", Arch: "amd64"}
	LinuxARM64 = Platform{OS: "linux", Arch: "arm64"}
)

// SupportedPlatforms is the default platform set: every target Pokkum v0.1 can
// build. A BuildRequest with no explicit platforms builds all of these.
//
// The slice is package-level mutable state only in the sense that Go cannot
// express an immutable slice; callers must not modify it. Use
// slices.Clone(SupportedPlatforms) if you need to sort or append.
var SupportedPlatforms = []Platform{LinuxAMD64, LinuxARM64}

// LocalPlatform returns the host architecture mapped to Linux (e.g. LinuxARM64 on ARM, LinuxAMD64 on x86_64).
func LocalPlatform() Platform {
	if runtime.GOARCH == "arm64" {
		return LinuxARM64
	}
	return LinuxAMD64
}

// String renders the platform in OCI notation: "linux/amd64", or
// "linux/arm/v7" when a variant is set. It is the canonical human- and
// config-facing spelling and round-trips through core.ParsePlatform.
func (p Platform) String() string {
	if p.OS == "" && p.Arch == "" {
		return ""
	}
	s := p.OS + "/" + p.Arch
	if p.Variant != "" {
		s += "/" + p.Variant
	}
	return s
}

// IsZero reports whether p is the zero value, i.e. carries no target at all.
func (p Platform) IsZero() bool { return p == Platform{} }

// Supported reports whether p is one of SupportedPlatforms. Adapters should
// check this at the top of any per-platform method and return a wrapped
// core.ErrUnsupportedPlatform when it is false, rather than attempting a build
// that will fail obscurely later.
func (p Platform) Supported() bool {
	for _, s := range SupportedPlatforms {
		if p == s {
			return true
		}
	}
	return false
}

// ToV1 converts to the go-containerregistry platform descriptor used in image
// indexes and config files. It never returns nil.
func (p Platform) ToV1() *v1.Platform {
	return &v1.Platform{OS: p.OS, Architecture: p.Arch, Variant: p.Variant}
}

// PlatformFromV1 converts a go-containerregistry platform descriptor back into
// a Platform. It reports false for a nil descriptor or one missing an OS or
// architecture; it does NOT check that the result is Supported, since callers
// commonly need to enumerate the platforms present in a base image index before
// filtering them.
//
// OSVersion, OSFeatures and Features are intentionally dropped: they are
// Windows-image concepts with no meaning for a Linux distroless target, and
// preserving them would make Platform non-comparable.
func PlatformFromV1(p *v1.Platform) (Platform, bool) {
	if p == nil || p.OS == "" || p.Architecture == "" {
		return Platform{}, false
	}
	return Platform{OS: p.OS, Arch: p.Architecture, Variant: p.Variant}, true
}

// BunTarget returns the value for `bun build --compile --target=…` for this
// platform, and reports whether a mapping exists. Bun spells the architectures
// differently from Go, which is the entire reason this method exists:
//
//	linux/amd64 → bun-linux-x64
//	linux/arm64 → bun-linux-arm64
//
// Bun also publishes -musl and -baseline suffixed targets. Pokkum deliberately
// uses neither: the base image is a glibc distroless variant, and the baseline
// targets trade performance for pre-2013 CPU support that a container host does
// not need.
func (p Platform) BunTarget() (string, bool) {
	if p.OS != "linux" || p.Variant != "" {
		return "", false
	}
	switch p.Arch {
	case "amd64":
		return "bun-linux-x64", true
	case "arm64":
		return "bun-linux-arm64", true
	default:
		return "", false
	}
}

// GoEnv returns the GOOS and GOARCH values used to cross-compile the
// pokkum-init supervisor for this platform, and reports whether a mapping
// exists. Go's naming matches Platform's, so this is close to the identity
// function; it exists so that the supervisor builder never has to reach into
// Platform's fields directly and so that a future non-identity mapping has one
// place to live.
func (p Platform) GoEnv() (goos, goarch string, ok bool) {
	if !p.Supported() {
		return "", "", false
	}
	return p.OS, p.Arch, true
}

// Artifact is the output of compiling the SvelteKit app for one platform.
//
// It is a SINGLE FILE, not a directory tree. The @jesterkit/exe-sveltekit
// adapter is run with embedStatic enabled, so the client bundle, prerendered
// pages and every other static asset are baked into the executable as embedded
// files. The resulting container therefore has exactly one application layer
// containing exactly one path. Downstream packages must not assume there is a
// sibling static/ directory to copy — there is not.
type Artifact struct {
	// Platform is the target this binary was compiled for. Required.
	Platform Platform

	// Path is the absolute host path of the compiled executable. Required.
	// The file is owned by the caller of Compile, which chose the location via
	// CompileRequest.OutputPath; the Compiler must not delete it.
	Path string

	// Size is the size of the executable in bytes. Informational: used for
	// progress reporting and for the build summary. Zero means "not measured".
	Size int64

	// SHA256 is the lowercase hex digest of the executable's contents, with no
	// "sha256:" prefix. It is what makes the build verifiable: two runs with the
	// same SOURCE_DATE_EPOCH and the same sources must produce the same value.
	//
	// This is deliberately a plain string and not a v1.Hash: the compiled binary
	// is not an OCI object at this stage, and the exception in the architecture
	// rules that admits v1 types covers the packager, the base-image resolver
	// and the registry only.
	//
	// Empty means the Compiler did not compute it; core may then compute it
	// itself, but a Compiler that can cheaply produce it should.
	SHA256 string
}

// PreflightRequest asks the Compiler to verify that the host toolchain and the
// project can actually be built, before any expensive work starts.
type PreflightRequest struct {
	// ProjectDir is the absolute path of the SvelteKit project root — the
	// directory holding package.json and svelte.config.js. Required.
	ProjectDir string

	// MinBunVersion is the lowest acceptable Bun version, as a bare semver
	// string ("1.2.0", no leading "v"). Empty means the adapter's own built-in
	// minimum applies. Downstream: prefer leaving this empty so that the
	// requirement lives in one place.
	MinBunVersion string

	// Env is additional environment for any probe subprocess, as KEY=VALUE
	// entries appended to the inherited environment. Nil means inherit only.
	Env []string

	// Hermetic enforces zero-network egress and requires all dependencies to be cached locally.
	Hermetic bool
}

// PreflightResult reports what the Compiler found. Every field is
// informational — it exists for the build summary, for image labels and for
// error messages — and a successful Preflight is the only contract that matters.
type PreflightResult struct {
	// BunPath is the absolute path of the bun executable that will be used.
	BunPath string

	// BunVersion is the detected Bun version, without a leading "v".
	BunVersion string

	// AdapterVersion is the resolved version of @jesterkit/exe-sveltekit found
	// in the project. Empty if it could not be determined (which is not by
	// itself an error — the adapter may be linked or vendored).
	AdapterVersion string

	// SvelteKitVersion is the detected @sveltejs/kit version, or empty.
	SvelteKitVersion string
}

// BuildStrategy specifies the packaging strategy for container image assembly.
type BuildStrategy string

const (
	// StrategyLayered builds a multi-layer arch-independent image layout with
	// Bun runtime; the exact layer set and count vary by build (see "pokkum
	// explain").
	StrategyLayered BuildStrategy = "layered"

	// StrategyExe builds a single compiled executable per architecture (legacy).
	StrategyExe BuildStrategy = "exe"

	// StrategyStatic builds a purely static site onto a minimal static file
	// server image (pokkum-static) with no Bun runtime, JS processing or
	// supervisor orchestration layer. The SvelteKit project is prerendered
	// during the build, but the running image only serves /app/client and
	// /app/prerendered files.
	StrategyStatic BuildStrategy = "static"

	// DefaultBuildStrategy is the default strategy for Pokkum builds.
	DefaultBuildStrategy = StrategyLayered
)

// Valid reports whether s is a known strategy. The zero value ("") is NOT
// valid; core normalises it to DefaultBuildStrategy before validating.
func (s BuildStrategy) Valid() bool {
	switch s {
	case StrategyLayered, StrategyExe, StrategyStatic:
		return true
	default:
		return false
	}
}

// ApplyStatic reports whether the strategy produces a purely static image with
// no Bun runtime and no supervisor layer.
func (s BuildStrategy) ApplyStatic() bool {
	return s == StrategyStatic
}

// PrepareRequest drives stage one of the two-stage compile: running the
// SvelteKit build so that the adapter emits entrypoint files.
type PrepareRequest struct {
	// Strategy selects the build packaging strategy (layered vs exe).
	Strategy BuildStrategy

	// ProjectDir is the absolute SvelteKit project root. Required.
	ProjectDir string

	// SourceDateEpoch is the deterministic build timestamp. Required (must be
	// non-zero). It is exported into the child process environment as
	// SOURCE_DATE_EPOCH so that any tooling that stamps timestamps observes it.
	// Adapters must never call time.Now.
	SourceDateEpoch time.Time

	// Env is additional environment for the build subprocess, as KEY=VALUE
	// entries appended to the inherited environment. Nil means inherit only.
	Env []string

	// Hermetic enforces zero-network egress during compile.
	Hermetic bool

	// HermeticMountIsolation additionally blocks path-based Unix domain
	// socket access (e.g. /var/run/docker.sock, if bind-mounted into
	// Pokkum's own runtime) for the build subprocess. Opt-in and additive —
	// ignored unless Hermetic is also true — since it relies on genuinely
	// new, previously-unexercised raw-syscall code (a /proc/self/exe
	// re-exec helper masking specific known paths via a bind mount inside a
	// fresh CLONE_NEWNS mount namespace) unlike Hermetic's own
	// CLONE_NEWNET-based network isolation, which has shipped as the
	// default --hermetic behavior since 2026-08-17 with no incident. Linux
	// only (see hermeticSandboxSupported); a Warn is logged and this is
	// silently ignored on other platforms, matching Hermetic's own
	// advisory-only fallback there. See internal/adapters/bunexec's
	// hermetic_reexec_linux.go and Roadmap.md's residual-hermetic-gap item.
	HermeticMountIsolation bool

	// Platforms is the set of targets the subsequent Compile calls will use. It
	// is passed so the adapter can fail fast on an unsupported target before
	// spending a full SvelteKit build. May be nil, meaning SupportedPlatforms.
	Platforms []Platform

	// Telemetry contains options for OpenTelemetry auto-instrumentation injection.
	Telemetry TelemetryOptions

	// NoInject suppresses the zero-config auto-injection for svelte.config.js.
	NoInject bool

	// NoPrune disables build-time vendor layer file pruning.
	NoPrune bool

	// KeepVendor specifies custom glob patterns to preserve during vendor pruning.
	KeepVendor []string

	// NoPrecompress disables build-time static asset pre-compression (.gz, .br, .zst).
	NoPrecompress bool

	// NoStrip disables stripping unneeded debug symbols from native .node ELF addons.
	NoStrip bool
}

// TelemetryOptions configures OpenTelemetry auto-instrumentation and metrics collection.
type TelemetryOptions struct {
	// Enabled indicates whether telemetry (traces and metrics) is enabled.
	Enabled bool

	// TracesEndpoint is the OTLP exporter endpoint URL for spans (traces).
	TracesEndpoint string

	// MetricsEndpoint is the OTLP exporter endpoint URL for metrics.
	MetricsEndpoint string

	// SampleRate is the trace sampling ratio between 0.0 and 1.0 (default: 1.0).
	SampleRate float64

	// MetricsOnly disables trace span generation while keeping OTEL metrics active.
	MetricsOnly bool

	// Environment is the deployment target environment ("dev", "preview", "production").
	Environment string

	// WithSidecar indicates whether to inject an OTEL Collector sidecar spec into K8s manifests.
	WithSidecar bool
}

// PrepareResult locates the artefacts the SvelteKit adapter emitted. The paths
// are absolute and live inside ProjectDir; they are inputs to Compile, not
// deliverables, and core does not copy or archive them.
type PrepareResult struct {
	// EntrypointPath is the absolute path of the generated TypeScript server
	// entrypoint, conventionally
	// <ProjectDir>/.svelte-kit/jesterkit-sveltekit/temp-server/index.ts.
	// Required in a successful result; Compile compiles exactly this file.
	EntrypointPath string

	// OutputDir is the absolute path of the adapter's output directory,
	// conventionally <ProjectDir>/.svelte-kit/jesterkit-sveltekit. It also
	// contains manifest.js, server/ and the generated assets.generated.ts that
	// embeds the static files. Informational; provided for diagnostics and for
	// cleanup decisions made by core.
	OutputDir string

	// StaticFallbackRelPath is the leaf filename @sveltejs/adapter-static
	// emitted as the SPA fallback page, relative to the static client output
	// root (OutputDir/client). Non-empty only for StrategyStatic builds whose
	// source configured an adapter-static fallback and where the file was
	// genuinely emitted (verified by the Compiler during Prepare). core turns
	// it into PackageRequest.StaticFallback (the in-image path
	// AppClientDirPrefix/<rel>) and the packager stamps EnvStaticFallback.
	// Empty means the project has no SPA fallback.
	StaticFallbackRelPath string

	// TelemetryPreloadRelPath is the OTel SDK bootstrap file's path relative
	// to OutputDir (e.g. "otel-bootstrap.ts"), set only for StrategyLayered
	// builds with telemetry enabled (see
	// sveltekitutils.PrepareLayeredTelemetryBootstrap). The packager joins
	// this with AppServerDirPrefix and inserts `bun --preload <path>` ahead
	// of the real entrypoint in the image's Entrypoint argv instead of the
	// unconditional DefaultLayeredEntrypoint(). Empty means no layered
	// telemetry bootstrap was generated (telemetry disabled, a different
	// strategy, or the project already has its own
	// src/instrumentation.server.ts). StrategyExe's telemetry wrapper uses a
	// different mechanism (EntrypointPath itself, see compiler.go's Prepare)
	// and never sets this field.
	TelemetryPreloadRelPath string
}

// CompileRequest drives stage two: `bun build --compile --target=…` against the
// prepared entrypoint, producing one self-contained executable.
type CompileRequest struct {
	// ProjectDir is the absolute SvelteKit project root, used as the working
	// directory of the compile subprocess so that relative imports resolve.
	// Required.
	ProjectDir string

	// EntrypointPath is PrepareResult.EntrypointPath. Required.
	EntrypointPath string

	// Platform is the single target to compile for. Required and must be
	// Supported; one CompileRequest yields one binary.
	Platform Platform

	// OutputPath is the absolute path to write the executable to. Required —
	// the Compiler must not invent a location, because core owns the build
	// scratch directory and its lifetime. Parent directories are created by the
	// Compiler if missing. An existing file at this path is overwritten.
	OutputPath string

	// SourceDateEpoch is the deterministic build timestamp. Required.
	SourceDateEpoch time.Time

	// Env is additional environment for the compile subprocess, as KEY=VALUE
	// entries appended to the inherited environment. Nil means inherit only.
	Env []string

	// Minify enables Bun's minifier. Defaults to false at the zero value;
	// core's default is true for release builds and is set explicitly.
	Minify bool

	// Sourcemap embeds a source map in the executable. Increases binary size
	// substantially; false by default.
	Sourcemap bool

	// Hermetic enforces zero network egress during compile, the same way
	// PrepareRequest.Hermetic does for Prepare. This exists because `bun
	// build --compile` bundles req.EntrypointPath and its transitive
	// imports — files a third-party SvelteKit adapter generated during
	// Prepare — and Bun's bundler executes `bunfig.toml` preload plugins and
	// `with { type: "macro" }` imports at bundle time, so this stage can run
	// attacker-influenced code exactly like Prepare's `bun run build` can. A
	// Compiler that sandboxes Prepare but not Compile is not actually
	// hermetic: a malicious build script only has to wait for this stage to
	// run before making its network call.
	Hermetic bool

	// HermeticMountIsolation is PrepareRequest.HermeticMountIsolation's
	// counterpart for Compile — see its doc comment. Applies to the exact
	// same `bun build --compile` stage described in Hermetic's own doc
	// comment above (row 18 of mem:self_review_checklist: a security
	// control must cover every untrusted-execution site, not just one).
	HermeticMountIsolation bool
}

// Compiler turns a SvelteKit project into one self-contained executable per
// platform. It is implemented by internal/adapters/bunexec.
//
// The three methods form a strict sequence per build: Preflight once, Prepare
// once, then Compile once per platform. core may call Compile concurrently for
// different platforms against the same PrepareResult, so implementations must
// not keep mutable per-build state on the receiver.
//
// Error expectations:
//   - Preflight returns core.ErrBunNotFound when no bun executable is on PATH,
//     core.ErrBunTooOld when the version is below the minimum, and
//     core.ErrAdapterMissing when @jesterkit/exe-sveltekit is not a dependency
//     of the project.
//   - Prepare returns core.ErrAdapterMisconfigured when the adapter the
//     requested BuildStrategy needs is not configured in the file SvelteKit
//     will actually read (a vite.config.* that passes options to the
//     sveltekit() plugin overrides svelte.config.js completely). This is
//     checked before any subprocess starts, so it must not have written
//     anything to the project directory when it returns.
//   - Prepare returns core.ErrPrepareFailed when `bun run build` exits non-zero
//     or the expected entrypoint is absent afterwards. The wrapped error must
//     carry the captured subprocess output — a bare exit status is useless to a
//     user debugging their own app.
//   - Compile returns core.ErrUnsupportedPlatform for a target with no Bun
//     mapping and core.ErrCompileFailed for a non-zero exit.
type Compiler interface {
	// Preflight verifies the host toolchain and the project layout. It must
	// have no side effects on the project directory.
	Preflight(ctx context.Context, req PreflightRequest) (PreflightResult, error)

	// Prepare runs the SvelteKit build once, producing the shared entrypoint
	// that every per-platform Compile call consumes. It writes into
	// ProjectDir/.svelte-kit and is therefore NOT safe to call concurrently for
	// the same ProjectDir.
	Prepare(ctx context.Context, req PrepareRequest) (PrepareResult, error)

	// Compile produces exactly one executable, at req.OutputPath, for exactly
	// one platform. Safe for concurrent use across distinct platforms and
	// distinct OutputPaths.
	Compile(ctx context.Context, req CompileRequest) (Artifact, error)
}
