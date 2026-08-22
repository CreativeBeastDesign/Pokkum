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
	// This filters prerendered *files*. A server-rendered route on the layered
	// strategy is compiled into the server bundle and cannot be removed by
	// dropping a file, so a pattern naming one is reported as unmatched rather
	// than silently treated as excluded.
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
