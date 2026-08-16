// Package supervisor provides the embedded pokkum-init supervisor binary for
// all supported platforms. The binaries are embedded zstd-compressed (built by
// `make supervisor`) and decompressed on-the-fly by Binary and Version, which
// keeps the pokkum CLI footprint down while preserving the raw ELF bytes the
// packager writes into the image.
package supervisor

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/embeddedbinaryutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

//go:embed all:bin
var binaries embed.FS

// errSupervisorCorrupt is reported when an embedded supervisor binary is present
// but cannot be decompressed. This is distinct from core.ErrSupervisorUnavailable
// (which means the asset is absent): a present-but-unusable blob is a build defect,
// not a "not built yet" condition, so it must not be conflated with the sentinel a
// fresh checkout's missing-asset path produces.
var errSupervisorCorrupt = errors.New("supervisor binary corrupt")

// decodeSupervisor decompresses the zstd-framed embedded representation of a
// pokkum-init binary back to the raw ELF, via the shared decode mechanism in
// embeddedbinaryutils (also used by internal/adapters/staticserver). It is a
// thin seam so the compression round-trip and corruption paths are directly
// testable without requiring the generated .zst assets to be present at build
// time.
func decodeSupervisor(compressed []byte) ([]byte, error) {
	return embeddedbinaryutils.Decode(supervisorDecoder(), compressed, errSupervisorCorrupt)
}

// supervisorDecoder lazily builds the single shared zstd decoder used for all
// on-the-fly supervisor decompression (Provider.Binary may be called
// concurrently for different platforms; see embeddedbinaryutils.NewDecoder
// for why one process-wide decoder is safe here).
var supervisorDecoder = sync.OnceValue(embeddedbinaryutils.NewDecoder)

var _ ports.SupervisorProvider = (*Provider)(nil)

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
// given platform, transparently decompressing the embedded zstd-compressed blob
// so the caller receives the raw ELF. The returned slice is owned by the caller
// and may be retained and read freely.
func (p *Provider) Binary(_ context.Context, plat ports.Platform) ([]byte, error) {
	// Validate that the platform is supported
	if !plat.Supported() {
		return nil, fmt.Errorf("supervisor: unsupported platform %q: %w", plat, core.ErrUnsupportedPlatform)
	}

	// Map platform to compressed binary filename. The embedded representation is
	// zstd-compressed (see Makefile `supervisor` target) to shrink the pokkum CLI
	// by ~7.3 MB; Binary decompresses it on-the-fly so the returned bytes are the
	// bit-identical raw ELF the packager writes to /pokkum/init.
	var binPath string
	switch plat {
	case ports.LinuxAMD64:
		binPath = "bin/pokkum-init-linux-amd64.zst"
	case ports.LinuxARM64:
		binPath = "bin/pokkum-init-linux-arm64.zst"
	default:
		// Defensive: Supported() already checked this, but be explicit
		return nil, fmt.Errorf("supervisor: unsupported platform %q: %w", plat, core.ErrUnsupportedPlatform)
	}

	// Read the embedded compressed binary.
	compressed, err := binaries.ReadFile(binPath)
	if err != nil {
		p.logger.Debug("supervisor binary not embedded", "platform", plat, "error", err)
		return nil, fmt.Errorf("supervisor: %s binary unavailable for %q; run `make supervisor` to build: %w", binPath, plat, core.ErrSupervisorUnavailable)
	}

	// Decompress on-the-fly to the raw ELF. DecodeAll is a single-shot decode that
	// allocates a fresh output slice owned by the caller, so no manual defensive
	// copy or shared-backing-array handling is needed here — unlike the previous
	// bare go:embed read, which handed out the same backing array on every call.
	data, err := decodeSupervisor(compressed)
	if err != nil {
		p.logger.Error("supervisor binary corrupt", "platform", plat, "error", err)
		return nil, fmt.Errorf("supervisor: %s decompress failed: %w: %w", binPath, err, errSupervisorCorrupt)
	}

	return data, nil
}

// Version returns the SHA256 digest of the raw (decompressed) embedded amd64
// binary as the supervisor version. Decompressing first keeps the digest a
// function of the binary's actual contents — it changes exactly when the binary
// contents change, independent of the compression framing.
func (p *Provider) Version(_ context.Context) (string, error) {
	compressed, err := binaries.ReadFile("bin/pokkum-init-linux-amd64.zst")
	if err != nil {
		// Version is optional; return empty string as documented
		p.logger.Debug("supervisor binary not embedded, version unknown", "error", err)
		return "", nil
	}

	data, err := decodeSupervisor(compressed)
	if err != nil {
		// Version is optional; a corrupt amd64 blob means the version is unknown.
		p.logger.Debug("supervisor binary corrupt, version unknown", "error", err)
		return "", nil
	}

	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash), nil
}
