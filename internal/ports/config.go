package ports

import "time"

// ConfigSchemaVersion is the current version of the .pokkum.yaml configuration file format.
const ConfigSchemaVersion = 1

// ConfigFilename is the default configuration file name.
const ConfigFilename = ".pokkum.yaml"

// DockerConfig holds target repository and push configuration.
type DockerConfig struct {
	Repo string `yaml:"repo,omitempty" json:"repo,omitempty"`

	// Tags are the image tags to apply, without the repository prefix. Empty
	// means core.DefaultTag ("latest"). May be overridden per-profile, same
	// as Repo.
	Tags []string `yaml:"tags,omitempty" json:"tags,omitempty"`
}

// ImageConfig holds OCI image metadata and runtime parameters.
type ImageConfig struct {
	Labels          map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Annotations     map[string]string `yaml:"annotations,omitempty" json:"annotations,omitempty"`
	Env             map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	RequireEnv      []string          `yaml:"require_env,omitempty" json:"require_env,omitempty"`
	Ports           []int             `yaml:"ports,omitempty" json:"ports,omitempty"`
	User            string            `yaml:"user,omitempty" json:"user,omitempty"`
	WorkingDir      string            `yaml:"working_dir,omitempty" json:"working_dir,omitempty"`
	Port            int               `yaml:"port,omitempty" json:"port,omitempty"`
	ProbePort       int               `yaml:"probe_port,omitempty" json:"probe_port,omitempty"`
	ShutdownTimeout string            `yaml:"shutdown_timeout,omitempty" json:"shutdown_timeout,omitempty"`

	// Origin and the headers/limits below mirror adapter-node's reverse-proxy
	// contract (RuntimeConfig.Origin's doc comment in packager.go has the
	// full rationale). All optional; each falls back to adapter-node's own
	// default when unset.
	Origin         string `yaml:"origin,omitempty" json:"origin,omitempty"`
	ProtocolHeader string `yaml:"protocol_header,omitempty" json:"protocol_header,omitempty"`
	HostHeader     string `yaml:"host_header,omitempty" json:"host_header,omitempty"`
	AddressHeader  string `yaml:"address_header,omitempty" json:"address_header,omitempty"`
	XFFDepth       int    `yaml:"xff_depth,omitempty" json:"xff_depth,omitempty"`
	BodySizeLimit  string `yaml:"body_size_limit,omitempty" json:"body_size_limit,omitempty"`
}

// BuildConfig holds settings that shape what the build produces, as opposed to
// how the resulting image is configured at runtime.
type BuildConfig struct {
	// ExcludeRoutes drops prerendered routes from the packaged output. Each
	// entry is an absolute route path; a bare path covers the route and its
	// subtree ("/storybook" excludes /storybook/button), "*" matches within a
	// segment and "**" across segments.
	//
	// Applied at build time where Pokkum authors the project's Vite config
	// (kit.files.routes is pointed at a filtered mirror, so the route's code
	// never enters the bundle). Otherwise only the route's prerendered output
	// is removed and the build warns that its code still ships.
	ExcludeRoutes []string `yaml:"exclude_routes,omitempty" json:"exclude_routes,omitempty"`
}

// SecurityConfig holds security scanning and validation policies.
type SecurityConfig struct {
	FailOnCVE            string               `yaml:"fail_on_cve,omitempty" json:"fail_on_cve,omitempty"`
	VerifyBase           *bool                `yaml:"verify_base,omitempty" json:"verify_base,omitempty"`
	AllowIncompleteScans *bool                `yaml:"allow_incomplete_scans,omitempty" json:"allow_incomplete_scans,omitempty"`
	AllowSecretPatterns  []string             `yaml:"allow_secret_patterns,omitempty" json:"allow_secret_patterns,omitempty"`
	VEXExemptions        []VEXExemptionConfig `yaml:"vex_exemptions,omitempty" json:"vex_exemptions,omitempty"`
}

// VEXExemptionConfig is the .pokkum.yaml shape of a VEXExemption — see that
// type's doc comment (internal/ports/vex.go) for what each field means and
// why Expires/Owner are mandatory. Expires is a string here (RFC3339 or a
// plain "2006-01-02" date) because YAML has no native date type; it is
// parsed and validated by internal/adapters/config.
type VEXExemptionConfig struct {
	CVE           string `yaml:"cve" json:"cve"`
	Package       string `yaml:"package,omitempty" json:"package,omitempty"`
	Justification string `yaml:"justification" json:"justification"`
	StatusNotes   string `yaml:"status_notes,omitempty" json:"status_notes,omitempty"`
	Expires       string `yaml:"expires" json:"expires"`
	Owner         string `yaml:"owner" json:"owner"`
}

// SBOMConfig holds software bill-of-materials settings.
type SBOMConfig struct {
	Format string `yaml:"format,omitempty" json:"format,omitempty"`
	Attach string `yaml:"attach,omitempty" json:"attach,omitempty"`
}

// CacheConfig holds local and remote layer caching settings.
type CacheConfig struct {
	Enabled         *bool  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	VerifyMode      string `yaml:"verify_mode,omitempty" json:"verify_mode,omitempty"`
	Pubkey          string `yaml:"pubkey,omitempty" json:"pubkey,omitempty"`
	KeylessIdentity string `yaml:"keyless_identity,omitempty" json:"keyless_identity,omitempty"`
	KeylessIssuer   string `yaml:"keyless_issuer,omitempty" json:"keyless_issuer,omitempty"`
}

// DeployConfig points a build at a PaaS control plane, so that a successful
// push can hand the image straight to Dokploy or SwiftWave instead of the
// operator wiring a separate CI step.
//
// The token is deliberately absent. TokenEnv names an environment variable to
// read the credential from at deploy time; there is no field that holds the
// secret itself, because .pokkum.yaml is a committed file and Pokkum's own
// secretguard exists to stop exactly this. A config that tried to carry the
// token would be a credential in source control recommended by the tool that
// scans for credentials in source control.
type DeployConfig struct {
	// Target is the platform: "dokploy" or "swiftwave". Empty disables
	// deployment entirely, and is the default.
	Target string `yaml:"target,omitempty" json:"target,omitempty"`

	// Method is "api" or "webhook". Empty means the target's default
	// (core.DefaultDeployMethod), which is "api" for Dokploy and "webhook"
	// for SwiftWave.
	Method string `yaml:"method,omitempty" json:"method,omitempty"`

	// Endpoint is the panel's base URL for method "api", or the complete
	// per-application webhook URL for method "webhook".
	//
	// A webhook URL contains its own secret, so it belongs in EndpointEnv
	// rather than here; both are accepted and EndpointEnv wins, so that the
	// committed file can stay secret-free.
	Endpoint string `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`

	// EndpointEnv names an environment variable holding Endpoint. Takes
	// precedence over Endpoint when the variable is set and non-empty.
	EndpointEnv string `yaml:"endpoint_env,omitempty" json:"endpoint_env,omitempty"`

	// Application is the platform-side application id. Required for method
	// "api"; ignored for "webhook", where the id is already in the URL.
	Application string `yaml:"application,omitempty" json:"application,omitempty"`

	// TokenEnv names the environment variable holding the API credential.
	// Empty means core.DefaultDeployTokenEnv ("POKKUM_DEPLOY_TOKEN").
	TokenEnv string `yaml:"token_env,omitempty" json:"token_env,omitempty"`

	// Auto controls whether a successful `pokkum build` deploys on its own.
	// Nil means true whenever Target is set: configuring a target is the
	// opt-in. Set false to keep the config but require an explicit
	// `pokkum deploy`.
	//
	// Auto-deploy never fires for a dry run, a non-push output mode, or a
	// build that failed — see core.ShouldAutoDeploy.
	Auto *bool `yaml:"auto,omitempty" json:"auto,omitempty"`

	// UpdateImage repoints the application at the exact digest just pushed
	// before triggering the rollout, instead of redeploying whatever reference
	// the platform already holds. Nil means false.
	//
	// Only Dokploy's API supports it; core rejects it elsewhere rather than
	// accepting the field and silently ignoring it.
	//
	// It defaults OFF because of how Dokploy implements it. Its
	// application.saveDockerProvider endpoint is a full overwrite, not a
	// patch: the handler writes dockerImage, username, password AND
	// registryUrl from the request on every call, and its input schema marks
	// all of them required — so a request that omits the credentials is
	// rejected, and one that sends nulls CLEARS the registry credentials the
	// application uses to pull. Enabling update_image without setting
	// RegistryUsernameEnv/RegistryPasswordEnv is therefore only safe for an
	// image a public pull can reach.
	UpdateImage *bool `yaml:"update_image,omitempty" json:"update_image,omitempty"`

	// RegistryURL, RegistryUsernameEnv and RegistryPasswordEnv carry the
	// credentials the platform needs to pull the image, for targets whose
	// image-update call rewrites them (see UpdateImage). The username and
	// password are named indirectly, as environment variables, for the same
	// reason TokenEnv is.
	//
	// Leave all three empty for a publicly pullable image; Pokkum then warns
	// that any stored credentials are being cleared rather than clearing them
	// silently.
	RegistryURL         string `yaml:"registry_url,omitempty" json:"registry_url,omitempty"`
	RegistryUsernameEnv string `yaml:"registry_username_env,omitempty" json:"registry_username_env,omitempty"`
	RegistryPasswordEnv string `yaml:"registry_password_env,omitempty" json:"registry_password_env,omitempty"`

	// Timeout bounds the deploy call, as a Go duration string ("60s", "2m").
	// Empty means ports.DefaultDeployTimeout.
	Timeout string `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// OTelConfig holds OpenTelemetry sidecar and tracing options.
type OTelConfig struct {
	Sidecar *bool `yaml:"sidecar,omitempty" json:"sidecar,omitempty"`
	Tracing *bool `yaml:"tracing,omitempty" json:"tracing,omitempty"`
	Metrics *bool `yaml:"metrics,omitempty" json:"metrics,omitempty"`
}

// BuildProfile defines partial overrides for build parameters.
type BuildProfile struct {
	Output    string   `yaml:"output,omitempty" json:"output,omitempty"`
	Platforms []string `yaml:"platforms,omitempty" json:"platforms,omitempty"`
	Base      string   `yaml:"base,omitempty" json:"base,omitempty"`
	Strategy  string   `yaml:"strategy,omitempty" json:"strategy,omitempty"`

	// Runtime overrides the top-level runtime ("bun" or "node") for this
	// profile. Empty means inherit.
	Runtime      string         `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	StubLauncher *bool          `yaml:"stub_launcher,omitempty" json:"stub_launcher,omitempty"`
	Sourcemap    *bool          `yaml:"sourcemap,omitempty" json:"sourcemap,omitempty"`
	Docker       DockerConfig   `yaml:"docker,omitempty" json:"docker,omitempty"`
	Image        ImageConfig    `yaml:"image,omitempty" json:"image,omitempty"`
	Build        BuildConfig    `yaml:"build,omitempty" json:"build,omitempty"`
	Security     SecurityConfig `yaml:"security,omitempty" json:"security,omitempty"`
	SBOM         SBOMConfig     `yaml:"sbom,omitempty" json:"sbom,omitempty"`
	Cache        CacheConfig    `yaml:"cache,omitempty" json:"cache,omitempty"`
	OTel         OTelConfig     `yaml:"otel,omitempty" json:"otel,omitempty"`
	Deploy       DeployConfig   `yaml:"deploy,omitempty" json:"deploy,omitempty"`
}

// ProjectConfig represents the root .pokkum.yaml configuration structure.
type ProjectConfig struct {
	Version  int          `yaml:"version" json:"version"`
	Docker   DockerConfig `yaml:"docker,omitempty" json:"docker,omitempty"`
	Strategy string       `yaml:"strategy,omitempty" json:"strategy,omitempty"`

	// Runtime is the image's application runtime ("bun" or "node"),
	// mirroring the --runtime flag. Empty means bun.
	Runtime      string                  `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	StubLauncher *bool                   `yaml:"stub_launcher,omitempty" json:"stub_launcher,omitempty"`
	Base         string                  `yaml:"base,omitempty" json:"base,omitempty"`
	Platforms    []string                `yaml:"platforms,omitempty" json:"platforms,omitempty"`
	Image        ImageConfig             `yaml:"image,omitempty" json:"image,omitempty"`
	Build        BuildConfig             `yaml:"build,omitempty" json:"build,omitempty"`
	Security     SecurityConfig          `yaml:"security,omitempty" json:"security,omitempty"`
	SBOM         SBOMConfig              `yaml:"sbom,omitempty" json:"sbom,omitempty"`
	Cache        CacheConfig             `yaml:"cache,omitempty" json:"cache,omitempty"`
	OTel         OTelConfig              `yaml:"otel,omitempty" json:"otel,omitempty"`
	Deploy       DeployConfig            `yaml:"deploy,omitempty" json:"deploy,omitempty"`
	Profiles     map[string]BuildProfile `yaml:"profiles,omitempty" json:"profiles,omitempty"`
}

// InitConfigOptions provides parameters for bootstrapping a new .pokkum.yaml.
type InitConfigOptions struct {
	Repo               string
	BasePreset         string
	Strategy           string
	EnableLocalProfile bool
	FailOnCVE          string
}

// ConfigManager is the port interface for loading, saving, and resolving Pokkum configurations.
type ConfigManager interface {
	Load(projectDir string) (*ProjectConfig, error)
	Save(projectDir string, cfg *ProjectConfig) error
	ApplyProfile(base *ProjectConfig, profileName string) (*ProjectConfig, error)
	GenerateDefault(opts InitConfigOptions) *ProjectConfig
	ResolveBuildTimestamp() (time.Time, error)
}
