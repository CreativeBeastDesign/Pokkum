package layercacheutils_test

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/layercacheutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestResolveCacheDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("POKKUM_CACHE_DIR", tmp)

	dir := layercacheutils.ResolveCacheDir()
	expected := filepath.Join(tmp, "layers")
	if dir != expected {
		t.Errorf("expected %q, got %q", expected, dir)
	}
}

func TestComputeKey(t *testing.T) {
	k1 := layercacheutils.ComputeKey("/usr/local/bin/bun", "hash1", ports.LinuxAMD64, ports.CompressionGzip)
	k2 := layercacheutils.ComputeKey("/usr/local/bin/bun", "hash1", ports.LinuxAMD64, ports.CompressionGzip)
	if k1 != k2 {
		t.Errorf("expected keys to be identical, got %q vs %q", k1, k2)
	}

	// Same content hash, path, platform and compression must always produce
	// the same key regardless of when the two calls happen — the whole point
	// of this cache key is that it does NOT vary with build time (see
	// ComputeKey's doc comment / docs/archive/Roadmap.md item 3f). There is deliberately
	// no "different modTime" case here anymore: a build-timestamp parameter
	// was removed from ComputeKey's signature entirely, rather than merely
	// asking callers to pass a fixed value, so this invariant can't quietly
	// regress by a future caller threading a real timestamp back in.

	kDifferentHash := layercacheutils.ComputeKey("/usr/local/bin/bun", "hash2", ports.LinuxAMD64, ports.CompressionGzip)
	if k1 == kDifferentHash {
		t.Errorf("expected different keys for different content hashes")
	}

	kDifferentPlatform := layercacheutils.ComputeKey("/usr/local/bin/bun", "hash1", ports.LinuxARM64, ports.CompressionGzip)
	if k1 == kDifferentPlatform {
		t.Errorf("expected different keys for different platforms")
	}

	kDifferentComp := layercacheutils.ComputeKey("/usr/local/bin/bun", "hash1", ports.LinuxAMD64, ports.CompressionZstd)
	if k1 == kDifferentComp {
		t.Errorf("expected different keys for different compressions")
	}
}

func TestComputeFileAndBytesSHA256(t *testing.T) {
	data := []byte("hello world")
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "hello.txt")
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	hash1, err := layercacheutils.ComputeFileSHA256(filePath)
	if err != nil {
		t.Fatalf("ComputeFileSHA256 failed: %v", err)
	}

	hash2 := layercacheutils.ComputeBytesSHA256(data)
	if hash1 != hash2 {
		t.Errorf("hash mismatch: %q vs %q", hash1, hash2)
	}
}

func TestPutAndGet_RoundTrip(t *testing.T) {
	cacheDir := t.TempDir()
	modTime := time.Unix(1700000000, 0)
	data := []byte("binary payload for testing cache")

	// Create test layer
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		buf := &bytes.Buffer{}
		tw := tar.NewWriter(buf)
		_ = tw.WriteHeader(&tar.Header{
			Name:    "pokkum/init",
			Mode:    0o555,
			Size:    int64(len(data)),
			ModTime: modTime,
		})
		_, _ = tw.Write(data)
		_ = tw.Close()
		return io.NopCloser(buf), nil
	})
	if err != nil {
		t.Fatalf("failed to build test layer: %v", err)
	}

	origDigest, err := layer.Digest()
	if err != nil {
		t.Fatalf("layer.Digest() error: %v", err)
	}
	origDiffID, err := layer.DiffID()
	if err != nil {
		t.Fatalf("layer.DiffID() error: %v", err)
	}

	key := layercacheutils.ComputeKey("/pokkum/init", layercacheutils.ComputeBytesSHA256(data), ports.LinuxAMD64, ports.CompressionGzip)

	// 1. Initial cache get must miss
	if _, ok := layercacheutils.Get(cacheDir, key, ports.CompressionGzip); ok {
		t.Fatalf("expected cache miss before Put")
	}

	// 2. Put layer into cache
	cachedLayer, err := layercacheutils.Put(cacheDir, key, layer, ports.CompressionGzip)
	if err != nil {
		t.Fatalf("layercacheutils.Put failed: %v", err)
	}

	// 3. Cache get must hit
	hitLayer, ok := layercacheutils.Get(cacheDir, key, ports.CompressionGzip)
	if !ok {
		t.Fatalf("expected cache hit after Put")
	}

	// 4. Verify digests match exactly
	hitDigest, err := hitLayer.Digest()
	if err != nil {
		t.Fatalf("hitLayer.Digest() error: %v", err)
	}
	if hitDigest != origDigest {
		t.Errorf("digest mismatch: cached %s vs original %s", hitDigest, origDigest)
	}

	hitDiffID, err := hitLayer.DiffID()
	if err != nil {
		t.Fatalf("hitLayer.DiffID() error: %v", err)
	}
	if hitDiffID != origDiffID {
		t.Errorf("diffID mismatch: cached %s vs original %s", hitDiffID, origDiffID)
	}

	cachedDigest, _ := cachedLayer.Digest()
	if cachedDigest != origDigest {
		t.Errorf("cachedLayer returned by Put had different digest: %s vs %s", cachedDigest, origDigest)
	}
}

func TestGet_CorruptFileEviction(t *testing.T) {
	cacheDir := t.TempDir()
	key := "corrupt-key"
	corruptFile := filepath.Join(cacheDir, key+".tar.gz")
	_ = os.WriteFile(corruptFile, []byte("not-a-valid-tar-gz"), 0o644)

	layer, ok := layercacheutils.Get(cacheDir, key, ports.CompressionGzip)
	if ok || layer != nil {
		t.Errorf("expected Get on corrupt file to fail and return ok=false")
	}

	// Corrupt file should be cleaned up
	if _, err := os.Stat(corruptFile); !os.IsNotExist(err) {
		t.Errorf("expected corrupt cache file to be evicted from disk")
	}
}
