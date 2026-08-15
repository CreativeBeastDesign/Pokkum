// Package staticserver provides the embedded pokkum-static PID-1 static file
// server binary for all supported platforms. The binaries are embedded
// zstd-compressed (built by `make static-server`) and decompressed on-the-fly
// by Binary and Version, mirroring the mechanism of the pokkum-init supervisor
// (internal/adapters/supervisor) — this keeps the pokkum CLI footprint down
// while preserving the raw ELF bytes the packager writes into the image.
package staticserver

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/klauspost/compress/zstd"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

//go:embed all:bin
var binaries embed.FS

// errStaticServerCorrupt is reported when an embedded pokkum-static binary is
// present but cannot be decompressed. This is distinct from
// core.ErrStaticServerUnavailable (which means the asset is absent): a
// present-but-unusable blob is a build defect, not a "not built yet" condition.
var errStaticServerCorrupt = errors.New("static server binary corrupt")

// decodeStaticServer decompresses the zstd-framed embedded representation of a
// pokkum-static binary back to the raw ELF. It is a directly-testable seam over
// the single-shot zstd.DecodeAll (mirroring decodeSupervisor).
//
// An empty input is treated as corrupt rather than passed to the decoder, so a
// broken embed cannot come through as an empty (nil-error) binary and violate
// the port contract "never nil or empty on a nil error".
func decodeStaticServer(compressed []byte) ([]byte, error) {
	if len(compressed) == 0 {
		return nil, errStaticServerCorrupt
	}
	return staticServerDecoder().DecodeAll(compressed, nil)
}

// staticServerDecoder lazily builds the single shared zstd decoder used for all
// on-the-fly static server decompression. A single process-wide decoder is
// concurrency-safe (Binary may be called concurrently for different platforms)
// and is never closed (stateless DecodeAll holds no per-call resources).
var staticServerDecoder = sync.OnceValue(func() *zstd.Decoder {
	d, err := zstd.NewReader(nil)
	if err != nil {
		// NewReader(nil, ...) only fails if an option is invalid; none are set here.
		panic(err)
	}
	return d
})

var _ ports.StaticServerProvider = (*Provider)(nil)

// Provider is the StaticServerProvider implementation, supplying the embedded
// pokkum-static binaries to the packager.
type Provider struct {
	logger *slog.Logger
}

// New creates a new static server provider. If logger is nil, slog.Default() is used.
func New(logger *slog.Logger) *Provider {
	if logger == nil {
		logger = slog.Default()
	}
	return &Provider{logger: logger}
}

// Binary returns the complete contents of the pokkum-static executable for the
// given platform, transparently decompressing the embedded zstd-compressed blob
// so the caller receives the raw ELF. The returned slice belongs to the caller.
func (p *Provider) Binary(_ context.Context, plat ports.Platform) ([]byte, error) {
	if !plat.Supported() {
		return nil, fmt.Errorf("staticserver: unsupported platform %q: %w", plat, core.ErrUnsupportedPlatform)
	}

	var binPath string
	switch plat {
	case ports.LinuxAMD64:
		binPath = "bin/pokkum-static-linux-amd64.zst"
	case ports.LinuxARM64:
		binPath = "bin/pokkum-static-linux-arm64.zst"
	default:
		return nil, fmt.Errorf("staticserver: unsupported platform %q: %w", plat, core.ErrUnsupportedPlatform)
	}

	compressed, err := binaries.ReadFile(binPath)
	if err != nil {
		p.logger.Debug("static server binary not embedded", "platform", plat, "error", err)
		return nil, fmt.Errorf("staticserver: %s binary unavailable for %q; run `make static-server` to build: %w", binPath, plat, core.ErrStaticServerUnavailable)
	}

	data, err := decodeStaticServer(compressed)
	if err != nil {
		p.logger.Error("static server binary corrupt", "platform", plat, "error", err)
		return nil, fmt.Errorf("staticserver: %s decompress failed: %w: %w", binPath, err, errStaticServerCorrupt)
	}
	return data, nil
}

// Version returns the SHA256 digest of the raw (decompressed) embedded amd64
// binary as the static server version, matching the supervisor's Version
// convention.
func (p *Provider) Version(_ context.Context) (string, error) {
	compressed, err := binaries.ReadFile("bin/pokkum-static-linux-amd64.zst")
	if err != nil {
		p.logger.Debug("static server binary not embedded, version unknown", "error", err)
		return "", nil
	}
	data, err := decodeStaticServer(compressed)
	if err != nil {
		p.logger.Debug("static server binary corrupt, version unknown", "error", err)
		return "", nil
	}
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash), nil
}
