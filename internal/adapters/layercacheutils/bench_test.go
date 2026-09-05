package layercacheutils_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/layercacheutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// benchLayerBytes is the size of the synthetic cached layer. This cache only
// ever holds the immutable binary layers — the Bun runtime (~90 MB raw),
// pokkum-init, pokkum-static — so ~20 MB of already-compressed blob is a
// representative single entry.
const benchLayerBytes = 20 << 20

// benchKey stands in for a ComputeKey result: 64 hex characters, which is
// what determines the on-disk filename length.
const benchKey = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"

// benchBinaryContent returns bytes that compress the way a stripped native
// executable does, i.e. barely at all. That matters: filling the fixture with
// zeroes or with repeated text would make the gzip stream a few kilobytes and
// turn every measurement below into a benchmark of syscall overhead rather
// than of moving 20 MB.
func benchBinaryContent(n int) []byte {
	buf := make([]byte, n)
	rng := rand.New(rand.NewSource(0xB0B1CE))
	_, _ = rng.Read(buf)
	return buf
}

// benchCompressedLayerFile writes a gzip-compressed tar holding one large
// pseudo-binary member and returns its path. tarball.LayerFromFile over this
// file produces exactly the kind of v1.Layer Put receives in production: one
// whose Compressed() is a plain os.Open of an already-compressed blob, so Put
// is measured as the byte copy it actually is rather than as a hidden
// re-compression that production never performs.
func benchCompressedLayerFile(tb testing.TB, dir string) string {
	tb.Helper()

	content := benchBinaryContent(benchLayerBytes)

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	hdr := &tar.Header{
		Typeflag: tar.TypeReg,
		Name:     "usr/local/bin/bun",
		Mode:     0o755,
		Size:     int64(len(content)),
		Format:   tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		tb.Fatalf("write tar header: %v", err)
	}
	if _, err := tw.Write(content); err != nil {
		tb.Fatalf("write tar body: %v", err)
	}
	if err := tw.Close(); err != nil {
		tb.Fatalf("close tar: %v", err)
	}
	if err := gw.Close(); err != nil {
		tb.Fatalf("close gzip: %v", err)
	}

	if buf.Len() < benchLayerBytes/2 {
		tb.Fatalf("degenerate fixture: %d bytes of pseudo-binary content compressed to %d — the blob under test is not ~20 MB", benchLayerBytes, buf.Len())
	}

	path := filepath.Join(dir, "layer.tar.gz")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		tb.Fatalf("write layer blob: %v", err)
	}
	return path
}

// benchSourceLayer builds the on-disk blob and wraps it as a v1.Layer.
func benchSourceLayer(tb testing.TB) v1.Layer {
	tb.Helper()
	blob := benchCompressedLayerFile(tb, tb.TempDir())
	layer, err := tarball.LayerFromFile(blob)
	if err != nil {
		tb.Fatalf("tarball.LayerFromFile(%s): %v", blob, err)
	}
	return layer
}

// BenchmarkPut measures writing one ~20 MB compressed layer into the on-disk
// cache: read the layer's compressed stream, copy it to a temp file inside the
// cache dir, rename it into place, and re-open the result as a disk-backed
// layer (which re-reads it to compute its digest and diffID).
//
// The cache dir is emptied between iterations with the timer stopped, so every
// measured iteration writes a genuinely new entry.
func BenchmarkPut(b *testing.B) {
	layer := benchSourceLayer(b)

	sz, err := layer.Size()
	if err != nil {
		b.Fatalf("layer size: %v", err)
	}

	cacheDir := filepath.Join(b.TempDir(), "layers")
	b.SetBytes(sz)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cached, err := layercacheutils.Put(cacheDir, benchKey, layer, ports.CompressionGzip)
		if err != nil {
			b.Fatalf("Put: %v", err)
		}
		b.StopTimer()
		if cached == nil {
			b.Fatal("Put returned a nil layer")
		}
		if _, statErr := os.Stat(filepath.Join(cacheDir, benchKey+".tar.gz")); statErr != nil {
			b.Fatalf("Put silently fell back to the in-memory layer: %v", statErr)
		}
		if err := os.RemoveAll(cacheDir); err != nil {
			b.Fatalf("clear cache dir: %v", err)
		}
		b.StartTimer()
	}
}

// BenchmarkGet measures a cache HIT: stat the entry, validate its magic bytes,
// and hand back a disk-backed v1.Layer. The layer construction is not free —
// tarball.LayerFromFile computes the entry's digest and diffID, which means
// reading and decompressing the whole 20 MB blob before Get returns.
func BenchmarkGet(b *testing.B) {
	layer := benchSourceLayer(b)

	cacheDir := filepath.Join(b.TempDir(), "layers")
	if _, err := layercacheutils.Put(cacheDir, benchKey, layer, ports.CompressionGzip); err != nil {
		b.Fatalf("seed cache: %v", err)
	}

	// Fixture floor: a Get that missed would measure a stat of a missing file
	// and report a plausible-looking but meaningless number.
	seeded, ok := layercacheutils.Get(cacheDir, benchKey, ports.CompressionGzip)
	if !ok {
		b.Fatal("seeded cache entry does not read back as a hit")
	}
	if _, err := seeded.Digest(); err != nil {
		b.Fatalf("seeded cache entry is not a usable layer: %v", err)
	}

	sz, err := layer.Size()
	if err != nil {
		b.Fatalf("layer size: %v", err)
	}

	b.SetBytes(sz)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, hit := layercacheutils.Get(cacheDir, benchKey, ports.CompressionGzip)
		if !hit || got == nil {
			b.Fatal("unexpected cache miss")
		}
	}
}

// BenchmarkComputeFileSHA256 measures the content hash that keys every cache
// lookup. BuildCustomFileLayer pays this on every build, hit or miss, before
// it can even ask the cache a question — so it is a floor on what the layer
// cache can save, not an avoidable cost.
func BenchmarkComputeFileSHA256(b *testing.B) {
	path := filepath.Join(b.TempDir(), "bun")
	content := benchBinaryContent(benchLayerBytes)
	if err := os.WriteFile(path, content, 0o755); err != nil {
		b.Fatalf("write source binary: %v", err)
	}

	want, err := layercacheutils.ComputeFileSHA256(path)
	if err != nil {
		b.Fatalf("ComputeFileSHA256: %v", err)
	}
	if len(want) != 64 {
		b.Fatalf("degenerate digest %q", want)
	}

	b.SetBytes(int64(len(content)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := layercacheutils.ComputeFileSHA256(path)
		if err != nil {
			b.Fatalf("ComputeFileSHA256: %v", err)
		}
		if got != want {
			b.Fatalf("digest drifted: %s != %s", got, want)
		}
	}
}
