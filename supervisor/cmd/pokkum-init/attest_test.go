package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// discardLogger returns a logger that writes nowhere, keeping test output quiet.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// superRoots returns the absolute attestation roots under base, mirroring the
// packager's root set but scoped to a test directory.
func superRoots(base string) []string {
	return []string{
		filepath.Join(base, "app", "server"),
		filepath.Join(base, "app", "client"),
		filepath.Join(base, "app", "prerendered"),
		filepath.Join(base, "app", "vendor"),
		filepath.Join(base, "app", "native"),
	}
}

// withRoots swaps the global attestAppDir/attestRoots to point at base for the
// duration of the test and restores them afterwards.
func withRoots(t *testing.T, base string) {
	t.Helper()
	oldDir := attestAppDir
	oldRoots := attestRoots
	attestAppDir = filepath.Join(base, "app")
	attestRoots = superRoots(base)
	t.Cleanup(func() {
		attestAppDir = oldDir
		attestRoots = oldRoots
	})
}

// writeTree writes files under base relative to base and returns base.
func writeTree(t *testing.T, base string, files map[string]string) string {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(base, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return base
}

// expectedDigestFor computes the digest the supervisor would derive from the
// current tree — the value a correctly-built image would have stamped.
func expectedDigestFor() (string, error) {
	recs, err := walkAttestTree()
	if err != nil {
		return "", err
	}
	return attestRootDigest(recs), nil
}

func TestVerifyAttestation_DisabledWhenNoExpected(t *testing.T) {
	base := writeTree(t, t.TempDir(), map[string]string{
		"app/server/index.js": "x",
	})
	withRoots(t, base)
	// An empty expected digest (attestation off) must never refuse to start,
	// regardless of what /app contains.
	if err := verifyAttestation(discardLogger(), ""); err != nil {
		t.Fatalf("empty expected should disable attestation, got error: %v", err)
	}
}

func TestVerifyAttestation_MatchingTreeSucceeds(t *testing.T) {
	base := writeTree(t, t.TempDir(), map[string]string{
		"app/server/index.js":             "console.log(1)",
		"app/server/handler.js":           "export default 1",
		"app/client/_app/immutable/x.js":  "var x=1",
		"app/client/_app/immutable/y.css": "body{}",
		"app/prerendered/about.html":      "<h1>about</h1>",
		"app/vendor/@sveltejs/kit/a.js":   "export 1",
		"app/native/addon.node":           "\x7fELFdata",
	})
	withRoots(t, base)

	expected, err := expectedDigestFor()
	if err != nil {
		t.Fatalf("expectedDigestFor: %v", err)
	}
	if expected == "" {
		t.Fatal("expected digest should not be empty for a populated tree")
	}
	if err := verifyAttestation(discardLogger(), expected); err != nil {
		t.Fatalf("matching tree refused to start: %v", err)
	}
}

func TestVerifyAttestation_TamperedFileRefuses(t *testing.T) {
	base := writeTree(t, t.TempDir(), map[string]string{
		"app/server/index.js": "original",
		"app/client/app.js":   "var x=1",
	})
	withRoots(t, base)

	expected, err := expectedDigestFor()
	if err != nil {
		t.Fatal(err)
	}

	// Tamper a server file after the digests were computed — simulates an
	// injection into /app after the image was built.
	if err := os.WriteFile(filepath.Join(base, "app", "server", "index.js"), []byte("COMPROMISED"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyAttestation(discardLogger(), expected); err == nil {
		t.Fatal("tampered /app tree was not refused")
	}
}

func TestVerifyAttestation_AddedFileRefuses(t *testing.T) {
	base := writeTree(t, t.TempDir(), map[string]string{
		"app/server/index.js": "x",
	})
	withRoots(t, base)

	expected, err := expectedDigestFor()
	if err != nil {
		t.Fatal(err)
	}

	// Injecting an entirely new file must also be caught.
	if err := os.MkdirAll(filepath.Join(base, "app", "client"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "app", "client", "injected.js"), []byte("evil"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyAttestation(discardLogger(), expected); err == nil {
		t.Fatal("added file in /app was not refused")
	}
}

func TestVerifyAttestation_MissingAppDirRefuses(t *testing.T) {
	// No app dir written at all.
	base := t.TempDir()
	withRoots(t, base)
	expected, err := expectedDigestFor()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyAttestation(discardLogger(), expected); err == nil {
		t.Fatal("expected a refusal when /app is missing but a digest was stamped")
	} else if !strings.Contains(err.Error(), attestAppDir) {
		t.Fatalf("error %q should name the missing dir %s", err, attestAppDir)
	}
}

func TestVerifyAttestation_AbsentOptionalRootsOK(t *testing.T) {
	// Only /app/server exists; the other roots were never packaged. The digest
	// must be computed over what exists and verification must succeed against
	// that same digest.
	base := writeTree(t, t.TempDir(), map[string]string{"app/server/index.js": "x"})
	withRoots(t, base)
	expected, err := expectedDigestFor()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyAttestation(discardLogger(), expected); err != nil {
		t.Fatalf("absent optional roots should verify: %v", err)
	}
}

func TestIsHexDigest(t *testing.T) {
	good := strings.Repeat("ab", 32) // 64 hex chars
	cases := []struct {
		in   string
		want bool
	}{
		{good, true},
		{strings.Repeat("a", 64), true},
		{strings.Repeat("A", 64), false}, // uppercase rejected
		{good + "0", false},              // too long
		{good[:63], false},               // too short
		{"", false},
		{"g" + strings.Repeat("a", 63), false}, // non-hex char
	}
	for _, c := range cases {
		if got := isHexDigest(c.in); got != c.want {
			t.Errorf("isHexDigest(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestAttestRootDigest_OrderIndependent(t *testing.T) {
	a := attestRootDigest([]attestRecord{{rel: "a/x", sha: "1"}, {rel: "b/y", sha: "2"}})
	b := attestRootDigest([]attestRecord{{rel: "b/y", sha: "2"}, {rel: "a/x", sha: "1"}})
	if a != b {
		t.Fatal("attestRootDigest is order-dependent")
	}
	if a == "" || len(a) != 64 {
		t.Fatalf("unexpected digest %q", a)
	}
}
