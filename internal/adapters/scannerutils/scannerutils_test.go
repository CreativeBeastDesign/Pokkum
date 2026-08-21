package scannerutils

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

func TestParseOSRelease(t *testing.T) {
	tests := []struct {
		input       string
		wantID      string
		wantVersion string
		wantName    string
	}{
		{
			input: `PRETTY_NAME="Debian GNU/Linux 12 (bookworm)"
NAME="Debian GNU/Linux"
VERSION_ID="12"
VERSION="12 (bookworm)"
VERSION_CODENAME=bookworm
ID=debian
HOME_URL="https://www.debian.org/"`,
			wantID:      "debian",
			wantVersion: "12",
			wantName:    "Debian GNU/Linux",
		},
		{
			input: `NAME="Alpine Linux"
ID=alpine
VERSION_ID=3.19.1
PRETTY_NAME="Alpine Linux v3.19"
HOME_URL="https://alpinelinux.org/"`,
			wantID:      "alpine",
			wantVersion: "3.19.1",
			wantName:    "Alpine Linux",
		},
		{
			input: `NAME="Wolfi"
ID=wolfi
VERSION_ID="20230201"
PRETTY_NAME="Wolfi"`,
			wantID:      "wolfi",
			wantVersion: "20230201",
			wantName:    "Wolfi",
		},
	}

	for _, tt := range tests {
		got, err := ParseOSRelease(strings.NewReader(tt.input))
		if err != nil {
			t.Fatalf("ParseOSRelease error: %v", err)
		}
		if got.ID != tt.wantID {
			t.Errorf("got ID %q, want %q", got.ID, tt.wantID)
		}
		if got.VersionID != tt.wantVersion {
			t.Errorf("got VersionID %q, want %q", got.VersionID, tt.wantVersion)
		}
		if got.Name != tt.wantName {
			t.Errorf("got Name %q, want %q", got.Name, tt.wantName)
		}
	}
}

func TestParseDPKGStatus(t *testing.T) {
	raw := `Package: libc6
Status: install ok installed
Priority: required
Section: libs
Installed-Size: 11228
Maintainer: GNU Libc Maintainers <debian-glibc@lists.debian.org>
Architecture: amd64
Multi-Arch: same
Source: glibc
Version: 2.36-9+deb12u4
Description: GNU C Library: Shared libraries
 An example description with
  multiple indented continuation lines.

Package: libssl3
Status: install ok installed
Priority: optional
Section: libs
Installed-Size: 5000
Architecture: amd64
Version: 3.0.11-1~deb12u1
Description: Secure Sockets Layer toolkit

Package: removed-pkg
Status: deinstall ok config-files
Version: 1.0.0
`

	pkgs, err := ParseDPKGStatus(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("ParseDPKGStatus error: %v", err)
	}

	if len(pkgs) != 2 {
		t.Fatalf("expected 2 installed packages (excluding removed-pkg), got %d: %+v", len(pkgs), pkgs)
	}

	if pkgs[0].Name != "libc6" || pkgs[0].Version != "2.36-9+deb12u4" || pkgs[0].Architecture != "amd64" {
		t.Errorf("unexpected first package: %+v", pkgs[0])
	}
	if pkgs[1].Name != "libssl3" || pkgs[1].Version != "3.0.11-1~deb12u1" {
		t.Errorf("unexpected second package: %+v", pkgs[1])
	}
}

func TestParseAPKInstalled(t *testing.T) {
	raw := `C:Q1musl-commit
P:musl
V:1.2.4_git20230717-r1
A:x86_64
S:624512
I:624512
T:the musl c library (libc) implementation
U:https://musl.libc.org/
L:MIT
o:musl

C:Q1ssl-commit
P:libssl3
V:3.1.4-r5
A:x86_64
L:Apache-2.0
`

	pkgs, err := ParseAPKInstalled(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("ParseAPKInstalled error: %v", err)
	}

	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(pkgs))
	}

	if pkgs[0].Name != "musl" || pkgs[0].Version != "1.2.4_git20230717-r1" || pkgs[0].License != "MIT" {
		t.Errorf("unexpected package 0: %+v", pkgs[0])
	}
	if pkgs[1].Name != "libssl3" || pkgs[1].Version != "3.1.4-r5" {
		t.Errorf("unexpected package 1: %+v", pkgs[1])
	}
}

func TestParseBunLock(t *testing.T) {
	data := []byte(`{
  "lockfileVersion": 1,
  "packages": {
    "svelte": ["svelte@5.0.0", "", {}, "sha512-x"],
    "@sveltejs/kit": ["@sveltejs/kit@2.31.0", "", {}, "sha512-y"]
  }
}`)

	pkgs, err := ParseBunLock(data)
	if err != nil {
		t.Fatalf("ParseBunLock error: %v", err)
	}

	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(pkgs))
	}

	seen := make(map[string]string)
	for _, p := range pkgs {
		seen[p.Name] = p.Version
	}

	if seen["svelte"] != "5.0.0" {
		t.Errorf("expected svelte@5.0.0, got %s", seen["svelte"])
	}
	if seen["@sveltejs/kit"] != "2.31.0" {
		t.Errorf("expected @sveltejs/kit@2.31.0, got %s", seen["@sveltejs/kit"])
	}
}

// TestParseBunLock_TrailingComma is a regression test for F6's root cause:
// every real bun.lock a `bun install` writes is JSONC-flavored, with a
// trailing comma before the closing '}' of "packages" (and commonly
// elsewhere too). encoding/json's strict parser rejects that outright, so
// without stripJSONTrailingCommas this returns an error and zero packages
// against literally any real-world bun.lock, even though the handwritten,
// comma-free fixtures elsewhere in this test file (e.g. TestParseBunLock
// above) pass just fine — that gap is exactly what let the bug ship
// unnoticed.
func TestParseBunLock_TrailingComma(t *testing.T) {
	data := []byte(`{
  "lockfileVersion": 1,
  "packages": {
    "mermaid": ["mermaid@10.9.6", "", {}, "sha512-x"],
    "bcryptjs": ["bcryptjs@3.0.3", "", {}, "sha512-x"],
  }
}`)

	pkgs, err := ParseBunLock(data)
	if err != nil {
		t.Fatalf("ParseBunLock() on a real-shaped (trailing-comma) bun.lock returned an error: %v", err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages, got %d: %+v", len(pkgs), pkgs)
	}

	seen := make(map[string]CatalogPackage)
	for _, p := range pkgs {
		seen[p.Name] = p
	}
	if seen["mermaid"].Version != "10.9.6" {
		t.Errorf("expected mermaid@10.9.6, got %s", seen["mermaid"].Version)
	}
	if !seen["mermaid"].Resolved {
		t.Error("expected mermaid to be marked Resolved (it came from the lockfile)")
	}
	if seen["bcryptjs"].Version != "3.0.3" {
		t.Errorf("expected bcryptjs@3.0.3, got %s", seen["bcryptjs"].Version)
	}
}

func TestIsConcreteVersion(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"10.9.6", true},
		{"0.1.7", true},
		{"2.68.0", true},
		{"^10.9.1", false},
		{"~5.0.0", false},
		{"*", false},
		{">1.0.0", false},
		{">=1.2.0", false},
		{"1.2.x", false},
		{"1.2.3 || 2.0.0", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsConcreteVersion(tt.version); got != tt.want {
			t.Errorf("IsConcreteVersion(%q) = %v, want %v", tt.version, got, tt.want)
		}
	}
}

func TestParsePackageLock(t *testing.T) {
	data := []byte(`{
  "name": "app",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "packages": {
    "": { "name": "app", "version": "1.0.0" },
    "node_modules/lodash": { "version": "4.17.21" },
    "node_modules/@types/node": { "version": "22.0.0" }
  }
}`)

	pkgs, err := ParsePackageLock(data)
	if err != nil {
		t.Fatalf("ParsePackageLock error: %v", err)
	}

	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(pkgs))
	}

	seen := make(map[string]string)
	for _, p := range pkgs {
		seen[p.Name] = p.Version
	}

	if seen["lodash"] != "4.17.21" {
		t.Errorf("expected lodash@4.17.21, got %s", seen["lodash"])
	}
	if seen["@types/node"] != "22.0.0" {
		t.Errorf("expected @types/node@22.0.0, got %s", seen["@types/node"])
	}
}

func TestParsePnpmLock(t *testing.T) {
	data := []byte(`
lockfileVersion: '6.0'
packages:
  /@sveltejs/kit@2.31.0(svelte@5.0.0):
    resolution: {integrity: sha512-x}
  /svelte@5.0.0:
    resolution: {integrity: sha512-y}
`)

	pkgs, err := ParsePnpmLock(data)
	if err != nil {
		t.Fatalf("ParsePnpmLock error: %v", err)
	}

	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(pkgs))
	}

	seen := make(map[string]string)
	for _, p := range pkgs {
		seen[p.Name] = p.Version
	}

	if seen["svelte"] != "5.0.0" {
		t.Errorf("expected svelte@5.0.0, got %s", seen["svelte"])
	}
	if seen["@sveltejs/kit"] != "2.31.0" {
		t.Errorf("expected @sveltejs/kit@2.31.0, got %s", seen["@sveltejs/kit"])
	}
}

func TestExtractProjectDependencies(t *testing.T) {
	dir := t.TempDir()
	pkgJSON := `{"name":"test-app","dependencies":{"svelte":"^5.0.0"},"devDependencies":{"vite":"^6.0.0"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	pkgs, err := ExtractProjectDependencies(dir)
	if err != nil {
		t.Fatalf("ExtractProjectDependencies error: %v", err)
	}

	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(pkgs))
	}
}

// buildTestImage constructs a single-layer OCI image containing the given
// files (path -> content), for exercising ExtractImagePackages against a
// synthetic tar layout without touching a real registry or daemon.
func buildTestImage(t *testing.T, files map[string]string) v1.Image {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar WriteHeader(%s): %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar Write(%s): %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	tarBytes := buf.Bytes()

	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(tarBytes)), nil
	})
	if err != nil {
		t.Fatalf("tarball.LayerFromOpener: %v", err)
	}

	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatalf("mutate.AppendLayers: %v", err)
	}
	return img
}

// TestExtractImagePackages_VendoredPackageJSON is a regression test for a bug
// where ExtractImagePackages only read the dependencies/devDependencies maps
// out of any package.json under an "app/" path, and never the package.json's
// own name+version fields. Pokkum's own image layout (see
// ports.AppVendorDirPrefix = "/app/vendor" and packager.go) ships each
// vendored npm package's own package.json at /app/vendor/<pkg>/package.json.
// So for a real Pokkum-built image, the old code produced a catalog of
// unresolved semver ranges declared by dependents (e.g. "body-parser": a
// dependency range from express's package.json) and never contained the
// actual installed packages (lodash, express) themselves — making CVE
// lookups against real vendored packages non-functional.
func TestExtractImagePackages_VendoredPackageJSON(t *testing.T) {
	files := map[string]string{
		// The app's own manifest declares its direct dependencies as ranges —
		// this should NOT clobber or duplicate the exact installed versions
		// recorded from each package's own vendored package.json below.
		"app/package.json": `{
			"name": "my-app",
			"version": "1.0.0",
			"dependencies": {
				"lodash": "^4.17.0",
				"express": "^4.18.0"
			}
		}`,
		// lodash has no runtime dependencies of its own.
		"app/vendor/lodash/package.json": `{
			"name": "lodash",
			"version": "4.17.21"
		}`,
		// express declares transitive dependencies that are NOT independently
		// vendored in this image (no app/vendor/body-parser, no
		// app/vendor/cookie) — those unresolved ranges must not appear in the
		// final catalog as if they were installed packages.
		"app/vendor/express/package.json": `{
			"name": "express",
			"version": "4.18.2",
			"dependencies": {
				"body-parser": "1.20.1",
				"cookie": "0.5.0"
			}
		}`,
	}

	img := buildTestImage(t, files)

	pkgs, _, err := ExtractImagePackages(context.Background(), img)
	if err != nil {
		t.Fatalf("ExtractImagePackages error: %v", err)
	}

	want := []CatalogPackage{
		{Name: "express", Version: "4.18.2", Type: PkgTypeNpm, Ecosystem: "npm", Resolved: true},
		{Name: "lodash", Version: "4.17.21", Type: PkgTypeNpm, Ecosystem: "npm", Resolved: true},
	}

	if len(pkgs) != len(want) {
		t.Fatalf("ExtractImagePackages() = %+v, want %+v (length mismatch: got %d, want %d)", pkgs, want, len(pkgs), len(want))
	}
	for i := range want {
		if pkgs[i] != want[i] {
			t.Errorf("pkgs[%d] = %+v, want %+v", i, pkgs[i], want[i])
		}
	}

	// Explicitly confirm the bug scenario: the unresolved dependency ranges
	// from express's own package.json (body-parser, cookie) must NOT be
	// reported as installed packages, since they aren't independently
	// vendored in this image.
	for _, p := range pkgs {
		if p.Name == "body-parser" || p.Name == "cookie" {
			t.Errorf("unexpected unresolved transitive dependency reported as installed package: %+v", p)
		}
	}
}

func TestMapDistroEcosystem(t *testing.T) {
	tests := []struct {
		distro  DistroInfo
		pkgType PackageType
		want    string
	}{
		{DistroInfo{ID: "debian", VersionID: "12.5"}, PkgTypeDeb, "Debian:12"},
		{DistroInfo{ID: "ubuntu", VersionID: "22.04"}, PkgTypeDeb, "Ubuntu:22.04"},
		{DistroInfo{ID: "alpine", VersionID: "3.19.1"}, PkgTypeApk, "Alpine:v3.19"},
		{DistroInfo{ID: "wolfi"}, PkgTypeApk, "Wolfi"},
		{DistroInfo{ID: "chainguard"}, PkgTypeApk, "Chainguard"},
		{DistroInfo{}, PkgTypeNpm, "npm"},
	}

	for _, tt := range tests {
		got := MapDistroEcosystem(tt.distro, tt.pkgType)
		if got != tt.want {
			t.Errorf("MapDistroEcosystem(%+v, %v) = %q, want %q", tt.distro, tt.pkgType, got, tt.want)
		}
	}
}
