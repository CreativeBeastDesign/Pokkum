package packager

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestBuildSinglePassLayer_TempFileCleanup pins the fix for the temp-file
// leak an earlier audit found in buildSinglePassLayer: os.CreateTemp created
// one file per layer, and the only cleanup was a runtime.SetFinalizer, which
// a normal CLI process exit does not run — the audit proved this empirically
// (a single-image build left a pokkum-layer-* file behind, and 12 stale
// files up to 1.5MB each were found accumulated from earlier runs).
//
// The fix is NewBuildContext: a context that tracks every temp file created
// while building through it, plus a cleanup func the caller (the cmd/pokkum
// composition root, via runCoreBuild) invokes once the whole build —
// including publish — is done. This test proves both halves: the temp files
// genuinely exist while "in flight" (so cleanup could never have safely run
// any earlier than this), and cleanup removes every one of them.
func TestBuildSinglePassLayer_TempFileCleanup(t *testing.T) {
	isolatedTmp := t.TempDir()
	t.Setenv("TMPDIR", isolatedTmp)
	// os.TempDir() on Linux additionally consults TMPDIR the same way; both
	// platforms this test needs to run on read it, so no further env var is
	// necessary.

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	modTime := time.Unix(1700000000, 0)

	ctx, cleanup := NewBuildContext(context.Background())

	// Build several layers, the way one real image build does (supervisor,
	// app, client, vendor, native, ...), to prove cleanup handles more than
	// a single accidental case.
	const layerCount = 3
	layers := make([]v1.Layer, layerCount)
	for i := 0; i < layerCount; i++ {
		layer, err := BuildDirectoryTreeLayer(ctx, ports.LinuxAMD64, srcDir, "/app/client", modTime, ports.CompressionGzip)
		if err != nil {
			t.Fatalf("BuildDirectoryTreeLayer #%d: %v", i, err)
		}
		layers[i] = layer
	}

	// While the build is "in flight" (before the caller's cleanup), the temp
	// files must exist on disk with a name that reflects their real
	// contents — this is exactly why cleanup cannot simply run immediately
	// after each layer is built: the layer's Compressed() reader still needs
	// to stream from this file until the image is actually published.
	inFlight, err := filepath.Glob(filepath.Join(isolatedTmp, "pokkum-layer-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(inFlight) != layerCount {
		t.Fatalf("expected %d in-flight layer temp files before cleanup, found %d: %v", layerCount, len(inFlight), inFlight)
	}
	for _, p := range inFlight {
		if filepath.Ext(p) != ".gz" {
			t.Errorf("temp file %q does not end in .tar.gz: the suffix should name its actual (gzip) contents, not a bare .tar", p)
		}
	}

	// Simulate the layers actually being consumed by a publish step (a
	// registry push, a daemon load, or a tarball write all call Compressed()
	// to stream the bytes to their destination) before cleanup runs.
	for i, layer := range layers {
		rc, err := layer.Compressed()
		if err != nil {
			t.Fatalf("Compressed() #%d: %v", i, err)
		}
		if _, err := io.Copy(io.Discard, rc); err != nil {
			t.Fatalf("drain #%d: %v", i, err)
		}
		if err := rc.Close(); err != nil {
			t.Fatalf("close #%d: %v", i, err)
		}
	}

	// This is the fix under test: once the caller (standing in for
	// cmd/pokkum's runCoreBuild, after core.Build's publish stage completes)
	// invokes the cleanup func, every temp file must be gone — deterministically,
	// not "eventually, if and when the garbage collector happens to run the
	// finalizer before the process exits".
	cleanup()

	remaining, err := filepath.Glob(filepath.Join(isolatedTmp, "pokkum-layer-*"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("expected no pokkum-layer-* files left in %s after cleanup, found %d: %v", isolatedTmp, len(remaining), remaining)
	}
}

// TestBuildSinglePassLayer_TempFileSuffix pins the naming half of the same
// fix: the temp file's suffix must name what it actually contains
// (gzip- or zstd-compressed tar), not the misleading bare ".tar" it used to
// carry regardless of compression.
func TestBuildSinglePassLayer_TempFileSuffix(t *testing.T) {
	isolatedTmp := t.TempDir()
	t.Setenv("TMPDIR", isolatedTmp)

	srcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	modTime := time.Unix(1700000000, 0)

	cases := []struct {
		name       string
		compress   ports.CompressionAlgorithm
		wantSuffix string
	}{
		{"gzip", ports.CompressionGzip, ".tar.gz"},
		{"zstd", ports.CompressionZstd, ".tar.zst"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cleanup := NewBuildContext(context.Background())
			t.Cleanup(cleanup)

			if _, err := BuildDirectoryTreeLayer(ctx, ports.LinuxAMD64, srcDir, "/app/client", modTime, tc.compress); err != nil {
				t.Fatalf("BuildDirectoryTreeLayer: %v", err)
			}

			matches, err := filepath.Glob(filepath.Join(isolatedTmp, "pokkum-layer-*"+tc.wantSuffix))
			if err != nil {
				t.Fatalf("glob: %v", err)
			}
			if len(matches) != 1 {
				all, _ := filepath.Glob(filepath.Join(isolatedTmp, "pokkum-layer-*"))
				t.Fatalf("expected exactly one pokkum-layer-*%s file, found %d matching (all pokkum-layer-* files: %v)", tc.wantSuffix, len(matches), all)
			}
		})
	}
}

// TestUncompressed_ZstdDecoderClosed pins the fix for the second confirmed
// leak: readCloserWithUnderlying.Close asserted r.Reader.(io.Closer), which
// silently fails for *zstd.Decoder because its Close method has the
// signature "Close()" with no error return and therefore does not satisfy
// io.Closer's "Close() error". The assertion never called Close on the
// decoder, so its internal goroutines and buffers were never released — the
// audit measured heap growing 391 KiB to 10.2 MiB over 300
// Uncompressed()+Close() cycles.
//
// This test uses goroutine count rather than heap bytes as the leak signal:
// klauspost/compress/zstd's Decoder spawns background goroutines to stream
// concurrent decoding from a non-bytes.Buffer source (our layer reads from
// an *os.File), and Decoder.Close cancels a context and waits for exactly
// those goroutines to exit. That makes goroutine count a direct, low-noise
// proxy for "was Close actually called" — unlike heap bytes it does not
// require GC timing assumptions to avoid false positives.
func TestUncompressed_ZstdDecoderClosed(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	srcFile := filepath.Join(tmpDir, "payload.bin")

	// Large enough and non-trivial enough that the decoder genuinely streams
	// from disk instead of taking some degenerate empty-frame shortcut.
	content := bytes.Repeat([]byte("the quick brown fox jumps over the lazy dog\n"), 4096)
	if err := os.WriteFile(srcFile, content, 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	modTime := time.Unix(1700000000, 0)
	layer, err := BuildCustomFileLayer(ctx, ports.LinuxAMD64, "/payload.bin", srcFile, modTime, ports.CompressionZstd)
	if err != nil {
		t.Fatalf("BuildCustomFileLayer: %v", err)
	}

	// Warm up once so any one-time setup (e.g. package-level zstd tables)
	// doesn't show up as "leaked" goroutines in the baseline.
	drainUncompressed(t, layer)
	baseline := stableGoroutineCount(t)

	const cycles = 200
	for i := 0; i < cycles; i++ {
		drainUncompressed(t, layer)
	}

	after := stableGoroutineCount(t)
	if after > baseline+1 {
		t.Errorf("goroutine count grew from %d to %d over %d Uncompressed()+Close() cycles: "+
			"the zstd decoder's Close() is not being invoked", baseline, after, cycles)
	}
}

func drainUncompressed(t *testing.T, layer v1.Layer) {
	t.Helper()
	rc, err := layer.Uncompressed()
	if err != nil {
		t.Fatalf("Uncompressed(): %v", err)
	}
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// stableGoroutineCount lets any goroutines that are exiting (Close was
// called but the runtime hasn't reaped them yet) actually finish, so the
// test isn't measuring scheduler timing instead of the real leak.
func stableGoroutineCount(t *testing.T) int {
	t.Helper()
	var n int
	deadline := time.Now().Add(2 * time.Second)
	for {
		runtime.GC()
		n = runtime.NumGoroutine()
		time.Sleep(5 * time.Millisecond)
		n2 := runtime.NumGoroutine()
		if n2 <= n || time.Now().After(deadline) {
			return n2
		}
	}
}
