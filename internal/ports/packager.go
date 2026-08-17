package ports

import (
	"context"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// The image runtime contract.
//
// These constants are the agreement between the packager (which writes the
// image config), the supervisor (which reads the environment at runtime) and
// the k8s manifest generator (which writes probes and container ports). All
// three work packages must reference these identifiers rather than repeating
// the literals, so that the contract has exactly one definition.
const (
	// SupervisorPath is where pokkum-init is placed in the image, and is
	// argv[0] of the entrypoint.
	SupervisorPath = "/pokkum/init"

	// AppBinaryPath is where the compiled SvelteKit executable is placed. It is
	// a single file; there is no sibling asset directory, because static assets
	// are embedded in the executable.
	AppBinaryPath = "/app/server"

	// BunBinaryPath is where the resolved Bun executable is placed in the image for StrategyLayered.
	BunBinaryPath = "/usr/local/bin/bun"

	// AppServerIndexPath is the entrypoint JS file for StrategyLayered.
	AppServerIndexPath = "/app/server/index.js"

	// AppClientDirPrefix is the in-image mount point for static client assets.
	AppClientDirPrefix = "/app/client"

	// AppVendorDirPrefix is the in-image mount point for split vendor JS modules.
	AppVendorDirPrefix = "/app/vendor"

	// AppNativeDirPrefix is the in-image mount point for native .node binaries and dynamic .so libraries.
	AppNativeDirPrefix = "/app/native"

	// AppPrerenderedDirPrefix is the in-image mount point for the extracted
	// prerendered static pages layer. It is a sibling of /app/server so that a
	// server resolving prerendered pages relative to its own directory (as
	// @sveltejs/adapter-node does) can be pointed at it via EnvPrerenderedDir.
	AppPrerenderedDirPrefix = "/app/prerendered"

	// AppServerDirPrefix is the in-image mount point for the layered-strategy
	// server JS tree (the staged build/ output). Distinct from AppBinaryPath,
	// which is the single-file exe-strategy artifact.
	AppServerDirPrefix = "/app/server"

	// StaticServerPath is where the pokkum-static binary is placed in the
	// image, and argv[0] of the entrypoint for StrategyStatic.
	StaticServerPath = "/pokkum/static"

	// WorkingDir is the image's working directory.
	WorkingDir = "/app"

	// DefaultUser is the distroless "nonroot" user and group, in the numeric
	// form required by Kubernetes' runAsNonRoot admission check. A named user
	// would fail that check because the kubelet cannot resolve names from an
	// image with no /etc/passwd worth reading.
	DefaultUser = "65532:65532"

	// DefaultPort is the port the SvelteKit application listens on, and the
	// value written to the PORT environment variable when the caller does not
	// override it.
	DefaultPort = 3000

	// DefaultProbePort is the port pokkum-init serves liveness and readiness
	// endpoints on. It is deliberately distinct from the application port so
	// that probes keep answering while the app is still starting or is already
	// draining.
	DefaultProbePort = 8081

	// DefaultShutdownTimeout is how long pokkum-init waits for the application
	// to exit after forwarding SIGTERM before sending SIGKILL. It is under the
	// Kubernetes default terminationGracePeriodSeconds of 30s only marginally;
	// manifest generation should raise the grace period when this is raised.
	DefaultShutdownTimeout = 30 * time.Second
)

// AttestationRoots is the fixed set of in-image directories the layered
// startup attestation covers. Both the packager (which computes the expected
// digest at build time) and pokkum-init (which re-derives it from the live
// tree at runtime) iterate exactly this set, so a directory that was never
// packaged is simply absent on both sides and contributes nothing. The set,
// not its order, is what matters: the aggregate digest is sorted by relative
// path before hashing, so iteration order cannot leak into it. A var, not a
// const, because it is an array literal rather than a typed constant.
var AttestationRoots = [...]string{
	AppServerDirPrefix,
	AppClientDirPrefix,
	AppPrerenderedDirPrefix,
	AppVendorDirPrefix,
	AppNativeDirPrefix,
}

// Environment variables read by pokkum-init and by the application at runtime.
const (
	// EnvPort is read by the SvelteKit server to choose its listen port.
	EnvPort = "PORT"

	// EnvProbePort is read by pokkum-init to choose its probe listen port.
	EnvProbePort = "POKKUM_PROBE_PORT"

	// EnvShutdownTimeout is read by pokkum-init as a Go duration string
	// ("30s"). An unparseable value must fall back to DefaultShutdownTimeout
	// rather than abort startup.
	EnvShutdownTimeout = "POKKUM_SHUTDOWN_TIMEOUT"

	// EnvRequiredEnv is read by pokkum-init as a comma-separated list of
	// environment variable names that must be present and non-empty at runtime.
	EnvRequiredEnv = "POKKUM_REQUIRED_ENV"

	// EnvPrerenderedDir is read by the patched adapter-node runtime handler to
	// choose where prerendered pages are served from. It is set to
	// AppPrerenderedDirPrefix on layered builds that extract prerendered pages
	// into their own layer. Absent means the server falls back to its compiled
	// default (path.Join(dir, "prerendered")).
	EnvPrerenderedDir = "POKKUM_PRERENDERED_DIR"

	// EnvStaticRoots is read by pokkum-static to choose the read-only
	// directories it serves, as a path-list separated by os.PathListSeparator.
	// It defaults to AppClientDirPrefix + os.PathListSeparator +
	// AppPrerenderedDirPrefix.
	EnvStaticRoots = "POKKUM_STATIC_ROOTS"

	// EnvStaticFallback is read by pokkum-static as the opt-in SPA-fallback
	// file: an in-image path served with 200 for any unmatched GET/HEAD route
	// (same ETag/Content-Encoding/Range negotiation as other served files).
	// It is stamped by the packager only for StrategyStatic builds where the
	// source project configured an adapter-static fallback page. Absent (or
	// empty) means no fallback — unmatched routes keep returning a plain 404.
	// The path always lives under one of the served static roots (typically
	// AppClientDirPrefix/<rel>).
	EnvStaticFallback = "POKKUM_STATIC_FALLBACK"

	// EnvAttestationDigest is read by pokkum-init as the expected SHA-256 root
	// digest of the layered-strategy /app runtime tree (server, client,
	// prerendered, vendor, native). It is stamped by the packager into the
	// image config at build time. When present and non-empty, pokkum-init
	// verifies the live /app tree matches before exec'ing the child and refuses
	// to start on mismatch (startup attestation, layered hardening Option C).
	// When absent, no verification runs — the escape hatch for an operator who
	// deliberately disables it by overriding this env var.
	EnvAttestationDigest = "POKKUM_ATTESTATION_DIGEST"
)

// adapter-node's documented reverse-proxy contract (see
// https://svelte.dev/docs/kit/adapter-node#Environment-variables). None of
// these are Pokkum-invented names — they are read directly by the bundled
// @sveltejs/adapter-node server (handler.js), which Pokkum embeds unmodified.
// Every one of these is optional and, left unset, falls back to
// adapter-node's own default (deriving origin from the raw socket, which is
// wrong behind a TLS-terminating ingress — the "403 Cross-site POST form
// submissions are forbidden" failure this contract exists to prevent).
const (
	// EnvOrigin is the app's own canonical origin (e.g. "https://example.com"),
	// used to validate the Origin header on form-action POSTs and to build
	// absolute URLs. Without it, adapter-node derives origin/protocol from the
	// raw socket, which reports the wrong scheme/host behind a
	// TLS-terminating reverse proxy or ingress.
	EnvOrigin = "ORIGIN"

	// EnvProtocolHeader names the proxy header adapter-node trusts for the
	// original request protocol (typically "x-forwarded-proto"), used instead
	// of ORIGIN when the protocol is derived per-request rather than pinned.
	EnvProtocolHeader = "PROTOCOL_HEADER"

	// EnvHostHeader names the proxy header adapter-node trusts for the
	// original request host (typically "x-forwarded-host").
	EnvHostHeader = "HOST_HEADER"

	// EnvAddressHeader names the proxy header adapter-node trusts for the
	// real client IP (typically "x-forwarded-for"). Without it, event.getClientAddress()
	// reports the proxy's own address for every request.
	EnvAddressHeader = "ADDRESS_HEADER"

	// EnvXFFDepth is the number of trusted proxy hops adapter-node counts
	// back from when parsing ADDRESS_HEADER (X-Forwarded-For-style headers
	// can list multiple hops). adapter-node defaults to 1 when unset.
	EnvXFFDepth = "XFF_DEPTH"

	// EnvBodySizeLimit caps request body size, in adapter-node's own
	// size-string format (e.g. "512K", "10M", or "Infinity" to disable).
	// adapter-node defaults to "512K" when unset.
	EnvBodySizeLimit = "BODY_SIZE_LIMIT"
)

// Probe endpoints served by pokkum-init on the probe port.
const (
	ProbePathLive  = "/healthz"
	ProbePathReady = "/readyz"
)

// Image label keys stamped onto every image Pokkum builds. They use the
// standard OCI annotation namespace where one exists so that generic tooling
// can read them, and a pokkum namespace for the rest.
const (
	LabelCreated       = "org.opencontainers.image.created"
	LabelBaseName      = "org.opencontainers.image.base.name"
	LabelBaseDigest    = "org.opencontainers.image.base.digest"
	LabelRevision      = "org.opencontainers.image.revision"
	LabelSource        = "org.opencontainers.image.source"
	LabelVersion       = "org.opencontainers.image.version"
	LabelPokkumVersion = "dev.pokkum.version"
	LabelSupervisor    = "dev.pokkum.supervisor.version"
	LabelBunVersion    = "dev.pokkum.bun.version"
	LabelRequiredEnv   = "dev.pokkum.required-env"

	// AnnotationRequiredEnv is the manifest annotation key for required env contract.
	AnnotationRequiredEnv = "pokkum.dev/required-env"
)

// DefaultLayeredEntrypoint returns the image entrypoint for StrategyLayered:
// the supervisor, "--", the Bun runtime executable, then the application server index.js.
func DefaultLayeredEntrypoint() []string {
	return []string{SupervisorPath, "--", BunBinaryPath, AppServerIndexPath}
}

// DefaultEntrypoint returns the image entrypoint for StrategyExe: the supervisor, a bare "--"
// separator, then the application binary.
func DefaultEntrypoint() []string {
	return []string{SupervisorPath, "--", AppBinaryPath}
}

// DefaultStaticEntrypoint returns the image entrypoint for StrategyStatic: the
// pokkum-static binary, which is its own PID-1 static file server (no
// supervisor orchestration layer is needed).
func DefaultStaticEntrypoint() []string {
	return []string{StaticServerPath}
}

// RuntimeConfig describes the runtime behaviour baked into the image config.
// Its zero value is meaningful: it means "all defaults", and WithDefaults turns
// it into the fully populated form. Only set a field to deviate from the
// contract above.
type RuntimeConfig struct {
	// Entrypoint overrides the image entrypoint. Nil means DefaultEntrypoint().
	// Overriding it is an escape hatch that disables the supervisor; nothing in
	// Pokkum needs it, but a user debugging a container will.
	Entrypoint []string

	// Cmd is appended to Entrypoint as the image's default arguments. Nil means
	// none, which is the normal case since the entrypoint is already complete.
	Cmd []string

	// User overrides the image user. Empty means DefaultUser. Setting this to
	// "0" or "root" will make the image fail runAsNonRoot admission; callers
	// that do it should warn.
	User string

	// WorkingDir overrides the image working directory. Empty means WorkingDir.
	WorkingDir string

	// Port is the application listen port written to PORT. Zero means
	// DefaultPort. It is also the port exposed in the image config.
	Port int

	// ProbePort is the supervisor probe port written to POKKUM_PROBE_PORT.
	// Zero means DefaultProbePort. It must differ from Port.
	ProbePort int

	// ShutdownTimeout is written to POKKUM_SHUTDOWN_TIMEOUT. Zero means
	// DefaultShutdownTimeout. Negative is invalid.
	ShutdownTimeout time.Duration

	// Env is additional environment baked into the image config, as a map so
	// that it is order-independent; the packager must emit it sorted by key so
	// that the image config is byte-identical across runs. Keys here override
	// the values Pokkum would otherwise set for PORT, POKKUM_PROBE_PORT and
	// POKKUM_SHUTDOWN_TIMEOUT, which is how a user pins an unusual value.
	Env map[string]string

	// RequireEnv lists environment variables that must be non-empty at runtime.
	// pokkum-init verifies presence at startup and fails fast if any are missing.
	RequireEnv []string

	// ExposedPorts lists additional TCP ports to declare in the image config.
	// Port and ProbePort are always exposed and need not be repeated. Nil means
	// none.
	ExposedPorts []int

	// Origin is written to ORIGIN, adapter-node's canonical-origin contract
	// variable (see EnvOrigin's doc comment). Empty means unset — adapter-node
	// falls back to its own raw-socket-derived origin, which is wrong behind a
	// TLS-terminating proxy.
	Origin string

	// ProtocolHeader is written to PROTOCOL_HEADER. Empty means unset.
	ProtocolHeader string

	// HostHeader is written to HOST_HEADER. Empty means unset.
	HostHeader string

	// AddressHeader is written to ADDRESS_HEADER. Empty means unset.
	AddressHeader string

	// XFFDepth is written to XFF_DEPTH. Zero means unset — adapter-node's own
	// default (1) applies. Negative is invalid.
	XFFDepth int

	// BodySizeLimit is written to BODY_SIZE_LIMIT verbatim (adapter-node's own
	// size-string format, e.g. "512K", "10M", "Infinity"). Empty means unset —
	// adapter-node's own default ("512K") applies.
	BodySizeLimit string
}

// WithDefaults returns a copy of c with every zero field replaced by its
// contract default. It does not mutate c and does not merge Env — the returned
// Env is the same map reference as c.Env when non-nil, so callers that intend
// to add entries must clone it first.
//
// Adapters should call this once at the top of Build rather than defaulting
// field by field, so that no code path can observe a half-defaulted config.
func (c RuntimeConfig) WithDefaults() RuntimeConfig {
	if c.Entrypoint == nil {
		c.Entrypoint = DefaultEntrypoint()
	}
	if c.User == "" {
		c.User = DefaultUser
	}
	if c.WorkingDir == "" {
		c.WorkingDir = WorkingDir
	}
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.ProbePort == 0 {
		c.ProbePort = DefaultProbePort
	}
	if c.ShutdownTimeout == 0 {
		c.ShutdownTimeout = DefaultShutdownTimeout
	}
	return c
}

// PackageRequest assembles one single-platform image from a base image, the
// supervisor and the compiled application.
type PackageRequest struct {
	// Platform is the target. Required and must match Base and App.
	Platform Platform

	// Base is the resolved base image for Platform, from
	// BaseImageResolver.Resolve. Required and non-nil. The packager appends
	// layers to it and must not mutate it.
	Base v1.Image

	// BaseRef is the human-readable reference the base image was resolved from
	// (for example "gcr.io/distroless/cc-debian12:nonroot"), used to populate
	// the org.opencontainers.image.base.name annotation. Optional.
	//
	// org.opencontainers.image.base.digest is always derived from Base.Digest()
	// regardless of this field, because the digest of the object the layers were
	// actually appended to cannot be wrong the way a caller-supplied string can.
	// base.name has no such authoritative source — a v1.Image does not know the
	// reference it was pulled from — so the caller states it here.
	//
	// If the caller also sets ports.LabelBaseName explicitly in Labels, the
	// label wins over this field: an explicit label is a deliberate override,
	// while BaseRef is just the packager's best-effort default, consistent with
	// every other overlay in this port (RuntimeConfig.Env, PackageRequest.Labels,
	// PackageRequest.Annotations) where the more specific, explicit value always
	// wins over the derived one.
	BaseRef string

	// Strategy selects the packaging strategy (StrategyLayered or StrategyExe).
	Strategy BuildStrategy

	// App is the compiled application binary for Platform (used when Strategy == StrategyExe).
	App Artifact

	// BunRuntime is the resolved Bun runtime binary (used when Strategy == StrategyLayered).
	BunRuntime BunResolverResult

	// AppServerDir is the host directory containing server JS files to package at /app/server (StrategyLayered).
	AppServerDir string

	// AppClientDir is the host directory containing static client files to package at /app/client (StrategyLayered).
	AppClientDir string

	// AppVendorDir is the host directory containing vendor JS dependency modules to package at /app/vendor (StrategyLayered).
	AppVendorDir string

	// AppNativeDir is the host directory containing native .node modules and dynamic .so libraries to package at /app/native (StrategyLayered).
	AppNativeDir string

	// AppPrerenderedDir is the host directory containing prerendered pages to
	// package at /app/prerendered (StrategyLayered and StrategyStatic). The
	// image serves them through POKKUM_PRERENDERED_DIR (layered adapter-node
	// handler) or as a root of pokkum-static. Optional: when empty, no
	// prerendered layer is added.
	AppPrerenderedDir string

	// StaticFallback is the in-image path of the opt-in SPA-fallback page for a
	// StrategyStatic build, e.g. AppClientDirPrefix + "/" + <rel> where <rel>
	// is the leaf filename @sveltejs/adapter-static emitted (set by core from
	// PrepareResult.StaticFallbackRelPath). When non-empty, the packager stamps
	// ports.EnvStaticFallback to this path so pokkum-static serves it with 200
	// on unmatched GET/HEAD routes; the packager also hard-fails if the file is
	// not actually staged under AppClientDir (it must never be silently
	// dropped). Empty (the default) means no SPA fallback.
	StaticFallback string

	// StaticServer is the pokkum-static PID-1 static file server binary for
	// Platform, from StaticServerProvider.Binary. Required and non-empty for
	// StrategyStatic. It becomes a single-file layer at StaticServerPath with
	// mode 0555.
	StaticServer []byte

	// NoPrune disables vendor layer file pruning when building AppVendorDir layer.
	NoPrune bool

	// KeepVendor specifies custom glob patterns to preserve during vendor pruning.
	KeepVendor []string

	// Sourcemap indicates whether sourcemaps should be preserved during packaging.
	Sourcemap bool

	// NoPrecompress disables build-time static asset pre-compression (.gz, .br, .zst).
	NoPrecompress bool

	// NoStrip disables stripping unneeded debug symbols from native .node ELF addons.
	NoStrip bool

	// Compression specifies the layer compression algorithm (CompressionGzip or CompressionZstd).
	Compression CompressionAlgorithm

	// Supervisor is the pokkum-init binary for Platform, from
	// SupervisorProvider.Binary. Required and non-empty. It becomes a
	// single-file layer at SupervisorPath with mode 0555.
	//
	// The supervisor is a separate layer from the application deliberately: it
	// changes only when pokkum itself is upgraded, so keeping it apart means a
	// rebuild of the app re-pushes one layer instead of two.
	Supervisor []byte

	// Runtime describes the image config. The zero value means all defaults.
	Runtime RuntimeConfig

	// CreatedAt is the deterministic build timestamp, from SOURCE_DATE_EPOCH.
	// Required (non-zero). It is written to the image config's created field,
	// to every history entry, and to the mtime of every file in every layer
	// Pokkum adds. Nothing in the packager may read the wall clock.
	CreatedAt time.Time

	// Labels are merged into the image config's labels. Pokkum's own labels
	// (see the Label* constants) are applied first, so a caller can override
	// them here. Nil means none.
	Labels map[string]string

	// Annotations are applied to the image manifest. Nil means none. Note that
	// Docker-media-type images do not carry manifest annotations; the packager
	// should emit OCI media types so that they survive.
	Annotations map[string]string
}

// IndexRequest assembles the multi-platform image index that ties the
// per-platform images together. It is what makes a single tag work on both an
// Apple-silicon laptop and an x86 cluster node.
type IndexRequest struct {
	// Images maps each platform to its packaged image. Required and non-empty.
	// The packager must emit the manifest descriptors in a deterministic order
	// (sorted by Platform.String()) regardless of Go's map iteration order,
	// otherwise the index digest changes between runs and reproducibility is
	// lost.
	Images map[Platform]v1.Image

	// Annotations are applied to the index manifest. Nil means none.
	Annotations map[string]string

	// CreatedAt is the deterministic build timestamp. Required (non-zero).
	CreatedAt time.Time
}

// Packager builds OCI images and indexes in memory. It is implemented by
// internal/adapters/packager.
//
// Nothing here touches a registry, a daemon or the network: a Packager turns
// values into v1.Image and v1.ImageIndex values, and the Registry/LocalLoader/
// TarballWriter ports decide where they end up. That separation is what lets
// `--tarball` and `--local` reuse the identical image bytes that a push would
// have produced.
//
// v1.Image and v1.ImageIndex appear in these signatures deliberately. They are
// the OCI image model, which is the domain of this tool, and they are lazy
// handles rather than materialised bytes — wrapping them would force the whole
// image through memory for no gain. This is the single sanctioned third-party
// leak; syft, cobra, viper, name.Reference and remote.Option must never appear
// in a port signature.
//
// Error expectations: core.ErrUnsupportedPlatform for a bad target,
// core.ErrPackageFailed for a layer or config assembly failure.
//
// Implementations must be safe for concurrent use.
type Packager interface {
	// Build appends the supervisor and application layers to the base image
	// and applies the runtime configuration, returning a single-platform image.
	Build(ctx context.Context, req PackageRequest) (v1.Image, error)

	// Index groups per-platform images into a multi-platform index. Callers
	// with a single platform may still call Index; whether to push an index or
	// a bare manifest in that case is core's decision, not the packager's.
	Index(ctx context.Context, req IndexRequest) (v1.ImageIndex, error)
}

// CompressionAlgorithm names the layer compression format.
type CompressionAlgorithm string

const (
	CompressionGzip CompressionAlgorithm = "gzip"
	CompressionZstd CompressionAlgorithm = "zstd"
)

// Valid reports whether c is a supported compression algorithm.
func (c CompressionAlgorithm) Valid() bool {
	return c == CompressionGzip || c == CompressionZstd || c == ""
}

// Normalize defaults empty compression to CompressionGzip.
func (c CompressionAlgorithm) Normalize() CompressionAlgorithm {
	if c == CompressionZstd {
		return CompressionZstd
	}
	return CompressionGzip
}
