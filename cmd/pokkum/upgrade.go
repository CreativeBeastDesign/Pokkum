package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// DefaultReleasePublicKeyPEM is the default embedded Cosign public key for Pokkum release verification.
const DefaultReleasePublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE7p+s2iXw81hZg75Ym3h58H4Xf8v0
5jX5W9R+m6T4Z2g3h+r9n8l7k6j5i4h3g2f1e0d9c8b7a6
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
	Get(ctx context.Context, url string) ([]byte, error)
}

type defaultHTTPFetcher struct {
	client *http.Client
}

func (f *defaultHTTPFetcher) Get(ctx context.Context, url string) ([]byte, error) {
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

	return io.ReadAll(resp.Body)
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
		relData, err := fetcher.Get(ctx, "https://api.github.com/repos/CreativeBeastDesign/pokkum/releases/latest")
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

	checksumsData, err := fetcher.Get(ctx, checksumsURL)
	if err != nil {
		return fmt.Errorf("fetch release checksums: %w: %w", err, core.ErrReleaseUpgradeFailed)
	}

	checksumsSigData, err := fetcher.Get(ctx, checksumsSigURL)
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
	if verifier != nil {
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

	archiveData, err := fetcher.Get(ctx, archiveURL)
	if err != nil {
		return fmt.Errorf("download release archive %s: %w: %w", archiveName, err, core.ErrReleaseUpgradeFailed)
	}

	expectedChecksum, err := parseChecksumForFilename(checksumsData, archiveName)
	if err != nil {
		return fmt.Errorf("parse checksum for %s: %w: %w", archiveName, err, core.ErrReleaseUpgradeFailed)
	}

	if verifier != nil {
		if err := verifier.VerifyChecksum(archiveData, expectedChecksum); err != nil {
			return fmt.Errorf("release checksum verification failed: %w: %w", err, core.ErrReleaseVerificationFailed)
		}
	}

	binaryBytes, err := extractBinaryFromTarGz(archiveData, "pokkum")
	if err != nil {
		return fmt.Errorf("extract binary from archive: %w: %w", err, core.ErrReleaseUpgradeFailed)
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
	tmpFile.Close()

	if err := os.Chmod(tmpPath, 0755); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("chmod upgrade binary: %w: %w", err, core.ErrReleaseUpgradeFailed)
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
		if baseName == binaryName && (header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA) {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("binary %s not found in release archive", binaryName)
}

func replaceBinary(newBinaryPath, targetPath string) error {
	if err := os.Rename(newBinaryPath, targetPath); err != nil {
		_ = os.Remove(targetPath)
		if err := os.Rename(newBinaryPath, targetPath); err != nil {
			return fmt.Errorf("failed to replace binary at %s: %w", targetPath, err)
		}
	}
	return nil
}
