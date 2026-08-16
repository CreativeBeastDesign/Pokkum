// Package config loads, parses, saves, and resolves Pokkum configuration
// from .pokkum.yaml, environment variables, and defaults, implementing the
// required precedence: explicit CLI flag > environment variable > profile override > project config > default.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

const (
	// EnvPrefix is the environment variable prefix for Pokkum configuration.
	EnvPrefix = "POKKUM"

	// ConfigFilename is the name of the configuration file.
	ConfigFilename = ports.ConfigFilename
)

// Manager implements ports.ConfigManager and loads/saves Pokkum configuration.
type Manager struct {
	v          *viper.Viper
	logger     *slog.Logger
	projectDir string
	cfgPath    string
}

// Loader is an alias to Manager for backwards compatibility.
type Loader = Manager

// New creates a new configuration manager.
// It searches for .pokkum.yaml in projectDir first, then in the current working directory.
func New(projectDir string, logger *slog.Logger) (*Manager, error) {
	v := viper.New()

	// Set environment variable prefix and binding
	v.SetEnvPrefix(EnvPrefix)
	v.AutomaticEnv()

	// Replace dots with underscores for env var names
	// Config key "docker.repo" maps to env var "POKKUM_DOCKER_REPO"
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	searchPaths := []string{projectDir, "."}
	configFound := false
	foundPath := ""

	for _, path := range searchPaths {
		if path == "" {
			continue
		}
		v.AddConfigPath(path)
		fullPath := filepath.Join(path, ConfigFilename)
		if _, err := os.Stat(fullPath); err == nil {
			configFound = true
			foundPath = fullPath
			if logger != nil {
				logger.Debug("config file found", "path", fullPath)
			}
			break
		}
	}

	v.SetConfigName(ConfigFilename[:len(ConfigFilename)-len(filepath.Ext(ConfigFilename))]) // ".pokkum"
	v.SetConfigType("yaml")

	if configFound {
		if err := v.ReadInConfig(); err != nil {
			if logger != nil {
				logger.Warn("failed to read config file into viper", "error", err)
			}
		}
	} else if logger != nil {
		logger.Debug("config file not found", "search_paths", searchPaths)
	}

	return &Manager{
		v:          v,
		logger:     logger,
		projectDir: projectDir,
		cfgPath:    foundPath,
	}, nil
}

// Load reads and parses .pokkum.yaml from projectDir (or current directory).
// Returns os.ErrNotExist if no configuration file is found.
func (m *Manager) Load(projectDir string) (*ports.ProjectConfig, error) {
	targetPath := m.cfgPath
	if targetPath == "" {
		if projectDir != "" {
			targetPath = filepath.Join(projectDir, ConfigFilename)
		} else {
			targetPath = ConfigFilename
		}
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		return nil, err
	}

	var cfg ports.ProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", targetPath, err)
	}

	if cfg.Profiles == nil {
		cfg.Profiles = make(map[string]ports.BuildProfile)
	}
	if cfg.Image.Labels == nil {
		cfg.Image.Labels = make(map[string]string)
	}
	if cfg.Image.Annotations == nil {
		cfg.Image.Annotations = make(map[string]string)
	}
	if cfg.Image.Env == nil {
		cfg.Image.Env = make(map[string]string)
	}

	return &cfg, nil
}

// Save marshals and writes a ProjectConfig to .pokkum.yaml in projectDir.
func (m *Manager) Save(projectDir string, cfg *ports.ProjectConfig) error {
	if cfg == nil {
		return fmt.Errorf("config: project config is nil")
	}

	if cfg.Version == 0 {
		cfg.Version = ports.ConfigSchemaVersion
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("config: marshal %s: %w", ConfigFilename, err)
	}

	outPath := filepath.Join(projectDir, ConfigFilename)
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		return fmt.Errorf("config: write %s: %w", outPath, err)
	}

	return nil
}

// ApplyProfile applies a named profile onto a base ProjectConfig and returns a merged copy.
func (m *Manager) ApplyProfile(base *ports.ProjectConfig, profileName string) (*ports.ProjectConfig, error) {
	if base == nil {
		return nil, fmt.Errorf("config: base config is nil")
	}

	profileName = strings.TrimSpace(profileName)
	if profileName == "" {
		return base, nil
	}

	profile, ok := base.Profiles[profileName]
	if !ok {
		return nil, fmt.Errorf("profile %q not found in configuration", profileName)
	}

	// Deep copy base config
	merged := deepCopyProjectConfig(base)

	if profile.Base != "" {
		merged.Base = profile.Base
	}
	if profile.Strategy != "" {
		merged.Strategy = profile.Strategy
	}
	if len(profile.Platforms) > 0 {
		merged.Platforms = append([]string{}, profile.Platforms...)
	}

	// Image overrides
	if profile.Image.Port != 0 {
		merged.Image.Port = profile.Image.Port
	}
	if profile.Image.ProbePort != 0 {
		merged.Image.ProbePort = profile.Image.ProbePort
	}
	if profile.Image.User != "" {
		merged.Image.User = profile.Image.User
	}
	if profile.Image.WorkingDir != "" {
		merged.Image.WorkingDir = profile.Image.WorkingDir
	}
	if profile.Image.ShutdownTimeout != "" {
		merged.Image.ShutdownTimeout = profile.Image.ShutdownTimeout
	}
	if len(profile.Image.Ports) > 0 {
		merged.Image.Ports = append([]int{}, profile.Image.Ports...)
	}
	if len(profile.Image.RequireEnv) > 0 {
		merged.Image.RequireEnv = append([]string{}, profile.Image.RequireEnv...)
	}
	for k, v := range profile.Image.Labels {
		if merged.Image.Labels == nil {
			merged.Image.Labels = make(map[string]string)
		}
		merged.Image.Labels[k] = v
	}
	for k, v := range profile.Image.Annotations {
		if merged.Image.Annotations == nil {
			merged.Image.Annotations = make(map[string]string)
		}
		merged.Image.Annotations[k] = v
	}
	for k, v := range profile.Image.Env {
		if merged.Image.Env == nil {
			merged.Image.Env = make(map[string]string)
		}
		merged.Image.Env[k] = v
	}

	// Security overrides
	if profile.Security.FailOnCVE != "" {
		merged.Security.FailOnCVE = profile.Security.FailOnCVE
	}
	if profile.Security.VerifyBase != nil {
		val := *profile.Security.VerifyBase
		merged.Security.VerifyBase = &val
	}
	if profile.Security.AllowIncompleteScans != nil {
		val := *profile.Security.AllowIncompleteScans
		merged.Security.AllowIncompleteScans = &val
	}
	if len(profile.Security.AllowSecretPatterns) > 0 {
		merged.Security.AllowSecretPatterns = append([]string{}, profile.Security.AllowSecretPatterns...)
	}

	// SBOM overrides
	if profile.SBOM.Format != "" {
		merged.SBOM.Format = profile.SBOM.Format
	}
	if profile.SBOM.Attach != "" {
		merged.SBOM.Attach = profile.SBOM.Attach
	}

	// Cache overrides
	if profile.Cache.Enabled != nil {
		val := *profile.Cache.Enabled
		merged.Cache.Enabled = &val
	}
	if profile.Cache.VerifyMode != "" {
		merged.Cache.VerifyMode = profile.Cache.VerifyMode
	}
	if profile.Cache.Pubkey != "" {
		merged.Cache.Pubkey = profile.Cache.Pubkey
	}
	if profile.Cache.KeylessIdentity != "" {
		merged.Cache.KeylessIdentity = profile.Cache.KeylessIdentity
	}
	if profile.Cache.KeylessIssuer != "" {
		merged.Cache.KeylessIssuer = profile.Cache.KeylessIssuer
	}

	// OTel overrides
	if profile.OTel.Sidecar != nil {
		val := *profile.OTel.Sidecar
		merged.OTel.Sidecar = &val
	}
	if profile.OTel.Tracing != nil {
		val := *profile.OTel.Tracing
		merged.OTel.Tracing = &val
	}
	if profile.OTel.Metrics != nil {
		val := *profile.OTel.Metrics
		merged.OTel.Metrics = &val
	}

	return merged, nil
}

// GenerateDefault creates a new ProjectConfig template based on InitConfigOptions.
func (m *Manager) GenerateDefault(opts ports.InitConfigOptions) *ports.ProjectConfig {
	basePreset := opts.BasePreset
	if basePreset == "" {
		basePreset = "distroless"
	}
	strategy := opts.Strategy
	if strategy == "" {
		strategy = "layered"
	}

	cfg := &ports.ProjectConfig{
		Version: ports.ConfigSchemaVersion,
		Docker: ports.DockerConfig{
			Repo: opts.Repo,
		},
		Strategy:  strategy,
		Base:      basePreset,
		Platforms: []string{"linux/amd64", "linux/arm64"},
		Security: ports.SecurityConfig{
			FailOnCVE: opts.FailOnCVE,
		},
		SBOM: ports.SBOMConfig{
			Format: "spdx-json",
			Attach: "attestation",
		},
		Profiles: make(map[string]ports.BuildProfile),
	}

	if opts.EnableLocalProfile {
		f := false
		cfg.Profiles["local"] = ports.BuildProfile{
			Output:    "local",
			Platforms: []string{"local"},
			Sourcemap: boolPtr(true),
			Security: ports.SecurityConfig{
				VerifyBase: &f,
			},
			SBOM: ports.SBOMConfig{
				Format: "none",
			},
		}
	}

	return cfg
}

func boolPtr(b bool) *bool {
	return &b
}

func deepCopyProjectConfig(src *ports.ProjectConfig) *ports.ProjectConfig {
	if src == nil {
		return nil
	}
	dst := *src

	if len(src.Platforms) > 0 {
		dst.Platforms = append([]string{}, src.Platforms...)
	}

	// Clone maps
	if src.Image.Labels != nil {
		dst.Image.Labels = make(map[string]string, len(src.Image.Labels))
		for k, v := range src.Image.Labels {
			dst.Image.Labels[k] = v
		}
	}
	if src.Image.Annotations != nil {
		dst.Image.Annotations = make(map[string]string, len(src.Image.Annotations))
		for k, v := range src.Image.Annotations {
			dst.Image.Annotations[k] = v
		}
	}
	if src.Image.Env != nil {
		dst.Image.Env = make(map[string]string, len(src.Image.Env))
		for k, v := range src.Image.Env {
			dst.Image.Env[k] = v
		}
	}
	if len(src.Image.Ports) > 0 {
		dst.Image.Ports = append([]int{}, src.Image.Ports...)
	}
	if len(src.Image.RequireEnv) > 0 {
		dst.Image.RequireEnv = append([]string{}, src.Image.RequireEnv...)
	}

	if src.Security.VerifyBase != nil {
		v := *src.Security.VerifyBase
		dst.Security.VerifyBase = &v
	}
	if src.Security.AllowIncompleteScans != nil {
		v := *src.Security.AllowIncompleteScans
		dst.Security.AllowIncompleteScans = &v
	}
	if len(src.Security.AllowSecretPatterns) > 0 {
		dst.Security.AllowSecretPatterns = append([]string{}, src.Security.AllowSecretPatterns...)
	}

	if src.Cache.Enabled != nil {
		v := *src.Cache.Enabled
		dst.Cache.Enabled = &v
	}

	if src.OTel.Sidecar != nil {
		v := *src.OTel.Sidecar
		dst.OTel.Sidecar = &v
	}
	if src.OTel.Tracing != nil {
		v := *src.OTel.Tracing
		dst.OTel.Tracing = &v
	}
	if src.OTel.Metrics != nil {
		v := *src.OTel.Metrics
		dst.OTel.Metrics = &v
	}

	if src.Profiles != nil {
		dst.Profiles = make(map[string]ports.BuildProfile, len(src.Profiles))
		for k, v := range src.Profiles {
			dst.Profiles[k] = v
		}
	}

	return &dst
}

// GetString retrieves a string configuration value with precedence:
// explicit value (if non-empty) > environment variable > config file > default.
func (m *Manager) GetString(key, defaultValue string) string {
	if m.v != nil {
		if val := m.v.GetString(key); val != "" {
			if m.logger != nil {
				m.logger.Debug("config value from environment or file", "key", key)
			}
			return val
		}
	}
	if m.logger != nil {
		m.logger.Debug("config value from default", "key", key)
	}
	return defaultValue
}

// GetStringSlice retrieves a string slice configuration value with precedence.
func (m *Manager) GetStringSlice(key string, defaultValue []string) []string {
	if m.v != nil {
		if val := m.v.GetStringSlice(key); len(val) > 0 {
			if m.logger != nil {
				m.logger.Debug("config value from environment or file", "key", key)
			}
			return val
		}
	}
	if m.logger != nil {
		m.logger.Debug("config value from default", "key", key)
	}
	return defaultValue
}

// GetBool retrieves a boolean configuration value with precedence.
func (m *Manager) GetBool(key string, defaultValue bool) bool {
	if m.v != nil && m.v.IsSet(key) {
		val := m.v.GetBool(key)
		if m.logger != nil {
			m.logger.Debug("config value from environment or file", "key", key, "value", val)
		}
		return val
	}
	if m.logger != nil {
		m.logger.Debug("config value from default", "key", key)
	}
	return defaultValue
}

// ResolveBuildTimestamp resolves the SOURCE_DATE_EPOCH for reproducible builds.
func (m *Manager) ResolveBuildTimestamp() (time.Time, error) {
	if val := os.Getenv("SOURCE_DATE_EPOCH"); val != "" {
		t, err := core.ParseSourceDateEpoch(val)
		if err != nil {
			return time.Time{}, err
		}
		if m.logger != nil {
			m.logger.Debug("source date epoch from environment variable", "timestamp", t.Unix())
		}
		return t, nil
	}

	cmd := "git"
	args := []string{"log", "-1", "--pretty=%ct"}
	out, err := runCommand(cmd, args)
	if err == nil && strings.TrimSpace(out) != "" {
		parsed, err := core.ParseSourceDateEpoch(strings.TrimSpace(out))
		if err == nil {
			if m.logger != nil {
				m.logger.Debug("source date epoch from git commit time", "timestamp", parsed.Unix())
			}
			return parsed, nil
		}
	}

	if m.logger != nil {
		m.logger.Debug("could not determine source date epoch from git, using epoch")
	}
	return time.Time{}, nil
}

func runCommand(cmd string, args []string) (string, error) {
	out, err := exec.Command(cmd, args...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
