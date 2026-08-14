package precompressutils_test

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/precompressutils"
)

func TestIsCompressible(t *testing.T) {
	tests := []struct {
		file string
		want bool
	}{
		{"bundle.js", true},
		{"style.css", true},
		{"index.html", true},
		{"data.json", true},
		{"logo.svg", true},
		{"app.wasm", true},
		{"bundle.js.map", true},
		{"font.woff2", false},
		{"image.png", false},
		{"photo.jpg", false},
		{"archive.zip", false},
		{"already.js.gz", false},
		{"already.js.br", false},
		{"already.js.zst", false},
	}

	for _, tt := range tests {
		got := precompressutils.IsCompressible(tt.file)
		if got != tt.want {
			t.Errorf("IsCompressible(%q) = %v, want %v", tt.file, got, tt.want)
		}
	}
}

func TestPrecompressFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "app.js")
	originalContent := strings.Repeat("console.log('Hello world from Pokkum SvelteKit compiler');\n", 100)
	if err := os.WriteFile(filePath, []byte(originalContent), 0o644); err != nil {
		t.Fatalf("failed to write source file: %v", err)
	}

	modTime := time.Unix(1700000000, 0)
	if err := precompressutils.PrecompressFile(filePath, modTime); err != nil {
		t.Fatalf("PrecompressFile failed: %v", err)
	}

	// 1. Verify .gz sidecar
	gzPath := filePath + ".gz"
	gzData, err := os.ReadFile(gzPath)
	if err != nil {
		t.Fatalf("reading .gz file failed: %v", err)
	}
	gr, err := gzip.NewReader(bytes.NewReader(gzData))
	if err != nil {
		t.Fatalf("gzip reader init failed: %v", err)
	}
	decompressedGz, err := io.ReadAll(gr)
	_ = gr.Close()
	if err != nil {
		t.Fatalf("reading gzip stream failed: %v", err)
	}
	if string(decompressedGz) != originalContent {
		t.Errorf("gzip decompressed content mismatch")
	}

	// 2. Verify .br sidecar
	brPath := filePath + ".br"
	brData, err := os.ReadFile(brPath)
	if err != nil {
		t.Fatalf("reading .br file failed: %v", err)
	}
	br := brotli.NewReader(bytes.NewReader(brData))
	decompressedBr, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("reading brotli stream failed: %v", err)
	}
	if string(decompressedBr) != originalContent {
		t.Errorf("brotli decompressed content mismatch")
	}

	// 3. Verify .zst sidecar
	zstPath := filePath + ".zst"
	zstData, err := os.ReadFile(zstPath)
	if err != nil {
		t.Fatalf("reading .zst file failed: %v", err)
	}
	zr, err := zstd.NewReader(bytes.NewReader(zstData))
	if err != nil {
		t.Fatalf("zstd reader init failed: %v", err)
	}
	decompressedZst, err := io.ReadAll(zr)
	zr.Close()
	if err != nil {
		t.Fatalf("reading zstd stream failed: %v", err)
	}
	if string(decompressedZst) != originalContent {
		t.Errorf("zstd decompressed content mismatch")
	}
}

// TestPrecompressFile_RecompressesStaleSidecar reproduces the confirmed bug:
// a .gz/.br/.zst sidecar generated for an earlier version of a source file
// must NOT be left untouched (and silently stale) when the source file is
// later overwritten with different content. Existence-only checks left the
// sidecar frozen at the old content forever; a compressed-encoding client
// would receive bytes that decompress to the wrong (old) file while a
// plain client got the new one.
func TestPrecompressFile_RecompressesStaleSidecar(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "app.js")

	oldContent := strings.Repeat("console.log('v1 old build output');\n", 100)
	newContent := strings.Repeat("console.log('v2 completely different build output!!');\n", 120)

	buildModTime := time.Unix(1700000000, 0)

	// 1. Write v1 and precompress it. Pin the source mtime explicitly so the
	// ordering against the sidecar's mtime (set below) is deterministic.
	srcMTimeV1 := time.Unix(1_600_000_000, 0)
	if err := os.WriteFile(filePath, []byte(oldContent), 0o644); err != nil {
		t.Fatalf("failed to write v1 source file: %v", err)
	}
	if err := os.Chtimes(filePath, srcMTimeV1, srcMTimeV1); err != nil {
		t.Fatalf("failed to set v1 mtime: %v", err)
	}
	if err := precompressutils.PrecompressFile(filePath, buildModTime); err != nil {
		t.Fatalf("PrecompressFile (v1) failed: %v", err)
	}

	gzPath := filePath + ".gz"
	if _, err := os.Stat(gzPath); err != nil {
		t.Fatalf("expected .gz sidecar to exist after v1 precompress: %v", err)
	}

	// 2. Overwrite the source in place with different content and a strictly
	// later mtime than the sidecar's — this is exactly what an incremental
	// rebuild that reuses the output directory does.
	if err := os.WriteFile(filePath, []byte(newContent), 0o644); err != nil {
		t.Fatalf("failed to overwrite source file with v2: %v", err)
	}
	srcMTimeV2 := buildModTime.Add(1 * time.Hour) // strictly newer than the .gz sidecar's mtime
	if err := os.Chtimes(filePath, srcMTimeV2, srcMTimeV2); err != nil {
		t.Fatalf("failed to set v2 mtime: %v", err)
	}

	// 3. Re-run precompression. The bug: the sidecar already exists, so the
	// old existence-only check skipped regeneration entirely.
	if err := precompressutils.PrecompressFile(filePath, buildModTime); err != nil {
		t.Fatalf("PrecompressFile (v2) failed: %v", err)
	}

	// 4. The .gz sidecar must now decompress to the NEW content, not the old.
	gzData, err := os.ReadFile(gzPath)
	if err != nil {
		t.Fatalf("reading .gz file failed: %v", err)
	}
	gr, err := gzip.NewReader(bytes.NewReader(gzData))
	if err != nil {
		t.Fatalf("gzip reader init failed: %v", err)
	}
	decompressedGz, err := io.ReadAll(gr)
	_ = gr.Close()
	if err != nil {
		t.Fatalf("reading gzip stream failed: %v", err)
	}
	if string(decompressedGz) == oldContent {
		t.Fatalf("BUG: .gz sidecar still reflects stale v1 content after source was overwritten")
	}
	if string(decompressedGz) != newContent {
		t.Errorf("gzip decompressed content mismatch: got stale/incorrect bytes, want v2 content")
	}

	// Same assertion for .br
	brPath := filePath + ".br"
	brData, err := os.ReadFile(brPath)
	if err != nil {
		t.Fatalf("reading .br file failed: %v", err)
	}
	br := brotli.NewReader(bytes.NewReader(brData))
	decompressedBr, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("reading brotli stream failed: %v", err)
	}
	if string(decompressedBr) != newContent {
		t.Errorf("BUG: .br sidecar does not reflect v2 content")
	}

	// Same assertion for .zst
	zstPath := filePath + ".zst"
	zstData, err := os.ReadFile(zstPath)
	if err != nil {
		t.Fatalf("reading .zst file failed: %v", err)
	}
	zr, err := zstd.NewReader(bytes.NewReader(zstData))
	if err != nil {
		t.Fatalf("zstd reader init failed: %v", err)
	}
	decompressedZst, err := io.ReadAll(zr)
	zr.Close()
	if err != nil {
		t.Fatalf("reading zstd stream failed: %v", err)
	}
	if string(decompressedZst) != newContent {
		t.Errorf("BUG: .zst sidecar does not reflect v2 content")
	}
}

// TestPrecompressFile_SkipsFreshSidecar proves the staleness fix does not
// regress the intended optimization: an existing sidecar that is already
// newer than the source file (i.e. genuinely up to date, such as on a true
// no-op re-run) must be left untouched rather than needlessly recompressed.
//
// To prove "untouched" deterministically (without relying on wall-clock
// timing), a sentinel .gz sidecar with recognizably different payload bytes
// is planted with a newer mtime than the source. If PrecompressFile
// recompressed it anyway, the sentinel payload would be replaced by the
// real compressed source content.
func TestPrecompressFile_SkipsFreshSidecar(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "app.js")
	content := strings.Repeat("console.log('stable content, unchanged between runs');\n", 100)

	srcMTime := time.Unix(1_600_000_000, 0)
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to write source file: %v", err)
	}
	if err := os.Chtimes(filePath, srcMTime, srcMTime); err != nil {
		t.Fatalf("failed to set source mtime: %v", err)
	}

	// Plant a sentinel .gz sidecar whose payload is recognizably different
	// from what real compression of `content` would produce, with an mtime
	// strictly newer than the source file.
	gzPath := filePath + ".gz"
	sentinelContent := "SENTINEL: this exact gzip payload must survive untouched"
	var sentinelBuf bytes.Buffer
	gw, err := gzip.NewWriterLevel(&sentinelBuf, gzip.BestCompression)
	if err != nil {
		t.Fatalf("failed to init sentinel gzip writer: %v", err)
	}
	if _, err := gw.Write([]byte(sentinelContent)); err != nil {
		t.Fatalf("failed to write sentinel content: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("failed to close sentinel gzip writer: %v", err)
	}
	if err := os.WriteFile(gzPath, sentinelBuf.Bytes(), 0o644); err != nil {
		t.Fatalf("failed to write sentinel .gz sidecar: %v", err)
	}
	sidecarMTime := srcMTime.Add(1 * time.Hour) // strictly newer than the source
	if err := os.Chtimes(gzPath, sidecarMTime, sidecarMTime); err != nil {
		t.Fatalf("failed to set sentinel sidecar mtime: %v", err)
	}

	buildModTime := time.Unix(1700000000, 0)
	if err := precompressutils.PrecompressFile(filePath, buildModTime); err != nil {
		t.Fatalf("PrecompressFile failed: %v", err)
	}

	// The sentinel sidecar must be exactly as planted: untouched.
	gotBytes, err := os.ReadFile(gzPath)
	if err != nil {
		t.Fatalf("reading .gz file failed: %v", err)
	}
	if !bytes.Equal(gotBytes, sentinelBuf.Bytes()) {
		t.Errorf("fresh sidecar was recompressed even though it was already newer than the source; " +
			"this regresses the skip-when-unchanged optimization")
	}
}

func TestPrecompressFile_SkipsTinyFiles(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "tiny.js")
	if err := os.WriteFile(filePath, []byte("x=1;"), 0o644); err != nil {
		t.Fatalf("failed to write tiny file: %v", err)
	}

	modTime := time.Unix(1700000000, 0)
	if err := precompressutils.PrecompressFile(filePath, modTime); err != nil {
		t.Fatalf("PrecompressFile failed: %v", err)
	}

	// Tiny file should not produce sidecars
	if _, err := os.Stat(filePath + ".gz"); !os.IsNotExist(err) {
		t.Errorf("expected no .gz sidecar for tiny file")
	}
}

func TestPrecompressDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "static", "js")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("failed to create directory structure: %v", err)
	}

	jsFile := filepath.Join(subDir, "bundle.js")
	cssFile := filepath.Join(tmpDir, "style.css")
	pngFile := filepath.Join(tmpDir, "logo.png")

	largeContent := []byte(strings.Repeat("var x = 12345;\n", 50))
	_ = os.WriteFile(jsFile, largeContent, 0o644)
	_ = os.WriteFile(cssFile, largeContent, 0o644)
	_ = os.WriteFile(pngFile, []byte("PNG_MOCK_DATA"), 0o644)

	modTime := time.Unix(1700000000, 0)
	if err := precompressutils.PrecompressDirectory(tmpDir, modTime); err != nil {
		t.Fatalf("PrecompressDirectory failed: %v", err)
	}

	// js and css must have .gz, .br, and .zst
	for _, f := range []string{jsFile, cssFile} {
		if _, err := os.Stat(f + ".gz"); err != nil {
			t.Errorf("missing .gz for %s", f)
		}
		if _, err := os.Stat(f + ".br"); err != nil {
			t.Errorf("missing .br for %s", f)
		}
		if _, err := os.Stat(f + ".zst"); err != nil {
			t.Errorf("missing .zst for %s", f)
		}
	}

	// png must NOT have sidecars
	if _, err := os.Stat(pngFile + ".gz"); !os.IsNotExist(err) {
		t.Errorf("png should not have .gz sidecar")
	}
}
