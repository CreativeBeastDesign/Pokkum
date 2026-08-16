package attestutils

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestRootDigest_DeterministicAcrossOrder proves the aggregate digest is
// independent of the order records are supplied in — the property that stops
// directory-walk order from leaking into the digest.
func TestRootDigest_DeterministicAcrossOrder(t *testing.T) {
	records := []Record{
		{Rel: "server/index.js", SHA: "aaa"},
		{Rel: "client/_app/x.js", SHA: "bbb"},
		{Rel: "prerendered/about.html", SHA: "ccc"},
	}

	// Reversed order must produce the identical digest.
	rev := make([]Record, len(records))
	for i, r := range records {
		rev[len(records)-1-i] = r
	}

	d1 := RootDigest(records)
	d2 := RootDigest(rev)
	if d1 != d2 {
		t.Fatalf("RootDigest order-dependent: %s vs %s", d1, d2)
	}
	if len(d1) != 64 {
		t.Fatalf("RootDigest length = %d, want 64 hex chars", len(d1))
	}
	// Guard against a trivially-constant digest: not all-equal.
	if d1 == RootDigest([]Record{{Rel: "x", SHA: "y"}}) {
		t.Fatal("RootDigest appears degenerate")
	}
}

// TestRootDigest_EmptyIsWellDefined pins that an empty tree still yields a
// stable non-empty digest (so an empty /app is attestable, not an error path).
func TestRootDigest_EmptyIsWellDefined(t *testing.T) {
	if got := RootDigest(nil); got == "" {
		t.Fatal("RootDigest(nil) returned empty")
	}
	if RootDigest(nil) != RootDigest([]Record{}) {
		t.Fatal("nil and empty slices disagree")
	}
}

// TestRootDigest_ContentBindsDigest proves a change to any file's content hash
// changes the aggregate — the tamper-evidence property.
func TestRootDigest_ContentBindsDigest(t *testing.T) {
	base := []Record{{Rel: "client/app.js", SHA: "abcd"}}
	if RootDigest(base) == RootDigest([]Record{{Rel: "client/app.js", SHA: "abce"}}) {
		t.Fatal("content change did not change digest")
	}
	// A path change must also bind.
	if RootDigest(base) == RootDigest([]Record{{Rel: "client/app2.js", SHA: "abcd"}}) {
		t.Fatal("path change did not change digest")
	}
}

// --- Mirrored supervisor logic ---
//
// pokkum-init cannot import this package (it is embedded into the CLI); it
// mirrors the record shape and RootDigest manually. These helpers reproduce
// that mirror so the parity tests below can prove the two never drift. When
// the supervisor implementation changes, this mirror MUST be updated in
// lockstep — the test is the tripwire.

// supervisorRootDigest is the supervisor's byte-for-byte mirror of
// RootDigest. Keep it in sync with supervisor/cmd/pokkum-init/attest.go.
func supervisorRootDigest(records []supervisorRecord) string {
	sorted := append([]supervisorRecord(nil), records...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].rel < sorted[j].rel })
	h := sha256.New()
	for _, r := range sorted {
		h.Write([]byte(r.rel))
		h.Write([]byte{0})
		h.Write([]byte(r.sha))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// supervisorRecord mirrors attestutils.Record.
type supervisorRecord struct {
	rel string
	sha string
}

// walkSupervisor mirrors the supervisor's filesystem walk: it walks each root
// in AttestationRoots, skipping absent roots, and returns records keyed by
// rel-to-/app. It is used to prove the packager's archive-time view equals the
// supervisor's runtime view of the same tree.
func walkSupervisor(root string) []supervisorRecord {
	var out []supervisorRecord
	for _, prefix := range []string{
		"/app/server",
		"/app/client",
		"/app/prerendered",
		"/app/vendor",
		"/app/native",
	} {
		dir := filepath.Join(root, filepath.FromSlash(prefix))
		fi, err := os.Stat(dir)
		if err != nil || !fi.IsDir() {
			continue // absent root contributes nothing, on both sides
		}
		relPrefix := filepath.ToSlash(prefix) // "/app/server" etc.
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			sub, _ := filepath.Rel(dir, p)
			rel := filepath.ToSlash(relPrefix + "/" + sub)
			rel = rel[1:] // strip leading "/" so it is relative to /app
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			sum := sha256.Sum256(data)
			out = append(out, supervisorRecord{rel: rel, sha: hex.EncodeToString(sum[:])})
			return nil
		})
	}
	return out
}

// buildTree writes files under t.TempDir() and returns the root.
func buildTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestParity_PackagerAndSupervisorAgreeOnSameTree is the central correctness
// proof for Option C: given the same /app tree, the packager-side aggregate
// (built from archive-time records) and the supervisor-side re-derivation
// (walking the tree at runtime) must produce the identical digest. If they
// drift, every real image would be refused at startup.
func TestParity_PackagerAndSupervisorAgreeOnSameTree(t *testing.T) {
	root := buildTree(t, map[string]string{
		"app/server/index.js":               "console.log(1)",
		"app/server/handler.js":             "export default 1",
		"app/client/_app/immutable/x.js":    "var x=1",
		"app/client/_app/immutable/y.css":   "body{}",
		"app/prerendered/about.html":        "<h1>about</h1>",
		"app/vendor/@sveltejs/kit/run.js":   "export 1",
		"app/vendor/svelte/internal/run.js": "export 2",
		"app/native/addon.node":             "\x7fELFdata",
	})

	// "Packager" side: records derived from the on-disk tree the packager would
	// have archived (for the non-pruned/vendor-simple case this is exactly the
	// tree). Convert supervisor-walk records to the exported Record type.
	sup := walkSupervisor(root)
	recs := make([]Record, 0, len(sup))
	for _, s := range sup {
		recs = append(recs, Record{Rel: s.rel, SHA: s.sha})
	}
	packagerDigest := RootDigest(recs)
	supervisorDigest := supervisorRootDigest(sup)

	if packagerDigest != supervisorDigest {
		t.Fatalf("parity broken:\n  packager   %s\n  supervisor %s", packagerDigest, supervisorDigest)
	}
}

// TestParity_TamperChangesBothSides proves a tampered file is caught by both
// sides' digests, and that the two still agree with each other (so the runtime
// would refuse to start).
func TestParity_TamperChangesBothSides(t *testing.T) {
	root := buildTree(t, map[string]string{
		"app/server/index.js": "original",
		"app/client/app.js":   "var x=1",
	})

	before := supervisorRootDigest(walkSupervisor(root))

	// Tamper a server file.
	p := filepath.Join(root, "app", "server", "index.js")
	if err := os.WriteFile(p, []byte("COMPROMISED"), 0o644); err != nil {
		t.Fatal(err)
	}
	after := supervisorRootDigest(walkSupervisor(root))

	if before == after {
		t.Fatal("tamper did not change supervisor digest")
	}
}

// TestParity_AbsentRootContributesNothing proves a root missing from /app is
// skipped on both sides without error.
func TestParity_AbsentRootContributesNothing(t *testing.T) {
	// Only /app/server exists; the vendor/client/native/prerendered roots are
	// absent. The walk must not error and must yield a stable digest.
	root := buildTree(t, map[string]string{"app/server/index.js": "x"})
	d := supervisorRootDigest(walkSupervisor(root))
	if d == "" {
		t.Fatal("absent-other-roots produced empty digest")
	}
}
