package remotecacheutils_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"sync"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/remotecacheutils"
)

// goldenSourceTreeHash is the SHA-256 of the fixture tree built by
// writeGoldenTree, under the wire format ComputeSourceTreeHash has always
// produced: for every path in ascending sorted order,
//
//	path "\x00" kind "\x00" contentDigestHex "\x00"
//
// This constant is the contract with every remote cache tag that already
// exists in the wild. A change to it means every `cache-<hash>` tag ever
// pushed has been silently invalidated and every project's first build after
// upgrading is a full miss. It must therefore only ever change as a
// deliberate, announced cache-format change — never as a side effect of
// reordering, parallelising, or "tidying" the hash computation.
//
// It is cross-checked below against an independent reimplementation of the
// framing (expectedTreeHash), so this value is not merely whatever the
// implementation happened to print on the day it was written.
const goldenSourceTreeHash = "66514b5597617260a5395598be66728af47b36730be1359f5f74d04b4bf01095"

// goldenEntry is one file in the fixture tree.
type goldenEntry struct {
	rel     string // slash-separated, relative to the project root
	content string
	mode    os.FileMode
	symlink string // when non-empty, rel is a symlink to this target
}

// goldenTree is deliberately unsorted here: the fixture exercises the sort
// inside ComputeSourceTreeHash rather than assuming it. It covers all three
// entry kinds hashTreeEntry distinguishes ("f", "x", "l"), several directory
// depths, a byte-identical pair of files at different paths (so a fold that
// dropped the path would still be caught), one file large enough to take
// visibly longer to hash than its neighbours (so a scheduler that folded
// results in completion order rather than sorted order would reorder), and
// entries that must be skipped by both skip paths.
var goldenTree = []goldenEntry{
	{rel: "package.json", content: `{"name":"pokkum-fixture","type":"module"}`, mode: 0o644},
	{rel: "src/routes/+page.svelte", content: "<h1>hello</h1>\n", mode: 0o644},
	{rel: "src/routes/about/+page.svelte", content: "<h1>hello</h1>\n", mode: 0o644},
	{rel: "src/lib/server/db.ts", content: "export const db = null;\n", mode: 0o644},
	{rel: "src/lib/big.ts", content: bigContent(), mode: 0o644},
	{rel: "scripts/build.sh", content: "#!/bin/sh\nexit 0\n", mode: 0o755},
	{rel: "static/robots.txt", content: "User-agent: *\n", mode: 0o644},
	{rel: "src/lib/link.ts", symlink: "./server/db.ts"},

	// Skipped by IgnoredBuildDirs: the whole subtree is abandoned at the
	// directory and never contributes.
	{rel: "node_modules/left-pad/index.js", content: "module.exports = 1;\n", mode: 0o644},
	{rel: ".svelte-kit/output/server/index.js", content: "generated\n", mode: 0o644},
	// Skipped per-file by the ignore matcher's default patterns.
	{rel: "static/app.js.map", content: `{"version":3}`, mode: 0o644},
}

func bigContent() string {
	b := make([]byte, 1<<20)
	for i := range b {
		b[i] = byte('a' + i%26)
	}
	return string(b)
}

// writeGoldenTree materialises goldenTree under root and returns the entries
// that ComputeSourceTreeHash is expected to actually hash, in no particular
// order.
//
// Modes are applied with an explicit Chmod rather than relying on the perm
// argument of os.WriteFile, which the process umask masks: under a umask that
// clears the owner-execute bit, "scripts/build.sh" would silently be hashed
// as kind "f" instead of "x" and the golden would fail for a reason that has
// nothing to do with the code under test.
func writeGoldenTree(t *testing.T, root string) []goldenEntry {
	t.Helper()

	skipped := map[string]bool{
		"node_modules/left-pad/index.js":     true,
		".svelte-kit/output/server/index.js": true,
		"static/app.js.map":                  true,
	}

	var hashed []goldenEntry
	for _, e := range goldenTree {
		full := filepath.Join(root, filepath.FromSlash(e.rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if e.symlink != "" {
			if err := os.Symlink(e.symlink, full); err != nil {
				t.Fatalf("symlink %s: %v", e.rel, err)
			}
		} else {
			if err := os.WriteFile(full, []byte(e.content), e.mode); err != nil {
				t.Fatalf("write %s: %v", e.rel, err)
			}
			if err := os.Chmod(full, e.mode); err != nil {
				t.Fatalf("chmod %s: %v", e.rel, err)
			}
		}
		if !skipped[e.rel] {
			hashed = append(hashed, e)
		}
	}
	return hashed
}

// expectedTreeHash recomputes the tree hash from the fixture *specification*
// rather than from the filesystem or from the implementation, so that the
// golden constant is anchored to the documented wire format and not to
// whatever ComputeSourceTreeHash currently does.
func expectedTreeHash(entries []goldenEntry) string {
	sorted := slices.Clone(entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].rel < sorted[j].rel })

	h := sha256.New()
	for _, e := range sorted {
		var (
			kind string
			sum  [32]byte
		)
		switch {
		case e.symlink != "":
			kind = "l"
			sum = sha256.Sum256([]byte(e.symlink))
		case e.mode&0o111 != 0:
			kind = "x"
			sum = sha256.Sum256([]byte(e.content))
		default:
			kind = "f"
			sum = sha256.Sum256([]byte(e.content))
		}
		h.Write([]byte(e.rel))
		h.Write([]byte{0})
		h.Write([]byte(kind))
		h.Write([]byte{0})
		h.Write([]byte(hex.EncodeToString(sum[:])))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestComputeSourceTreeHash_Golden pins the source tree hash of a fixed
// fixture tree.
//
// It is the guard for parallelising the per-entry digest computation: only the
// *computation* of each entry's digest may run concurrently, while the fold
// must stay byte-identical and in ascending sorted path order. A fold that
// ran in completion order, or that dropped the path or kind framing, produces
// a different digest here and fails.
//
// The golden is checked twice over — against an independent reimplementation
// of the framing, and against a hard-coded constant — so that neither a
// drifting implementation nor a drifting test can quietly move it.
func TestComputeSourceTreeHash_Golden(t *testing.T) {
	root := t.TempDir()
	hashed := writeGoldenTree(t, root)

	want := expectedTreeHash(hashed)
	if want != goldenSourceTreeHash {
		t.Fatalf("independently computed fixture hash %s does not match the pinned golden %s;\n"+
			"if this is a deliberate cache-format change, every existing cache-<hash> tag in the wild is invalidated by it",
			want, goldenSourceTreeHash)
	}

	got, err := remotecacheutils.ComputeSourceTreeHash(root)
	if err != nil {
		t.Fatalf("ComputeSourceTreeHash: %v", err)
	}
	if got != goldenSourceTreeHash {
		t.Fatalf("source tree hash changed: got %s, want %s\n"+
			"the NUL-framed fold (path\\0kind\\0digest\\0, ascending sorted path order) is the remote cache key format; "+
			"changing it silently invalidates every remote cache tag that exists",
			got, goldenSourceTreeHash)
	}
}

// TestComputeSourceTreeHash_ConcurrencyInvariant runs the hash at several
// GOMAXPROCS levels — including 1, which takes the serial path — and asserts
// every one produces the pinned golden.
//
// Running the same tree at several widths is what actually exercises the
// hazard: with one worker there is no interleaving at all, and with more
// workers than files the large entry in the fixture finishes last, so a fold
// that consumed results in completion order rather than by index would
// disagree with the serial run.
func TestComputeSourceTreeHash_ConcurrencyInvariant(t *testing.T) {
	root := t.TempDir()
	writeGoldenTree(t, root)

	original := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(original) })

	for _, procs := range []int{1, 2, 3, 4, 8, 16} {
		runtime.GOMAXPROCS(procs)
		for rep := 0; rep < 5; rep++ {
			got, err := remotecacheutils.ComputeSourceTreeHash(root)
			if err != nil {
				t.Fatalf("GOMAXPROCS=%d: ComputeSourceTreeHash: %v", procs, err)
			}
			if got != goldenSourceTreeHash {
				t.Fatalf("GOMAXPROCS=%d rep %d: hash %s != golden %s — the per-entry digests are being folded in a concurrency-dependent order",
					procs, rep, got, goldenSourceTreeHash)
			}
		}
	}
}

// TestComputeSourceTreeHash_ConcurrentCallers hashes the same tree from many
// goroutines at once. The per-entry work now runs on shared machinery
// (poolutils' buffer pool, one result slice per call); this asserts nothing
// leaks between concurrent calls, and gives -race something to inspect.
func TestComputeSourceTreeHash_ConcurrentCallers(t *testing.T) {
	root := t.TempDir()
	writeGoldenTree(t, root)

	const callers = 16
	results := make([]string, callers)
	errs := make([]error, callers)

	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			results[i], errs[i] = remotecacheutils.ComputeSourceTreeHash(root)
		}()
	}
	wg.Wait()

	for i := range results {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if results[i] != goldenSourceTreeHash {
			t.Fatalf("caller %d: hash %s != golden %s", i, results[i], goldenSourceTreeHash)
		}
	}
}

// TestComputeSourceTreeHash_UnreadableFileIsReportedDeterministically pins the
// error-reporting behaviour the parallel path has to preserve: with more than
// one unreadable entry, the error named is the one belonging to the
// lowest-indexed (first in sorted order) failure, exactly as the serial loop
// reported. Whichever worker happened to fail first must not decide it.
func TestComputeSourceTreeHash_UnreadableFileIsReportedDeterministically(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 000 does not make a file unreadable")
	}

	root := t.TempDir()
	for _, rel := range []string{"a-first.ts", "z-last.ts"} {
		full := filepath.Join(root, rel)
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		if err := os.Chmod(full, 0o000); err != nil {
			t.Fatalf("chmod %s: %v", rel, err)
		}
		t.Cleanup(func() { _ = os.Chmod(full, 0o644) })
	}

	for i := 0; i < 20; i++ {
		_, err := remotecacheutils.ComputeSourceTreeHash(root)
		if err == nil {
			t.Fatal("expected an error for an unreadable file, got nil")
		}
		if want := `"a-first.ts"`; !contains(err.Error(), want) {
			t.Fatalf("attempt %d: error names the wrong file: %v (want the first in sorted order, %s)", i, err, want)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
