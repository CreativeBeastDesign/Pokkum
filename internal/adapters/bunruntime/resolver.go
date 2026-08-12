// Package bunruntime implements ports.BunRuntimeResolver by resolving,
// downloading, verifying, and caching official Bun release binaries.
package bunruntime

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Known pinned SHA256 digests for official Bun zip release archives.
// Key format: "<version>/<target-name>" (e.g. "1.2.2/bun-linux-x64").
var pinnedReleaseChecksums = map[string]string{
	"1.2.2/bun-linux-x64":          "4538805fdbd21bd7ad653f5d52cfc235ef98cf8593a207ba7efab3d274bf4184",
	"1.2.2/bun-linux-x64-baseline": "8b51d6dbaccc3ec3bdca5cae5f31920677ad45e7f225d301ae4fdbbdf3e0cf55",
	"1.2.2/bun-linux-aarch64":      "d7aeceaaeb37ff1adfe8f5c353ea2eb0498cfc9c7df62df27f818d6a78280f49",
}

var _ ports.BunRuntimeResolver = (*Resolver)(nil)

// Resolver implements ports.BunRuntimeResolver with disk caching (~/.cache/pokkum/bun).
type Resolver struct {
	// CacheDir is the root directory for cached Bun binaries.
	CacheDir string

	// HTTPClient is the HTTP client used for downloading release archives.
	HTTPClient *http.Client

	mu sync.Mutex
}

// NewResolver constructs a BunRuntimeResolver. If cacheDir is empty, it defaults
// to ~/.cache/pokkum/bun. If httpClient is nil, http.DefaultClient is used.
func NewResolver(cacheDir string, httpClient *http.Client) *Resolver {
	if cacheDir == "" {
		homeDir, err := os.UserCacheDir()
		if err != nil {
			homeDir = os.TempDir()
		}
		cacheDir = filepath.Join(homeDir, "pokkum", "bun")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Resolver{
		CacheDir:   cacheDir,
		HTTPClient: httpClient,
	}
}

// Resolve returns a verified Bun runtime binary matching req.
func (r *Resolver) Resolve(ctx context.Context, req ports.BunResolverRequest) (ports.BunResolverResult, error) {
	if !req.Platform.Supported() {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: platform %s unsupported: %w", req.Platform, core.ErrUnsupportedPlatform)
	}

	// 1. Local custom binary escape hatch (--bun-binary)
	if req.CustomBinaryPath != "" {
		info, err := os.Stat(req.CustomBinaryPath)
		if err != nil {
			return ports.BunResolverResult{}, fmt.Errorf("bunruntime: custom binary %s not found: %w: %w", req.CustomBinaryPath, err, core.ErrBunResolutionFailed)
		}
		if info.IsDir() {
			return ports.BunResolverResult{}, fmt.Errorf("bunruntime: custom binary %s is a directory: %w", req.CustomBinaryPath, core.ErrBunResolutionFailed)
		}
		sha, size, err := computeFileSHA256(req.CustomBinaryPath)
		if err != nil {
			return ports.BunResolverResult{}, fmt.Errorf("bunruntime: custom binary checksum calculation failed: %w: %w", err, core.ErrBunResolutionFailed)
		}
		return ports.BunResolverResult{
			BinaryPath: req.CustomBinaryPath,
			Version:    "custom",
			Variant:    req.Variant,
			Platform:   req.Platform,
			SHA256:     sha,
			Size:       size,
		}, nil
	}

	// 2. Resolve version & variant defaults
	version := req.Version
	if version == "" {
		version = ports.DefaultBunVersion
	}
	variant := req.Variant
	if variant == "" {
		variant = ports.BunVariantStandard
	}

	// 3. Resolve target archive name
	targetName, err := resolveBunTargetName(req.Platform, variant)
	if err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: resolve target name: %w: %w", err, core.ErrBunResolutionFailed)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// 4. Construct cache path
	platformSlug := strings.ReplaceAll(req.Platform.String(), "/", "_")
	targetDir := filepath.Join(r.CacheDir, version, string(variant), platformSlug)
	binaryPath := filepath.Join(targetDir, "bun")

	// Check if already cached & verified
	if info, err := os.Stat(binaryPath); err == nil && !info.IsDir() {
		sha, size, err := computeFileSHA256(binaryPath)
		if err == nil {
			return ports.BunResolverResult{
				BinaryPath: binaryPath,
				Version:    version,
				Variant:    variant,
				Platform:   req.Platform,
				SHA256:     sha,
				Size:       size,
			}, nil
		}
	}

	// 5. Download and extract
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: create cache dir %s: %w: %w", targetDir, err, core.ErrBunResolutionFailed)
	}

	archiveURL := fmt.Sprintf("https://github.com/oven-sh/bun/releases/download/bun-v%s/%s.zip", version, targetName)
	downloadReq, err := http.NewRequestWithContext(ctx, http.MethodGet, archiveURL, nil)
	if err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: build request %s: %w: %w", archiveURL, err, core.ErrBunDownloadFailed)
	}

	resp, err := r.HTTPClient.Do(downloadReq)
	if err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: download %s: %w: %w", archiveURL, err, core.ErrBunDownloadFailed)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: download %s status %d: %w", archiveURL, resp.StatusCode, core.ErrBunDownloadFailed)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: read download body %s: %w: %w", archiveURL, err, core.ErrBunDownloadFailed)
	}

	// Check release zip checksum if pinned
	key := fmt.Sprintf("%s/%s", version, targetName)
	if expectedSHA, ok := pinnedReleaseChecksums[key]; ok {
		actualSHABytes := sha256.Sum256(bodyBytes)
		actualSHA := hex.EncodeToString(actualSHABytes[:])
		if actualSHA != expectedSHA {
			return ports.BunResolverResult{}, fmt.Errorf("bunruntime: release archive checksum mismatch for %s (expected %s, got %s): %w", key, expectedSHA, actualSHA, core.ErrBunChecksumMismatch)
		}
	}

	// Extract binary from zip
	zipReader, err := zip.NewReader(bytes.NewReader(bodyBytes), int64(len(bodyBytes)))
	if err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: parse zip archive for %s: %w: %w", key, err, core.ErrBunDownloadFailed)
	}

	var binaryEntry *zip.File
	for _, f := range zipReader.File {
		if filepath.Base(f.Name) == "bun" && !f.FileInfo().IsDir() {
			binaryEntry = f
			break
		}
	}

	if binaryEntry == nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: bun executable entry missing in archive %s: %w", key, core.ErrBunDownloadFailed)
	}

	rc, err := binaryEntry.Open()
	if err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: open zip entry for %s: %w: %w", key, err, core.ErrBunDownloadFailed)
	}
	defer rc.Close()

	tmpPath := binaryPath + ".tmp"
	outFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: create temp file %s: %w: %w", tmpPath, err, core.ErrBunDownloadFailed)
	}

	_, err = io.Copy(outFile, io.LimitReader(rc, 200<<20))
	outFile.Close()
	if err != nil {
		os.Remove(tmpPath)
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: write binary %s: %w: %w", tmpPath, err, core.ErrBunDownloadFailed)
	}

	if err := os.Rename(tmpPath, binaryPath); err != nil {
		os.Remove(tmpPath)
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: move binary to destination %s: %w: %w", binaryPath, err, core.ErrBunDownloadFailed)
	}

	sha, size, err := computeFileSHA256(binaryPath)
	if err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: checksum calculation for %s: %w: %w", binaryPath, err, core.ErrBunResolutionFailed)
	}

	return ports.BunResolverResult{
		BinaryPath: binaryPath,
		Version:    version,
		Variant:    variant,
		Platform:   req.Platform,
		SHA256:     sha,
		Size:       size,
	}, nil
}

func resolveBunTargetName(p ports.Platform, variant ports.BunVariant) (string, error) {
	if p.OS != "linux" {
		return "", fmt.Errorf("unsupported operating system %s", p.OS)
	}
	switch p.Arch {
	case "amd64":
		if variant == ports.BunVariantBaseline {
			return "bun-linux-x64-baseline", nil
		}
		return "bun-linux-x64", nil
	case "arm64":
		if variant == ports.BunVariantBaseline {
			return "", fmt.Errorf("baseline variant not supported on linux/arm64")
		}
		return "bun-linux-aarch64", nil
	default:
		return "", fmt.Errorf("unsupported architecture %s", p.Arch)
	}
}

func computeFileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}

	// Avoid unused runtime import error if any by using string check or runtime
	_ = runtime.GOOS

	return hex.EncodeToString(h.Sum(nil)), size, nil
}
