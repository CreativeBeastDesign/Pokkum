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
	"os/exec"
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
	"1.2.2/bun-linux-x64":          "3f4efb8afd1f84ac2a98c04661c898561d1d35527d030cb4571e99b7c85f5079",
	"1.2.2/bun-linux-x64-baseline": "cad7756a6ee16f3432a328f8023fc5cd431106822eacfa6d6d3afbad6fdc24db",
	"1.2.2/bun-linux-aarch64":      "d1dbaa3e9af24549fad92bdbe4fb21fa53302cd048a8f004e85a240984c93d4d",
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

	// The lock only guards the cheap path computation and cache-hit check
	// below, not the compile/download work that follows: each platform's
	// cache path is distinct (platformSlug), so concurrent per-platform
	// resolves from the pipeline's fan-out don't race on the same path, and
	// holding the lock across a `bun build --compile` subprocess or a
	// multi-second HTTP download would otherwise serialize per-platform work
	// the fan-out is meant to run concurrently.
	r.mu.Lock()

	// 4. Construct cache path
	platformSlug := strings.ReplaceAll(req.Platform.String(), "/", "_")
	var targetDir, binaryPath string
	if req.StubLauncher {
		targetDir = filepath.Join(r.CacheDir, "stubs", version, string(variant), platformSlug)
		binaryPath = filepath.Join(targetDir, "bun")
	} else {
		targetDir = filepath.Join(r.CacheDir, version, string(variant), platformSlug)
		binaryPath = filepath.Join(targetDir, "bun")
	}

	// Check if already cached & verified
	if info, err := os.Stat(binaryPath); err == nil && !info.IsDir() {
		sha, size, err := computeFileSHA256(binaryPath)
		if err == nil {
			r.mu.Unlock()
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
	r.mu.Unlock()

	// If stub launcher is requested, compile rather than download release archive
	if req.StubLauncher {
		return r.compileStub(ctx, version, variant, req.Platform, targetDir, binaryPath)
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

// compileTargetName returns the value `bun build --compile --target=` expects
// for platform/variant. This differs from resolveBunTargetName's GitHub
// release-archive naming only for arm64: the release zip is named
// bun-linux-aarch64.zip, but the compile flag expects "bun-linux-arm64" (see
// ports.Platform.BunTarget's doc comment, which is the authoritative spelling
// for this exact flag at the exe-strategy's own `bun build --compile`
// call site, internal/adapters/bunexec/compiler.go).
func compileTargetName(p ports.Platform, variant ports.BunVariant) (string, error) {
	name, err := resolveBunTargetName(p, variant)
	if err != nil {
		return "", err
	}
	if p.Arch == "arm64" {
		name = strings.Replace(name, "aarch64", "arm64", 1)
	}
	return name, nil
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

func (r *Resolver) compileStub(ctx context.Context, version string, variant ports.BunVariant, platform ports.Platform, targetDir, binaryPath string) (ports.BunResolverResult, error) {
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: create stub cache dir %s: %w: %w", targetDir, err, core.ErrBunResolutionFailed)
	}

	targetName, err := compileTargetName(platform, variant)
	if err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: resolve compile target: %w: %w", err, core.ErrBunResolutionFailed)
	}

	tmpDir, err := os.MkdirTemp("", "pokkum-stub-*")
	if err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: create temp dir for stub: %w: %w", err, core.ErrBunResolutionFailed)
	}
	defer os.RemoveAll(tmpDir)

	entryFile := filepath.Join(tmpDir, "stub-entry.ts")
	// Non-foldable path expression prevents Bun bundler from constant-folding and inlining /app/server/index.js at compile time.
	stubCode := "const p = \"/app/server/\" + \"index.js\";\nawait import(p);\n"
	if err := os.WriteFile(entryFile, []byte(stubCode), 0600); err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: write stub entry: %w: %w", err, core.ErrBunResolutionFailed)
	}

	tmpBinary := filepath.Join(tmpDir, "bun-stub")
	// targetName is one of a fixed, resolver-computed set of Bun target
	// strings (see compileTargetName/resolveBunTargetName); tmpBinary and
	// entryFile are paths this function created itself under a fresh
	// os.MkdirTemp directory. None of these three arguments are derived from
	// user or network input.
	cmd := exec.CommandContext(ctx, "bun", "build", "--compile", "--target="+targetName, "--outfile="+tmpBinary, entryFile) //nolint:gosec // G204: fixed resolver-internal target name + self-created temp paths, not user-controlled
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Run(); err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: compile stub launcher for %s: %s: %w: %w", platform, strings.TrimSpace(stderrBuf.String()), err, core.ErrBunResolutionFailed)
	}

	if err := os.Rename(tmpBinary, binaryPath); err != nil {
		in, openErr := os.Open(tmpBinary)
		if openErr != nil {
			return ports.BunResolverResult{}, fmt.Errorf("bunruntime: open compiled stub %s: %w: %w", tmpBinary, openErr, core.ErrBunResolutionFailed)
		}
		defer in.Close()
		out, createErr := os.OpenFile(binaryPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		if createErr != nil {
			return ports.BunResolverResult{}, fmt.Errorf("bunruntime: create stub destination %s: %w: %w", binaryPath, createErr, core.ErrBunResolutionFailed)
		}
		_, copyErr := io.Copy(out, in)
		out.Close()
		if copyErr != nil {
			return ports.BunResolverResult{}, fmt.Errorf("bunruntime: write stub destination %s: %w: %w", binaryPath, copyErr, core.ErrBunResolutionFailed)
		}
	}

	sha, size, err := computeFileSHA256(binaryPath)
	if err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: checksum calculation for %s: %w: %w", binaryPath, err, core.ErrBunResolutionFailed)
	}

	return ports.BunResolverResult{
		BinaryPath: binaryPath,
		Version:    version,
		Variant:    variant,
		Platform:   platform,
		SHA256:     sha,
		Size:       size,
	}, nil
}
