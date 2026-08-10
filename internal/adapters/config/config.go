// Package config loads Pokkum configuration from environment, config files,
// and defaults, implementing the required precedence: explicit flag > environment
// variable > config file > default.
package config

import (
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

const (
	// EnvPrefix is the environment variable prefix for Pokkum configuration.
	EnvPrefix = "POKKUM"

	// ConfigFilename is the name of the configuration file.
	ConfigFilename = ".pokkum.yaml"
)

// Loader loads configuration from environment, config files, and defaults.
type Loader struct {
	v      *viper.Viper
	logger *slog.Logger
}

// New creates a new configuration loader.
// It searches for .pokkum.yaml in projectDir first, then in the current working directory.
func New(projectDir string, logger *slog.Logger) (*Loader, error) {
	v := viper.New()

	// Set environment variable prefix and binding
	v.SetEnvPrefix(EnvPrefix)
	v.AutomaticEnv()

	// Replace dots with underscores for env var names
	// Config key "docker.repo" maps to env var "POKKUM_DOCKER_REPO"
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Search for config file in project directory first, then current directory
	searchPaths := []string{projectDir, "."}
	configFound := false

	for _, path := range searchPaths {
		if path == "" {
			continue
		}
		v.AddConfigPath(path)
		if _, err := os.Stat(filepath.Join(path, ConfigFilename)); err == nil {
			configFound = true
			logger.Debug("config file found", "path", filepath.Join(path, ConfigFilename))
			break
		}
	}

	v.SetConfigName(ConfigFilename[:len(ConfigFilename)-len(filepath.Ext(ConfigFilename))]) // ".pokkum"
	v.SetConfigType("yaml")

	// Try to read config file if it exists; ignore errors if it doesn't
	if configFound {
		if err := v.ReadInConfig(); err != nil {
			logger.Warn("failed to read config file", "error", err)
		}
	} else {
		logger.Debug("config file not found", "search_paths", searchPaths)
	}

	return &Loader{v: v, logger: logger}, nil
}

// GetString retrieves a string configuration value with precedence:
// explicit value (if non-empty) > environment variable > config file > default.
func (l *Loader) GetString(key, defaultValue string) string {
	// Environment variable (POKKUM_KEY format)
	if val := l.v.GetString(key); val != "" {
		l.logger.Debug("config value from environment or file", "key", key)
		return val
	}
	l.logger.Debug("config value from default", "key", key)
	return defaultValue
}

// GetStringSlice retrieves a string slice configuration value with precedence.
func (l *Loader) GetStringSlice(key string, defaultValue []string) []string {
	if val := l.v.GetStringSlice(key); len(val) > 0 {
		l.logger.Debug("config value from environment or file", "key", key)
		return val
	}
	l.logger.Debug("config value from default", "key", key)
	return defaultValue
}

// GetBool retrieves a boolean configuration value with precedence.
func (l *Loader) GetBool(key string, defaultValue bool) bool {
	if l.v.IsSet(key) {
		val := l.v.GetBool(key)
		l.logger.Debug("config value from environment or file", "key", key, "value", val)
		return val
	}
	l.logger.Debug("config value from default", "key", key)
	return defaultValue
}

// ResolveBuildTimestamp resolves the SOURCE_DATE_EPOCH for reproducible builds.
// It tries in order:
// 1. Explicit SOURCE_DATE_EPOCH environment variable (parsed via core.ParseSourceDateEpoch)
// 2. Last git commit time (git log -1 --pretty=%ct)
// 3. Zero (epoch), which core.Normalize will replace with the Unix epoch
//
// This function logs the source of the timestamp at Debug level and tolerates
// the project not being a git repo.
func (l *Loader) ResolveBuildTimestamp() (time.Time, error) {
	// Check for explicit SOURCE_DATE_EPOCH environment variable
	if val := os.Getenv("SOURCE_DATE_EPOCH"); val != "" {
		t, err := core.ParseSourceDateEpoch(val)
		if err != nil {
			return time.Time{}, err
		}
		l.logger.Debug("source date epoch from environment variable", "timestamp", t.Unix())
		return t, nil
	}

	// Try to get git commit time
	cmd := "git"
	args := []string{"log", "-1", "--pretty=%ct"}
	out, err := runCommand(cmd, args)
	if err == nil && strings.TrimSpace(out) != "" {
		// Parse the git timestamp (Unix timestamp in seconds)
		parsed, err := core.ParseSourceDateEpoch(strings.TrimSpace(out))
		if err == nil {
			l.logger.Debug("source date epoch from git commit time", "timestamp", parsed.Unix())
			return parsed, nil
		}
	}

	// Git not available or error; log at Debug level and use zero (which becomes epoch)
	l.logger.Debug("could not determine source date epoch from git, using epoch")
	return time.Time{}, nil
}

// runCommand executes a command and returns its output as a string.
// This is a helper to avoid importing os/exec details in the main config logic.
func runCommand(cmd string, args []string) (string, error) {
	out, err := exec.Command(cmd, args...).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
