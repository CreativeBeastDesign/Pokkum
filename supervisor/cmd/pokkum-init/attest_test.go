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

// TestReadAttestFile_RefusesEscapingSymlink proves the attestation's file reads
// are contained to the attestation root structurally (gosec G122, roadmap item
// walk-callback-symlink-toctou).
//
// This is the direct containment seam. The walk itself cannot be made to
// exhibit the escape statically: filepath.WalkDir never follows symlinks, and
// walkAttestRoot's d.Info().Mode().IsRegular() filter drops every symlink the
// walk reports (proved by TestWalkAttestTree_SymlinkEntriesContributeNothing
// below), so a symlink sitting in the tree is never read on either the old or
// the new code. What os.Root closes is the window BETWEEN that lstat and the
// read, which is one syscall wide and not reproducible on demand
// (mem:self_review_checklist row 45: test the invariant the design maintains,
// not the absence of the interleaving). So the property is asserted where the
// read happens: readAttestFile must refuse a path resolving out of the root,
// and must still read a path inside it.
//
// Reverting readAttestFile's body to os.ReadFile(filepath.Join(root.Name(),
// sub)) — what the callback did before — makes this test fail: it returns the
// outside file's bytes, which would then be hashed into the digest the
// container's startup gate trusts.
func TestReadAttestFile_RefusesEscapingSymlink(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("outside-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	appRoot := filepath.Join(base, "app", "server")
	if err := os.MkdirAll(appRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appRoot, "index.js"), []byte("in-root-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(appRoot, "escape.js")); err != nil {
		t.Fatalf("creating the test symlink failed; this containment test must never be silently skipped (checklist rows 39/47): %v", err)
	}

	root, err := os.OpenRoot(appRoot)
	if err != nil {
		t.Fatalf("OpenRoot(%s): %v", appRoot, err)
	}
	defer func() { _ = root.Close() }()

	// A legitimate in-root file still reads, byte for byte.
	got, err := readAttestFile(root, "index.js")
	if err != nil {
		t.Fatalf("readAttestFile(index.js) error = %v, want nil", err)
	}
	if string(got) != "in-root-bytes" {
		t.Errorf("readAttestFile(index.js) = %q, want %q", got, "in-root-bytes")
	}

	// The escaping symlink must not deliver the outside file's bytes.
	escaped, err := readAttestFile(root, "escape.js")
	if string(escaped) == "outside-bytes" {
		t.Fatalf("readAttestFile read through a symlink leaving the attestation root and returned %q; those bytes would be hashed into the startup digest", escaped)
	}
	if err == nil {
		t.Errorf("readAttestFile(escape.js) error = nil (returned %q), want a refusal", escaped)
	}
}

// TestReadAttestFile_FollowsRelativeSymlinkInsideRoot pins the preserved half
// of the semantics: a relative symlink resolving back inside the attestation
// root is still followed, as os.ReadFile did. The walk never hands such a path
// to readAttestFile today (symlinks are filtered out before the read), so this
// guards the read primitive itself, not a live code path.
func TestReadAttestFile_FollowsRelativeSymlinkInsideRoot(t *testing.T) {
	appRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(appRoot, "real.js"), []byte("real-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.js", filepath.Join(appRoot, "alias.js")); err != nil {
		t.Fatalf("creating the test symlink failed; this containment test must never be silently skipped (checklist rows 39/47): %v", err)
	}
	root, err := os.OpenRoot(appRoot)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer func() { _ = root.Close() }()

	got, err := readAttestFile(root, "alias.js")
	if err != nil {
		t.Fatalf("readAttestFile(alias.js) error = %v, want nil", err)
	}
	if string(got) != "real-bytes" {
		t.Errorf("readAttestFile(alias.js) = %q, want %q", got, "real-bytes")
	}
}

// TestWalkAttestTree_SymlinkEntriesContributeNothing is the walk-level half of
// the containment story, and the reason the escape above is not statically
// reachable through walkAttestTree: a symlink sitting in an attestation root —
// escaping or not — is filtered out by the IsRegular check and contributes no
// record, so the digest is identical to the same tree without it.
//
// This is also the behaviour a container depends on: adding a symlink to /app
// must not make the attestation read foreign bytes, and it must not make the
// digest depend on anything but the regular files the packager archived.
func TestWalkAttestTree_SymlinkEntriesContributeNothing(t *testing.T) {
	// Baseline: regular files only.
	baseA := writeTree(t, t.TempDir(), map[string]string{
		"app/server/index.js":    "server",
		"app/client/_app/a.js":   "client",
		"app/prerendered/i.html": "<h1>i</h1>",
		"outside/secret":         "outside-bytes",
	})
	withRoots(t, baseA)
	baseline, err := expectedDigestFor()
	if err != nil {
		t.Fatalf("baseline digest: %v", err)
	}

	// Same tree plus two symlinks in an attested root: one escaping to a file
	// outside the app dir, one relative and resolving inside it.
	baseB := writeTree(t, t.TempDir(), map[string]string{
		"app/server/index.js":    "server",
		"app/client/_app/a.js":   "client",
		"app/prerendered/i.html": "<h1>i</h1>",
		"outside/secret":         "outside-bytes",
	})
	if err := os.Symlink(filepath.Join(baseB, "outside", "secret"), filepath.Join(baseB, "app", "server", "escape.js")); err != nil {
		t.Fatalf("creating the test symlink failed; this containment test must never be silently skipped (checklist rows 39/47): %v", err)
	}
	if err := os.Symlink("index.js", filepath.Join(baseB, "app", "server", "alias.js")); err != nil {
		t.Fatal(err)
	}
	withRoots(t, baseB)
	withSymlinks, err := expectedDigestFor()
	if err != nil {
		t.Fatalf("digest with symlinks: %v", err)
	}

	if withSymlinks != baseline {
		t.Errorf("symlink entries changed the attestation digest: baseline %s, with symlinks %s — the walk must hash only regular files", baseline, withSymlinks)
	}

	// And the walk must still cover the real files, so the comparison above is
	// not two empty digests agreeing with each other (row 47: "did nothing"
	// must be distinguishable from "found nothing").
	recs, err := walkAttestTree()
	if err != nil {
		t.Fatalf("walkAttestTree: %v", err)
	}
	if len(recs) != 3 {
		t.Fatalf("walkAttestTree() returned %d records, want 3 regular files: %+v", len(recs), recs)
	}
}
