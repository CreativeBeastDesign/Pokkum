package bunruntime

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

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

// buildBunZip builds a minimal Bun release zip containing a single 'bun'
// entry with the given content, matching the layout resolver.Resolve expects
// (filepath.Base(entry) == "bun").
func buildBunZip(t *testing.T, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	f, err := zw.Create("bun-linux-x64/bun")
	if err != nil {
		t.Fatalf("zip create error: %v", err)
	}
	if _, err := f.Write(content); err != nil {
		t.Fatalf("zip write error: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close error: %v", err)
	}
	return buf.Bytes()
}

// newTestReleaseKeypair generates a throwaway Ed25519 OpenPGP entity for
// tests to sign fixture SHASUMS256.txt content with, so verification tests
// exercise real signature checking — including rejecting a wrong signer and
// a tampered payload — without needing Bun's actual private key. Ed25519 is
// chosen purely for fast test key generation, not to mirror Bun's real key
// (an ECDSA/EdDSA key per the embedded production key's packet structure).
func newTestReleaseKeypair(t *testing.T) (*openpgp.Entity, string) {
	t.Helper()
	entity, err := openpgp.NewEntity("Test Release Key", "", "test@example.invalid", &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA})
	if err != nil {
		t.Fatalf("generate test keypair: %v", err)
	}
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, "PGP PUBLIC KEY BLOCK", nil)
	if err != nil {
		t.Fatalf("armor encode: %v", err)
	}
	if err := entity.Serialize(w); err != nil {
		t.Fatalf("serialize entity: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close armor writer: %v", err)
	}
	return entity, buf.String()
}

// signSHASUMS clear-signs content (an unsigned SHASUMS256.txt body) with
// entity's private key, returning the armored .asc bytes as
// fetchVerifiedChecksum expects to receive them from GitHub.
func signSHASUMS(t *testing.T, entity *openpgp.Entity, content string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := clearsign.Encode(&buf, entity.PrivateKey, nil)
	if err != nil {
		t.Fatalf("clearsign encode: %v", err)
	}
	if _, err := io.WriteString(w, content); err != nil {
		t.Fatalf("write clearsign content: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close clearsign writer: %v", err)
	}
	return buf.Bytes()
}

// pathRoutedClient builds an *http.Client that redirects any request to
// server, preserving the original request's path — unlike a client that
// always fetches server.URL regardless of what was asked for, this lets a
// single mock server correctly distinguish a request for the .zip archive
// from a request for SHASUMS256.txt.asc, which resolver.Resolve now issues
// as two genuinely different requests.
func pathRoutedClient(server *httptest.Server) *http.Client {
	return &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			newReq, err := http.NewRequestWithContext(req.Context(), req.Method, server.URL+req.URL.Path, req.Body)
			if err != nil {
				return nil, err
			}
			return server.Client().Do(newReq)
		}),
	}
}

func TestResolver_DownloadAndCache(t *testing.T) {
	bunBinaryContent := []byte("#!/bin/sh\necho 'fake bun'")
	zipBytes := buildBunZip(t, bunBinaryContent)

	zipSHA := sha256.Sum256(zipBytes)
	entity, pubKey := newTestReleaseKeypair(t)
	shasums := hex.EncodeToString(zipSHA[:]) + "  bun-linux-x64.zip\n"
	ascBytes := signSHASUMS(t, entity, shasums)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "SHASUMS256.txt.asc"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(ascBytes)
		case strings.HasSuffix(r.URL.Path, ".zip"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(zipBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	resolver := NewResolver(cacheDir, pathRoutedClient(server))
	resolver.ReleaseKeyArmored = pubKey

	req := ports.BunResolverRequest{
		Platform:        ports.LinuxAMD64,
		Version:         "9.9.9", // Unpinned version: exercises fetchVerifiedChecksum.
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

// TestResolver_ExpandedPinTable_EntriesAreWellFormed guards the real
// pinnedReleaseChecksums entries added 2026-08-18 (the most recent handful
// of Bun releases, beyond just ports.DefaultBunVersion): every key must be
// "<version>/<target-name>" with a target name Resolve's own
// resolveBunTargetName can actually produce, and every value a syntactically
// valid 64-hex-char SHA256 digest — a typo'd key silently never matches at
// runtime (falling through to the dynamic GPG path instead, functionally
// harmless but defeating the whole point of pinning it), and a malformed
// value would make every real resolve of that key fail checksum comparison.
// The actual cryptographic correctness of each value was verified for real
// by scripts/pin-bun-checksums against Bun's live, GPG-signed release
// manifest at the time it was added (see the doc comment on
// pinnedReleaseChecksums) — not re-verified here, since that would require
// downloading real multi-megabyte release archives in a unit test.
func TestResolver_ExpandedPinTable_EntriesAreWellFormed(t *testing.T) {
	validTargets := map[string]bool{
		"bun-linux-x64":          true,
		"bun-linux-x64-baseline": true,
		"bun-linux-aarch64":      true,
	}
	shaPattern := regexp.MustCompile(`^[0-9a-f]{64}$`)

	if len(pinnedReleaseChecksums) < 6 {
		t.Fatalf("test premise broken: expected at least 6 pinned versions (1.2.2 plus the 2026-08-18 expansion), got %d entries — did the expansion get reverted?", len(pinnedReleaseChecksums))
	}

	for key, sha := range pinnedReleaseChecksums {
		parts := strings.SplitN(key, "/", 2)
		if len(parts) != 2 {
			t.Errorf("pinnedReleaseChecksums key %q is not in <version>/<target-name> format", key)
			continue
		}
		version, target := parts[0], parts[1]
		if version == "" {
			t.Errorf("pinnedReleaseChecksums key %q has an empty version component", key)
		}
		if !validTargets[target] {
			t.Errorf("pinnedReleaseChecksums key %q names target %q, which resolveBunTargetName does not produce for any supported linux/{amd64,arm64} + variant combination", key, target)
		}
		if !shaPattern.MatchString(sha) {
			t.Errorf("pinnedReleaseChecksums[%q] = %q is not a 64-char lowercase hex SHA256 digest", key, sha)
		}
	}

	// Spot-check the specific 2026-08-18 additions are present, not just
	// that *some* 6+ entries exist (which the count check above allows to
	// pass even if unrelated entries were substituted).
	for _, version := range []string{"1.3.14", "1.3.13", "1.3.12", "1.3.11", "1.3.10"} {
		for target := range validTargets {
			key := version + "/" + target
			if _, ok := pinnedReleaseChecksums[key]; !ok {
				t.Errorf("expected pinnedReleaseChecksums to contain %q (2026-08-18 expansion)", key)
			}
		}
	}
}

// --- Checksum trust-on-first-use (TOFU) pinning -----------------------------
//
// Follow-up to PB-2: fetchVerifiedChecksum's own doc comment documents that a
// valid GPG signature over SHASUMS256.txt doesn't bind its content to the
// release it claims to be for — a party able to substitute the HTTP response
// could serve an older, genuinely-signed release under a newer version's
// name. checkAndPinChecksum persists the first verified checksum per
// (version, target) and hard-fails on later disagreement.

// TestChecksum_TOFU_PinsOnFirstUse is a direct unit test of the persistence
// mechanism (not routed through Resolve, which would need a real archive
// download+GPG cycle just to reach it): the first checksum seen for a key is
// written to the pin store file.
func TestChecksum_TOFU_PinsOnFirstUse(t *testing.T) {
	cacheDir := t.TempDir()
	r := NewResolver(cacheDir, nil)

	if err := r.checkAndPinChecksum("9.9.9/bun-linux-x64", "aaaa"); err != nil {
		t.Fatalf("unexpected error pinning a fresh key: %v", err)
	}

	data, err := os.ReadFile(r.tofuPinStorePath())
	if err != nil {
		t.Fatalf("expected the pin store to be written, got: %v", err)
	}
	var pins map[string]string
	if err := json.Unmarshal(data, &pins); err != nil {
		t.Fatalf("pin store is not valid JSON: %v", err)
	}
	if pins["9.9.9/bun-linux-x64"] != "aaaa" {
		t.Errorf("expected pinned checksum aaaa, got %+v", pins)
	}
}

// TestChecksum_TOFU_MatchingRepeatSucceeds proves a repeat verification of
// the identical checksum for an already-pinned key is a silent no-op.
func TestChecksum_TOFU_MatchingRepeatSucceeds(t *testing.T) {
	cacheDir := t.TempDir()
	r := NewResolver(cacheDir, nil)

	if err := r.checkAndPinChecksum("9.9.9/bun-linux-x64", "aaaa"); err != nil {
		t.Fatalf("first pin failed: %v", err)
	}
	if err := r.checkAndPinChecksum("9.9.9/bun-linux-x64", "aaaa"); err != nil {
		t.Errorf("expected a matching repeat to succeed silently, got: %v", err)
	}
}

// TestChecksum_TOFU_DisagreementHardFails is the core regression guard: a
// checksum for an already-pinned key that disagrees with the pinned value is
// exactly the downgrade/substitution scenario this mechanism exists to
// catch, and must hard-fail with the dedicated sentinel.
func TestChecksum_TOFU_DisagreementHardFails(t *testing.T) {
	cacheDir := t.TempDir()
	r := NewResolver(cacheDir, nil)

	if err := r.checkAndPinChecksum("9.9.9/bun-linux-x64", "aaaa"); err != nil {
		t.Fatalf("first pin failed: %v", err)
	}

	err := r.checkAndPinChecksum("9.9.9/bun-linux-x64", "bbbb")
	if !errors.Is(err, core.ErrBunChecksumPinViolation) {
		t.Fatalf("expected core.ErrBunChecksumPinViolation for a disagreeing checksum, got: %v", err)
	}
}

// TestChecksum_TOFU_WriteFailureIsBestEffort proves a pin store that can't be
// written to (e.g. the cache root is unwritable) does not fail the caller —
// persistence is hardening infrastructure, not itself the security boundary
// (the GPG signature check already ran before this is ever called).
func TestChecksum_TOFU_WriteFailureIsBestEffort(t *testing.T) {
	root := t.TempDir()
	blockerFile := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blockerFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// r.CacheDir is a path *through* the regular file above, so
	// os.MkdirAll(r.CacheDir, ...) inside checkAndPinChecksum's write path
	// deterministically fails ("not a directory") without relying on
	// platform-specific permission semantics.
	r := NewResolver(filepath.Join(blockerFile, "cache"), nil)

	if err := r.checkAndPinChecksum("9.9.9/bun-linux-x64", "aaaa"); err != nil {
		t.Errorf("expected a pin-store write failure to be swallowed (best-effort), got: %v", err)
	}
}

// TestResolver_ChecksumTOFU_EndToEnd_MatchingRepeatDownloadSucceeds drives
// the TOFU check through the real Resolve() call path (not just the
// standalone method): resolving the same unpinned (version, target) twice —
// forcing two real downloads by removing the cached binary between them,
// since a cache hit would never re-enter the download+verify path at all —
// succeeds both times because the mock server serves the same genuine
// checksum both times.
func TestResolver_ChecksumTOFU_EndToEnd_MatchingRepeatDownloadSucceeds(t *testing.T) {
	bunBinaryContent := []byte("#!/bin/sh\necho 'fake bun'")
	zipBytes := buildBunZip(t, bunBinaryContent)

	zipSHA := sha256.Sum256(zipBytes)
	entity, pubKey := newTestReleaseKeypair(t)
	shasums := hex.EncodeToString(zipSHA[:]) + "  bun-linux-x64.zip\n"
	ascBytes := signSHASUMS(t, entity, shasums)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "SHASUMS256.txt.asc"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(ascBytes)
		case strings.HasSuffix(r.URL.Path, ".zip"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(zipBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	resolver := NewResolver(cacheDir, pathRoutedClient(server))
	resolver.ReleaseKeyArmored = pubKey

	req := ports.BunResolverRequest{
		Platform:        ports.LinuxAMD64,
		Version:         "9.9.9",
		Variant:         ports.BunVariantStandard,
		SourceDateEpoch: time.Unix(1700000000, 0),
	}

	if _, err := resolver.Resolve(context.Background(), req); err != nil {
		t.Fatalf("first resolve failed: %v", err)
	}

	// Remove only the cached binary (and its digest sidecar), not the pin
	// store, so the next Resolve genuinely re-downloads and re-verifies
	// rather than short-circuiting on a cache hit.
	targetDir := filepath.Join(cacheDir, "9.9.9", "standard", "linux_amd64")
	if err := os.RemoveAll(targetDir); err != nil {
		t.Fatal(err)
	}

	if _, err := resolver.Resolve(context.Background(), req); err != nil {
		t.Fatalf("expected the second (re-downloaded) resolve to match the pinned checksum and succeed, got: %v", err)
	}
}

// TestResolver_ChecksumTOFU_EndToEnd_DowngradeHardFails proves Resolve()
// itself — not just the standalone method — rejects a real download whose
// verified checksum disagrees with a pin already established for that key,
// simulating an attacker substituting an older, genuinely-signed release
// under the same version's name on a later build.
func TestResolver_ChecksumTOFU_EndToEnd_DowngradeHardFails(t *testing.T) {
	bunBinaryContent := []byte("#!/bin/sh\necho 'fake bun'")
	zipBytes := buildBunZip(t, bunBinaryContent)

	zipSHA := sha256.Sum256(zipBytes)
	entity, pubKey := newTestReleaseKeypair(t)
	shasums := hex.EncodeToString(zipSHA[:]) + "  bun-linux-x64.zip\n"
	ascBytes := signSHASUMS(t, entity, shasums)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "SHASUMS256.txt.asc"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(ascBytes)
		case strings.HasSuffix(r.URL.Path, ".zip"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(zipBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	resolver := NewResolver(cacheDir, pathRoutedClient(server))
	resolver.ReleaseKeyArmored = pubKey

	// Pre-seed the pin store with a DIFFERENT (legitimately-established,
	// simulated) checksum for the exact key the mock server will serve —
	// modeling a build that trusted a real release earlier, before an
	// attacker later substituted an older one under the same version tag.
	pinPath := resolver.tofuPinStorePath()
	if err := os.MkdirAll(filepath.Dir(pinPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pinPath, []byte(`{"9.9.9/bun-linux-x64":"0000000000000000000000000000000000000000000000000000000000000000"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	req := ports.BunResolverRequest{
		Platform:        ports.LinuxAMD64,
		Version:         "9.9.9",
		Variant:         ports.BunVariantStandard,
		SourceDateEpoch: time.Unix(1700000000, 0),
	}

	_, err := resolver.Resolve(context.Background(), req)
	if !errors.Is(err, core.ErrBunChecksumPinViolation) {
		t.Fatalf("expected core.ErrBunChecksumPinViolation for a checksum disagreeing with the pinned baseline, got: %v", err)
	}
}

// TestResolver_Offline_FailsClosedOnCacheMiss is PR-2's regression guard
// (security review finding F6): a --hermetic build threads Offline: true
// into every BunResolverRequest, and on a cold cache Resolve must fail
// closed with core.ErrHermeticViolation rather than silently reaching the
// network — matching Preflight's pre-populated node_modules requirement for
// the same reason. Asserts zero HTTP requests were made, not just that the
// call returned an error, since a fail-closed check placed after the
// download attempt would also return an error while still having leaked a
// request.
func TestResolver_Offline_FailsClosedOnCacheMiss(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	resolver := NewResolver(cacheDir, pathRoutedClient(server))

	_, err := resolver.Resolve(context.Background(), ports.BunResolverRequest{
		Platform:        ports.LinuxAMD64,
		Version:         "9.9.9",
		Variant:         ports.BunVariantStandard,
		SourceDateEpoch: time.Unix(1700000000, 0),
		Offline:         true,
	})
	if !errors.Is(err, core.ErrHermeticViolation) {
		t.Fatalf("expected core.ErrHermeticViolation on a cold-cache offline resolve, got: %v", err)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("expected zero HTTP requests for an offline resolve, got %d", n)
	}
}

// TestResolver_Offline_SucceedsOnCacheHit proves Offline doesn't break the
// legitimate case: a bun runtime already verified and cached by a prior
// (non-hermetic) resolve must still resolve successfully offline, with no
// network access needed or attempted.
func TestResolver_Offline_SucceedsOnCacheHit(t *testing.T) {
	bunBinaryContent := []byte("#!/bin/sh\necho 'fake bun'")
	zipBytes := buildBunZip(t, bunBinaryContent)

	zipSHA := sha256.Sum256(zipBytes)
	entity, pubKey := newTestReleaseKeypair(t)
	shasums := hex.EncodeToString(zipSHA[:]) + "  bun-linux-x64.zip\n"
	ascBytes := signSHASUMS(t, entity, shasums)

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch {
		case strings.HasSuffix(r.URL.Path, "SHASUMS256.txt.asc"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(ascBytes)
		case strings.HasSuffix(r.URL.Path, ".zip"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(zipBytes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	resolver := NewResolver(cacheDir, pathRoutedClient(server))
	resolver.ReleaseKeyArmored = pubKey

	req := ports.BunResolverRequest{
		Platform:        ports.LinuxAMD64,
		Version:         "9.9.9",
		Variant:         ports.BunVariantStandard,
		SourceDateEpoch: time.Unix(1700000000, 0),
	}
	if _, err := resolver.Resolve(context.Background(), req); err != nil {
		t.Fatalf("failed to warm the cache: %v", err)
	}
	warmupRequests := requests.Load()
	if warmupRequests == 0 {
		t.Fatal("test premise broken: warm-up resolve made no HTTP requests")
	}

	req.Offline = true
	res, err := resolver.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("expected an offline resolve to succeed on a warm cache, got: %v", err)
	}
	if res.Version != "9.9.9" {
		t.Errorf("expected version 9.9.9, got %s", res.Version)
	}
	if got := requests.Load(); got != warmupRequests {
		t.Errorf("expected no additional HTTP requests for the offline cache-hit resolve, got %d more", got-warmupRequests)
	}
}

// TestResolver_CacheHit_TamperedBinaryIsRejected is a security-review
// regression guard (finding F1): the original cache-hit check only asked
// "does a file exist at this path", trusting it forever with zero
// comparison against anything — the one code path that received no
// integrity checking at all, even though the whole point of this file's
// fix is closing exactly that gap. A binary whose bytes no longer match the
// digest sidecar recorded when it was verified must be treated as a cache
// miss (triggering a fresh, re-verified download), not silently trusted.
func TestResolver_CacheHit_TamperedBinaryIsRejected(t *testing.T) {
	realContent := []byte("#!/bin/sh\necho 'the real, verified bun'")
	zipBytes := buildBunZip(t, realContent)
	realSHA := sha256.Sum256(zipBytes)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".zip") {
			_, _ = w.Write(zipBytes)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	resolver := NewResolver(cacheDir, pathRoutedClient(server))
	// Pin directly so this test exercises only the cache-hit path, not
	// fetchVerifiedChecksum.
	req := ports.BunResolverRequest{
		Platform:        ports.LinuxAMD64,
		Version:         "1.2.2",
		Variant:         ports.BunVariantStandard,
		SourceDateEpoch: time.Unix(1700000000, 0),
	}
	pinnedKey := "1.2.2/bun-linux-x64"
	pinnedReleaseChecksums[pinnedKey] = hex.EncodeToString(realSHA[:])
	t.Cleanup(func() { delete(pinnedReleaseChecksums, pinnedKey) })

	res, err := resolver.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("initial resolve: %v", err)
	}

	// Simulate tampering: overwrite the cached binary in place, as another
	// local process (or another user on a shared/misconfigured cache
	// directory) could.
	tamperedContent := []byte("#!/bin/sh\necho 'attacker-substituted bun'")
	if err := os.WriteFile(res.BinaryPath, tamperedContent, 0o700); err != nil {
		t.Fatalf("simulate tampering: %v", err)
	}

	// A second resolve must NOT trust the tampered file — it must detect the
	// sidecar/content mismatch, fall through to a fresh download, and
	// restore the genuinely-verified content.
	res2, err := resolver.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("resolve after tampering: %v", err)
	}
	restoredContent, err := os.ReadFile(res2.BinaryPath)
	if err != nil {
		t.Fatalf("read restored binary: %v", err)
	}
	if bytes.Equal(restoredContent, tamperedContent) {
		t.Fatal("BUG: tampered cache content was trusted and returned as-is instead of being re-verified")
	}
	if !bytes.Equal(restoredContent, realContent) {
		t.Errorf("restored binary content mismatch: got %q, want %q", restoredContent, realContent)
	}
}

// TestResolver_CacheHit_NoSidecarIsTreatedAsMiss covers the other half of
// F1: a file that exists at the expected cache path but was never verified
// by this resolver (no digest sidecar at all — e.g. a stale file from
// before this fix shipped, or planted by something else entirely) must not
// be trusted either.
func TestResolver_CacheHit_NoSidecarIsTreatedAsMiss(t *testing.T) {
	realContent := []byte("#!/bin/sh\necho 'the real, verified bun'")
	zipBytes := buildBunZip(t, realContent)
	realSHA := sha256.Sum256(zipBytes)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".zip") {
			_, _ = w.Write(zipBytes)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	cacheDir := t.TempDir()
	targetDir := filepath.Join(cacheDir, "1.2.2", "standard", "linux_amd64")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	unverifiedPath := filepath.Join(targetDir, "bun")
	unverifiedContent := []byte("nobody ever verified this")
	if err := os.WriteFile(unverifiedPath, unverifiedContent, 0o700); err != nil {
		t.Fatalf("plant unverified file: %v", err)
	}
	// Deliberately no .sha256 sidecar written.

	resolver := NewResolver(cacheDir, pathRoutedClient(server))
	pinnedKey := "1.2.2/bun-linux-x64"
	pinnedReleaseChecksums[pinnedKey] = hex.EncodeToString(realSHA[:])
	t.Cleanup(func() { delete(pinnedReleaseChecksums, pinnedKey) })

	res, err := resolver.Resolve(context.Background(), ports.BunResolverRequest{
		Platform:        ports.LinuxAMD64,
		Version:         "1.2.2",
		Variant:         ports.BunVariantStandard,
		SourceDateEpoch: time.Unix(1700000000, 0),
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	got, err := os.ReadFile(res.BinaryPath)
	if err != nil {
		t.Fatalf("read resolved binary: %v", err)
	}
	if bytes.Equal(got, unverifiedContent) {
		t.Fatal("BUG: an unverified file with no digest sidecar was trusted as a cache hit")
	}
	if !bytes.Equal(got, realContent) {
		t.Errorf("resolved binary content = %q, want %q", got, realContent)
	}
}

// TestResolver_RejectsInvalidVersionAndVariant is a security-review
// regression guard (finding F7): version and variant strings from
// --bun-version/--bun-variant are interpolated directly into a filesystem
// cache path and a release download URL, unvalidated. A version containing
// path-traversal sequences could make the cache path escape CacheDir
// entirely; an unrecognized variant used to be silently treated as
// "standard" rather than rejected.
func TestResolver_RejectsInvalidVersionAndVariant(t *testing.T) {
	t.Run("version with path traversal is rejected", func(t *testing.T) {
		resolver := NewResolver(t.TempDir(), nil)
		_, err := resolver.Resolve(context.Background(), ports.BunResolverRequest{
			Platform:        ports.LinuxAMD64,
			Version:         "../../../../etc",
			SourceDateEpoch: time.Unix(1700000000, 0),
		})
		if err == nil {
			t.Fatal("expected error for a path-traversal version string, got nil")
		}
		if !errors.Is(err, core.ErrBunResolutionFailed) {
			t.Errorf("expected ErrBunResolutionFailed, got: %v", err)
		}
	})

	t.Run("version with slash is rejected", func(t *testing.T) {
		resolver := NewResolver(t.TempDir(), nil)
		_, err := resolver.Resolve(context.Background(), ports.BunResolverRequest{
			Platform:        ports.LinuxAMD64,
			Version:         "1.2.2/../../evil",
			SourceDateEpoch: time.Unix(1700000000, 0),
		})
		if err == nil {
			t.Fatal("expected error for a version string containing '/', got nil")
		}
	})

	t.Run("unknown variant is rejected, not silently treated as standard", func(t *testing.T) {
		resolver := NewResolver(t.TempDir(), nil)
		_, err := resolver.Resolve(context.Background(), ports.BunResolverRequest{
			Platform:        ports.LinuxAMD64,
			Variant:         ports.BunVariant("turbo"),
			SourceDateEpoch: time.Unix(1700000000, 0),
		})
		if err == nil {
			t.Fatal("expected error for an unrecognized variant, got nil")
		}
		if !errors.Is(err, core.ErrBunResolutionFailed) {
			t.Errorf("expected ErrBunResolutionFailed, got: %v", err)
		}
	})
}

// TestResolver_UnpinnedVersion_ChecksumVerification is PB-2's regression
// guard: an unpinned --bun-version used to download and install a ~90MB
// binary with no integrity check at all (TestResolver_DownloadAndCache
// above previously demonstrated exactly that gap by asserting success with
// no signed manifest served at all). Every subtest here drives Resolve
// through fetchVerifiedChecksum and asserts it fails closed — no case here
// should ever let a download through unverified.
func TestResolver_UnpinnedVersion_ChecksumVerification(t *testing.T) {
	newServerAndResolver := func(t *testing.T, handler http.HandlerFunc) *Resolver {
		t.Helper()
		server := httptest.NewServer(handler)
		t.Cleanup(server.Close)
		resolver := NewResolver(t.TempDir(), pathRoutedClient(server))
		return resolver
	}

	baseReq := func() ports.BunResolverRequest {
		return ports.BunResolverRequest{
			Platform:        ports.LinuxAMD64,
			Version:         "9.9.9",
			Variant:         ports.BunVariantStandard,
			SourceDateEpoch: time.Unix(1700000000, 0),
		}
	}

	t.Run("finds the correct entry in a multi-line manifest where the target is not first", func(t *testing.T) {
		// Bun's real SHASUMS256.txt lists ~20 release variants (darwin, linux,
		// windows, each with baseline/musl/profile combinations). This proves
		// parseSHASUMSEntry does an exact filename match anywhere in the
		// signed content, not just "first line wins" — a decoy entry for a
		// different file, placed before the real one, must not be picked.
		bunBinaryContent := []byte("#!/bin/sh\necho 'real bun'")
		zipBytes := buildBunZip(t, bunBinaryContent)
		zipSHA := sha256.Sum256(zipBytes)

		entity, pubKey := newTestReleaseKeypair(t)
		decoySHA := sha256.Sum256([]byte("decoy content for a different platform"))
		shasums := hex.EncodeToString(decoySHA[:]) + "  bun-darwin-aarch64.zip\n" +
			hex.EncodeToString(decoySHA[:]) + "  bun-linux-x64-baseline.zip\n" +
			hex.EncodeToString(zipSHA[:]) + "  bun-linux-x64.zip\n" +
			hex.EncodeToString(decoySHA[:]) + "  bun-windows-x64.zip\n"
		ascBytes := signSHASUMS(t, entity, shasums)

		resolver := newServerAndResolver(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "SHASUMS256.txt.asc"):
				_, _ = w.Write(ascBytes)
			case strings.HasSuffix(r.URL.Path, ".zip"):
				_, _ = w.Write(zipBytes)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		resolver.ReleaseKeyArmored = pubKey

		res, err := resolver.Resolve(context.Background(), baseReq())
		if err != nil {
			t.Fatalf("unexpected error resolving against a multi-line manifest: %v", err)
		}
		readContent, err := os.ReadFile(res.BinaryPath)
		if err != nil {
			t.Fatalf("failed to read resolved binary: %v", err)
		}
		if !bytes.Equal(readContent, bunBinaryContent) {
			t.Errorf("resolved binary content mismatch: got %q, want %q", readContent, bunBinaryContent)
		}
	})

	t.Run("tampered checksum entry in an otherwise validly-signed manifest is rejected", func(t *testing.T) {
		bunBinaryContent := []byte("#!/bin/sh\necho 'real bun'")
		zipBytes := buildBunZip(t, bunBinaryContent)

		entity, pubKey := newTestReleaseKeypair(t)
		// A validly-signed manifest whose checksum entry does NOT match the
		// zip actually served — e.g. an attacker who can influence the
		// manifest's content pre-signing, or a stale/wrong entry.
		wrongSHA := sha256.Sum256([]byte("not the real zip content"))
		shasums := hex.EncodeToString(wrongSHA[:]) + "  bun-linux-x64.zip\n"
		ascBytes := signSHASUMS(t, entity, shasums)

		resolver := newServerAndResolver(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "SHASUMS256.txt.asc"):
				_, _ = w.Write(ascBytes)
			case strings.HasSuffix(r.URL.Path, ".zip"):
				_, _ = w.Write(zipBytes)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		resolver.ReleaseKeyArmored = pubKey

		_, err := resolver.Resolve(context.Background(), baseReq())
		if err == nil {
			t.Fatal("expected error for checksum mismatch, got nil")
		}
		if !errors.Is(err, core.ErrBunChecksumMismatch) {
			t.Errorf("expected ErrBunChecksumMismatch, got: %v", err)
		}
	})

	t.Run("manifest signed by an untrusted key is rejected", func(t *testing.T) {
		bunBinaryContent := []byte("#!/bin/sh\necho 'real bun'")
		zipBytes := buildBunZip(t, bunBinaryContent)
		zipSHA := sha256.Sum256(zipBytes)
		shasums := hex.EncodeToString(zipSHA[:]) + "  bun-linux-x64.zip\n"

		// Signed by an entity the resolver does NOT trust (attacker's own
		// key), even though the checksum inside is genuinely correct for
		// the served zip — a correct checksum from an untrusted signer must
		// not be accepted.
		attackerEntity, _ := newTestReleaseKeypair(t)
		ascBytes := signSHASUMS(t, attackerEntity, shasums)

		_, trustedPubKey := newTestReleaseKeypair(t)

		resolver := newServerAndResolver(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "SHASUMS256.txt.asc"):
				_, _ = w.Write(ascBytes)
			case strings.HasSuffix(r.URL.Path, ".zip"):
				_, _ = w.Write(zipBytes)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		resolver.ReleaseKeyArmored = trustedPubKey

		_, err := resolver.Resolve(context.Background(), baseReq())
		if err == nil {
			t.Fatal("expected error for untrusted signer, got nil")
		}
		if !errors.Is(err, core.ErrBunSignatureVerificationFailed) {
			t.Errorf("expected ErrBunSignatureVerificationFailed, got: %v", err)
		}
	})

	t.Run("missing checksum entry for the requested target is rejected", func(t *testing.T) {
		bunBinaryContent := []byte("#!/bin/sh\necho 'real bun'")
		zipBytes := buildBunZip(t, bunBinaryContent)

		entity, pubKey := newTestReleaseKeypair(t)
		// A validly-signed manifest that simply never mentions the file
		// resolver.Resolve is asking about (e.g. a different platform only).
		shasums := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  bun-darwin-aarch64.zip\n"
		ascBytes := signSHASUMS(t, entity, shasums)

		resolver := newServerAndResolver(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "SHASUMS256.txt.asc"):
				_, _ = w.Write(ascBytes)
			case strings.HasSuffix(r.URL.Path, ".zip"):
				_, _ = w.Write(zipBytes)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		resolver.ReleaseKeyArmored = pubKey

		_, err := resolver.Resolve(context.Background(), baseReq())
		if err == nil {
			t.Fatal("expected error for missing checksum entry, got nil")
		}
		if !errors.Is(err, core.ErrBunSignatureVerificationFailed) {
			t.Errorf("expected ErrBunSignatureVerificationFailed, got: %v", err)
		}
	})

	t.Run("SHASUMS256.txt.asc fetch failure (404) is rejected, not silently skipped", func(t *testing.T) {
		bunBinaryContent := []byte("#!/bin/sh\necho 'real bun'")
		zipBytes := buildBunZip(t, bunBinaryContent)

		resolver := newServerAndResolver(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, ".zip"):
				_, _ = w.Write(zipBytes)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		_, pubKey := newTestReleaseKeypair(t)
		resolver.ReleaseKeyArmored = pubKey

		_, err := resolver.Resolve(context.Background(), baseReq())
		if err == nil {
			t.Fatal("expected error when SHASUMS256.txt.asc cannot be fetched, got nil")
		}
		if !errors.Is(err, core.ErrBunSignatureVerificationFailed) {
			t.Errorf("expected ErrBunSignatureVerificationFailed, got: %v", err)
		}
	})

	t.Run("malformed (non-clearsigned) manifest body is rejected", func(t *testing.T) {
		bunBinaryContent := []byte("#!/bin/sh\necho 'real bun'")
		zipBytes := buildBunZip(t, bunBinaryContent)

		resolver := newServerAndResolver(t, func(w http.ResponseWriter, r *http.Request) {
			switch {
			case strings.HasSuffix(r.URL.Path, "SHASUMS256.txt.asc"):
				_, _ = w.Write([]byte("this is not a PGP clearsigned message"))
			case strings.HasSuffix(r.URL.Path, ".zip"):
				_, _ = w.Write(zipBytes)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		})
		_, pubKey := newTestReleaseKeypair(t)
		resolver.ReleaseKeyArmored = pubKey

		_, err := resolver.Resolve(context.Background(), baseReq())
		if err == nil {
			t.Fatal("expected error for malformed clearsign body, got nil")
		}
		if !errors.Is(err, core.ErrBunSignatureVerificationFailed) {
			t.Errorf("expected ErrBunSignatureVerificationFailed, got: %v", err)
		}
	})
}

// TestEmbeddedBunReleaseKey_ParsesAndHasSigningCapability is the ONLY test
// in this file that exercises embeddedBunReleaseKeyArmored itself — every
// other test injects ReleaseKeyArmored with a throwaway test keypair. It is
// deliberately offline and deterministic (unlike
// TestResolver_ReleaseKey_VerifiesRealBunSignature below, which skips on any
// error, including the embedded key failing to parse at all) so that
// corrupting so much as one character of the pinned armor block — the one
// constant this entire feature's trust rests on — cannot silently pass the
// suite.
func TestEmbeddedBunReleaseKey_ParsesAndHasSigningCapability(t *testing.T) {
	r := &Resolver{}
	keyring, err := r.releaseKeyring()
	if err != nil {
		t.Fatalf("releaseKeyring() with production default (ReleaseKeyArmored unset): %v", err)
	}
	if len(keyring) != 1 {
		t.Fatalf("keyring has %d entities, want exactly 1", len(keyring))
	}

	entity := keyring[0]
	gotFingerprint := hex.EncodeToString(entity.PrimaryKey.Fingerprint)
	const wantFingerprint = "f3dcc08a8572c0749b3e18888eab4d40a7b22b59"
	if gotFingerprint != wantFingerprint {
		t.Errorf("fingerprint = %s, want %s (matches the comment on embeddedBunReleaseKeyArmored)", gotFingerprint, wantFingerprint)
	}

	if _, ok := entity.Identities["Robobun <robobun@oven.sh>"]; !ok {
		t.Errorf("identities = %v, want to include \"Robobun <robobun@oven.sh>\"", entity.Identities)
	}

	// A key that parses but isn't usable for signing (revoked, expired, or
	// simply not flagged for signing) would make every unpinned-version
	// resolve fail — this is the check F5 in the security review flagged as
	// missing: the fingerprint alone doesn't prove the key still works.
	signingKeys := keyring.KeysByIdUsage(entity.PrimaryKey.KeyId, packet.KeyFlagSign)
	if len(signingKeys) == 0 {
		t.Error("embedded release key has no signing-capable key (revoked, expired, or not sign-flagged) — every unpinned Bun version resolve would fail")
	}
}

// TestResolver_ReleaseKey_VerifiesRealBunSignature documents (and, when run
// with network access, actually re-verifies) that embeddedBunReleaseKeyArmored
// genuinely validates a real signature from Bun's own release process,
// rather than being a plausible-looking but unverified constant. This is
// the same check performed manually to source the key (fetched from
// keys.openpgp.org, confirmed against a real SHASUMS256.txt.asc from
// bun-v1.2.2) before it was pinned into resolver.go.
func TestResolver_ReleaseKey_VerifiesRealBunSignature(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping network-dependent real-signature check in short mode")
	}

	resolver := NewResolver(t.TempDir(), http.DefaultClient)
	sha, err := resolver.fetchVerifiedChecksum(context.Background(), "1.2.2", "bun-linux-x64")
	if err != nil {
		t.Skipf("network unavailable or Bun release changed shape, skipping: %v", err)
	}

	const knownGoodSHA = "3f4efb8afd1f84ac2a98c04661c898561d1d35527d030cb4571e99b7c85f5079"
	if sha != knownGoodSHA {
		t.Errorf("verified checksum = %s, want %s (matches pinnedReleaseChecksums, which was sourced from the same signed manifest)", sha, knownGoodSHA)
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
	// verifiedCacheHit requires a digest sidecar recording what this
	// resolver itself verified/produced last time — a bare file at the
	// expected path is deliberately NOT enough (see resolver.go's
	// verifiedCacheHit doc comment), so this fixture must include one to
	// model "a previous real resolve already ran and cached this."
	contentSHA := sha256.Sum256(content)
	if err := os.WriteFile(cachedStubPath+".sha256", []byte(hex.EncodeToString(contentSHA[:])), 0600); err != nil {
		t.Fatalf("failed to write cache digest sidecar: %v", err)
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

		// Populate stock bun cache. A digest sidecar is required alongside
		// each planted file — see verifiedCacheHit's doc comment — to model
		// "a previous verified resolve already cached this" and keep this
		// test fast and network-free; without it, the stock resolve below
		// would fall through to a real download and the stub resolve to a
		// real `bun build --compile`.
		stockDir := filepath.Join(cacheDir, "1.2.2", "standard", "linux_amd64")
		_ = os.MkdirAll(stockDir, 0755)
		stockBinary := filepath.Join(stockDir, "bun")
		stockContent := []byte("stock-bun-bytes")
		_ = os.WriteFile(stockBinary, stockContent, 0755)
		stockSHA := sha256.Sum256(stockContent)
		_ = os.WriteFile(stockBinary+".sha256", []byte(hex.EncodeToString(stockSHA[:])), 0600)

		// Populate stub bun cache
		stubDir := filepath.Join(cacheDir, "stubs", "1.2.2", "standard", "linux_amd64")
		_ = os.MkdirAll(stubDir, 0755)
		stubBinary := filepath.Join(stubDir, "bun")
		stubContent := []byte("stub-launcher-bytes")
		_ = os.WriteFile(stubBinary, stubContent, 0755)
		stubSHA := sha256.Sum256(stubContent)
		_ = os.WriteFile(stubBinary+".sha256", []byte(hex.EncodeToString(stubSHA[:])), 0600)

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
