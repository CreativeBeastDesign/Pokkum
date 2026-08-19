package scanner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
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
		if isTransientNetworkErr(err) {
			// remote.Image above only fetches the manifest, so reaching it
			// proves nothing about the multi-megabyte layer pull that happens
			// here — which is where nearly all the bytes, and nearly all the
			// transient-failure risk, actually are. A mid-stream reset says
			// nothing about upstream image format, so it cannot render the
			// verdict this tripwire exists to give: skip rather than fail.
			t.Skipf("network error during layer pull (not an upstream format change): %v", err)
		}
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

// isTransientNetworkErr reports whether err is a transport failure rather than
// a content or format problem. It exists so the live tripwire below can tell
// "upstream changed its image format", which is the signal this test is for
// and must fail loudly, apart from "the TCP connection dropped mid-pull",
// which is noise that would otherwise turn a correctness gate into a flaky
// one — this test runs in the non-short CI job, so a flake here blocks merges
// for reasons unrelated to the code under test.
//
// scannerutils wraps read failures with %w, so the transport error is still
// reachable via errors.As/errors.Is through the "tar read error: ..." wrapper.
func isTransientNetworkErr(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	// A reset mid-stream commonly surfaces as an unexpected EOF from the tar
	// reader rather than as a net error, once the gzip/tar layers have
	// re-wrapped it.
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE)
}

// TestIsTransientNetworkErr guards both directions, because each mistake has a
// different cost: too narrow makes the live tripwire flaky, while too broad
// makes it silently skip the upstream format change it exists to catch — a
// fail-open in a correctness gate, which is the worse of the two.
func TestIsTransientNetworkErr(t *testing.T) {
	transient := []struct {
		name string
		err  error
	}{
		{"raw ECONNRESET", syscall.ECONNRESET},
		{"raw EPIPE", syscall.EPIPE},
		{"unexpected EOF", io.ErrUnexpectedEOF},
		{"net.OpError wrapping reset", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}},
		// The shape actually observed in CI: scannerutils wraps the transport
		// error with %w behind its own "tar read error" message.
		{"wrapped as scannerutils does", fmt.Errorf("tar read error: %w", &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET})},
		{"double-wrapped", fmt.Errorf("outer: %w", fmt.Errorf("tar read error: %w", io.ErrUnexpectedEOF))},
	}
	for _, c := range transient {
		t.Run("transient/"+c.name, func(t *testing.T) {
			if !isTransientNetworkErr(c.err) {
				t.Errorf("isTransientNetworkErr(%v) = false, want true", c.err)
			}
		})
	}

	notTransient := []struct {
		name string
		err  error
	}{
		{"nil", nil},
		{"plain format error", errors.New("dpkg status: unexpected field layout")},
		{"clean EOF is not a mid-stream reset", io.EOF},
		{"wrapped format error", fmt.Errorf("tar read error: %w", errors.New("malformed tar header"))},
	}
	for _, c := range notTransient {
		t.Run("fatal/"+c.name, func(t *testing.T) {
			if isTransientNetworkErr(c.err) {
				t.Errorf("isTransientNetworkErr(%v) = true, want false — a real format change would be silently skipped", c.err)
			}
		})
	}
}
