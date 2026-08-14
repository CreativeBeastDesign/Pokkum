package remotecacheutils_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/remotecacheutils"
)

func TestComputeSourceTreeHash(t *testing.T) {
	tmpDir := t.TempDir()

	srcDir := filepath.Join(tmpDir, "src")
	_ = os.MkdirAll(srcDir, 0o755)
	_ = os.WriteFile(filepath.Join(srcDir, "app.js"), []byte("console.log('hi');"), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(`{"name":"test"}`), 0o644)

	// Create ignored directory
	nodeModules := filepath.Join(tmpDir, "node_modules", "some-pkg")
	_ = os.MkdirAll(nodeModules, 0o755)
	_ = os.WriteFile(filepath.Join(nodeModules, "index.js"), []byte("ignored"), 0o644)

	// Compute initial hash
	h1, err := remotecacheutils.ComputeSourceTreeHash(tmpDir)
	if err != nil {
		t.Fatalf("ComputeSourceTreeHash failed: %v", err)
	}
	if h1 == "" {
		t.Fatalf("expected non-empty hash")
	}

	// Re-compute without changes -> must match
	h2, err := remotecacheutils.ComputeSourceTreeHash(tmpDir)
	if err != nil {
		t.Fatalf("ComputeSourceTreeHash 2 failed: %v", err)
	}
	if h1 != h2 {
		t.Errorf("expected deterministic hash, got %s != %s", h1, h2)
	}

	// Modify node_modules -> hash should NOT change
	_ = os.WriteFile(filepath.Join(nodeModules, "index.js"), []byte("modified-ignored"), 0o644)
	h3, _ := remotecacheutils.ComputeSourceTreeHash(tmpDir)
	if h1 != h3 {
		t.Errorf("expected node_modules changes to be ignored, got %s != %s", h1, h3)
	}

	// Modify src file -> hash MUST change
	_ = os.WriteFile(filepath.Join(srcDir, "app.js"), []byte("console.log('updated');"), 0o644)
	h4, _ := remotecacheutils.ComputeSourceTreeHash(tmpDir)
	if h1 == h4 {
		t.Errorf("expected hash to change when src/app.js changes")
	}
}

func TestComputeInputHash(t *testing.T) {
	tmpDir := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(`{"name":"test"}`), 0o644)
	_ = os.WriteFile(filepath.Join(tmpDir, "bun.lock"), []byte(`lockfile-contents`), 0o644)

	params1 := remotecacheutils.InputParams{
		ProjectDir:      tmpDir,
		BaseImageDigest: "sha256:1111111111111111111111111111111111111111111111111111111111111111",
		BunVersion:      "1.2.2",
		BunVariant:      "standard",
		Platforms:       []string{"linux/amd64", "linux/arm64"},
		Strategy:        "layered",
		Compression:     "gzip",
	}

	hash1, err := remotecacheutils.ComputeInputHash(params1)
	if err != nil {
		t.Fatalf("ComputeInputHash failed: %v", err)
	}
	if len(hash1) != 64 {
		t.Fatalf("expected 64-char sha256 hex, got %d chars: %s", len(hash1), hash1)
	}

	// Verify CacheTag
	tag := remotecacheutils.CacheTag(hash1)
	if tag != "cache-"+hash1 {
		t.Errorf("CacheTag mismatch: %s", tag)
	}

	// Changing base image changes the input hash
	params2 := params1
	params2.BaseImageDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	hash2, _ := remotecacheutils.ComputeInputHash(params2)
	if hash1 == hash2 {
		t.Errorf("expected hash to change on base image change")
	}

	// Platform order independence
	params3 := params1
	params3.Platforms = []string{"linux/arm64", "linux/amd64"} // reversed
	hash3, _ := remotecacheutils.ComputeInputHash(params3)
	if hash1 != hash3 {
		t.Errorf("expected hash to be independent of platform order")
	}
}
