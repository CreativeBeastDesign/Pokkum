package bunruntime

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestResolver_CustomBinaryPath(t *testing.T) {
	tmpDir := t.TempDir()
	customPath := filepath.Join(tmpDir, "custom-bun")
	content := []byte("#!/bin/sh\necho 'bun 1.2.2'")
	if err := os.WriteFile(customPath, content, 0755); err != nil {
		t.Fatalf("failed to write custom binary: %v", err)
	}

	resolver := NewResolver(t.TempDir(), nil)
	req := ports.BunResolverRequest{
		Platform:         ports.LinuxAMD64,
		CustomBinaryPath: customPath,
		SourceDateEpoch:  time.Unix(1700000000, 0),
	}

	res, err := resolver.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.BinaryPath != customPath {
		t.Errorf("expected BinaryPath %s, got %s", customPath, res.BinaryPath)
	}
	if res.Version != "custom" {
		t.Errorf("expected Version 'custom', got %s", res.Version)
	}

	expectedSHA := sha256.Sum256(content)
	if res.SHA256 != hex.EncodeToString(expectedSHA[:]) {
		t.Errorf("expected SHA256 %s, got %s", hex.EncodeToString(expectedSHA[:]), res.SHA256)
	}
}

func TestResolver_UnsupportedPlatform(t *testing.T) {
	resolver := NewResolver(t.TempDir(), nil)
	req := ports.BunResolverRequest{
		Platform:        ports.Platform{OS: "darwin", Arch: "arm64"},
		SourceDateEpoch: time.Unix(1700000000, 0),
	}

	_, err := resolver.Resolve(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for unsupported platform, got nil")
	}
	if !errors.Is(err, core.ErrUnsupportedPlatform) {
		t.Errorf("expected error to wrap ErrUnsupportedPlatform, got %v", err)
	}
}

func TestResolver_DownloadAndCache(t *testing.T) {
	bunBinaryContent := []byte("#!/bin/sh\necho 'fake bun'")

	// Build zip containing 'bun'
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create("bun-linux-x64/bun")
	if err != nil {
		t.Fatalf("zip create error: %v", err)
	}
	if _, err := f.Write(bunBinaryContent); err != nil {
		t.Fatalf("zip write error: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close error: %v", err)
	}
	zipBytes := buf.Bytes()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(zipBytes)
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	resolver := NewResolver(cacheDir, server.Client())

	// Override URL creation in test by mocking transport or custom client
	client := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			resp, err := server.Client().Get(server.URL)
			return resp, err
		}),
	}
	resolver.HTTPClient = client

	req := ports.BunResolverRequest{
		Platform:        ports.LinuxAMD64,
		Version:         "9.9.9", // Unpinned version to skip checksum map
		Variant:         ports.BunVariantStandard,
		SourceDateEpoch: time.Unix(1700000000, 0),
	}

	res, err := resolver.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected resolve error: %v", err)
	}

	if res.Version != "9.9.9" {
		t.Errorf("expected version 9.9.9, got %s", res.Version)
	}

	readContent, err := os.ReadFile(res.BinaryPath)
	if err != nil {
		t.Fatalf("failed to read cached binary: %v", err)
	}
	if !bytes.Equal(readContent, bunBinaryContent) {
		t.Errorf("cached binary content mismatch: expected %q, got %q", bunBinaryContent, readContent)
	}

	// Resolve second time, ensuring cache hit
	res2, err := resolver.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error on second resolve: %v", err)
	}
	if res2.BinaryPath != res.BinaryPath {
		t.Errorf("expected same cached binary path %s, got %s", res.BinaryPath, res2.BinaryPath)
	}
}

func TestResolver_StubLauncher_BypassedByCustomBinary(t *testing.T) {
	tmpDir := t.TempDir()
	customPath := filepath.Join(tmpDir, "custom-bun")
	content := []byte("#!/bin/sh\necho 'custom'")
	if err := os.WriteFile(customPath, content, 0755); err != nil {
		t.Fatalf("failed to write custom binary: %v", err)
	}

	resolver := NewResolver(t.TempDir(), nil)
	req := ports.BunResolverRequest{
		Platform:         ports.LinuxAMD64,
		CustomBinaryPath: customPath,
		StubLauncher:     true,
		SourceDateEpoch:  time.Unix(1700000000, 0),
	}

	res, err := resolver.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.BinaryPath != customPath {
		t.Errorf("expected BinaryPath %s, got %s", customPath, res.BinaryPath)
	}
}

func TestResolver_StubLauncher_CachedHit(t *testing.T) {
	cacheDir := t.TempDir()
	stubTargetDir := filepath.Join(cacheDir, "stubs", "1.2.2", "standard", "linux_amd64")
	if err := os.MkdirAll(stubTargetDir, 0755); err != nil {
		t.Fatalf("failed to create stub cache dir: %v", err)
	}

	cachedStubPath := filepath.Join(stubTargetDir, "bun")
	content := []byte("compiled-stub-binary-bytes")
	if err := os.WriteFile(cachedStubPath, content, 0755); err != nil {
		t.Fatalf("failed to write cached stub: %v", err)
	}

	resolver := NewResolver(cacheDir, nil)
	req := ports.BunResolverRequest{
		Platform:        ports.LinuxAMD64,
		Version:         "1.2.2",
		Variant:         ports.BunVariantStandard,
		StubLauncher:    true,
		SourceDateEpoch: time.Unix(1700000000, 0),
	}

	res, err := resolver.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.BinaryPath != cachedStubPath {
		t.Errorf("expected BinaryPath %s, got %s", cachedStubPath, res.BinaryPath)
	}

	expectedSHA := sha256.Sum256(content)
	if res.SHA256 != hex.EncodeToString(expectedSHA[:]) {
		t.Errorf("expected SHA256 %s, got %s", hex.EncodeToString(expectedSHA[:]), res.SHA256)
	}
}

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestResolver_StubLauncher_AdversarialTargetAndCacheIsolation(t *testing.T) {
	t.Run("unsupported_os_fails_fast", func(t *testing.T) {
		resolver := NewResolver(t.TempDir(), nil)
		req := ports.BunResolverRequest{
			Platform:        ports.Platform{OS: "darwin", Arch: "arm64"},
			Version:         "1.2.2",
			Variant:         ports.BunVariantStandard,
			StubLauncher:    true,
			SourceDateEpoch: time.Unix(1700000000, 0),
		}

		_, err := resolver.Resolve(context.Background(), req)
		if err == nil {
			t.Fatal("expected error for unsupported OS with StubLauncher")
		}
		if !errors.Is(err, core.ErrUnsupportedPlatform) {
			t.Errorf("expected ErrUnsupportedPlatform, got: %v", err)
		}
	})

	t.Run("baseline_variant_on_arm64_fails_fast", func(t *testing.T) {
		resolver := NewResolver(t.TempDir(), nil)
		req := ports.BunResolverRequest{
			Platform:        ports.LinuxARM64,
			Version:         "1.2.2",
			Variant:         ports.BunVariantBaseline,
			StubLauncher:    true,
			SourceDateEpoch: time.Unix(1700000000, 0),
		}

		_, err := resolver.Resolve(context.Background(), req)
		if err == nil {
			t.Fatal("expected error for baseline variant on arm64 with StubLauncher")
		}
		if !errors.Is(err, core.ErrBunResolutionFailed) {
			t.Errorf("expected ErrBunResolutionFailed, got: %v", err)
		}
	})

	t.Run("cache_path_isolation_between_stock_and_stub", func(t *testing.T) {
		cacheDir := t.TempDir()

		// Populate stock bun cache
		stockDir := filepath.Join(cacheDir, "1.2.2", "standard", "linux_amd64")
		_ = os.MkdirAll(stockDir, 0755)
		stockBinary := filepath.Join(stockDir, "bun")
		_ = os.WriteFile(stockBinary, []byte("stock-bun-bytes"), 0755)

		// Populate stub bun cache
		stubDir := filepath.Join(cacheDir, "stubs", "1.2.2", "standard", "linux_amd64")
		_ = os.MkdirAll(stubDir, 0755)
		stubBinary := filepath.Join(stubDir, "bun")
		_ = os.WriteFile(stubBinary, []byte("stub-launcher-bytes"), 0755)

		resolver := NewResolver(cacheDir, nil)

		// Request stock
		stockRes, err := resolver.Resolve(context.Background(), ports.BunResolverRequest{
			Platform:        ports.LinuxAMD64,
			Version:         "1.2.2",
			Variant:         ports.BunVariantStandard,
			StubLauncher:    false,
			SourceDateEpoch: time.Unix(1700000000, 0),
		})
		if err != nil {
			t.Fatalf("resolve stock failed: %v", err)
		}
		if stockRes.BinaryPath != stockBinary {
			t.Errorf("expected stock binary %s, got %s", stockBinary, stockRes.BinaryPath)
		}

		// Request stub
		stubRes, err := resolver.Resolve(context.Background(), ports.BunResolverRequest{
			Platform:        ports.LinuxAMD64,
			Version:         "1.2.2",
			Variant:         ports.BunVariantStandard,
			StubLauncher:    true,
			SourceDateEpoch: time.Unix(1700000000, 0),
		})
		if err != nil {
			t.Fatalf("resolve stub failed: %v", err)
		}
		if stubRes.BinaryPath != stubBinary {
			t.Errorf("expected stub binary %s, got %s", stubBinary, stubRes.BinaryPath)
		}

		if stockRes.SHA256 == stubRes.SHA256 {
			t.Errorf("stock and stub SHA256 should differ")
		}
	})
}

// TestResolver_StubLauncher_CompileIsDeterministic guards the bug fixed in
// compileStub: `bun build --compile` embeds something derived from the entry
// file's path into the compiled binary, and every prior call built that
// entry file under a fresh os.MkdirTemp directory, so compiling the exact
// same stub source for the exact same (version, variant, platform) produced
// a different SHA256 on every invocation — a direct violation of this
// repo's bit-for-bit OCI reproducibility invariant (a --stub-launcher image
// would fail `pokkum verify --rebuild`). No prior test in this file invoked
// the real bun compiler at all, which is how the bug shipped undetected.
// Skipped like TestRealBuildIsReproducibleAcrossRuns: real compiles are slow
// and require bun on PATH.
func TestResolver_StubLauncher_CompileIsDeterministic(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real bun compile determinism check in short mode")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not found on PATH; skipping real bun compile determinism check")
	}

	platforms := []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64}

	for _, platform := range platforms {
		t.Run(platform.String(), func(t *testing.T) {
			req := ports.BunResolverRequest{
				Platform:        platform,
				Version:         "1.2.2",
				Variant:         ports.BunVariantStandard,
				StubLauncher:    true,
				SourceDateEpoch: time.Unix(1700000000, 0),
			}

			const runs = 3
			shas := make([]string, 0, runs)
			for i := 0; i < runs; i++ {
				// A fresh cache dir per run forces a real compile each time
				// instead of short-circuiting on the cache-hit path.
				resolver := NewResolver(t.TempDir(), nil)
				res, err := resolver.Resolve(context.Background(), req)
				if err != nil {
					t.Fatalf("run %d: Resolve failed: %v", i, err)
				}
				shas = append(shas, res.SHA256)
			}

			for i := 1; i < runs; i++ {
				if shas[i] != shas[0] {
					t.Errorf("compiled stub launcher is non-deterministic for %s: run 0 SHA256=%s, run %d SHA256=%s", platform, shas[0], i, shas[i])
				}
			}
		})
	}
}
