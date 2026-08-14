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
