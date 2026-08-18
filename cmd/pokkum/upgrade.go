package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

const (
	// maxMetadataBytes bounds the release JSON, checksums.txt, and
	// checksums.txt.sig fetches. All three are small text/binary blobs
	// (KB range even for a release with many platform archives) fetched
	// BEFORE any signature or checksum verification, so a compromised
	// asset host or an HTTPS MITM with a valid cert must not be able to
	// force unbounded memory use on the build host pre-authentication.
	// Matches bunruntime.Resolver.fetchSmall's cap for the same class of
	// fetch (a small signed manifest).
	maxMetadataBytes = 1 << 20 // 1MB

	// maxArchiveBytes bounds the release tar.gz download, which is also
	// fully read into memory before checksum verification (~line 314) --
	// this is the cap that actually matters for the OOM concern, since a
	// compiled CLI binary archive is legitimately only tens of MB.
	// Matches bunruntime.Resolver.Resolve's cap for the same class of
	// artifact (a compiled binary release archive).
	maxArchiveBytes = 512 << 20 // 512MB

	// maxExtractedBinaryBytes bounds the single tar entry extracted from
	// an already checksum-verified archive. This read happens AFTER
	// verification, so exploiting it requires a signing-key compromise
	// rather than a MITM -- but it still closes a decompression-bomb gap:
	// gzip can inflate a tiny archive into an enormous tar entry. The
	// extracted binary can never legitimately exceed the archive's own
	// size, so the same bound is used here.
	maxExtractedBinaryBytes = 512 << 20 // 512MB

	// backupSuffix names the sibling path replaceBinary uses to hold the
	// previous binary while swapping in the new one, so a failure partway
	// through the swap has something to restore from.
	backupSuffix = ".pokkum-upgrade-old"
)

// renameFile is os.Rename, indirected so tests can inject a deterministic,
// targeted rename failure (e.g. to simulate EXDEV or a locked file on
// Windows) without touching the real filesystem's permission model.
// Mirrors this package's existing exitFunc/execSelfPath testability seams
// (verify.go, hermetic_reexec_linux.go).
var renameFile = os.Rename

// DefaultReleasePublicKeyPEM is the default embedded Cosign public key for
// Pokkum release verification, used when --key is not given. It must match
// the private key CI signs releases with (the COSIGN_PRIVATE_KEY secret
// referenced by the "signs" block in .goreleaser.yaml) or every upgrade
// will correctly report verified=false against real releases.
//
// This key must be generated with `cosign generate-key-pair` — cosign's own
// --key loader rejects plain OpenSSL-generated PEM keys (confirmed against
// a real cosign v3.1.3 binary: both SEC1 "EC PRIVATE KEY" and PKCS8
// "PRIVATE KEY" fail with "unsupported pem type"). The public half it
// writes alongside the private key is standard PKIX PEM, same as this
// constant — only the private key needs cosign's own format.
const DefaultReleasePublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE2HR7TtlhsEwhADJJgxE7vyi7qTpY
QXexxQdEFu8lDf+lWHRraoeilxMOcTTsxRsQRVCrNjeG6Lm0ND+djT3V/Q==
-----END PUBLIC KEY-----`

type upgradeFlags struct {
	check   bool
	target  string
	output  string
	offline bool
	key     string
}

// UpgradeResult holds output details for self-upgrade checks.
type UpgradeResult struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpToDate        bool   `json:"up_to_date"`
	UpdateAvailable bool   `json:"update_available"`
	Verified        bool   `json:"verified"`
	Downloaded      bool   `json:"downloaded,omitempty"`
	Applied         bool   `json:"applied,omitempty"`
}

type githubRelease struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
	URL     string `json:"html_url"`
}

type releaseFetcher interface {
	// Get fetches url and returns its body, rejecting (not truncating) any
	// response whose body exceeds maxBytes. Callers pick maxBytes per call
	// site -- small metadata fetches use a much tighter cap than the
	// release archive itself.
	Get(ctx context.Context, url string, maxBytes int64) ([]byte, error)
}

type defaultHTTPFetcher struct {
	client *http.Client
}

func (f *defaultHTTPFetcher) Get(ctx context.Context, url string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "pokkum-cli-upgrade")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http status %d for %s", resp.StatusCode, url)
	}

	// LimitReader capped at maxBytes+1 so a body that lands exactly on the
	// cap isn't misread as "maybe truncated, maybe exactly the limit" --
	// reading one byte past the cap and checking length below tells the two
	// cases apart unambiguously. Nothing about these bytes is trusted yet
	// (this read happens before any signature/checksum check), so an
	// oversized body is rejected outright rather than silently truncated
	// and handed to a caller that might accept truncated-but-plausible
	// data.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response body for %s: %w", url, err)
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("response body for %s exceeds %d byte limit", url, maxBytes)
	}
	return body, nil
}

func newUpgradeCommand(ctx context.Context, logger *slog.Logger, verifier ports.ReleaseVerifier) *cobra.Command {
	flags := &upgradeFlags{}

	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Check for or apply signed self-updates for Pokkum CLI",
		Long: `Upgrade checks for new releases of Pokkum CLI, verifies release signatures,
and reports or applies updates.`,
		RunE: func(_ *cobra.Command, _ []string) error {
			fetcher := &defaultHTTPFetcher{client: &http.Client{Timeout: 15 * time.Second}}
			execPath, err := os.Executable()
			if err != nil {
				execPath = "pokkum"
			}
			return runUpgrade(ctx, logger, verifier, fetcher, flags, execPath)
		},
	}

	cmd.Flags().BoolVar(&flags.check, "check", false, "Check for available updates without applying")
	cmd.Flags().StringVar(&flags.target, "version", "", "Target version to upgrade to (default: latest)")
	cmd.Flags().StringVar(&flags.output, "output", "text", "Output format (text or json)")
	cmd.Flags().BoolVar(&flags.offline, "offline", false, "Disable network calls to release API")
	cmd.Flags().StringVar(&flags.key, "key", "", "Path to public key PEM file for release verification")

	return cmd
}

func runUpgrade(ctx context.Context, logger *slog.Logger, verifier ports.ReleaseVerifier, fetcher releaseFetcher, flags *upgradeFlags, execPath string) error {
	currVer := version
	if currVer == "" {
		currVer = "1.0.0-dev"
	}

	if flags.offline {
		latestVer := currVer
		if flags.target != "" {
			latestVer = strings.TrimPrefix(flags.target, "v")
		}
		upToDate := currVer == latestVer
		res := UpgradeResult{
			CurrentVersion:  currVer,
			LatestVersion:   latestVer,
			UpToDate:        upToDate,
			UpdateAvailable: !upToDate,
			Verified:        false,
		}

		if flags.output == "json" {
			envelope := ports.JSONEnvelope{
				SchemaVersion: "1.0",
				Command:       "upgrade",
				Status:        "success",
				Data:          res,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(envelope)
		}

		fmt.Printf("Pokkum Version Check (Offline)\n")
		fmt.Printf("==============================\n")
		fmt.Printf("Current Version: %s\n", currVer)
		fmt.Printf("Latest Version:  %s (offline mode - unverified)\n", latestVer)
		if flags.check {
			return nil
		}
		return fmt.Errorf("upgrade failed: network calls disabled in offline mode: %w", core.ErrHermeticViolation)
	}

	latestVer := currVer
	if flags.target != "" {
		latestVer = strings.TrimPrefix(flags.target, "v")
	} else {
		relData, err := fetcher.Get(ctx, "https://api.github.com/repos/CreativeBeastDesign/pokkum/releases/latest", maxMetadataBytes)
		if err != nil {
			return fmt.Errorf("fetch latest release: %w: %w", err, core.ErrReleaseUpgradeFailed)
		}
		var rel githubRelease
		if err := json.Unmarshal(relData, &rel); err != nil {
			return fmt.Errorf("unmarshal release metadata: %w: %w", err, core.ErrReleaseUpgradeFailed)
		}
		if rel.TagName != "" {
			latestVer = strings.TrimPrefix(rel.TagName, "v")
		}
	}

	baseURL := fmt.Sprintf("https://github.com/CreativeBeastDesign/pokkum/releases/download/v%s", latestVer)
	checksumsURL := baseURL + "/checksums.txt"
	checksumsSigURL := baseURL + "/checksums.txt.sig"

	checksumsData, err := fetcher.Get(ctx, checksumsURL, maxMetadataBytes)
	if err != nil {
		return fmt.Errorf("fetch release checksums: %w: %w", err, core.ErrReleaseUpgradeFailed)
	}

	checksumsSigData, err := fetcher.Get(ctx, checksumsSigURL, maxMetadataBytes)
	if err != nil {
		return fmt.Errorf("fetch release signature: %w: %w", err, core.ErrReleaseUpgradeFailed)
	}

	pubKeyPEM := []byte(DefaultReleasePublicKeyPEM)
	if flags.key != "" {
		kPEM, err := os.ReadFile(flags.key)
		if err != nil {
			return fmt.Errorf("read public key file: %w: %w", err, core.ErrInvalidRequest)
		}
		pubKeyPEM = kPEM
	}

	verified := false
	switch {
	case verifier == nil && !flags.check:
		// Apply mode with no verifier at all must fail closed: silently
		// skipping straight to download+install would reintroduce the
		// original bug (Verified hardcoded true with zero checks) in a new
		// shape — "no verifier configured" behaving identically to "verified".
		return fmt.Errorf("upgrade: no release verifier configured, refusing to install an unverified binary: %w", core.ErrReleaseVerificationFailed)
	case verifier == nil:
		// --check only reports; nothing gets installed, so an honest
		// verified=false is safe to surface instead of hard-failing.
		if logger != nil {
			logger.WarnContext(ctx, "no release verifier configured; skipping signature check")
		}
	default:
		err := verifier.VerifyArtifactSignature(ctx, ports.VerifyReleaseArtifactRequest{
			ArtifactBytes:  checksumsData,
			SignatureBytes: checksumsSigData,
			PublicKeyPEM:   pubKeyPEM,
		})
		if err != nil {
			if !flags.check {
				return fmt.Errorf("release signature verification failed: %w: %w", err, core.ErrReleaseVerificationFailed)
			}
			if logger != nil {
				logger.WarnContext(ctx, "release signature verification failed", "error", err)
			}
		} else {
			verified = true
		}
	}

	upToDate := currVer == latestVer
	res := UpgradeResult{
		CurrentVersion:  currVer,
		LatestVersion:   latestVer,
		UpToDate:        upToDate,
		UpdateAvailable: !upToDate,
		Verified:        verified,
	}

	if flags.check {
		if flags.output == "json" {
			envelope := ports.JSONEnvelope{
				SchemaVersion: "1.0",
				Command:       "upgrade",
				Status:        "success",
				Data:          res,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(envelope)
		}

		fmt.Printf("Pokkum Version Check\n")
		fmt.Printf("====================\n")
		fmt.Printf("Current Version: %s\n", currVer)
		fmt.Printf("Latest Version:  %s\n", latestVer)
		fmt.Printf("Verified:        %t\n", verified)

		if upToDate {
			fmt.Println("Pokkum is already up to date!")
		} else {
			fmt.Printf("New version available (%s -> %s). Run `pokkum upgrade` to update.\n", currVer, latestVer)
		}
		return nil
	}

	if upToDate && flags.target == "" {
		if flags.output == "json" {
			envelope := ports.JSONEnvelope{
				SchemaVersion: "1.0",
				Command:       "upgrade",
				Status:        "success",
				Data:          res,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(envelope)
		}

		fmt.Printf("Pokkum Version Check\n")
		fmt.Printf("====================\n")
		fmt.Printf("Current Version: %s\n", currVer)
		fmt.Printf("Latest Version:  %s\n", latestVer)
		fmt.Printf("Verified:        %t\n", verified)
		fmt.Println("Pokkum is already up to date!")
		return nil
	}

	// Download & apply upgrade binary
	archiveName := fmt.Sprintf("pokkum_%s_%s_%s.tar.gz", latestVer, runtime.GOOS, runtime.GOARCH)
	archiveURL := fmt.Sprintf("%s/%s", baseURL, archiveName)

	archiveData, err := fetcher.Get(ctx, archiveURL, maxArchiveBytes)
	if err != nil {
		return fmt.Errorf("download release archive %s: %w: %w", archiveName, err, core.ErrReleaseUpgradeFailed)
	}

	expectedChecksum, err := parseChecksumForFilename(checksumsData, archiveName)
	if err != nil {
		return fmt.Errorf("parse checksum for %s: %w: %w", archiveName, err, core.ErrReleaseUpgradeFailed)
	}

	// verifier is guaranteed non-nil here: the signature-check switch above
	// already returned an error for (verifier == nil && !flags.check), and
	// this download/apply path only runs when !flags.check.
	if verifier == nil {
		return fmt.Errorf("upgrade: no release verifier configured, refusing to install an unverified binary: %w", core.ErrReleaseVerificationFailed)
	}
	if err := verifier.VerifyChecksum(archiveData, expectedChecksum); err != nil {
		return fmt.Errorf("release checksum verification failed: %w: %w", err, core.ErrReleaseVerificationFailed)
	}

	binaryBytes, err := extractBinaryFromTarGz(archiveData, "pokkum")
	if err != nil {
		return fmt.Errorf("extract binary from archive: %w: %w", err, core.ErrReleaseUpgradeFailed)
	}

	// Preserve the currently-installed binary's mode (owner/perm bits) for
	// the replacement rather than hardcoding one -- a binary installed as
	// e.g. 0o750 must not be silently widened to 0o755 by an upgrade. Only
	// fall back to a sane default when there is no existing target to read
	// a mode from (a fresh install through this path).
	const defaultBinaryMode = 0o755
	targetMode := os.FileMode(defaultBinaryMode)
	if info, err := os.Stat(execPath); err == nil {
		targetMode = info.Mode().Perm()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat existing binary at %s: %w: %w", execPath, err, core.ErrReleaseUpgradeFailed)
	}

	tempDir := filepath.Dir(execPath)
	if _, err := os.Stat(tempDir); err != nil {
		tempDir = os.TempDir()
	}

	tmpFile, err := os.CreateTemp(tempDir, "pokkum-upgrade-*")
	if err != nil {
		return fmt.Errorf("create temp binary file: %w: %w", err, core.ErrReleaseUpgradeFailed)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(binaryBytes); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write upgrade binary: %w: %w", err, core.ErrReleaseUpgradeFailed)
	}

	// Chmod the still-open file handle rather than the path
	// (os.Chmod(tmpPath, ...)): a path-based chmod after close is
	// symlink-raceable in a writable directory (something could swap
	// tmpPath for a symlink between Close and Chmod); Chmod on the open
	// handle operates on the inode directly and can't be redirected that
	// way.
	if err := tmpFile.Chmod(targetMode); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("chmod upgrade binary: %w: %w", err, core.ErrReleaseUpgradeFailed)
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close upgrade binary: %w: %w", err, core.ErrReleaseUpgradeFailed)
	}

	if err := replaceBinary(tmpPath, execPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("replace executable binary: %w: %w", err, core.ErrReleaseUpgradeFailed)
	}

	res.Downloaded = true
	res.Applied = true

	if flags.output == "json" {
		envelope := ports.JSONEnvelope{
			SchemaVersion: "1.0",
			Command:       "upgrade",
			Status:        "success",
			Data:          res,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(envelope)
	}

	fmt.Printf("Successfully upgraded Pokkum CLI from %s to %s (verified: %t)\n", currVer, latestVer, verified)
	return nil
}

func parseChecksumForFilename(checksumsData []byte, filename string) (string, error) {
	lines := strings.Split(string(checksumsData), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && (fields[1] == filename || strings.HasSuffix(fields[1], "/"+filename) || fields[1] == "*"+filename) {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksum for %s not found in checksums.txt", filename)
}

func extractBinaryFromTarGz(tarGzData []byte, binaryName string) ([]byte, error) {
	return extractBinaryFromTarGzCapped(tarGzData, binaryName, maxExtractedBinaryBytes)
}

// extractBinaryFromTarGzCapped is extractBinaryFromTarGz with the size cap
// as a parameter, so tests can exercise oversized-entry rejection with a
// small cap instead of needing to construct a real 512MB+ tar entry.
func extractBinaryFromTarGzCapped(tarGzData []byte, binaryName string, maxBytes int64) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(tarGzData))
	if err != nil {
		return nil, fmt.Errorf("gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar reader: %w", err)
		}

		baseName := filepath.Base(header.Name)
		if baseName == binaryName && header.Typeflag == tar.TypeReg {
			// This read happens AFTER checksum verification, so exploiting
			// it needs a signing-key compromise rather than a MITM -- but
			// gzip can still inflate a small, checksum-matching archive
			// into an enormous tar entry (a decompression bomb), so it is
			// capped the same way the pre-verification fetches are.
			data, err := io.ReadAll(io.LimitReader(tr, maxBytes+1))
			if err != nil {
				return nil, fmt.Errorf("read tar entry %s: %w", header.Name, err)
			}
			if int64(len(data)) > maxBytes {
				return nil, fmt.Errorf("extracted binary %s exceeds %d byte limit", binaryName, maxBytes)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("binary %s not found in release archive", binaryName)
}

// replaceBinary atomically swaps newBinaryPath into targetPath. On any
// failure during the swap it restores the previous binary from a sibling
// backup path, so targetPath is guaranteed to end up with either the new
// binary or the original working one -- never neither.
//
// Historically this retried the exact same os.Rename after unconditionally
// unlinking targetPath. That retry is the identical operation and so
// cannot succeed for the deterministic reason the first attempt failed
// (most commonly EXDEV -- the temp dir fell back to os.TempDir() on a
// different filesystem than the install dir -- but also a locked file on
// Windows or a read-only remount). In every one of those cases the target
// had already been unlinked, so the caller's own subsequent cleanup of the
// temp file left no `pokkum` binary on disk at all: a security tool that
// self-destructs on a partial failure.
func replaceBinary(newBinaryPath, targetPath string) error {
	backupPath := targetPath + backupSuffix

	// os.Rename replaces an existing destination atomically on every
	// platform this CLI ships for (Go's Windows implementation passes
	// MOVEFILE_REPLACE_EXISTING), so a stale backupPath left over from a
	// previously interrupted upgrade is safely overwritten here rather
	// than needing to be cleaned up first.
	targetExisted := true
	if err := renameFile(targetPath, backupPath); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("back up existing binary at %s: %w", targetPath, err)
		}
		// Nothing to back up (e.g. a fresh install) -- proceed without a
		// restore path, since there is nothing working to restore to.
		targetExisted = false
	}

	if err := renameOrCopyFile(newBinaryPath, targetPath); err != nil {
		if targetExisted {
			if restoreErr := renameFile(backupPath, targetPath); restoreErr != nil {
				return fmt.Errorf(
					"replace binary at %s failed (%w) and restoring the previous binary also failed (%v): the previous binary is preserved at %s, restore it manually",
					targetPath, err, restoreErr, backupPath,
				)
			}
		}
		return fmt.Errorf("replace binary at %s: %w", targetPath, err)
	}

	if targetExisted {
		if err := os.Remove(backupPath); err != nil {
			// The new binary is already installed and confirmed in place;
			// a leftover backup file is a tidiness issue, not a
			// correctness one, so this is intentionally non-fatal.
			return nil
		}
	}
	return nil
}

// renameOrCopyFile moves src to dst via rename, falling back to a
// copy-then-remove only when rename fails with EXDEV (src and dst on
// different filesystems -- exactly the case where the upgrade temp dir
// fell back to os.TempDir()). Any other rename error (permission denied, a
// read-only remount, a file in use on Windows) is returned as-is: retrying
// the identical rename cannot succeed for those, which was the original
// bug.
func renameOrCopyFile(src, dst string) error {
	err := renameFile(src, dst)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return err
	}

	if copyErr := copyFileContents(src, dst); copyErr != nil {
		return fmt.Errorf("cross-device rename fallback copy %s to %s: %w", src, dst, copyErr)
	}
	// The copy already succeeded and dst is a fully-formed, independent
	// file; failing to remove the now-redundant temp source is a leftover
	// file, not a correctness problem for the caller.
	_ = os.Remove(src)
	return nil
}

// copyFileContents copies src's bytes into a freshly created dst,
// preserving src's file mode, and cleans up a partial dst on any failure
// so a failed copy never leaves a half-written binary at dst. O_EXCL
// guarantees dst did not already exist -- it either didn't exist before
// this call (the normal case, since replaceBinary already moved anything
// at that path aside) or the create fails loudly instead of writing
// through whatever was already there.
func copyFileContents(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}

	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("copy to %s: %w", dst, err)
	}
	// OpenFile's mode is subject to umask when creating a new file, so
	// re-assert the exact source mode on the still-open handle (the same
	// symlink-race-safe primitive used above for the upgrade binary's own
	// chmod) rather than trusting umask arithmetic to have preserved it.
	if err := out.Chmod(info.Mode().Perm()); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("chmod %s: %w", dst, err)
	}
	if err := out.Sync(); err != nil {
		out.Close()
		_ = os.Remove(dst)
		return fmt.Errorf("sync %s: %w", dst, err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("close %s: %w", dst, err)
	}
	return nil
}
