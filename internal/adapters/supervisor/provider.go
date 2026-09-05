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

// Embedded asset paths, named so the switch in Binary and the memoisation
// table below cannot drift apart into two spellings of the same path.
const (
	assetLinuxAMD64 = "bin/pokkum-init-linux-amd64.zst"
	assetLinuxARM64 = "bin/pokkum-init-linux-arm64.zst"
)

// blobResult is the memoised outcome of reading and decompressing one embedded
// asset. The two failure modes stay separate because Binary reports them as
// the genuinely different conditions they are: an absent asset is
// core.ErrSupervisorUnavailable ("not built yet"), a present-but-undecodable
// one is errSupervisorCorrupt (a build defect).
type blobResult struct {
	data      []byte
	readErr   error
	decodeErr error
}

// supervisorBlobs memoises the decompressed ELF for each embedded asset.
//
// The embedded FS is immutable for the life of the process, so decompressing a
// given asset is a pure function evaluated at most once here. Without this,
// one build decompresses ~8 MB per Binary call — once per platform from the
// packaging fan-out, plus a whole extra decompression of the amd64 blob just
// to hash it for Version.
//
// The consequence for callers is that Binary now hands out the same backing
// array on every call. That is exactly what ports.SupervisorProvider already
// documents ("the caller must not modify it, since an embedded implementation
// will hand out the same backing array on every call") and what the packager's
// bytesOpener already relies on when it declines to copy; see Binary's doc
// comment.
var supervisorBlobs = map[string]func() blobResult{
	assetLinuxAMD64: sync.OnceValue(func() blobResult { return readSupervisorBlob(assetLinuxAMD64) }),
	assetLinuxARM64: sync.OnceValue(func() blobResult { return readSupervisorBlob(assetLinuxARM64) }),
}

// loadSupervisorBlob returns the memoised blob for path, falling back to an
// uncached read for any path not in the table. The fallback exists so that
// adding an embedded asset without a matching cache entry is a performance
// regression rather than a nil-map-value panic on a build path;
// TestSupervisorBlobsCoverEverySupportedPlatform asserts no such gap exists
// today.
func loadSupervisorBlob(path string) blobResult {
	if load, ok := supervisorBlobs[path]; ok {
		return load()
	}
	return readSupervisorBlob(path)
}

// readSupervisorBlob performs the actual embedded read and zstd decode. It is
// the function memoised by supervisorBlobs and must stay free of logging and
// error formatting, both of which belong to the per-call sites so they keep
// firing on every call rather than only on the first.
func readSupervisorBlob(path string) blobResult {
	compressed, err := binaries.ReadFile(path)
	if err != nil {
		return blobResult{readErr: err}
	}
	data, err := decodeSupervisor(compressed)
	if err != nil {
		return blobResult{decodeErr: err}
	}
	return blobResult{data: data}
}

// supervisorDigest memoises the SHA-256 of the decompressed amd64 blob, the
// value Version reports. Empty string means the blob is absent or corrupt;
// Version re-derives which of the two it is (from the already-memoised
// blobResult) so it can keep logging that distinction on every call.
var supervisorDigest = sync.OnceValue(func() string {
	res := loadSupervisorBlob(assetLinuxAMD64)
	// Empty with no error would mean an embedded blob that decoded to nothing;
	// report "unknown" rather than the SHA-256 of zero bytes.
	if len(res.data) == 0 {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(res.data))
})

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
// so the caller receives the raw ELF.
//
// The returned slice may be retained and read freely, but MUST NOT be
// modified: the decompressed bytes are memoised per platform (see
// supervisorBlobs), so every call for the same platform hands back the same
// backing array, and a caller that wrote to it would corrupt the binary every
// later caller receives. This is the read-only, shared contract
// ports.SupervisorProvider states for an embedded implementation, and the one
// the packager's bytesOpener relies on when it wraps these bytes in a reader
// instead of copying them. A caller that needs to mutate must clone first.
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
		binPath = assetLinuxAMD64
	case ports.LinuxARM64:
		binPath = assetLinuxARM64
	default:
		// Defensive: Supported() already checked this, but be explicit
		return nil, fmt.Errorf("supervisor: unsupported platform %q: %w", plat, core.ErrUnsupportedPlatform)
	}

	// Read + decompress, memoised per asset. Both failure modes are logged on
	// every call, not only on the first, so a build that asks twice still says
	// so twice.
	res := loadSupervisorBlob(binPath)
	if res.readErr != nil {
		p.logger.Debug("supervisor binary not embedded", "platform", plat, "error", res.readErr)
		return nil, fmt.Errorf("supervisor: %s binary unavailable for %q; run `make supervisor` to build: %w", binPath, plat, core.ErrSupervisorUnavailable)
	}
	if res.decodeErr != nil {
		p.logger.Error("supervisor binary corrupt", "platform", plat, "error", res.decodeErr)
		return nil, fmt.Errorf("supervisor: %s decompress failed: %w: %w", binPath, res.decodeErr, errSupervisorCorrupt)
	}

	return res.data, nil
}

// Version returns the SHA256 digest of the raw (decompressed) embedded amd64
// binary as the supervisor version. Decompressing first keeps the digest a
// function of the binary's actual contents — it changes exactly when the binary
// contents change, independent of the compression framing.
//
// Both the decompression and the digest are memoised, so this no longer
// decompresses the amd64 blob a second time purely to hash it.
func (p *Provider) Version(_ context.Context) (string, error) {
	res := loadSupervisorBlob(assetLinuxAMD64)
	if res.readErr != nil {
		// Version is optional; return empty string as documented
		p.logger.Debug("supervisor binary not embedded, version unknown", "error", res.readErr)
		return "", nil
	}
	if res.decodeErr != nil {
		// Version is optional; a corrupt amd64 blob means the version is unknown.
		p.logger.Debug("supervisor binary corrupt, version unknown", "error", res.decodeErr)
		return "", nil
	}

	return supervisorDigest(), nil
}
