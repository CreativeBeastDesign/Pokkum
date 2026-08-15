// Package core is the application layer of Pokkum: the domain model, the
// request validation, the sentinel errors, and the Build pipeline that drives
// the ports.
//
// core imports internal/ports and the standard library, plus
// github.com/google/go-containerregistry/pkg/v1 for the OCI value types that
// are genuinely part of this tool's domain and golang.org/x/sync/errgroup for
// the platform fan-out in pipeline.go. It must never import an adapter.
//
// # Where types live
//
// The boundary vocabulary — Platform, Artifact, SBOMFormat, BaseImagePreset,
// RuntimeConfig and the per-port request/response structs — is declared in
// internal/ports, which is the leaf of the dependency graph. core imports it.
// The rationale is in the ports package doc; the short version is that core is
// the only consumer of the port interfaces and therefore has to be able to
// import them, so ports cannot import core back.
//
// The types below are re-exported here as aliases so that core-facing code and
// the command layer may spell them core.Platform, core.Artifact and so on. An
// alias is the same type, not a copy: core.Platform and ports.Platform are
// interchangeable everywhere, there is no conversion, and passing one where the
// other is expected always compiles. Use whichever reads better in context.
//
// What core owns outright, and ports deliberately does not: parsing user input
// (ParsePlatform and friends), validation (BuildRequest.Validate), the output
// mode, the result aggregate, and every sentinel error. ports holds only
// behaviour that cannot fail.
package core

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Re-exported boundary types. These are aliases, so core.Platform IS
// ports.Platform. See the package doc.
type (
	// Platform identifies a build target, e.g. linux/amd64.
	Platform = ports.Platform

	// Artifact is one compiled, self-contained application binary.
	Artifact = ports.Artifact

	// SBOMFormat selects the bill-of-materials serialisation.
	SBOMFormat = ports.SBOMFormat

	// SBOMAttachMode selects how the SBOM artifact is attached.
	SBOMAttachMode = ports.SBOMAttachMode

	// BaseImagePreset names a supported base-image tier.
	BaseImagePreset = ports.BaseImagePreset

	// BaseImageVerifyMode selects how base image signature verification is performed.
	BaseImageVerifyMode = ports.BaseImageVerifyMode

	// RuntimeConfig describes the runtime behaviour baked into the image.
	RuntimeConfig = ports.RuntimeConfig

	// SBOMDocument is a generated bill of materials.
	SBOMDocument = ports.SBOMDocument

	// TelemetryOptions configures OpenTelemetry auto-instrumentation and metrics collection.
	TelemetryOptions = ports.TelemetryOptions

	// BunVariant specifies CPU/build variants of the Bun runtime binary.
	BunVariant = ports.BunVariant

	// CompressionAlgorithm names the layer compression format.
	CompressionAlgorithm = ports.CompressionAlgorithm

	// BunResolverRequest specifies criteria for resolving a Bun runtime binary.
	BunResolverRequest = ports.BunResolverRequest

	// BunResolverResult reports resolved Bun runtime binary artifact details.
	BunResolverResult = ports.BunResolverResult

	// BunRuntimeResolver provides pinned Bun runtime binaries for layer packaging.
	BunRuntimeResolver = ports.BunRuntimeResolver

	// BuildStrategy specifies the packaging strategy (StrategyLayered or StrategyExe).
	BuildStrategy = ports.BuildStrategy

	// Severity indicates the security risk level of a vulnerability.
	Severity = ports.Severity

	// Vulnerability details a security advisory or CVE.
	Vulnerability = ports.Vulnerability

	// ScanRequest specifies parameters for vulnerability scanning.
	ScanRequest = ports.ScanRequest

	// ScanResult contains discovered security vulnerabilities.
	ScanResult = ports.ScanResult

	// Scanner scans container images, tarballs, or toolchain versions for CVEs and advisories.
	Scanner = ports.Scanner

	// SecretMatch describes a detected secret or sensitive token leak in a file.
	SecretMatch = ports.SecretMatch

	// SecretScanRequest configures directory secret scanning options.
	SecretScanRequest = ports.SecretScanRequest

	// SecretScanResult reports secret scan findings.
	SecretScanResult = ports.SecretScanResult

	// SecretGuard defines the boundary port for build-time secret leak detection.
	SecretGuard = ports.SecretGuard

	// RemoteCacheVerifyMode specifies the verification mode for remote cache hits.
	RemoteCacheVerifyMode = ports.RemoteCacheVerifyMode

	// RemoteCacheVerifyOptions specifies verification criteria for remote cache hits.
	RemoteCacheVerifyOptions = ports.RemoteCacheVerifyOptions
)

// Re-exported enum values, so that the command layer can build a BuildRequest
// without also importing ports.
const (
	CacheVerifyAuto      = ports.CacheVerifyAuto
	CacheVerifyStaticKey = ports.CacheVerifyStaticKey
	CacheVerifyKeyless   = ports.CacheVerifyKeyless
	CacheVerifyNone      = ports.CacheVerifyNone

	SBOMFormatSPDXJSON      = ports.SBOMFormatSPDXJSON
	SBOMFormatCycloneDXJSON = ports.SBOMFormatCycloneDXJSON
	SBOMFormatNone          = ports.SBOMFormatNone
	DefaultSBOMFormat       = ports.DefaultSBOMFormat

	SBOMAttachReferrer    = ports.SBOMAttachReferrer
	SBOMAttachTag         = ports.SBOMAttachTag
	DefaultSBOMAttachMode = ports.DefaultSBOMAttachMode

	BaseImageDistroless    = ports.BaseImageDistroless
	BaseImageChainguard    = ports.BaseImageChainguard
	BaseImageCustom        = ports.BaseImageCustom
	DefaultBaseImagePreset = ports.DefaultBaseImagePreset

	DefaultTag = ports.DefaultTag

	DefaultBunVersion = ports.DefaultBunVersion
	// BunVariantStandard targets standard AVX2 x86-64 / arm64 CPUs.
	BunVariantStandard = ports.BunVariantStandard
	// BunVariantBaseline targets pre-AVX2 x86-64 CPUs.
	BunVariantBaseline = ports.BunVariantBaseline

	// StrategyLayered emits the 5-layer arch-independent layout (default).
	StrategyLayered = ports.StrategyLayered
	// StrategyExe emits the single executable layout (legacy).
	StrategyExe = ports.StrategyExe
	// DefaultBuildStrategy is StrategyLayered.
	DefaultBuildStrategy = ports.DefaultBuildStrategy

	// CompressionGzip applies gzip compression (default).
	CompressionGzip = ports.CompressionGzip
	// CompressionZstd applies zstd compression.
	CompressionZstd = ports.CompressionZstd

	// Re-exported severity constants
	SeverityLow      = ports.SeverityLow
	SeverityMedium   = ports.SeverityMedium
	SeverityHigh     = ports.SeverityHigh
	SeverityCritical = ports.SeverityCritical
)

// Re-exported functions and values.
var (
	ParseSeverity      = ports.ParseSeverity
	LinuxAMD64         = ports.LinuxAMD64
	LinuxARM64         = ports.LinuxARM64
	SupportedPlatforms = ports.SupportedPlatforms
)

// OutputMode selects where a finished image is sent. It lives in core rather
// than ports because it is a dispatch decision, not boundary vocabulary: core
// switches on it to choose between the Registry, LocalLoader and TarballWriter
// ports. No adapter ever reads it, which is precisely how core selects a
// destination without importing any adapter package.
type OutputMode string

const (
	// OutputPush publishes to a remote registry. The default, ko-style.
	OutputPush OutputMode = "push"

	// OutputLocal imports into the local Docker daemon (--local).
	OutputLocal OutputMode = "local"

	// OutputTarball writes an OCI archive to disk (--tarball path).
	OutputTarball OutputMode = "tarball"
)

// DefaultOutputMode is used when OutputOptions.Mode is the zero value.
const DefaultOutputMode = OutputPush

// Valid reports whether m is a known mode. The zero value ("") is NOT valid;
// Normalize replaces it with DefaultOutputMode.
func (m OutputMode) Valid() bool {
	switch m {
	case OutputPush, OutputLocal, OutputTarball:
		return true
	default:
		return false
	}
}

// String implements fmt.Stringer.
func (m OutputMode) String() string { return string(m) }

// ParseOutputMode converts user input to an OutputMode, accepting any case and
// surrounding whitespace. It returns ErrInvalidOutputMode for anything else.
func ParseOutputMode(s string) (OutputMode, error) {
	m := OutputMode(strings.ToLower(strings.TrimSpace(s)))
	if !m.Valid() {
		return "", fmt.Errorf("output mode %q: %w", s, ErrInvalidOutputMode)
	}
	return m, nil
}

// ParsePlatform converts an OCI platform string such as "linux/amd64" into a
// Platform. Unlike ports.PlatformFromV1, which is lenient because it enumerates
// whatever a base image happens to contain, this is strict: it accepts only
// platforms Pokkum can actually build, and returns ErrUnsupportedPlatform
// otherwise. It is the entry point for user input.
func ParsePlatform(s string) (Platform, error) {
	parts := strings.Split(strings.TrimSpace(s), "/")
	var p Platform
	switch len(parts) {
	case 2:
		p = Platform{OS: parts[0], Arch: parts[1]}
	case 3:
		p = Platform{OS: parts[0], Arch: parts[1], Variant: parts[2]}
	default:
		return Platform{}, fmt.Errorf("platform %q: want os/arch: %w", s, ErrUnsupportedPlatform)
	}
	if !p.Supported() {
		return Platform{}, fmt.Errorf("platform %q: %w (supported: %s)", s, ErrUnsupportedPlatform, PlatformList(SupportedPlatforms))
	}
	return p, nil
}

// ParsePlatforms converts a list of platform strings. A single element "all"
// (in any case) expands to SupportedPlatforms. Duplicates are rejected, since
// building the same platform twice is always a mistake and silently collapsing
// it would hide a typo in a --platform list.
func ParsePlatforms(ss []string) ([]Platform, error) {
	if len(ss) == 1 && strings.EqualFold(strings.TrimSpace(ss[0]), "all") {
		return slices.Clone(SupportedPlatforms), nil
	}
	out := make([]Platform, 0, len(ss))
	for _, s := range ss {
		p, err := ParsePlatform(s)
		if err != nil {
			return nil, err
		}
		if slices.Contains(out, p) {
			return nil, fmt.Errorf("platform %q listed twice: %w", s, ErrInvalidRequest)
		}
		out = append(out, p)
	}
	return out, nil
}

// PlatformList renders platforms as a comma-separated string, for error
// messages and build summaries.
func PlatformList(ps []Platform) string {
	ss := make([]string, len(ps))
	for i, p := range ps {
		ss[i] = p.String()
	}
	return strings.Join(ss, ", ")
}

// ParseSBOMFormat converts user input to an SBOMFormat, accepting any case and
// surrounding whitespace, plus the aliases "spdx" and "cyclonedx" for the JSON
// variants and "off"/"false"/"disabled" for none.
func ParseSBOMFormat(s string) (SBOMFormat, error) {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case "spdx", "spdx-json", "spdxjson":
		return SBOMFormatSPDXJSON, nil
	case "cyclonedx", "cyclonedx-json", "cyclonedxjson":
		return SBOMFormatCycloneDXJSON, nil
	case "none", "off", "false", "disabled":
		return SBOMFormatNone, nil
	default:
		return "", fmt.Errorf("sbom format %q: %w", s, ErrInvalidSBOMFormat)
	}
}

// ParseSBOMAttachMode converts user input to an SBOMAttachMode, accepting any case
// and surrounding whitespace.
func ParseSBOMAttachMode(s string) (SBOMAttachMode, error) {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case "referrer", "referrers", "oci1.1":
		return SBOMAttachReferrer, nil
	case "tag", "tags":
		return SBOMAttachTag, nil
	default:
		return "", fmt.Errorf("sbom attach mode %q: %w", s, ErrInvalidSBOMAttachMode)
	}
}

// ParseBaseImagePreset converts user input to a BaseImagePreset, accepting any
// case and surrounding whitespace.
func ParseBaseImagePreset(s string) (BaseImagePreset, error) {
	p := BaseImagePreset(strings.ToLower(strings.TrimSpace(s)))
	if !p.Valid() {
		return "", fmt.Errorf("base image preset %q: %w", s, ErrInvalidBaseImage)
	}
	return p, nil
}

// ParseBaseImageVerifyMode converts user input to a BaseImageVerifyMode,
// accepting any case and surrounding whitespace.
func ParseBaseImageVerifyMode(s string) (BaseImageVerifyMode, error) {
	m := BaseImageVerifyMode(strings.ToLower(strings.TrimSpace(s)))
	if !m.Valid() {
		return "", fmt.Errorf("base image verify mode %q: %w", s, ErrInvalidBaseImage)
	}
	return m, nil
}

// ParseCacheVerifyMode converts user input to a RemoteCacheVerifyMode.
func ParseCacheVerifyMode(s string) (RemoteCacheVerifyMode, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return CacheVerifyAuto, nil
	}
	m := RemoteCacheVerifyMode(s)
	if !m.Valid() {
		return "", fmt.Errorf("cache verify mode %q: %w", s, ErrInvalidRequest)
	}
	return m, nil
}

// ParseSourceDateEpoch parses a SOURCE_DATE_EPOCH value: a decimal count of
// seconds since the Unix epoch, as specified by the reproducible-builds
// standard. An empty string yields the zero Time and a nil error, meaning
// "unset"; BuildRequest.Normalize then substitutes the epoch itself.
//
// The result is always UTC and always second-precision, because that is the
// only precision the specification carries and because a sub-second component
// would make an image built from a clock differ from one built from the
// environment variable that recorded it.
func ParseSourceDateEpoch(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("SOURCE_DATE_EPOCH %q: not an integer: %w", s, ErrInvalidRequest)
	}
	if n < 0 {
		return time.Time{}, fmt.Errorf("SOURCE_DATE_EPOCH %q: negative: %w", s, ErrInvalidRequest)
	}
	return time.Unix(n, 0).UTC(), nil
}

// BaseImageOptions selects the base image. The zero value means the distroless
// default.
type BaseImageOptions struct {
	// Preset is the tier. Empty means DefaultBaseImagePreset, unless Ref is set
	// and Preset is empty, in which case Normalize infers BaseImageCustom.
	Preset BaseImagePreset

	// Ref overrides the preset's canonical reference. Required when Preset is
	// BaseImageCustom. Pinning it to a digest ("…@sha256:…") is what makes a
	// build reproducible across time as well as across machines.
	Ref string

	// UpdateBase forces re-resolving base image tags against remote registry and updating pokkum.lock.
	UpdateBase bool

	// Offline strictly enforces using pokkum.lock and local cache without remote registry calls.
	Offline bool

	// NoVerifyBase suppresses Cosign signature verification on upstream base images.
	NoVerifyBase bool

	// VerifyMode selects how base image signature verification is performed. Empty means use the preset default.
	VerifyMode BaseImageVerifyMode

	// KeylessSAN is the expected Fulcio certificate Subject Alternative Name for keyless verification. Empty means use the preset default.
	KeylessSAN string

	// KeylessIssuer is the expected OIDC issuer for keyless verification. Empty means use the preset default.
	KeylessIssuer string

	// TrustedRootPath is the path to a Sigstore trusted-root JSON file overriding the embedded default. Empty means use the embedded default.
	TrustedRootPath string
}

// SBOMOptions controls bill-of-materials generation and attachment.
type SBOMOptions struct {
	// Format selects the serialisation. Empty means DefaultSBOMFormat; set it
	// to SBOMFormatNone to disable generation entirely.
	Format SBOMFormat

	// AttachMode selects between OCI 1.1 referrer attachment (default) or tag convention.
	AttachMode SBOMAttachMode

	// NoAttach suppresses publishing the SBOM alongside the image. The field is
	// negative so that the zero value means "attach", which is the behaviour
	// almost everyone wants; set it when the registry credentials can push the
	// image but not the extra sha256-….sbom tag, which is a real and confusing
	// failure on locked-down registries.
	//
	// Attachment only happens in OutputPush mode; the daemon and tarball
	// destinations have nowhere to put it, and core skips it silently there.
	NoAttach bool
}

// OutputOptions selects the destination of the finished image.
type OutputOptions struct {
	// Mode is the destination. Empty means DefaultOutputMode.
	Mode OutputMode

	// TarballPath is the archive path. Required when Mode is OutputTarball,
	// ignored otherwise.
	TarballPath string
}

// CompileOptions tunes the Bun compile step.
type CompileOptions struct {
	// NoMinify disables Bun's minifier. Negative for the same reason as
	// SBOMOptions.NoAttach: minification on is the right default for an image
	// that ships to production, so the zero value must mean "on".
	NoMinify bool

	// Sourcemap embeds a source map in the executable. Off by default; it adds
	// substantially to the binary and therefore to the layer that is pushed on
	// every deploy.
	Sourcemap bool

	// MinBunVersion overrides the minimum acceptable Bun version. Empty means
	// the compiler adapter's built-in minimum, which is where the requirement
	// should normally live.
	MinBunVersion string

	// Env is additional environment for the build and compile subprocesses, as
	// KEY=VALUE entries appended to the inherited environment. Nil means
	// inherit only. SOURCE_DATE_EPOCH is set by the adapter from the request's
	// timestamp and must not be set here.
	Env []string

	// Strategy selects the packaging strategy (StrategyLayered or StrategyExe).
	Strategy BuildStrategy

	// Compression selects the layer compression algorithm (CompressionGzip or CompressionZstd).
	Compression CompressionAlgorithm

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

	// NoCache disables checking and publishing to the remote composite OCI input cache.
	NoCache bool
}

// BunRuntimeOptions configures Bun runtime resolution and caching for layer assembly.
type BunRuntimeOptions struct {
	// Version is the requested Bun version (e.g. "1.2.2"). Empty means DefaultBunVersion.
	Version string

	// Variant is the CPU variant ("standard" or "baseline"). Empty means BunVariantStandard.
	Variant BunVariant

	// CustomBinaryPath is an optional local path to a bun executable (--bun-binary).
	CustomBinaryPath string
}

// BuildRequest is the complete description of one `pokkum build`. It is
// assembled by the command layer from flags, config and environment, then
// Normalize'd and Validate'd before Build touches anything.
//
// The zero value is not usable — ProjectDir at minimum must be set — but every
// other field has a documented meaning at its zero value, so a caller only sets
// what it wants to change.
type BuildRequest struct {
	// ProjectDir is the SvelteKit project root, the directory holding
	// package.json. Required. Normalize makes it absolute.
	ProjectDir string

	// Repo is the destination repository, without tag or digest, e.g.
	// "ghcr.io/acme/app".
	//
	// Required for OutputPush; an empty value there is ErrNoDockerRepo. For
	// OutputLocal and OutputTarball, Normalize substitutes
	// "pokkum.local/<project-dir-basename>", because those destinations are
	// local and a wrong name there costs nothing, whereas guessing a remote
	// repository could publish to somewhere unintended.
	Repo string

	// Tags are the tags to apply, without the repository prefix. Empty means
	// DefaultTag. The image is always addressable by digest regardless.
	Tags []string

	// Platforms are the build targets. Empty means SupportedPlatforms — that
	// is, a multi-arch build is the default, matching the project's premise
	// that one command produces an image that runs everywhere.
	Platforms []Platform

	// SourceDateEpoch is the deterministic build timestamp, threaded into every
	// layer mtime, the image config, the history entries and the SBOM.
	//
	// The zero value means "unset", and Normalize replaces it with the Unix
	// epoch — not with the current time. That is deliberate and matches ko:
	// a build with no declared timestamp must still be byte-reproducible, and
	// the only timestamp that satisfies that is a constant. Callers that want a
	// real date must supply one, normally from the SOURCE_DATE_EPOCH
	// environment variable or the git commit time.
	//
	// Normalize truncates it to second precision and converts it to UTC.
	SourceDateEpoch time.Time

	// BaseImage selects the base image. Zero value means distroless.
	BaseImage BaseImageOptions

	// SBOM controls bill-of-materials generation. Zero value means SPDX-JSON,
	// attached on push.
	SBOM SBOMOptions

	// Output selects the destination. Zero value means push.
	Output OutputOptions

	// Compile tunes the Bun compile step. Zero value means minified, no
	// source map.
	Compile CompileOptions

	// Runtime describes the runtime contract baked into the image. Zero value
	// means the documented defaults: port 3000, probe port 8081, user
	// 65532:65532, 30s shutdown timeout.
	Runtime RuntimeConfig

	// Telemetry controls OpenTelemetry auto-instrumentation injection.
	Telemetry TelemetryOptions

	// Sign enables SLSA, Cosign, and DSSE signing.
	Sign bool

	// Labels are merged into the image config labels, overriding Pokkum's own.
	// Nil means none.
	Labels map[string]string

	// Annotations are applied to the image and index manifests. Nil means none.
	Annotations map[string]string

	// WorkDir is the scratch directory for compiled binaries. Empty means core
	// creates a temporary directory and removes it when the build finishes.
	// Set it to keep the intermediate binaries for inspection.
	WorkDir string

	// Concurrency caps how many platforms are compiled and packaged in
	// parallel. Zero means one worker per platform, which is the right default
	// for the two-platform case; a negative value is invalid.
	Concurrency int

	// Insecure permits plain HTTP and skips TLS verification for both the base
	// image pull and the push. Intended for local test registries only.
	Insecure bool

	// BunRuntime configures Bun runtime binary resolution and caching.
	BunRuntime BunRuntimeOptions

	// AllowSecretPatterns contains regex patterns to ignore during build-time secret scanning.
	AllowSecretPatterns []string

	// Hermetic enforces zero-network egress and offline mode during build.
	Hermetic bool

	// RegistryConfigPath is the optional custom OCI config.json path for authentication.
	RegistryConfigPath string

	// FailOnCVE sets the minimum vulnerability severity threshold that causes build failure.
	// Empty means warn-only unless POKKUM_FAIL_ON_CVE is set.
	FailOnCVE Severity

	// AllowIncompleteScan permits the build to succeed even if vulnerability database lookups fail.
	AllowIncompleteScan bool

	// CacheVerify configures cryptographic signature verification on candidate remote cache-hit images.
	CacheVerify RemoteCacheVerifyOptions
}

// Normalize fills in defaults in place. It is idempotent, it never fails, and
// it must be called before Validate — Validate assumes normalised input and
// will, for example, reject an empty Repo only after Normalize has had its
// chance to supply a local one.
//
// Build calls Normalize then Validate itself, so callers that go through Build
// need not do either.
func (r *BuildRequest) Normalize() {
	if r.Hermetic {
		r.BaseImage.Offline = true
	}
	if r.Compile.Strategy == "" {
		r.Compile.Strategy = DefaultBuildStrategy
	}
	if r.BunRuntime.Version == "" {
		r.BunRuntime.Version = DefaultBunVersion
	}
	if r.BunRuntime.Variant == "" {
		r.BunRuntime.Variant = BunVariantStandard
	}
	if r.ProjectDir != "" {
		if abs, err := filepath.Abs(r.ProjectDir); err == nil {
			r.ProjectDir = abs
		}
	}
	if len(r.Platforms) == 0 {
		r.Platforms = slices.Clone(SupportedPlatforms)
	}
	if len(r.Tags) == 0 {
		r.Tags = []string{DefaultTag}
	}
	if r.SourceDateEpoch.IsZero() {
		r.SourceDateEpoch = time.Unix(0, 0).UTC()
	} else {
		r.SourceDateEpoch = r.SourceDateEpoch.Truncate(time.Second).UTC()
	}

	if r.BaseImage.Preset == "" {
		if r.BaseImage.Ref != "" {
			r.BaseImage.Preset = BaseImageCustom
		} else {
			r.BaseImage.Preset = DefaultBaseImagePreset
		}
	}
	if r.BaseImage.Ref == "" {
		if ref, ok := r.BaseImage.Preset.DefaultRef(); ok {
			r.BaseImage.Ref = ref
		}
	}

	if r.CacheVerify.VerifyMode == "" {
		r.CacheVerify.VerifyMode = CacheVerifyAuto
	}
	if r.CacheVerify.VerifyMode != CacheVerifyNone {
		r.CacheVerify.VerifySignature = true
	}

	if r.SBOM.Format == "" {
		r.SBOM.Format = DefaultSBOMFormat
	}
	if r.SBOM.AttachMode == "" {
		r.SBOM.AttachMode = DefaultSBOMAttachMode
	}
	if r.Output.Mode == "" {
		r.Output.Mode = DefaultOutputMode
	}
	if r.Repo == "" && r.Output.Mode != OutputPush && r.ProjectDir != "" {
		r.Repo = "pokkum.local/" + strings.ToLower(filepath.Base(r.ProjectDir))
	}

	r.Runtime = r.Runtime.WithDefaults()

	if r.Concurrency == 0 {
		r.Concurrency = len(r.Platforms)
	}
}

// Validate reports the first problem that would make this build fail, or nil.
// Every returned error wraps a sentinel from errors.go and names the offending
// field and value.
//
// Validate does not touch the filesystem or the network: it checks the shape of
// the request only. Whether ProjectDir actually contains a SvelteKit project is
// the Compiler's Preflight to answer, and reporting it here would mean two
// places could disagree.
func (r BuildRequest) Validate() error {
	if strings.TrimSpace(r.ProjectDir) == "" {
		return fmt.Errorf("project directory is required: %w", ErrInvalidRequest)
	}

	if !r.Output.Mode.Valid() {
		return fmt.Errorf("output mode %q: %w", r.Output.Mode, ErrInvalidOutputMode)
	}
	if r.Output.Mode == OutputTarball && strings.TrimSpace(r.Output.TarballPath) == "" {
		return fmt.Errorf("tarball path is required in %s mode: %w", r.Output.Mode, ErrInvalidRequest)
	}
	if strings.TrimSpace(r.Repo) == "" {
		return fmt.Errorf("destination repository is required in %s mode: %w", r.Output.Mode, ErrNoDockerRepo)
	}
	if strings.ContainsAny(r.Repo, " \t") {
		return fmt.Errorf("repository %q contains whitespace: %w", r.Repo, ErrInvalidRequest)
	}
	if i := strings.LastIndexAny(r.Repo, ":@"); i >= 0 && !strings.Contains(r.Repo[i:], "/") {
		return fmt.Errorf("repository %q must not include a tag or digest: %w", r.Repo, ErrInvalidRequest)
	}

	if len(r.Platforms) == 0 {
		return fmt.Errorf("at least one platform is required: %w", ErrUnsupportedPlatform)
	}
	seen := make(map[Platform]struct{}, len(r.Platforms))
	for _, p := range r.Platforms {
		if !p.Supported() {
			return fmt.Errorf("platform %q: %w (supported: %s)", p, ErrUnsupportedPlatform, PlatformList(SupportedPlatforms))
		}
		if _, dup := seen[p]; dup {
			return fmt.Errorf("platform %q listed twice: %w", p, ErrInvalidRequest)
		}
		seen[p] = struct{}{}
	}

	for _, t := range r.Tags {
		if strings.TrimSpace(t) == "" {
			return fmt.Errorf("empty tag: %w", ErrInvalidRequest)
		}
		if strings.ContainsAny(t, ":@/ ") {
			return fmt.Errorf("tag %q must not contain ':', '@', '/' or spaces: %w", t, ErrInvalidRequest)
		}
	}

	if !r.BaseImage.Preset.Valid() {
		return fmt.Errorf("base image preset %q: %w", r.BaseImage.Preset, ErrInvalidBaseImage)
	}
	if strings.TrimSpace(r.BaseImage.Ref) == "" {
		return fmt.Errorf("base image reference is required for preset %q: %w", r.BaseImage.Preset, ErrInvalidBaseImage)
	}

	if !r.SBOM.Format.Valid() {
		return fmt.Errorf("sbom format %q: %w", r.SBOM.Format, ErrInvalidSBOMFormat)
	}
	if !r.SBOM.AttachMode.Valid() {
		return fmt.Errorf("sbom attach mode %q: %w", r.SBOM.AttachMode, ErrInvalidSBOMAttachMode)
	}

	if r.SourceDateEpoch.IsZero() {
		return fmt.Errorf("source date epoch is required; call Normalize first: %w", ErrInvalidRequest)
	}

	if err := validateRuntime(r.Runtime); err != nil {
		return err
	}

	if r.FailOnCVE != "" && !r.FailOnCVE.Valid() {
		return fmt.Errorf("fail-on-cve severity %q: %w", r.FailOnCVE, ErrInvalidRequest)
	}

	if r.CacheVerify.VerifyMode != "" && !r.CacheVerify.VerifyMode.Valid() {
		return fmt.Errorf("cache verify mode %q: %w", r.CacheVerify.VerifyMode, ErrInvalidRequest)
	}

	if r.Concurrency < 0 {
		return fmt.Errorf("concurrency %d must not be negative: %w", r.Concurrency, ErrInvalidRequest)
	}
	return nil
}

func validateRuntime(rc RuntimeConfig) error {
	rc = rc.WithDefaults()
	if rc.Port < 1 || rc.Port > 65535 {
		return fmt.Errorf("application port %d out of range: %w", rc.Port, ErrInvalidRuntimeConfig)
	}
	if rc.ProbePort < 1 || rc.ProbePort > 65535 {
		return fmt.Errorf("probe port %d out of range: %w", rc.ProbePort, ErrInvalidRuntimeConfig)
	}
	if rc.Port == rc.ProbePort {
		return fmt.Errorf("application port and probe port are both %d: %w", rc.Port, ErrInvalidRuntimeConfig)
	}
	if rc.ShutdownTimeout < 0 {
		return fmt.Errorf("shutdown timeout %s must not be negative: %w", rc.ShutdownTimeout, ErrInvalidRuntimeConfig)
	}
	if len(rc.Entrypoint) == 0 {
		return fmt.Errorf("entrypoint must not be empty: %w", ErrInvalidRuntimeConfig)
	}
	for _, p := range rc.ExposedPorts {
		if p < 1 || p > 65535 {
			return fmt.Errorf("exposed port %d out of range: %w", p, ErrInvalidRuntimeConfig)
		}
	}
	for _, envKey := range rc.RequireEnv {
		trimmed := strings.TrimSpace(envKey)
		if trimmed == "" {
			return fmt.Errorf("required env var name must not be empty: %w", ErrInvalidRuntimeConfig)
		}
		if strings.ContainsAny(trimmed, "= \t\n\r") {
			return fmt.Errorf("required env var name %q must not contain '=' or whitespace: %w", envKey, ErrInvalidRuntimeConfig)
		}
	}
	return nil
}

// ImageResult describes where the finished image ended up. It is the
// destination-independent normalisation of ports.PublishResult: whichever of
// the three publishing ports ran, core produces one of these, so that the
// command layer formats a single shape.
type ImageResult struct {
	// Mode is the destination that was used.
	Mode OutputMode

	// Ref is the primary reference. For OutputPush it is the immutable
	// "repo@sha256:…" form — the thing to put in a manifest. For OutputLocal it
	// is the tag the daemon knows. For OutputTarball it is the reference
	// recorded inside the archive.
	Ref string

	// Digest is the digest of the published manifest or index.
	Digest v1.Hash

	// Tags are the tags that were written, without the repository prefix.
	Tags []string

	// TarballPath is the archive that was written, for OutputTarball only.
	TarballPath string

	// Platforms are the platforms present in the published artefact. For
	// OutputLocal this is a single platform even when more were built, because
	// the Docker image store holds one.
	Platforms []Platform

	// IsIndex reports whether Ref addresses a multi-platform index rather than
	// a single image manifest.
	IsIndex bool

	// Size is the transferred or written size in bytes, or zero if unknown.
	Size int64

	// Cached indicates whether the image build was skipped due to a remote cache hit.
	Cached bool
}

// String returns Ref, so an ImageResult can be printed directly.
func (r ImageResult) String() string { return r.Ref }

// NewImageResult normalises a port-level publish result into an ImageResult.
// It exists so that the three destination branches of the pipeline converge on
// one line each rather than repeating the field mapping.
func NewImageResult(mode OutputMode, pr ports.PublishResult, platforms []Platform, isIndex bool) ImageResult {
	return ImageResult{
		Mode:        mode,
		Ref:         pr.Ref,
		Digest:      pr.Digest,
		Tags:        slices.Clone(pr.Tags),
		TarballPath: pr.Path,
		Platforms:   slices.Clone(platforms),
		IsIndex:     isIndex,
		Size:        pr.Size,
	}
}

// SBOMResult describes the bill of materials produced for a build. It is nil in
// a BuildResult when the format was SBOMFormatNone.
type SBOMResult struct {
	// Format is the serialisation that was produced.
	Format SBOMFormat

	// AttachMode is the attachment strategy used (referrer or tag).
	AttachMode SBOMAttachMode

	// SHA256 is the lowercase hex digest of the document contents.
	SHA256 string

	// PackageCount is the number of packages catalogued.
	PackageCount int

	// Ref is the reference the SBOM was attached under, in
	// "repo:sha256-….sbom" form. Empty when it was generated but not attached.
	Ref string

	// Size is the document size in bytes.
	Size int64
}

// BaseImageInfo records which base image a build actually used, so that the
// build is auditable and reproducible after the fact.
type BaseImageInfo struct {
	// Preset is the tier that was selected.
	Preset BaseImagePreset

	// Ref is the reference as requested, tag or digest.
	Ref string

	// PinnedRef is Ref in digest form, "repo@sha256:…". This is the value to
	// feed back into a later build to reproduce this one exactly.
	PinnedRef string

	// Digest is the digest of the resolved base manifest or index.
	Digest v1.Hash
}

// Toolchain records the versions that produced a build, for the summary and
// for the image labels. Every field may be empty when a version could not be
// determined; that is informational only and never an error.
type Toolchain struct {
	// PokkumVersion is the version of the pokkum CLI.
	PokkumVersion string

	// BunVersion is the Bun that compiled the application.
	BunVersion string

	// AdapterVersion is the @jesterkit/exe-sveltekit version.
	AdapterVersion string

	// SvelteKitVersion is the @sveltejs/kit version.
	SvelteKitVersion string

	// SupervisorVersion is the pokkum-init version.
	SupervisorVersion string
}

// BuildResult is everything one `pokkum build` produced. It is returned even
// for a single-platform build, and it carries enough detail to print a summary,
// write provenance, and reproduce the build later.
type BuildResult struct {
	// Image is where the finished image went.
	Image ImageResult

	// Cached indicates whether the build was satisfied from the remote input cache.
	Cached bool

	// Artifacts are the compiled binaries, one per platform, in the order the
	// request listed the platforms. The files may already have been deleted if
	// the build used a temporary WorkDir.
	Artifacts []Artifact

	// BaseImage records the base that was used, pinned to a digest.
	BaseImage BaseImageInfo

	// SBOM describes the bill of materials, or is nil when SBOM generation was
	// disabled.
	SBOM *SBOMResult

	// Toolchain records the versions involved.
	Toolchain Toolchain

	// SourceDateEpoch is the timestamp the build was stamped with, echoed back
	// so that a caller can record the value needed to reproduce it.
	SourceDateEpoch time.Time

	// Duration is the wall-clock time the build took. Informational only; it is
	// measured by core, never by an adapter, and it is deliberately absent from
	// anything that affects the output digest.
	Duration time.Duration
}

// ArtifactFor returns the compiled artifact for a platform, and reports whether
// one was produced.
func (r BuildResult) ArtifactFor(p Platform) (Artifact, bool) {
	for _, a := range r.Artifacts {
		if a.Platform == p {
			return a, true
		}
	}
	return Artifact{}, false
}

// IsUnsupportedPlatform is a convenience for the most frequently checked
// sentinel, so callers do not have to import errors alongside core for the one
// classification they actually branch on.
func IsUnsupportedPlatform(err error) bool { return errors.Is(err, ErrUnsupportedPlatform) }
