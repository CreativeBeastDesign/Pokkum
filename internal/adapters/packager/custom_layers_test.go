package packager

import (
	"archive/tar"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
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

	layer, pruned, _, err := BuildDirectoryTreeLayerWithPruning(ctx, ports.LinuxAMD64, tmpDir, "/app/vendor", modTime, ports.CompressionGzip, pruneOpts)
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

	// PruneResult must account for exactly the three junk files above: this is
	// the only production code path that runs pruning (see BuildDirectoryTreeLayerWithPruning's
	// doc comment), so if this accounting is wrong, the build-log summary in
	// Packager.Build silently lies about what was removed from the image.
	if pruned.FilesPruned != 3 {
		t.Errorf("expected 3 files pruned, got %d (%v)", pruned.FilesPruned, pruned.PrunedPaths)
	}
	wantPruned := map[string]bool{"index.d.ts": true, "index.js.map": true, "README.md": true}
	for _, p := range pruned.PrunedPaths {
		if !wantPruned[p] {
			t.Errorf("unexpected path in PrunedPaths: %q", p)
		}
		delete(wantPruned, p)
	}
	if len(wantPruned) != 0 {
		t.Errorf("PrunedPaths missing expected entries: %v", wantPruned)
	}
	wantBytes := int64(len("declare const a: number") + len("{}") + len("# Readme"))
	if pruned.BytesSaved != wantBytes {
		t.Errorf("expected BytesSaved %d, got %d", wantBytes, pruned.BytesSaved)
	}
}

// TestBuildDirectoryTreeLayerWithPruning_ExcludeDirs guards the fix for a
// real bug (see Lessons.md's "missing /app/server/index.js" entry): AppServerDir
// packages a whole build output tree that also contains client/ and
// prerendered/ subdirectories already packaged into their own layers
// elsewhere. ExcludeDirs must skip those subtrees entirely — even under
// NoPrune: true, since this isn't about disposable junk — while leaving a
// same-named FILE (as opposed to directory) alone, and leaving nested
// directory structure that isn't excluded intact.
func TestBuildDirectoryTreeLayerWithPruning_ExcludeDirs(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()

	_ = os.WriteFile(filepath.Join(tmpDir, "index.js"), []byte("entrypoint"), 0644)
	_ = os.MkdirAll(filepath.Join(tmpDir, "server", "chunks"), 0755)
	_ = os.WriteFile(filepath.Join(tmpDir, "server", "chunks", "handler.js"), []byte("handler"), 0644)
	_ = os.MkdirAll(filepath.Join(tmpDir, "client"), 0755)
	_ = os.WriteFile(filepath.Join(tmpDir, "client", "app.js"), []byte("client bundle"), 0644)
	_ = os.MkdirAll(filepath.Join(tmpDir, "prerendered"), 0755)
	_ = os.WriteFile(filepath.Join(tmpDir, "prerendered", "index.html"), []byte("<html></html>"), 0644)

	modTime := time.Unix(1700000000, 0)
	pruneOpts := pruneutils.PruneOptions{
		NoPrune:     true,
		ExcludeDirs: []string{"client", "prerendered"},
	}

	layer, _, _, err := BuildDirectoryTreeLayerWithPruning(ctx, ports.LinuxAMD64, tmpDir, "/app/server", modTime, ports.CompressionGzip, pruneOpts)
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

	// The real entrypoint and its nested server/ subtree must survive intact
	// and with their original relative nesting preserved — this is the exact
	// shape a real @sveltejs/adapter-node build's chunk files depend on to
	// resolve their own relative imports correctly.
	for _, want := range []string{"app/server/index.js", "app/server/server/chunks/handler.js"} {
		if !entries[want] {
			t.Errorf("expected %s in layer tar, got %v", want, entries)
		}
	}
	// client/ and prerendered/ must be excluded entirely, despite NoPrune: true.
	for name := range entries {
		if strings.HasPrefix(name, "app/server/client/") || strings.HasPrefix(name, "app/server/prerendered/") {
			t.Errorf("expected client/prerendered to be excluded, found %s", name)
		}
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
