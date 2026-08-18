// Package config loads, parses, saves, and resolves Pokkum configuration
// from .pokkum.yaml, environment variables, and defaults, implementing the
// required precedence: explicit CLI flag > environment variable > profile override > project config > default.
//
// There is no generic dotted-key -> POKKUM_* environment binding here: each
// field that has an environment override reads it explicitly via os.Getenv
// at its own call site (see cmd/pokkum/build.go). Only a fixed, documented
// subset of fields has an override at all (docker.repo, docker.tags,
// security.fail_on_cve, cache.verify_mode, cache.pubkey,
// cache.keyless_identity, cache.keyless_issuer, plus the per-profile
// sourcemap setting) — see Vocabulary.md for the authoritative list.
// Everything else is config-file or flag only.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// ConfigFilename is the name of the configuration file.
const ConfigFilename = ports.ConfigFilename

// Manager implements ports.ConfigManager and loads/saves Pokkum configuration.
type Manager struct {
	logger     *slog.Logger
	projectDir string
	cfgPath    string
}

// Loader is an alias to Manager for backwards compatibility.
type Loader = Manager

// New creates a new configuration manager.
// It searches for .pokkum.yaml in projectDir first, then in the current working directory.
func New(projectDir string, logger *slog.Logger) (*Manager, error) {
	searchPaths := []string{projectDir, "."}
	foundPath := ""

	for _, path := range searchPaths {
		if path == "" {
			continue
		}
		fullPath := filepath.Join(path, ConfigFilename)
		if _, err := os.Stat(fullPath); err == nil {
			foundPath = fullPath
			if logger != nil {
				logger.Debug("config file found", "path", fullPath)
			}
			break
		}
	}

	if foundPath == "" && logger != nil {
		logger.Debug("config file not found", "search_paths", searchPaths)
	}

	return &Manager{
		logger:     logger,
		projectDir: projectDir,
		cfgPath:    foundPath,
	}, nil
}

// Load reads and parses .pokkum.yaml from projectDir (or current directory).
// Returns os.ErrNotExist if no configuration file is found.
//
// Parsing rejects unknown top-level or nested keys (e.g. a typo'd
// "strategey:" instead of "strategy:") rather than silently dropping them,
// matching this repo's fail-fast-before-any-network-call convention.
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
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
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
	if profile.Runtime != "" {
		merged.Runtime = profile.Runtime
	}
	if profile.StubLauncher != nil {
		val := *profile.StubLauncher
		merged.StubLauncher = &val
	}
	if len(profile.Platforms) > 0 {
		merged.Platforms = append([]string{}, profile.Platforms...)
	}
	if profile.Docker.Repo != "" {
		merged.Docker.Repo = profile.Docker.Repo
	}
	if len(profile.Docker.Tags) > 0 {
		merged.Docker.Tags = append([]string{}, profile.Docker.Tags...)
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
	if profile.Image.Origin != "" {
		merged.Image.Origin = profile.Image.Origin
	}
	if profile.Image.ProtocolHeader != "" {
		merged.Image.ProtocolHeader = profile.Image.ProtocolHeader
	}
	if profile.Image.HostHeader != "" {
		merged.Image.HostHeader = profile.Image.HostHeader
	}
	if profile.Image.AddressHeader != "" {
		merged.Image.AddressHeader = profile.Image.AddressHeader
	}
	if profile.Image.XFFDepth != 0 {
		merged.Image.XFFDepth = profile.Image.XFFDepth
	}
	if profile.Image.BodySizeLimit != "" {
		merged.Image.BodySizeLimit = profile.Image.BodySizeLimit
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
	if len(profile.Security.VEXExemptions) > 0 {
		merged.Security.VEXExemptions = append([]ports.VEXExemptionConfig{}, profile.Security.VEXExemptions...)
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

	if src.StubLauncher != nil {
		v := *src.StubLauncher
		dst.StubLauncher = &v
	}

	if len(src.Platforms) > 0 {
		dst.Platforms = append([]string{}, src.Platforms...)
	}
	if len(src.Docker.Tags) > 0 {
		dst.Docker.Tags = append([]string{}, src.Docker.Tags...)
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
	if len(src.Security.VEXExemptions) > 0 {
		dst.Security.VEXExemptions = append([]ports.VEXExemptionConfig{}, src.Security.VEXExemptions...)
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
