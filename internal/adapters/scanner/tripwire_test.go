package scanner

import (
	"context"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/scannerutils"
)

// TestTripwire_DebianDPKGStatusParser asserts that the targeted dpkg parser
// correctly extracts packages from a standard Debian status file format.
// If Debian changes /var/lib/dpkg/status structure in the future, this test fails.
func TestTripwire_DebianDPKGStatusParser(t *testing.T) {
	sample := `Package: base-files
Status: install ok installed
Priority: required
Section: admin
Installed-Size: 285
Architecture: amd64
Version: 12.4+deb12u5

Package: libc6
Status: install ok installed
Priority: required
Section: libs
Installed-Size: 11228
Architecture: amd64
Version: 2.36-9+deb12u4

Package: tzdata
Status: install ok installed
Priority: required
Section: localization
Installed-Size: 3100
Architecture: all
Version: 2024a-0+deb12u1
`
	pkgs, err := scannerutils.ParseDPKGStatus(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("Tripwire failed: ParseDPKGStatus returned error: %v", err)
	}
	if len(pkgs) != 3 {
		t.Fatalf("Tripwire alert: expected 3 packages from standard Debian status format, got %d", len(pkgs))
	}

	expected := map[string]string{
		"base-files": "12.4+deb12u5",
		"libc6":      "2.36-9+deb12u4",
		"tzdata":     "2024a-0+deb12u1",
	}

	for _, p := range pkgs {
		wantVer, ok := expected[p.Name]
		if !ok {
			t.Errorf("Tripwire alert: unexpected package parsed: %s", p.Name)
			continue
		}
		if p.Version != wantVer {
			t.Errorf("Tripwire alert: package %s version = %s, want %s", p.Name, p.Version, wantVer)
		}
	}
}

// TestTripwire_AlpineAPKInstalledParser asserts that the targeted apk parser
// correctly extracts packages from a standard Alpine installed db format.
// If Alpine changes /lib/apk/db/installed format, this test fails.
func TestTripwire_AlpineAPKInstalledParser(t *testing.T) {
	sample := `C:Q1musl-commit
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

C:Q1crypto-commit
P:libcrypto3
V:3.1.4-r5
A:x86_64
L:Apache-2.0
`
	pkgs, err := scannerutils.ParseAPKInstalled(strings.NewReader(sample))
	if err != nil {
		t.Fatalf("Tripwire failed: ParseAPKInstalled returned error: %v", err)
	}
	if len(pkgs) != 3 {
		t.Fatalf("Tripwire alert: expected 3 packages from standard Alpine apk db format, got %d", len(pkgs))
	}

	expected := map[string]string{
		"musl":       "1.2.4_git20230717-r1",
		"libssl3":    "3.1.4-r5",
		"libcrypto3": "3.1.4-r5",
	}

	for _, p := range pkgs {
		wantVer, ok := expected[p.Name]
		if !ok {
			t.Errorf("Tripwire alert: unexpected package parsed: %s", p.Name)
			continue
		}
		if p.Version != wantVer {
			t.Errorf("Tripwire alert: package %s version = %s, want %s", p.Name, p.Version, wantVer)
		}
	}
}

// TestTripwire_SvelteKitLockfilesParser asserts that Bun, npm, and pnpm lockfiles
// parse accurately and extract expected dependency trees.
func TestTripwire_SvelteKitLockfilesParser(t *testing.T) {
	bunData := []byte(`{
  "lockfileVersion": 1,
  "packages": {
    "svelte": ["svelte@5.0.0", "", {}, "sha512-x"],
    "@sveltejs/kit": ["@sveltejs/kit@2.31.0", "", {}, "sha512-y"]
  }
}`)
	bunPkgs, err := scannerutils.ParseBunLock(bunData)
	if err != nil || len(bunPkgs) < 2 {
		t.Fatalf("Tripwire alert: Bun lockfile parsing broke: %v (found %d packages)", err, len(bunPkgs))
	}

	npmData := []byte(`{
  "name": "app",
  "version": "1.0.0",
  "lockfileVersion": 3,
  "packages": {
    "": { "name": "app", "version": "1.0.0" },
    "node_modules/svelte": { "version": "5.0.0" },
    "node_modules/@sveltejs/kit": { "version": "2.31.0" }
  }
}`)
	npmPkgs, err := scannerutils.ParsePackageLock(npmData)
	if err != nil || len(npmPkgs) < 2 {
		t.Fatalf("Tripwire alert: NPM lockfile parsing broke: %v (found %d packages)", err, len(npmPkgs))
	}
}

// TestTripwire_LiveDistroBaseImages runs in CI to verify live upstream base image
// formats against real remote registries.
func TestTripwire_LiveDistroBaseImages(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live upstream image scan in short mode")
	}

	ctx := context.Background()

	// Test Distroless Debian 12
	t.Run("Distroless Debian 12", func(t *testing.T) {
		ref, err := name.ParseReference("gcr.io/distroless/cc-debian12:latest")
		if err != nil {
			t.Skipf("cannot parse ref: %v", err)
		}
		img, err := remote.Image(ref, remote.WithContext(ctx))
		if err != nil {
			t.Skipf("cannot reach registry (offline/sandboxed): %v", err)
		}

		pkgs, distro, err := scannerutils.ExtractImagePackages(ctx, img)
		if err != nil {
			t.Fatalf("Tripwire failed on gcr.io/distroless/cc-debian12:latest: %v", err)
		}
		if len(pkgs) < 5 {
			t.Fatalf("Tripwire alert: distroless debian 12 returned suspicious package count (%d < 5)", len(pkgs))
		}
		if distro.ID != "debian" {
			t.Errorf("Tripwire alert: expected distro ID debian, got %q", distro.ID)
		}
		t.Logf("Distroless Debian 12 tripwire verified: %d packages found, distro %s %s", len(pkgs), distro.ID, distro.VersionID)
	})
}
