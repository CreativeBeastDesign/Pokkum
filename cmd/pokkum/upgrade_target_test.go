package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// TestCheckUpgradeTarget_PackageManagedInstallsPointAtTheirManager guards
// against self-replacing a binary a package manager owns: the manager would
// keep recording the old version and act on files that no longer match.
func TestCheckUpgradeTarget_PackageManagedInstallsPointAtTheirManager(t *testing.T) {
	for _, tc := range []struct {
		dir, want string
	}{
		{filepath.Join("Cellar", "pokkum", "1.2.1", "bin"), "brew upgrade pokkum"},
		{filepath.Join("lib", "node_modules", "@pokkum", "pokkum_darwin_arm64", "bin"), "npm install -g @pokkum/cli@latest"},
	} {
		dir := filepath.Join(t.TempDir(), tc.dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		exe := filepath.Join(dir, "pokkum")
		if err := os.WriteFile(exe, []byte("bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		// Reached through a symlink, as /opt/homebrew/bin/pokkum is.
		link := filepath.Join(t.TempDir(), "pokkum")
		if err := os.Symlink(exe, link); err != nil {
			t.Fatal(err)
		}
		err := checkUpgradeTarget(link)
		if err == nil || !strings.Contains(err.Error(), tc.want) || !errors.Is(err, core.ErrReleaseUpgradeFailed) {
			t.Errorf("%s: err = %v, want one naming %q", tc.dir, err, tc.want)
		}
	}
}

// countingFetcher records every URL fetched, so a test can prove the release
// archive was never downloaded.
type countingFetcher struct {
	mockFetcher
	urls []string
}

func (c *countingFetcher) Get(ctx context.Context, url string, limit int64) ([]byte, error) {
	c.urls = append(c.urls, url)
	return c.mockFetcher.Get(ctx, url, limit)
}

// TestRunUpgrade_UnwritableInstallDirFailsBeforeDownloadWithSudoHint is the
// field failure: install.sh had put pokkum in a root-owned /usr/local/bin, and
// upgrade died with a bare "permission denied" after downloading the release.
// It drives runUpgrade itself, so it also proves the check runs before the
// archive download rather than merely existing.
func TestRunUpgrade_UnwritableInstallDirFailsBeforeDownloadWithSudoHint(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write to a 0o555 directory")
	}
	privKey, pubPEM := generateTestKeyPair(t)
	keyPath := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(keyPath, pubPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	exe := filepath.Join(dir, "pokkum")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	ver := "1.2.0"
	archiveName := fmt.Sprintf("pokkum_%s_%s_%s.tar.gz", ver, runtime.GOOS, runtime.GOARCH)
	tarGz := createMockTarGz(t, []byte("new"))
	checksums := []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(tarGz), archiveName))
	h := sha256.Sum256(checksums)
	sig, err := ecdsa.SignASN1(rand.Reader, privKey, h[:])
	if err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("https://github.com/CreativeBeastDesign/pokkum/releases/download/v%s", ver)
	fetcher := &countingFetcher{mockFetcher: mockFetcher{responses: map[string][]byte{
		base + "/checksums.txt":     checksums,
		base + "/checksums.txt.sig": sig,
		base + "/" + archiveName:    tarGz,
	}}}

	flags := &upgradeFlags{target: ver, output: "json", key: keyPath}
	err = runUpgrade(context.Background(), discardLogger(), cosign.NewSigner(discardLogger()), fetcher, flags, exe)
	if err == nil || !strings.Contains(err.Error(), "sudo pokkum upgrade") || !errors.Is(err, core.ErrReleaseUpgradeFailed) {
		t.Fatalf("err = %v, want a sudo hint wrapping ErrReleaseUpgradeFailed", err)
	}
	for _, u := range fetcher.urls {
		if strings.HasSuffix(u, archiveName) {
			t.Errorf("downloaded %s before discovering the install directory is not writable", u)
		}
	}
	if got, _ := os.ReadFile(exe); string(got) != "old" {
		t.Errorf("installed binary changed to %q", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("probe left files behind: %v", entries)
	}
}

func TestCheckUpgradeTarget_WritableDirIsAcceptedAndLeftClean(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "pokkum")
	if err := os.WriteFile(exe, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkUpgradeTarget(exe); err != nil {
		t.Fatalf("writable install dir rejected: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("probe left files behind: %v", entries)
	}
}
