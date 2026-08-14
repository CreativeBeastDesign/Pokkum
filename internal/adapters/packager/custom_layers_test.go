package packager

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/pruneutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestBuildCustomFileLayer(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	sourceFile := filepath.Join(tmpDir, "bun")
	content := []byte("#!/bin/sh\necho 'bun'")
	if err := os.WriteFile(sourceFile, content, 0755); err != nil {
		t.Fatalf("failed to write source file: %v", err)
	}

	modTime := time.Unix(1700000000, 0)
	layer, err := BuildCustomFileLayer(ctx, ports.LinuxAMD64, "/usr/local/bin/bun", sourceFile, modTime, ports.CompressionGzip)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rc, err := layer.Uncompressed()
	if err != nil {
		t.Fatalf("failed to open uncompressed layer stream: %v", err)
	}
	defer rc.Close()

	tr := tar.NewReader(rc)
	foundBun := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read error: %v", err)
		}
		if hdr.Name == "usr/local/bin/bun" {
			foundBun = true
			if hdr.Uid != nonrootUID || hdr.Gid != nonrootGID {
				t.Errorf("expected UID:GID %d:%d, got %d:%d", nonrootUID, nonrootGID, hdr.Uid, hdr.Gid)
			}
			if !hdr.ModTime.Equal(modTime) {
				t.Errorf("expected modTime %v, got %v", modTime, hdr.ModTime)
			}
			readBytes, err := io.ReadAll(tr)
			if err != nil {
				t.Fatalf("read entry error: %v", err)
			}
			if string(readBytes) != string(content) {
				t.Errorf("expected content %q, got %q", content, readBytes)
			}
		}
	}

	if !foundBun {
		t.Error("expected /usr/local/bin/bun entry in tar layer, but was missing")
	}
}

func TestBuildDirectoryTreeLayer(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	// Create nested structure
	subDir := filepath.Join(tmpDir, "sub")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatalf("mkdir error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("hello"), 0644); err != nil {
		t.Fatalf("write file1 error: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "file2.txt"), []byte("world"), 0644); err != nil {
		t.Fatalf("write file2 error: %v", err)
	}

	modTime := time.Unix(1700000000, 0)
	layer, err := BuildDirectoryTreeLayer(ctx, ports.LinuxAMD64, tmpDir, "/app/client", modTime, ports.CompressionGzip)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rc, err := layer.Uncompressed()
	if err != nil {
		t.Fatalf("failed to open uncompressed layer stream: %v", err)
	}
	defer rc.Close()

	tr := tar.NewReader(rc)
	entries := make(map[string]bool)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read error: %v", err)
		}
		entries[hdr.Name] = true
	}

	expectedEntries := []string{
		"app/",
		"app/client/",
		"app/client/file1.txt",
		"app/client/sub/",
		"app/client/sub/file2.txt",
	}

	for _, e := range expectedEntries {
		if !entries[e] {
			t.Errorf("expected entry %s missing in layer tar", e)
		}
	}
}

func TestBuildDirectoryTreeLayerWithPruning(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	_ = os.WriteFile(filepath.Join(tmpDir, "index.js"), []byte("console.log(1)"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "index.d.ts"), []byte("declare const a: number"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "index.js.map"), []byte("{}"), 0644)
	_ = os.WriteFile(filepath.Join(tmpDir, "README.md"), []byte("# Readme"), 0644)

	modTime := time.Unix(1700000000, 0)
	pruneOpts := pruneutils.PruneOptions{
		NoPrune:       false,
		KeepSourcemap: false,
	}

	layer, err := BuildDirectoryTreeLayerWithPruning(ctx, ports.LinuxAMD64, tmpDir, "/app/vendor", modTime, ports.CompressionGzip, pruneOpts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rc, err := layer.Uncompressed()
	if err != nil {
		t.Fatalf("failed to open uncompressed layer stream: %v", err)
	}
	defer rc.Close()

	tr := tar.NewReader(rc)
	entries := make(map[string]bool)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read error: %v", err)
		}
		entries[hdr.Name] = true
	}

	if !entries["app/vendor/index.js"] {
		t.Errorf("expected app/vendor/index.js in layer tar")
	}
	if entries["app/vendor/index.d.ts"] {
		t.Errorf("index.d.ts was not pruned from layer tar")
	}
	if entries["app/vendor/index.js.map"] {
		t.Errorf("index.js.map was not pruned from layer tar")
	}
	if entries["app/vendor/README.md"] {
		t.Errorf("README.md was not pruned from layer tar")
	}
}

func TestBuildCustomFileLayer_LayerCaching(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	cacheDir := t.TempDir()
	t.Setenv("POKKUM_CACHE_DIR", cacheDir)

	sourceFile := filepath.Join(tmpDir, "bun")
	if err := os.WriteFile(sourceFile, []byte("#!/bin/sh\necho 'cached bun'"), 0755); err != nil {
		t.Fatalf("failed to write source file: %v", err)
	}

	modTime := time.Unix(1700000000, 0)

	// First call builds and caches
	layer1, err := BuildCustomFileLayer(ctx, ports.LinuxAMD64, "/usr/local/bin/bun", sourceFile, modTime, ports.CompressionGzip)
	if err != nil {
		t.Fatalf("first BuildCustomFileLayer failed: %v", err)
	}
	digest1, err := layer1.Digest()
	if err != nil {
		t.Fatalf("layer1.Digest() failed: %v", err)
	}

	// Second call should hit the cache and return identical layer digest & diffID
	layer2, err := BuildCustomFileLayer(ctx, ports.LinuxAMD64, "/usr/local/bin/bun", sourceFile, modTime, ports.CompressionGzip)
	if err != nil {
		t.Fatalf("second BuildCustomFileLayer failed: %v", err)
	}
	digest2, err := layer2.Digest()
	if err != nil {
		t.Fatalf("layer2.Digest() failed: %v", err)
	}

	if digest1 != digest2 {
		t.Errorf("expected cached layer digest %s to equal %s", digest2, digest1)
	}

	diffID1, _ := layer1.DiffID()
	diffID2, _ := layer2.DiffID()
	if diffID1 != diffID2 {
		t.Errorf("expected cached layer diffID %s to equal %s", diffID2, diffID1)
	}
}
