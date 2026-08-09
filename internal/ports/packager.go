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
)

// DefaultEntrypoint returns the image entrypoint: the supervisor, a bare "--"
// separator, then the application binary. The separator exists so that
// pokkum-init can distinguish its own flags from the child's argv without
// ambiguity, and so that a user-supplied argument that happens to look like a
// supervisor flag cannot be misinterpreted.
//
// It returns a fresh slice on every call because v1 image configs are mutated
// in place by go-containerregistry helpers and a shared package-level slice
// would be an aliasing hazard.
func DefaultEntrypoint() []string {
	return []string{SupervisorPath, "--", AppBinaryPath}
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

	// ExposedPorts lists additional TCP ports to declare in the image config.
	// Port and ProbePort are always exposed and need not be repeated. Nil means
	// none.
	ExposedPorts []int
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

	// App is the compiled application binary for Platform. Required. It becomes
	// a single-file layer at AppBinaryPath with mode 0555.
	App Artifact

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
