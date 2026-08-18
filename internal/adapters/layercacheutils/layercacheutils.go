package layercacheutils

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/google/go-containerregistry/pkg/v1/types"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/poolutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// IsUtilityPackage marks this as a reusable utility, not a port adapter.
const IsUtilityPackage = true

// ResolveCacheDir returns the configured or standard cache directory for layer tarballs.
func ResolveCacheDir() string {
	if env := os.Getenv("POKKUM_CACHE_DIR"); env != "" {
		return filepath.Join(env, "layers")
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	return filepath.Join(cacheDir, "pokkum", "layers")
}

// ComputeKey returns a deterministic SHA-256 hash identifying an immutable
// binary layer's contents, keyed ONLY on what actually determines the
// resulting tar/layer bytes: the in-image path, the source content's
// SHA-256, the target platform, and the compression algorithm.
//
// A build timestamp is deliberately NOT one of the inputs, even though the
// layer this key addresses is built with a pinned tar ModTime of its own
// (see packager.pinnedImmutableBinaryEpoch). This cache is exclusively used
// for the immutable third-party/embedded-binary layers — the Bun runtime,
// pokkum-init, pokkum-static — whose bytes are fully determined by the four
// parameters above and never by which commit or SOURCE_DATE_EPOCH triggered
// the build. Earlier, this function also hashed in the caller's modTime
// (effectively SOURCE_DATE_EPOCH), which meant every commit minted a new
// cache key for a layer whose content had not changed at all: a guaranteed
// on-disk cache miss on every single build. Dropping it here — rather than
// merely passing a fixed modTime through from the caller — makes that
// invariant impossible to violate by accident from this function's own
// signature, for any future caller.
func ComputeKey(targetPath string, contentSHA256 string, platform ports.Platform, compression ports.CompressionAlgorithm) string {
	h := sha256.New()
	h.Write([]byte(targetPath))
	h.Write([]byte("\x00"))
	h.Write([]byte(contentSHA256))
	h.Write([]byte("\x00"))
	h.Write([]byte(platform.String()))
	h.Write([]byte("\x00"))
	h.Write([]byte(string(compression.Normalize())))
	return hex.EncodeToString(h.Sum(nil))
}

// ComputeFileSHA256 computes the SHA-256 hex digest of a host file.
func ComputeFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck // read-only

	h := sha256.New()
	if _, err := poolutils.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ComputeBytesSHA256 computes the SHA-256 hex digest of an in-memory byte slice.
func ComputeBytesSHA256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func layerFileExt(compression ports.CompressionAlgorithm) string {
	if compression.Normalize() == ports.CompressionZstd {
		return ".tar.zst"
	}
	return ".tar.gz"
}

func layerOptions(compression ports.CompressionAlgorithm) []tarball.LayerOption {
	if compression.Normalize() == ports.CompressionZstd {
		return []tarball.LayerOption{
			tarball.WithMediaType(types.OCILayerZStd),
		}
	}
	return []tarball.LayerOption{
		tarball.WithMediaType(types.OCILayer),
	}
}

// Get returns the cached v1.Layer from disk if it exists and is readable.
func Get(cacheDir string, key string, compression ports.CompressionAlgorithm) (v1.Layer, bool) {
	if cacheDir == "" || key == "" {
		return nil, false
	}

	targetPath := filepath.Join(cacheDir, key+layerFileExt(compression))
	info, err := os.Stat(targetPath)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return nil, false
	}

	// Validate magic header bytes for compression format
	f, err := os.Open(targetPath)
	if err != nil {
		return nil, false
	}
	var magic [4]byte
	n, readErr := f.Read(magic[:])
	_ = f.Close()

	if readErr != nil || n < 2 {
		_ = os.Remove(targetPath)
		return nil, false
	}

	if compression.Normalize() == ports.CompressionZstd {
		if magic[0] != 0x28 || magic[1] != 0xb5 || magic[2] != 0x2f || magic[3] != 0xfd {
			_ = os.Remove(targetPath)
			return nil, false
		}
	} else {
		if magic[0] != 0x1f || magic[1] != 0x8b {
			_ = os.Remove(targetPath)
			return nil, false
		}
	}

	layer, err := tarball.LayerFromFile(targetPath, layerOptions(compression)...)
	if err != nil {
		_ = os.Remove(targetPath)
		return nil, false
	}

	return layer, true
}

// Put writes the layer's compressed stream to the cache and returns a disk-backed v1.Layer.
func Put(cacheDir string, key string, layer v1.Layer, compression ports.CompressionAlgorithm) (v1.Layer, error) {
	if cacheDir == "" || key == "" {
		return layer, nil
	}

	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return layer, nil // Fallback silently to in-memory layer if cache dir unwritable
	}

	targetPath := filepath.Join(cacheDir, key+layerFileExt(compression))

	tmpFile, err := os.CreateTemp(cacheDir, ".layer-tmp-*"+layerFileExt(compression))
	if err != nil {
		return layer, nil // Fallback silently
	}
	tmpName := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpName) // Clean up temp file on failure or after rename
	}()

	rc, err := layer.Compressed()
	if err != nil {
		_ = tmpFile.Close()
		return layer, fmt.Errorf("reading compressed layer: %w", err)
	}

	_, copyErr := poolutils.Copy(tmpFile, rc)
	_ = rc.Close()
	closeErr := tmpFile.Close()

	if copyErr != nil {
		return layer, fmt.Errorf("writing cached layer stream: %w", copyErr)
	}
	if closeErr != nil {
		return layer, fmt.Errorf("closing temp layer file: %w", closeErr)
	}

	if err := os.Rename(tmpName, targetPath); err != nil {
		return layer, nil
	}

	cachedLayer, err := tarball.LayerFromFile(targetPath, layerOptions(compression)...)
	if err != nil {
		return layer, nil
	}
	return cachedLayer, nil
}
