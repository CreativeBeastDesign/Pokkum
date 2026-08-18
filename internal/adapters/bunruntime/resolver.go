// Package bunruntime implements ports.BunRuntimeResolver by resolving,
// downloading, verifying, and caching official Bun release binaries.
package bunruntime

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Known pinned SHA256 digests for official Bun zip release archives.
// Key format: "<version>/<target-name>" (e.g. "1.2.2/bun-linux-x64").
//
// These entries are a fast, network-free path for the one version Pokkum
// defaults to (ports.DefaultBunVersion) — not the only source of integrity
// verification. Any version not listed here still gets verified, via
// fetchVerifiedChecksum below, against Bun's own GPG-signed release
// manifest. This map existing or not existing for a given version has no
// bearing on whether that version's download is checked; it only decides
// whether checking it costs a network round trip.
var pinnedReleaseChecksums = map[string]string{
	"1.2.2/bun-linux-x64":          "3f4efb8afd1f84ac2a98c04661c898561d1d35527d030cb4571e99b7c85f5079",
	"1.2.2/bun-linux-x64-baseline": "cad7756a6ee16f3432a328f8023fc5cd431106822eacfa6d6d3afbad6fdc24db",
	"1.2.2/bun-linux-aarch64":      "d1dbaa3e9af24549fad92bdbe4fb21fa53302cd048a8f004e85a240984c93d4d",
}

// embeddedBunReleaseKeyArmored is Bun's official release-signing OpenPGP
// public key, identity "Robobun <robobun@oven.sh>", fingerprint
// F3DC C08A 8572 C074 9B3E 1888 8EAB 4D40 A7B2 2B59. Fetched from
// keys.openpgp.org (a verifying keyserver requiring email-address proof for
// UID publication) on 2026-08-17, and confirmed to genuinely verify a real
// SHASUMS256.txt.asc from github.com/oven-sh/bun's bun-v1.2.2 release
// before being pinned here — see resolver_test.go's
// TestResolver_ReleaseKey_VerifiesRealBunSignature, which is skipped rather
// than run offline but documents the exact verification that was performed
// to source this key. Pinning it here (rather than fetching it from a
// keyserver at resolve time) means an unpinned --bun-version's integrity
// check does not itself depend on a keyserver being reachable or honest.
const embeddedBunReleaseKeyArmored = `-----BEGIN PGP PUBLIC KEY BLOCK-----
Comment: F3DC C08A 8572 C074 9B3E  1888 8EAB 4D40 A7B2 2B59
Comment: Robobun <robobun@oven.sh>

xjMEY9GQFhYJKwYBBAHaRw8BAQdAkppAqaXl0RkROz6NfdvlwYd2UuUVfHLk2NNY
IzEdnT/NGVJvYm9idW4gPHJvYm9idW5Ab3Zlbi5zaD7CkwQTFgoAOxYhBPPcwIqF
csB0mz4YiI6rTUCnsitZBQJj0ZAWAhsDBQsJCAcCAiICBhUKCQgLAgQWAgMBAh4H
AheAAAoJEI6rTUCnsitZ96UBAMvwCLD6Ud1RZkpvvnGUU+idHt2hcNmYU2d0XxDI
HQ0rAQCA9VFOtjZefQkfhDzgIgnEEdSFXaMiyY+D+LP8awNMCs44BGPRkBYSCisG
AQQBl1UBBQEBB0BpfePYSOEx8PihYkNXjlK5YT89CGHjEK5etleaB9i6OwMBCAfC
eAQYFgoAIBYhBPPcwIqFcsB0mz4YiI6rTUCnsitZBQJj0ZAWAhsMAAoJEI6rTUCn
sitZvhAA/j4SQxOCLheRG86A2181WAP4qLS1qxSw+fCf28DgiPfbAQCL0kcel+M9
qbRIlMnwn6TwlQgN9w1qqlSnA9CbKXT9Aw==
=zRMz
-----END PGP PUBLIC KEY BLOCK-----
`

var _ ports.BunRuntimeResolver = (*Resolver)(nil)

// Resolver implements ports.BunRuntimeResolver with disk caching (~/.cache/pokkum/bun).
type Resolver struct {
	// CacheDir is the root directory for cached Bun binaries.
	CacheDir string

	// HTTPClient is the HTTP client used for downloading release archives.
	HTTPClient *http.Client

	// ReleaseKeyArmored is the armored OpenPGP public key trusted to verify
	// Bun's signed SHASUMS256.txt release manifests, for any version not
	// covered by pinnedReleaseChecksums. Empty (the production default) uses
	// embeddedBunReleaseKeyArmored; tests override this with a throwaway
	// keypair so they can exercise real signature verification — including
	// the tamper-rejection path — without access to Bun's actual private key.
	ReleaseKeyArmored string

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
	if err := validateBunVersion(version); err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: %w: %w", err, core.ErrBunResolutionFailed)
	}
	variant := req.Variant
	if variant == "" {
		variant = ports.BunVariantStandard
	}

	// 3. Resolve target archive name (also validates variant)
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

	// Check if already cached & verified. A file existing at binaryPath is
	// NOT sufficient on its own — see verifiedCacheHit's doc comment: only a
	// binary whose contents still match the digest sidecar this resolver
	// itself wrote after cryptographically verifying it is treated as a hit.
	// Anything else (no sidecar, mismatched sidecar) is a cache miss, forcing
	// a fresh download-and-verify rather than trusting arbitrary bytes some
	// other process placed at that path.
	if sha, size, ok := verifiedCacheHit(binaryPath); ok {
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
	r.mu.Unlock()

	// If stub launcher is requested, compile rather than download release archive
	if req.StubLauncher {
		return r.compileStub(ctx, version, variant, req.Platform, targetDir, binaryPath)
	}

	if req.Offline {
		return ports.BunResolverResult{}, fmt.Errorf(
			"bunruntime: resolve bun %s (%s, %s): hermetic build requires a pre-cached bun runtime binary, but none was verified at %s — pre-populate the cache (a prior non-hermetic build, or `pokkum base update`-style warm-up) before running with --hermetic: %w",
			version, variant, req.Platform, binaryPath, core.ErrHermeticViolation,
		)
	}

	// 5. Download and extract. 0o700: this cache directory holds a binary
	// that becomes trusted purely because it lives at this path (see
	// verifiedCacheHit) — 0755 would let any other local user on a
	// shared/misconfigured cache root (e.g. the os.TempDir() fallback in
	// NewResolver, when $HOME/$XDG_CACHE_HOME aren't set) read or, in a
	// world-writable parent, plant files here.
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
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

	// Capped well above any real Bun release archive (~100MB uncompressed;
	// the zip itself is smaller) so a compromised/misbehaving endpoint can't
	// force unbounded memory use on the build host — this read happens
	// before any integrity check, so nothing about these bytes is trusted
	// yet. LimitReader+1 so a body that exactly fills the cap doesn't read
	// as an ambiguous "maybe truncated, maybe exactly the limit".
	const maxArchiveBytes = 512 << 20
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, maxArchiveBytes+1))
	if err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: read download body %s: %w: %w", archiveURL, err, core.ErrBunDownloadFailed)
	}
	if len(bodyBytes) > maxArchiveBytes {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: download body %s exceeds %d byte limit: %w", archiveURL, maxArchiveBytes, core.ErrBunDownloadFailed)
	}

	// Establish a trusted checksum for this exact (version, target) and
	// verify the download against it — unconditionally, not only when the
	// version happens to be statically pinned. A version outside
	// pinnedReleaseChecksums still gets checked, via fetchVerifiedChecksum,
	// against Bun's own GPG-signed SHASUMS256.txt.asc for that release. This
	// is a fail-closed check: if a trusted checksum can't be established at
	// all (network failure, missing/invalid signature, no matching entry),
	// the download is rejected rather than silently accepted.
	key := fmt.Sprintf("%s/%s", version, targetName)
	expectedSHA, ok := pinnedReleaseChecksums[key]
	if !ok {
		expectedSHA, err = r.fetchVerifiedChecksum(ctx, version, targetName)
		if err != nil {
			return ports.BunResolverResult{}, fmt.Errorf("bunruntime: could not establish a trusted checksum for %s: %w", key, err)
		}
		// pinnedReleaseChecksums entries are compiled into the Pokkum binary
		// itself and already maximally trusted — TOFU only adds value for
		// the dynamically-fetched path, where fetchVerifiedChecksum's own
		// doc comment explains the real gap this closes: a valid GPG
		// signature alone doesn't bind SHASUMS256.txt's content to the
		// release it claims to be for, so a party able to substitute the
		// HTTP response could serve an older, genuinely-signed release under
		// this version's name. Pinning what gets verified the first time and
		// hard-failing on later disagreement doesn't protect the very first
		// resolve, but does catch a downgrade attempted against a project
		// whose cache already has a trusted baseline.
		if pinErr := r.checkAndPinChecksum(key, expectedSHA); pinErr != nil {
			return ports.BunResolverResult{}, pinErr
		}
	}
	actualSHABytes := sha256.Sum256(bodyBytes)
	actualSHA := hex.EncodeToString(actualSHABytes[:])
	if actualSHA != expectedSHA {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: release archive checksum mismatch for %s (expected %s, got %s): %w", key, expectedSHA, actualSHA, core.ErrBunChecksumMismatch)
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

	// A unique temp file per call (os.CreateTemp), not a deterministic
	// binaryPath+".tmp", so two concurrent pokkum processes resolving the
	// same (version, variant, platform) — e.g. two separate `pokkum build`
	// invocations on a shared CI cache — can't interleave writes into the
	// same file and produce a torn binary that would then be trusted
	// forever via the cache-hit path.
	tmpFile, err := os.CreateTemp(targetDir, "bun-*.tmp")
	if err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: create temp file in %s: %w: %w", targetDir, err, core.ErrBunDownloadFailed)
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Chmod(0o700); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: chmod temp file %s: %w: %w", tmpPath, err, core.ErrBunDownloadFailed)
	}

	written, err := io.Copy(tmpFile, io.LimitReader(rc, 200<<20))
	tmpFile.Close()
	if err != nil {
		os.Remove(tmpPath)
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: write binary %s: %w: %w", tmpPath, err, core.ErrBunDownloadFailed)
	}
	// io.Copy's LimitReader silently returns nil once the cap is hit rather
	// than an error, which would otherwise let a >200MB "bun" entry be
	// truncated and cached as if it were complete. UncompressedSize64 is
	// from the zip's own central directory, read before any bytes of this
	// entry were copied, so this check catches truncation the LimitReader
	// cap would otherwise hide. Compared as uint64 (converting the
	// known-non-negative, LimitReader-bounded `written` up, not the
	// attacker-influenced central-directory field down) to avoid a lossy
	// int64 conversion of an untrusted uint64.
	if want := binaryEntry.UncompressedSize64; written < 0 || uint64(written) != want {
		os.Remove(tmpPath)
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: extracted %d bytes for %s, want %d (truncated or oversized): %w", written, key, want, core.ErrBunDownloadFailed)
	}

	if err := os.Rename(tmpPath, binaryPath); err != nil {
		os.Remove(tmpPath)
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: move binary to destination %s: %w: %w", binaryPath, err, core.ErrBunDownloadFailed)
	}

	sha, size, err := computeFileSHA256(binaryPath)
	if err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: checksum calculation for %s: %w: %w", binaryPath, err, core.ErrBunResolutionFailed)
	}
	// Record what we just cryptographically verified (the zip) and
	// extracted, so a later Resolve call's cache-hit check (verifiedCacheHit)
	// can detect if anything replaces these bytes in between.
	if err := writeCacheDigestSidecar(binaryPath, sha); err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: write cache digest sidecar for %s: %w: %w", binaryPath, err, core.ErrBunResolutionFailed)
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

// fetchVerifiedChecksum fetches Bun's GPG-signed SHASUMS256.txt.asc for
// version from the official GitHub release, verifies its signature against
// the release-signing key (embeddedBunReleaseKeyArmored, or
// r.ReleaseKeyArmored if set), and returns the trusted SHA256 hex digest for
// targetName+".zip" from the signed content.
//
// Fails closed: any error along this path (fetch failure, malformed
// clearsigned message, signature verification failure, no matching entry in
// the verified content) returns an error rather than a usable checksum —
// there is no fallback to an unverified value.
//
// Known limitation, partially mitigated (flagged during an adversarial
// security review so it isn't mistaken for an oversight): SHASUMS256.txt's
// entries are keyed only by filename ("bun-linux-x64.zip"), identical across
// every Bun release, and the signed content contains nothing binding it to
// the release it was published under. A party in a position to substitute
// what this fetch receives (compromised CDN/distribution channel, malicious
// registry proxy, a trusted-root MITM) could serve the genuine,
// genuinely-Bun-signed SHASUMS256.txt.asc and matching zip from an OLDER
// release while this function believes it's verifying `version`. Every check
// here would pass — signature valid, checksum matches — while silently
// downgrading to a known-vulnerable but authentically-signed build, and the
// SBOM/SLSA provenance would then incorrectly claim the newer version.
//
// checkAndPinChecksum (called by this function's caller in Resolve, not
// here) adds trust-on-first-use pinning: the first checksum this resolver
// verifies for a given (version, target) is persisted, and a later
// disagreement hard-fails the build. This closes the realistic, repeated
// threat model — a developer machine or CI runner with a persistent cache
// (e.g. `actions/cache`) resolving the same pinned dependency across many
// builds over time — but it does NOT, and structurally cannot, protect the
// very first resolve of a given (version, target) on a fresh cache: an
// attacker positioned to tamper with that first fetch establishes the
// "trusted" baseline the pin mechanism then faithfully protects. On an
// ephemeral CI runner with a fresh cache every run, TOFU provides no
// protection at all. Full protection against a first-contact MITM would
// still require either cross-checking against release-tag-scoped metadata
// from an independent trust anchor, or a genuinely out-of-band pin (shipped
// in the Pokkum binary itself, like pinnedReleaseChecksums above, but for
// every version rather than a curated few) — both meaningfully larger
// changes, tracked as a further roadmap follow-up.
func (r *Resolver) fetchVerifiedChecksum(ctx context.Context, version, targetName string) (string, error) {
	ascURL := fmt.Sprintf("https://github.com/oven-sh/bun/releases/download/bun-v%s/SHASUMS256.txt.asc", version)

	ascBody, err := r.fetchSmall(ctx, ascURL)
	if err != nil {
		return "", fmt.Errorf("bunruntime: fetch %s: %w: %w", ascURL, err, core.ErrBunSignatureVerificationFailed)
	}

	keyring, err := r.releaseKeyring()
	if err != nil {
		return "", fmt.Errorf("bunruntime: parse release signing key: %w: %w", err, core.ErrBunSignatureVerificationFailed)
	}

	block, _ := clearsign.Decode(ascBody)
	if block == nil {
		return "", fmt.Errorf("bunruntime: %s is not a valid clearsigned message: %w", ascURL, core.ErrBunSignatureVerificationFailed)
	}
	// nil config: the openpgp v1 API used here doesn't reject weak signature
	// hash algorithms (SHA-1/MD5) by default the way openpgp/v2 does — not
	// currently exploitable (forging a signature over attacker-chosen
	// content still needs a hash collision against an existing genuine Bun
	// signature, and Bun signs with SHA-512), but noted so a future
	// migration to openpgp/v2 — or passing an explicit
	// packet.Config{RejectMessageHashAlgorithms: ...} here — isn't lost.
	if _, err := block.VerifySignature(keyring, nil); err != nil {
		return "", fmt.Errorf("bunruntime: signature verification failed for %s: %w: %w", ascURL, err, core.ErrBunSignatureVerificationFailed)
	}

	// block.Plaintext is only trustworthy past this point — it is the exact
	// content VerifySignature just checked the signature over (block.Bytes),
	// with the clearsign dash-escaping undone.
	sha, err := parseSHASUMSEntry(block.Plaintext, targetName+".zip")
	if err != nil {
		return "", fmt.Errorf("bunruntime: %s: %w: %w", ascURL, err, core.ErrBunSignatureVerificationFailed)
	}
	return sha, nil
}

// tofuPinStorePath returns the path to this resolver's trust-on-first-use
// checksum pin store, alongside the cached binaries themselves.
func (r *Resolver) tofuPinStorePath() string {
	return filepath.Join(r.CacheDir, "checksums-pinned.json")
}

// checkAndPinChecksum implements trust-on-first-use for a (version, target)
// key whose checksum came from the dynamic fetchVerifiedChecksum path (not
// pinnedReleaseChecksums, which is compiled into the binary and already
// maximally trusted — TOFU adds nothing there). The first time a given key
// is verified, its checksum is persisted to disk; on every later resolve of
// the same key, a disagreement is treated as a possible downgrade or
// substitution attack (see fetchVerifiedChecksum's doc comment for the real
// gap this closes — a valid GPG signature over SHASUMS256.txt does not bind
// its content to the release it claims to be for) and rejected.
//
// This is real, useful hardening with an honest, stated limit: it does
// nothing for the very first resolve of a given (version, target) on a
// given cache — an attacker positioned to tamper with that first fetch
// establishes the "trusted" baseline. It is most valuable for a repeat-build
// dev machine or CI with a persistent cache (e.g. actions/cache), where a
// later, different fetch for a key already resolved once is the actual
// attack this defends against; it provides no protection on an ephemeral
// CI runner with a fresh cache every run.
//
// Persisting the new pin is best-effort: a failure to write it (permission
// issue, full disk) does not fail the build, since the pin store is
// hardening infrastructure, not itself a security-critical check — the real
// GPG signature verification in fetchVerifiedChecksum already happened
// before this is ever called. A write failure just means this exact key
// keeps behaving as "first use" until a write eventually succeeds.
func (r *Resolver) checkAndPinChecksum(key, checksum string) error {
	path := r.tofuPinStorePath()

	r.mu.Lock()
	data, readErr := os.ReadFile(path)
	r.mu.Unlock()

	pins := map[string]string{}
	if readErr == nil {
		// A corrupt or foreign pin store is treated as no prior pin, not an
		// error — this is best-effort hardening, not itself the security
		// boundary (that's the GPG signature check that already ran).
		_ = json.Unmarshal(data, &pins)
	}

	if existing, ok := pins[key]; ok {
		if existing != checksum {
			return fmt.Errorf(
				"bunruntime: checksum for %s changed since it was first verified here (pinned %s, now %s) — this could mean a compromised distribution channel served an older, genuinely-signed release under this version's name (see fetchVerifiedChecksum's doc comment); if this is a deliberate, trusted change, remove this key from %s: %w",
				key, existing, checksum, path, core.ErrBunChecksumPinViolation,
			)
		}
		return nil
	}

	pins[key] = checksum
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil //nolint:nilerr // best-effort persistence, see doc comment
	}
	marshaled, err := json.MarshalIndent(pins, "", "  ")
	if err != nil {
		return nil //nolint:nilerr // best-effort persistence, see doc comment
	}
	_ = os.WriteFile(path, marshaled, 0o600)
	return nil
}

// releaseKeyring returns the keyring trusted to verify Bun release
// signatures: r.ReleaseKeyArmored if set (used by tests to inject a
// throwaway keypair so they can exercise real verification, including the
// tamper-rejection path, without Bun's actual private key), otherwise the
// embedded production key.
func (r *Resolver) releaseKeyring() (openpgp.EntityList, error) {
	armored := r.ReleaseKeyArmored
	if armored == "" {
		armored = embeddedBunReleaseKeyArmored
	}
	return openpgp.ReadArmoredKeyRing(strings.NewReader(armored))
}

// fetchSmall performs a simple GET and returns the response body, capped at
// 1MB and failing on any non-200 status. Distinct from the release-archive
// download in Resolve: this is only ever used for the few-KB
// SHASUMS256.txt.asc manifest, never the ~90MB binary archive.
func (r *Resolver) fetchSmall(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return body, nil
}

// parseSHASUMSEntry finds the SHA256 hex digest for filename in
// SHASUMS256.txt-format plaintext (one "<hex>  <filename>" line per file,
// GNU coreutils sha256sum output format — two spaces, but strings.Fields
// splits on any run of whitespace so this tolerates either).
func parseSHASUMSEntry(plaintext []byte, filename string) (string, error) {
	for _, line := range strings.Split(string(plaintext), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != filename {
			continue
		}
		sha := strings.ToLower(fields[0])
		if len(sha) != 64 {
			return "", fmt.Errorf("malformed checksum length %d for %s", len(sha), filename)
		}
		return sha, nil
	}
	return "", fmt.Errorf("no checksum entry for %s in signed manifest", filename)
}

// bunVersionPattern restricts accepted Bun version strings to a safe
// charset before they're interpolated into a cache filesystem path and a
// release download URL. Without this, a version string containing "../"
// could make the cache path escape CacheDir entirely.
var bunVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.\-]*$`)

func validateBunVersion(version string) error {
	if !bunVersionPattern.MatchString(version) {
		return fmt.Errorf("bun version %q contains characters outside the allowed set (alphanumeric, '.', '-', not starting with '.' or '-')", version)
	}
	return nil
}

func resolveBunTargetName(p ports.Platform, variant ports.BunVariant) (string, error) {
	if p.OS != "linux" {
		return "", fmt.Errorf("unsupported operating system %s", p.OS)
	}
	// An unrecognized variant must be a hard error, not a silent fall-through
	// to "standard": every branch below treats "not baseline" as standard,
	// so a typo'd --bun-variant would previously produce a silently wrong
	// artifact under a differently-named cache directory.
	switch variant {
	case ports.BunVariantStandard, ports.BunVariantBaseline:
	default:
		return "", fmt.Errorf("unknown bun variant %q (must be %q or %q)", variant, ports.BunVariantStandard, ports.BunVariantBaseline)
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

// cacheDigestSidecarPath returns the path of the sidecar file recording the
// trusted SHA256 of the binary at binaryPath, at the time it was verified
// and written by this resolver.
func cacheDigestSidecarPath(binaryPath string) string {
	return binaryPath + ".sha256"
}

// verifiedCacheHit reports whether binaryPath is a cache hit whose contents
// are still exactly what this resolver verified and wrote there — not
// merely a file that happens to exist at that path. A binary with no
// sidecar, an unreadable sidecar, or a sidecar that disagrees with the
// binary's current hash is NOT a hit: it is treated identically to a cold
// cache, forcing a fresh download-and-verify. Without this, a cache-hit
// check that only asks "does a file exist here" trusts arbitrary bytes any
// other local process (or another local user on a shared/misconfigured
// cache directory) could have placed at that exact path — a cache hit was
// the one code path that received zero integrity checking even before this
// file's checksum-verification-for-unpinned-versions fix, since it never
// compares against pinnedReleaseChecksums or a signed manifest at all.
func verifiedCacheHit(binaryPath string) (sha string, size int64, ok bool) {
	info, err := os.Stat(binaryPath)
	if err != nil || info.IsDir() {
		return "", 0, false
	}
	wantSHA, err := os.ReadFile(cacheDigestSidecarPath(binaryPath))
	if err != nil {
		return "", 0, false
	}
	gotSHA, gotSize, err := computeFileSHA256(binaryPath)
	if err != nil || gotSHA != strings.TrimSpace(string(wantSHA)) {
		return "", 0, false
	}
	return gotSHA, gotSize, true
}

// writeCacheDigestSidecar records sha as the trusted digest for the
// just-verified-and-written binary at binaryPath, so a later Resolve call
// for the same path can prove nothing tampered with it since. Only ever
// called immediately after a binary this resolver itself produced (via a
// cryptographically verified download, or a locally compiled stub) has had
// its digest freshly computed — never with an externally-supplied value.
func writeCacheDigestSidecar(binaryPath, sha string) error {
	return os.WriteFile(cacheDigestSidecarPath(binaryPath), []byte(sha), 0o600)
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

	return hex.EncodeToString(h.Sum(nil)), size, nil
}

func (r *Resolver) compileStub(ctx context.Context, version string, variant ports.BunVariant, platform ports.Platform, targetDir, binaryPath string) (ports.BunResolverResult, error) {
	// 0o700: matches the download path's cache directory hardening — see
	// verifiedCacheHit's doc comment for why this cache root shouldn't be
	// readable/writable by other local users.
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
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

	const entryFilename = "stub-entry.ts"
	entryFile := filepath.Join(tmpDir, entryFilename)
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
	//
	// The entry argument is passed as a bare relative filename (entryFilename,
	// not entryFile's absolute path), with cmd.Dir set to tmpDir. `bun build
	// --compile` embeds something derived from the entry file's path into the
	// compiled binary's bytes, so passing the absolute path — which differs
	// on every call because tmpDir is a fresh os.MkdirTemp directory — made
	// every compiled stub non-deterministic even for byte-identical source,
	// target, and output path. Passing a fixed relative argument instead
	// (verified empirically across repeated runs and separate MkdirTemp
	// directories) produces byte-identical output; see Lessons.md's
	// stub-launcher determinism entry for the investigation.
	cmd := exec.CommandContext(ctx, "bun", "build", "--compile", "--target="+targetName, "--outfile="+tmpBinary, entryFilename) //nolint:gosec // G204: fixed resolver-internal target name + self-created temp paths, not user-controlled
	cmd.Dir = tmpDir
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
		out, createErr := os.OpenFile(binaryPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
		if createErr != nil {
			return ports.BunResolverResult{}, fmt.Errorf("bunruntime: create stub destination %s: %w: %w", binaryPath, createErr, core.ErrBunResolutionFailed)
		}
		_, copyErr := io.Copy(out, in)
		out.Close()
		if copyErr != nil {
			return ports.BunResolverResult{}, fmt.Errorf("bunruntime: write stub destination %s: %w: %w", binaryPath, copyErr, core.ErrBunResolutionFailed)
		}
	} else if err := os.Chmod(binaryPath, 0o700); err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: chmod stub destination %s: %w: %w", binaryPath, err, core.ErrBunResolutionFailed)
	}

	sha, size, err := computeFileSHA256(binaryPath)
	if err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: checksum calculation for %s: %w: %w", binaryPath, err, core.ErrBunResolutionFailed)
	}
	// Same rationale as the download path: record what we just compiled and
	// hashed, so a later cache-hit (verifiedCacheHit) can detect tampering.
	if err := writeCacheDigestSidecar(binaryPath, sha); err != nil {
		return ports.BunResolverResult{}, fmt.Errorf("bunruntime: write cache digest sidecar for %s: %w: %w", binaryPath, err, core.ErrBunResolutionFailed)
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
