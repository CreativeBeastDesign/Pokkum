package packager

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	layer, err := BuildCustomFileLayer(ctx, ports.LinuxAMD64, "/usr/local/bin/bun", sourceFile, modTime)
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
	layer, err := BuildDirectoryTreeLayer(ctx, ports.LinuxAMD64, tmpDir, "/app/client", modTime)
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
