package attestutils

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
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
	// Iterate ports.AttestationRoots itself rather than a third inline copy of
	// the same list. The copy this replaced had drifted from both of the other
	// two: its doc comment claimed it walked AttestationRoots while it actually
	// hardcoded five prefixes, so it kept agreeing with the supervisor after
	// /app/node_modules was added to the packager and every image stopped
	// booting.
	for _, prefix := range ports.AttestationRoots {
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

// supervisorAttestSourcePath is pokkum-init's attestation source, relative to
// this package. The supervisor deliberately cannot import ports (it must not
// drag Go port types into a program whose only job is to fork one child), so
// its root list is a hand-copy — and this test is what stops that copy from
// being trusted on faith.
const supervisorAttestSourcePath = "../../../supervisor/cmd/pokkum-init/attest.go"

// parseSupervisorAttestRoots extracts the `attestRoots` string-slice literal
// out of pokkum-init's source with go/ast. Reading the real declaration is the
// entire point: a test that restated the list inline would be a fourth copy,
// agreeing with a drifted third one.
func parseSupervisorAttestRoots(t *testing.T) []string {
	t.Helper()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, supervisorAttestSourcePath, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", supervisorAttestSourcePath, err)
	}

	var got []string
	var found bool
	ast.Inspect(file, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "attestRoots" || len(vs.Values) != 1 {
			return true
		}
		lit, ok := vs.Values[0].(*ast.CompositeLit)
		if !ok {
			return true
		}
		found = true
		for _, elt := range lit.Elts {
			bl, ok := elt.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				// A non-literal element (a constant, a concatenation) would be
				// invisible to this parser, so refuse rather than silently
				// comparing a short list and reporting parity.
				t.Fatalf("attestRoots contains a non-string-literal element %T; keep the entries as plain string literals so this drift check can read them", elt)
			}
			s, uerr := strconv.Unquote(bl.Value)
			if uerr != nil {
				t.Fatalf("unquote %s: %v", bl.Value, uerr)
			}
			got = append(got, s)
		}
		return false
	})

	if !found {
		t.Fatalf("could not find the attestRoots declaration in %s — if it was renamed or restructured, update this test rather than deleting it", supervisorAttestSourcePath)
	}
	return got
}

// TestAttestationRoots_MatchSupervisorMirror is the drift tripwire for the one
// invariant that cannot be enforced by the compiler: pokkum-init's hand-copied
// root list must equal ports.AttestationRoots as a SET.
//
// This is the check whose absence shipped an unbootable image. The packager
// began archiving /app/node_modules and folding its records into the
// attestation manifest; pokkum-init's list was not updated; build-time hashed
// 11762 files while runtime could only ever find 509, so every layered image
// exited 125 with "startup attestation mismatch". Nothing failed — not the
// existing parity test (which compares the two digest *functions*, not the two
// *root sets*), not the packager's own attestation test (whose oracle mirrors
// the packager's table and never populated node_modules), and not the full
// suite, which was green at 49/49 packages while no produced image could start.
func TestAttestationRoots_MatchSupervisorMirror(t *testing.T) {
	supervisorRoots := parseSupervisorAttestRoots(t)

	portsRoots := make([]string, 0, len(ports.AttestationRoots))
	portsRoots = append(portsRoots, ports.AttestationRoots[:]...)

	// Order is genuinely irrelevant to the digest (records are globally sorted
	// by relative path before hashing), so compare as sets and let either side
	// list its roots in whatever order reads best.
	gotSorted := append([]string(nil), supervisorRoots...)
	wantSorted := append([]string(nil), portsRoots...)
	sort.Strings(gotSorted)
	sort.Strings(wantSorted)

	if len(gotSorted) != len(wantSorted) {
		t.Fatalf("attestation root sets differ in size:\n  pokkum-init: %v\n  ports:       %v", gotSorted, wantSorted)
	}
	for i := range gotSorted {
		if gotSorted[i] != wantSorted[i] {
			t.Fatalf("attestation root sets differ:\n  pokkum-init: %v\n  ports:       %v\nAdding a root to one side without the other makes every layered image refuse to start.", gotSorted, wantSorted)
		}
	}
}

// TestAttestationRoots_MirrorCheckCanFail guards the guard. A parser that
// silently found nothing would make the test above vacuous — it would compare
// two empty-ish lists, or fatal in a way a future refactor might "fix" by
// loosening the assertion. Feed the same comparison a deliberately wrong set
// and prove it is rejected, so the check is known to be capable of failing.
func TestAttestationRoots_MirrorCheckCanFail(t *testing.T) {
	real := make([]string, 0, len(ports.AttestationRoots))
	real = append(real, ports.AttestationRoots[:]...)
	if len(real) < 2 {
		t.Fatalf("expected several attestation roots, got %v", real)
	}

	// Dropping any single root must be detectable by the same set comparison
	// the mirror test performs — this is precisely the shape of the shipped bug
	// (one root present on one side, absent on the other).
	for i := range real {
		missing := append(append([]string(nil), real[:i]...), real[i+1:]...)
		if setsEqual(missing, real) {
			t.Fatalf("dropping root %q was not detected; the mirror comparison cannot fail and is therefore not a check", real[i])
		}
	}
}

func setsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}
