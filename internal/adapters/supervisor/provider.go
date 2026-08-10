// Package supervisor provides the embedded pokkum-init supervisor binary for
// all supported platforms.
package supervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"log/slog"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

//go:embed all:bin
var binaries embed.FS

// Provider is the SupervisorProvider implementation, supplying the embedded
// pokkum-init binaries to the packager.
type Provider struct {
	logger *slog.Logger
}

// New creates a new supervisor provider. If logger is nil, slog.Default() is used.
func New(logger *slog.Logger) *Provider {
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{logger: logger}
}

// Binary returns the complete contents of the pokkum-init executable for the
// given platform. It returns a defensive copy so that the caller may mutate
// the returned slice without affecting subsequent calls.
func (p *Provider) Binary(_ context.Context, plat ports.Platform) ([]byte, error) {
	// Validate that the platform is supported
	if !plat.Supported() {
		return nil, fmt.Errorf("supervisor: unsupported platform %q: %w", plat, core.ErrUnsupportedPlatform)
	}

	// Map platform to binary filename
	var binPath string
	switch plat {
	case ports.LinuxAMD64:
		binPath = "bin/pokkum-init-linux-amd64"
	case ports.LinuxARM64:
		binPath = "bin/pokkum-init-linux-arm64"
	default:
		// Defensive: Supported() already checked this, but be explicit
		return nil, fmt.Errorf("supervisor: unsupported platform %q: %w", plat, core.ErrUnsupportedPlatform)
	}

	// Read the embedded binary
	data, err := binaries.ReadFile(binPath)
	if err != nil {
		p.logger.Debug("supervisor binary not embedded", "platform", plat, "error", err)
		return nil, fmt.Errorf("supervisor: %s binary unavailable for %q; run `make supervisor` to build: %w", binPath, plat, core.ErrSupervisorUnavailable)
	}

	// go:embed hands out the same backing array on every call, so return a copy
	// and let callers treat the result as their own.
	return bytes.Clone(data), nil
}

// Version returns the SHA256 digest of the embedded amd64 binary as the
// supervisor version. This provides a reproducible version string that changes
// exactly when the binary contents change.
func (p *Provider) Version(_ context.Context) (string, error) {
	data, err := binaries.ReadFile("bin/pokkum-init-linux-amd64")
	if err != nil {
		// Version is optional; return empty string as documented
		p.logger.Debug("supervisor binary not embedded, version unknown", "error", err)
		return "", nil
	}

	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash), nil
}
