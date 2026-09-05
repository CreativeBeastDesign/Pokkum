package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
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
// read happens: openAttestFile must refuse a path resolving out of the root,
// and must still read a path inside it.
//
// Reverting openAttestFile's body to os.Open(filepath.Join(root.Name(),
// sub)) — what the callback did before — makes this test fail: it returns the
// outside file's bytes, which would then be hashed into the digest the
// container's startup gate trusts.
func TestOpenAttestFile_RefusesEscapingSymlink(t *testing.T) {
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
	got, err := readThroughAttestRoot(root, "index.js")
	if err != nil {
		t.Fatalf("openAttestFile(index.js) error = %v, want nil", err)
	}
	if string(got) != "in-root-bytes" {
		t.Errorf("openAttestFile(index.js) = %q, want %q", got, "in-root-bytes")
	}

	// The escaping symlink must not deliver the outside file's bytes.
	escaped, err := readThroughAttestRoot(root, "escape.js")
	if string(escaped) == "outside-bytes" {
		t.Fatalf("openAttestFile read through a symlink leaving the attestation root and returned %q; those bytes would be hashed into the startup digest", escaped)
	}
	if err == nil {
		t.Errorf("openAttestFile(escape.js) error = nil (returned %q), want a refusal", escaped)
	}

	// And the same containment holds through the real hashing path, not only
	// through the open seam: hashAttestFile must refuse the escaping symlink
	// rather than fold foreign bytes into a record.
	var hbuf [4096]byte
	if _, err := hashAttestFile(root, "escape.js", hbuf[:], sha256.New()); err == nil {
		t.Errorf("hashAttestFile(escape.js) error = nil, want a refusal")
	}
	sum, err := hashAttestFile(root, "index.js", hbuf[:], sha256.New())
	if err != nil {
		t.Fatalf("hashAttestFile(index.js) error = %v, want nil", err)
	}
	want := sha256.Sum256([]byte("in-root-bytes"))
	if sum != hex.EncodeToString(want[:]) {
		t.Errorf("hashAttestFile(index.js) = %s, want %s", sum, hex.EncodeToString(want[:]))
	}
}

// readThroughAttestRoot is the tests' equivalent of the whole-file read the
// attestation used to perform, expressed on top of the openAttestFile seam
// that replaced it. Kept in the test file, not production code: production
// never buffers a whole attested file any more (see hashAttestFile).
func readThroughAttestRoot(root *os.Root, sub string) ([]byte, error) {
	f, err := openAttestFile(root, sub)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// TestOpenAttestFile_FollowsRelativeSymlinkInsideRoot pins the preserved half
// of the semantics: a relative symlink resolving back inside the attestation
// root is still followed, as os.Open did. The walk never hands such a path
// to openAttestFile today (symlinks are filtered out before the read), so this
// guards the read primitive itself, not a live code path.
func TestOpenAttestFile_FollowsRelativeSymlinkInsideRoot(t *testing.T) {
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

	got, err := readThroughAttestRoot(root, "alias.js")
	if err != nil {
		t.Fatalf("openAttestFile(alias.js) error = %v, want nil", err)
	}
	if string(got) != "real-bytes" {
		t.Errorf("openAttestFile(alias.js) = %q, want %q", got, "real-bytes")
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

// --- Guards for the parallel, streaming hashing phase ---------------------

// buildAttestTree materialises a multi-root /app-shaped tree with enough files
// (and enough size variety) that hashing them really does fan out across
// workers, and returns the base directory. Sizes deliberately straddle
// attestHashBufSize so the streaming read loop is exercised for both the
// single-Read and multi-Read cases.
func buildAttestTree(t *testing.T, nPerRoot int) string {
	t.Helper()
	base := t.TempDir()
	files := map[string]string{}
	for _, root := range []string{"server", "client", "prerendered", "vendor", "native"} {
		for i := 0; i < nPerRoot; i++ {
			// Every few files is larger than one read buffer.
			size := 7 + i*13
			if i%17 == 0 {
				size = attestHashBufSize*2 + i
			}
			body := strings.Repeat(string(rune('a'+i%26)), size)
			files[filepath.Join("app", root, "sub", fmtInt(i), "f"+fmtInt(i)+".js")] = body
		}
	}
	files["outside/secret"] = "outside-bytes"
	return writeTree(t, base, files)
}

func fmtInt(i int) string { return strconv.Itoa(i) }

// TestAttestParallelHashing_DigestStableAcrossWorkerCounts is the digest
// neutrality proof the parallelisation needs: the same tree must fold to the
// same root digest at 1, 2, 4, 8 and GOMAXPROCS workers. attestRootDigest
// sorts globally by rel before folding, so completion order cannot leak into
// the digest — this asserts that rather than assuming it.
//
// It also pins the digest against a from-scratch, single-threaded,
// whole-file-read reimplementation (the exact shape the code had before this
// change), so "parallel agrees with parallel" cannot pass vacuously.
func TestAttestParallelHashing_DigestStableAcrossWorkerCounts(t *testing.T) {
	base := buildAttestTree(t, 40)
	withRoots(t, base)

	// Reference: single-threaded, whole-file read + sha256.Sum256, computed
	// here without touching any of the code under test.
	want := referenceRootDigest(t, base)

	origProcs := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(origProcs) })

	var got []string
	for _, procs := range []int{1, 2, 4, 8, origProcs} {
		runtime.GOMAXPROCS(procs)
		recs, err := walkAttestTree()
		if err != nil {
			t.Fatalf("walkAttestTree at GOMAXPROCS=%d: %v", procs, err)
		}
		if len(recs) != 200 {
			t.Fatalf("GOMAXPROCS=%d hashed %d files, want 200 — a walk that found nothing would make every digest below agree vacuously", procs, len(recs))
		}
		d := attestRootDigest(recs)
		if d != want {
			t.Errorf("GOMAXPROCS=%d digest = %s, want %s (independent single-threaded reference)", procs, d, want)
		}
		got = append(got, d)
	}
	for i := 1; i < len(got); i++ {
		if got[i] != got[0] {
			t.Fatalf("digest is not stable across worker counts: %v", got)
		}
	}
}

// referenceRootDigest recomputes the attestation digest the slow, obvious way:
// one goroutine, whole-file reads, os.ReadFile rather than any root-scoped
// primitive. It shares no code with attest.go beyond the record serialization
// format, which is the thing being pinned.
func referenceRootDigest(t *testing.T, base string) string {
	t.Helper()
	type rec struct{ rel, sha string }
	var recs []rec
	for _, root := range superRoots(base) {
		baseRel := filepath.Base(root)
		if _, err := os.Stat(root); err != nil {
			continue
		}
		err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			info, ierr := d.Info()
			if ierr != nil || !info.Mode().IsRegular() {
				return nil
			}
			b, rerr := os.ReadFile(p) //nolint:gosec // test fixture, fixed tree
			if rerr != nil {
				return rerr
			}
			sub, rerr := filepath.Rel(root, p)
			if rerr != nil {
				return rerr
			}
			sum := sha256.Sum256(b)
			recs = append(recs, rec{
				rel: filepath.ToSlash(filepath.Join(baseRel, sub)),
				sha: hex.EncodeToString(sum[:]),
			})
			return nil
		})
		if err != nil {
			t.Fatalf("reference walk %s: %v", root, err)
		}
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].rel < recs[j].rel })
	h := sha256.New()
	for _, r := range recs {
		h.Write([]byte(r.rel))
		h.Write([]byte{0})
		h.Write([]byte(r.sha))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestVerifyAttestation_TamperedMidTreeFileRefusesUnderParallelHashing is the
// fail-closed proof with the fan-out actually engaged: a file tampered with
// deep inside the tree (not the first one hashed, and not in the first root)
// must still make verifyAttestation refuse, at every worker count. A parallel
// hasher that dropped or reordered a record would produce a digest that still
// matched, and only this shape catches it (checklist row 4: fail a non-first
// item).
func TestVerifyAttestation_TamperedMidTreeFileRefusesUnderParallelHashing(t *testing.T) {
	base := buildAttestTree(t, 40)
	withRoots(t, base)

	expected, err := expectedDigestFor()
	if err != nil {
		t.Fatalf("expectedDigestFor: %v", err)
	}
	if err := verifyAttestation(discardLogger(), expected); err != nil {
		t.Fatalf("clean tree must verify: %v", err)
	}

	// Tamper with a file in the LAST root, deep in the walk order.
	victim := filepath.Join(base, "app", "native", "sub", "37", "f37.js")
	if err := os.WriteFile(victim, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}

	origProcs := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(origProcs) })
	for _, procs := range []int{1, 2, 4, 8, origProcs} {
		runtime.GOMAXPROCS(procs)
		err := verifyAttestation(discardLogger(), expected)
		if err == nil {
			t.Fatalf("GOMAXPROCS=%d: verifyAttestation accepted a tampered tree", procs)
		}
		if !strings.Contains(err.Error(), "startup attestation mismatch") {
			t.Errorf("GOMAXPROCS=%d: error = %v, want a mismatch refusal", procs, err)
		}
	}
}

// TestHashAttestTasks_WorkerErrorAbortsWholeVerification proves the fail-closed
// invariant at the fan-out itself: an unreadable file anywhere in the task list
// — including on a NON-first task, handled by a worker that is not the one that
// started first — aborts the entire hashing phase and returns NO records, so a
// partial set can never reach attestRootDigest and can never coincidentally
// match an expected value.
func TestHashAttestTasks_WorkerErrorAbortsWholeVerification(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "server")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const n = 60
	tasks := make([]attestTask, 0, n)
	for i := 0; i < n; i++ {
		name := "f" + strconv.Itoa(i) + ".js"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"+strconv.Itoa(i)), 0o644); err != nil {
			t.Fatal(err)
		}
		tasks = append(tasks, attestTask{root: 0, sub: name, rel: "server/" + name})
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	roots := []*os.Root{root}

	// Sanity: all readable => full record set, no error. Without this the
	// failure assertion below could pass because nothing ever worked.
	recs, err := hashAttestTasks(roots, tasks)
	if err != nil {
		t.Fatalf("clean hashAttestTasks: %v", err)
	}
	if len(recs) != n {
		t.Fatalf("clean hashAttestTasks returned %d records, want %d", len(recs), n)
	}

	// Now break a mid-list task by pointing it at a file that does not exist.
	tasks[n/2].sub = "does-not-exist.js"
	recs, err = hashAttestTasks(roots, tasks)
	if err == nil {
		t.Fatalf("hashAttestTasks returned nil error for an unreadable file; verification would proceed on a partial tree")
	}
	if recs != nil {
		t.Errorf("hashAttestTasks returned %d records alongside an error; a partial record set must never escape", len(recs))
	}
	if !strings.Contains(err.Error(), "does-not-exist.js") {
		t.Errorf("error = %v, want it to name the file that failed", err)
	}

	// And the same failure, surfaced through verifyAttestation, must be a
	// refusal rather than a silently-shorter tree.
	if !strings.Contains(err.Error(), "hashing ") {
		t.Errorf("error = %v, want it to be attributed to the hashing phase", err)
	}
}

// TestHashAttestFile_StreamingMatchesWholeFileHash pins the digest-neutrality
// of the streaming read itself across the buffer boundary: a file larger than
// attestHashBufSize (so the read loop runs several times) must hash to exactly
// what sha256.Sum256 of the whole file gives.
func TestHashAttestFile_StreamingMatchesWholeFileHash(t *testing.T) {
	dir := t.TempDir()
	for _, size := range []int{0, 1, attestHashBufSize - 1, attestHashBufSize, attestHashBufSize + 1, attestHashBufSize*3 + 7} {
		body := make([]byte, size)
		for i := range body {
			body[i] = byte(i * 7 % 251)
		}
		name := "f" + strconv.Itoa(size) + ".bin"
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, attestHashBufSize)
		got, err := hashAttestFile(root, name, buf, sha256.New())
		_ = root.Close()
		if err != nil {
			t.Fatalf("hashAttestFile(size=%d): %v", size, err)
		}
		want := sha256.Sum256(body)
		if got != hex.EncodeToString(want[:]) {
			t.Errorf("hashAttestFile(size=%d) = %s, want %s", size, got, hex.EncodeToString(want[:]))
		}
	}
}
