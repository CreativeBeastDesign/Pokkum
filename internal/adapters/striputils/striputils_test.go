package striputils_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/striputils"
)

func TestIsELFBinary(t *testing.T) {
	tmpDir := t.TempDir()

	elfPath := filepath.Join(tmpDir, "mock.node")
	// Standard 64-bit LSB ELF header bytes
	elfHeader := []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	if err := os.WriteFile(elfPath, elfHeader, 0o755); err != nil {
		t.Fatalf("failed to write mock ELF: %v", err)
	}

	nonElfPath := filepath.Join(tmpDir, "script.js")
	if err := os.WriteFile(nonElfPath, []byte("console.log('hi');"), 0o644); err != nil {
		t.Fatalf("failed to write script: %v", err)
	}

	if !striputils.IsELFBinary(elfPath) {
		t.Errorf("expected IsELFBinary to return true for mock ELF")
	}

	if striputils.IsELFBinary(nonElfPath) {
		t.Errorf("expected IsELFBinary to return false for JS file")
	}

	if striputils.IsELFBinary(filepath.Join(tmpDir, "non_existent")) {
		t.Errorf("expected IsELFBinary to return false for missing file")
	}
}

func TestStripELFFile_NonELF(t *testing.T) {
	tmpDir := t.TempDir()
	txtPath := filepath.Join(tmpDir, "readme.txt")
	_ = os.WriteFile(txtPath, []byte("hello"), 0o644)

	ctx := context.Background()
	modTime := time.Unix(1700000000, 0)
	ok, err := striputils.StripELFFile(ctx, txtPath, modTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Errorf("expected ok=false for non-ELF file")
	}
}

func TestStripDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	nativeDir := filepath.Join(tmpDir, "native")
	_ = os.MkdirAll(nativeDir, 0o755)

	jsFile := filepath.Join(nativeDir, "index.js")
	_ = os.WriteFile(jsFile, []byte("console.log('test')"), 0o644)

	ctx := context.Background()
	modTime := time.Unix(1700000000, 0)

	count, skipped, err := striputils.StripDirectory(ctx, nativeDir, modTime)
	if err != nil {
		t.Fatalf("StripDirectory failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected count=0, got %d", count)
	}
	if len(skipped) != 0 {
		t.Errorf("expected no skipped files for a directory with no ELF binaries, got %v", skipped)
	}

	// Non-existent directory
	count, skipped, err = striputils.StripDirectory(ctx, filepath.Join(tmpDir, "nonexistent"), modTime)
	if err != nil {
		t.Fatalf("unexpected error on missing dir: %v", err)
	}
	if count != 0 {
		t.Errorf("expected count=0 on missing dir")
	}
	if len(skipped) != 0 {
		t.Errorf("expected no skipped files on missing dir, got %v", skipped)
	}
}
