package layercacheutils

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"

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

// layerMeta is the on-disk sidecar written next to every cached layer blob.
//
// It exists so that a cache hit costs a stat plus a few hundred bytes of JSON
// instead of a full read of the blob (to recompute the compressed digest) plus
// a full decompression of it (to recompute the diffID), which is what
// tarball.LayerFromFile does eagerly. Every field here is a value the builder
// already computed exactly once while writing the blob; the sidecar just stops
// it being thrown away.
//
// The sidecar is advisory, never authoritative about trust: a missing,
// unparseable, or size-mismatched sidecar is treated as a cache MISS, so the
// worst a damaged sidecar can do is cost a rebuild. It is never a reason to
// serve bytes whose digest was not verified against the blob actually on disk.
type layerMeta struct {
	DiffID    string `json:"diffid"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediatype"`
}

// sidecarPath returns the metadata path for a cached blob key.
func sidecarPath(cacheDir string, key string) string {
	return filepath.Join(cacheDir, key+".json")
}

// expectedMediaType is the media type a cached layer is served with, which is
// derived from the requested compression rather than from whatever media type
// the layer being cached happened to carry. This mirrors the media type
// Get used to force via tarball.LayerFromFile's WithMediaType option.
func expectedMediaType(compression ports.CompressionAlgorithm) types.MediaType {
	if compression.Normalize() == ports.CompressionZstd {
		return types.OCILayerZStd
	}
	return types.OCILayer
}

// cachedFileLayer is a v1.Layer backed by an on-disk compressed blob whose
// digest, diffID and size were computed when the blob was built and recorded
// in its sidecar. Nothing here re-reads or re-hashes the blob to answer a
// metadata question.
type cachedFileLayer struct {
	path        string
	diffID      v1.Hash
	digest      v1.Hash
	size        int64
	mediaType   types.MediaType
	compression ports.CompressionAlgorithm
}

func (l *cachedFileLayer) Digest() (v1.Hash, error)            { return l.digest, nil }
func (l *cachedFileLayer) DiffID() (v1.Hash, error)            { return l.diffID, nil }
func (l *cachedFileLayer) Size() (int64, error)                { return l.size, nil }
func (l *cachedFileLayer) MediaType() (types.MediaType, error) { return l.mediaType, nil }

func (l *cachedFileLayer) Compressed() (io.ReadCloser, error) {
	return os.Open(l.path)
}

func (l *cachedFileLayer) Uncompressed() (io.ReadCloser, error) {
	f, err := os.Open(l.path)
	if err != nil {
		return nil, err
	}
	if l.compression.Normalize() == ports.CompressionZstd {
		zr, err := zstd.NewReader(f)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		// IOReadCloser(), not the *zstd.Decoder itself: Decoder.Close() returns
		// no error and therefore does NOT satisfy io.Closer, so handing the
		// decoder over directly would leave Close below silently doing nothing
		// and leak the decoder's goroutines (the same trap packager's own
		// readCloserWithUnderlying documents).
		return &readCloserWithUnderlying{Reader: zr.IOReadCloser(), closer: f}, nil
	}
	gr, err := gzip.NewReader(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &readCloserWithUnderlying{Reader: gr, closer: f}, nil
}

// readCloserWithUnderlying closes the decompressor's backing file as well as
// the decompressor itself, so an Uncompressed() reader never leaks the fd.
type readCloserWithUnderlying struct {
	io.Reader
	closer io.Closer
}

func (r *readCloserWithUnderlying) Close() error {
	if c, ok := r.Reader.(io.Closer); ok {
		_ = c.Close()
	}
	return r.closer.Close()
}

// readSidecar loads and validates the metadata sidecar for key. It returns
// ok=false for every reason a caller must treat as a cache miss: no sidecar,
// unreadable or unparseable JSON, a malformed digest, a media type that does
// not match the requested compression, or a recorded size that disagrees with
// the blob actually on disk. It never repairs or trusts partial data.
func readSidecar(cacheDir string, key string, compression ports.CompressionAlgorithm, blobSize int64) (layerMeta, bool) {
	raw, err := os.ReadFile(sidecarPath(cacheDir, key))
	if err != nil {
		return layerMeta{}, false
	}
	var meta layerMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return layerMeta{}, false
	}
	if meta.Size != blobSize {
		return layerMeta{}, false
	}
	if types.MediaType(meta.MediaType) != expectedMediaType(compression) {
		return layerMeta{}, false
	}
	return meta, true
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

	// The sidecar carries the digest/diffID/size this blob was built with, so a
	// hit costs a stat and a few hundred bytes of JSON rather than a full read
	// plus a full decompression (which is what tarball.LayerFromFile does
	// eagerly). Anything wrong with it — absent, unparseable, wrong media type,
	// or a size that disagrees with the blob on disk — is a cache MISS, never a
	// reason to serve unverified metadata. The blob itself is deliberately NOT
	// removed in that case: a concurrent Put renames the blob before its
	// sidecar, so "blob without sidecar" is a legal transient state, and
	// deleting it would sabotage the writer.
	meta, ok := readSidecar(cacheDir, key, compression, info.Size())
	if !ok {
		return nil, false
	}
	diffID, err := v1.NewHash(meta.DiffID)
	if err != nil {
		return nil, false
	}
	digest, err := v1.NewHash(meta.Digest)
	if err != nil {
		return nil, false
	}

	return &cachedFileLayer{
		path:        targetPath,
		diffID:      diffID,
		digest:      digest,
		size:        meta.Size,
		mediaType:   expectedMediaType(compression),
		compression: compression,
	}, true
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

	written, copyErr := poolutils.Copy(tmpFile, rc)
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

	// Everything below reuses metadata the built layer already carries.
	// buildSinglePassLayer computed the compressed digest, the diffID and the
	// byte count exactly once while streaming the tar; re-deriving them here
	// (which is what tarball.LayerFromFile did) would read the whole blob back
	// and decompress it for values we are already holding.
	//
	// written is the number of bytes actually on disk and is what the sidecar
	// records; the layer's own Size must agree with it, because layer.Digest()
	// describes precisely the stream that was copied. If any of these is
	// unavailable or disagrees, no sidecar is written at all and the next Get
	// simply misses — a cheap rebuild instead of trusted-but-wrong metadata.
	meta, ok := metaForLayer(layer, written, compression)
	if !ok {
		return layer, nil
	}
	if err := writeSidecar(cacheDir, key, meta); err != nil {
		return layer, nil
	}

	diffID, err := v1.NewHash(meta.DiffID)
	if err != nil {
		return layer, nil
	}
	digest, err := v1.NewHash(meta.Digest)
	if err != nil {
		return layer, nil
	}
	return &cachedFileLayer{
		path:        targetPath,
		diffID:      diffID,
		digest:      digest,
		size:        meta.Size,
		mediaType:   expectedMediaType(compression),
		compression: compression,
	}, nil
}

// metaForLayer collects the sidecar values from a layer that has just been
// written to disk, cross-checking the layer's own reported size against the
// byte count actually copied. ok=false means "do not write a sidecar",
// never "write a partial one".
func metaForLayer(layer v1.Layer, written int64, compression ports.CompressionAlgorithm) (layerMeta, bool) {
	digest, err := layer.Digest()
	if err != nil {
		return layerMeta{}, false
	}
	diffID, err := layer.DiffID()
	if err != nil {
		return layerMeta{}, false
	}
	size, err := layer.Size()
	if err != nil {
		return layerMeta{}, false
	}
	if size != written {
		return layerMeta{}, false
	}
	return layerMeta{
		DiffID:    diffID.String(),
		Digest:    digest.String(),
		Size:      written,
		MediaType: string(expectedMediaType(compression)),
	}, true
}

// writeSidecar writes the metadata file with the same temp-file-plus-rename
// discipline the blob itself uses, so a reader never observes a half-written
// sidecar. It is written AFTER the blob has been renamed into place: the
// reverse order would advertise metadata for a blob that is not there yet.
func writeSidecar(cacheDir string, key string, meta layerMeta) error {
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	tmpFile, err := os.CreateTemp(cacheDir, ".meta-tmp-*.json")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	defer func() {
		_ = os.Remove(tmpName) // No-op once the rename below succeeded.
	}()
	if _, err := tmpFile.Write(raw); err != nil {
		_ = tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, sidecarPath(cacheDir, key))
}
