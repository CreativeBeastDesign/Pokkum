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

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
