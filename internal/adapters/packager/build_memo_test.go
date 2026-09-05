package packager

// Guards for the per-build memos: the tree-layer memo (arch-independent trees
// are built once per build, not once per platform) and the strip memo (a host
// directory is rewritten in place at most once per build, before any platform
// tars it).
//
// Both are correctness guards as much as performance ones. The tree-layer memo
// must hand every platform the SAME, CORRECT layer, PruneResult and
// attestation records; the strip memo is what keeps an in-place rewrite from
// landing in the middle of another platform's tar walk.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/attestutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/pruneutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

var memoEpoch = time.Unix(1700000000, 0).UTC()

// writeMemoTree materialises a small vendor-shaped tree with a mix of files
// that survive pruning and files that do not, so the memoised PruneResult and
// record set are both non-trivial.
func writeMemoTree(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"pkg-a/index.js":           "module.exports = function a() { return 1 }",
		"pkg-a/dist/index.mjs":     "export default function a() { return 1 }",
		"pkg-a/README.md":          "# pkg-a\nthis is junk and must be pruned",
		"pkg-a/dist/index.d.ts":    "export default function a(): number;",
		"pkg-b/index.js":           "module.exports = function b() { return 2 }",
		"pkg-b/test/index.test.js": "require('assert')",
		"pkg-b/LICENSE":            "MIT",
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

func hashesOf(t *testing.T, l v1.Layer) (v1.Hash, v1.Hash, int64) {
	t.Helper()
	d, err := l.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	id, err := l.DiffID()
	if err != nil {
		t.Fatalf("DiffID: %v", err)
	}
	sz, err := l.Size()
	if err != nil {
		t.Fatalf("Size: %v", err)
	}
	return d, id, sz
}

// TestTreeLayerIsBuiltOncePerBuildAcrossPlatforms is the central guard for
// change 1: several platforms of one build, handed the same host directory,
// must produce exactly ONE tree layer between them — asserted on the build
// counter, not on how long anything took — and every platform must receive an
// identical and correct layer, PruneResult and record set.
func TestTreeLayerIsBuiltOncePerBuildAcrossPlatforms(t *testing.T) {
	dir := t.TempDir()
	writeMemoTree(t, dir)

	ctx, cleanup := NewBuildContext(context.Background())
	defer cleanup()

	platforms := []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64, ports.LinuxAMD64}

	type result struct {
		layer   v1.Layer
		pruned  pruneutils.PruneResult
		records []attestutils.Record
	}
	results := make([]result, len(platforms))

	before := treeLayerBuilds.Load()

	var wg sync.WaitGroup
	errs := make([]error, len(platforms))
	start := make(chan struct{})
	for i, p := range platforms {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			layer, pruned, records, err := BuildDirectoryTreeLayerWithPruning(
				ctx, p, dir, ports.AppVendorDirPrefix, memoEpoch, ports.CompressionGzip, pruneutils.PruneOptions{})
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = result{layer: layer, pruned: pruned, records: records}
		}()
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("platform %d: %v", i, err)
		}
	}

	if built := treeLayerBuilds.Load() - before; built != 1 {
		t.Errorf("the tree was built %d times for %d platforms; the per-build memo should have built it exactly once", built, len(platforms))
	}

	// Every platform must have received the same layer, and it must be a
	// correct one — not an empty or half-written stand-in.
	wantDigest, wantDiffID, wantSize := hashesOf(t, results[0].layer)
	if wantSize <= 0 {
		t.Fatalf("memoised layer has size %d", wantSize)
	}
	for i, r := range results {
		d, id, sz := hashesOf(t, r.layer)
		if d != wantDigest || id != wantDiffID || sz != wantSize {
			t.Errorf("platform %d got a different layer: digest=%s diffID=%s size=%d, want %s/%s/%d",
				i, d, id, sz, wantDigest, wantDiffID, wantSize)
		}
		if r.pruned.FilesPruned != results[0].pruned.FilesPruned || r.pruned.BytesSaved != results[0].pruned.BytesSaved {
			t.Errorf("platform %d got PruneResult %+v, want %+v", i, r.pruned, results[0].pruned)
		}
		if len(r.records) != len(results[0].records) {
			t.Errorf("platform %d got %d attestation records, want %d", i, len(r.records), len(results[0].records))
		}
		for j := range r.records {
			if r.records[j] != results[0].records[j] {
				t.Errorf("platform %d record %d = %+v, want %+v", i, j, r.records[j], results[0].records[j])
			}
		}
	}

	// The memoised results must be genuinely populated, or every comparison
	// above would be comparing nothing to nothing.
	if results[0].pruned.FilesPruned == 0 {
		t.Error("fixture pruned nothing: the PruneResult comparison proves nothing")
	}
	if len(results[0].records) == 0 {
		t.Error("fixture produced no attestation records: the record comparison proves nothing")
	}

	// Each caller must own its slices, so one platform cannot corrupt
	// another's view.
	results[0].pruned.PrunedPaths[0] = "MUTATED"
	if results[1].pruned.PrunedPaths[0] == "MUTATED" {
		t.Error("PruneResult.PrunedPaths is shared between platforms; a mutation by one is visible to another")
	}
}

// TestTreeLayerMemoIsScopedToOneBuild pins the scope rule: the memo must not
// leak across builds. A process-global memo would eventually hand out a layer
// whose temp file a previous build's cleanup had already deleted.
func TestTreeLayerMemoIsScopedToOneBuild(t *testing.T) {
	dir := t.TempDir()
	writeMemoTree(t, dir)

	before := treeLayerBuilds.Load()

	for i := 0; i < 2; i++ {
		ctx, cleanup := NewBuildContext(context.Background())
		if _, _, _, err := BuildDirectoryTreeLayerWithPruning(
			ctx, ports.LinuxAMD64, dir, ports.AppVendorDirPrefix, memoEpoch, ports.CompressionGzip, pruneutils.PruneOptions{}); err != nil {
			cleanup()
			t.Fatalf("build %d: %v", i, err)
		}
		cleanup()
	}

	if built := treeLayerBuilds.Load() - before; built != 2 {
		t.Errorf("two separate builds produced %d tree builds, want 2: the memo is outliving its build", built)
	}
}

// TestTreeLayerMemoKeyDistinguishesInputs pins what the memo is keyed on. A
// key that collapsed any of these would hand a caller a layer built from
// different bytes than it asked for — the worst possible failure here, since
// it lands silently in an image.
func TestTreeLayerMemoKeyDistinguishesInputs(t *testing.T) {
	base := func() string {
		return treeLayerMemoKey("/tmp/tree", "/app/vendor", memoEpoch, ports.CompressionGzip, pruneutils.PruneOptions{}, true)
	}
	cases := map[string]string{
		"different host dir":      treeLayerMemoKey("/tmp/other", "/app/vendor", memoEpoch, ports.CompressionGzip, pruneutils.PruneOptions{}, true),
		"different target prefix": treeLayerMemoKey("/tmp/tree", "/app/client", memoEpoch, ports.CompressionGzip, pruneutils.PruneOptions{}, true),
		"different modTime":       treeLayerMemoKey("/tmp/tree", "/app/vendor", memoEpoch.Add(time.Second), ports.CompressionGzip, pruneutils.PruneOptions{}, true),
		"different compression":   treeLayerMemoKey("/tmp/tree", "/app/vendor", memoEpoch, ports.CompressionZstd, pruneutils.PruneOptions{}, true),
		"records not wanted":      treeLayerMemoKey("/tmp/tree", "/app/vendor", memoEpoch, ports.CompressionGzip, pruneutils.PruneOptions{}, false),
		"NoPrune set":             treeLayerMemoKey("/tmp/tree", "/app/vendor", memoEpoch, ports.CompressionGzip, pruneutils.PruneOptions{NoPrune: true}, true),
		"KeepSourcemap set":       treeLayerMemoKey("/tmp/tree", "/app/vendor", memoEpoch, ports.CompressionGzip, pruneutils.PruneOptions{KeepSourcemap: true}, true),
		"KeepPatterns set":        treeLayerMemoKey("/tmp/tree", "/app/vendor", memoEpoch, ports.CompressionGzip, pruneutils.PruneOptions{KeepPatterns: []string{"**/*.node"}}, true),
		"ExcludeDirs set":         treeLayerMemoKey("/tmp/tree", "/app/vendor", memoEpoch, ports.CompressionGzip, pruneutils.PruneOptions{ExcludeDirs: []string{"client"}}, true),
		"KeepPatterns reordered":  treeLayerMemoKey("/tmp/tree", "/app/vendor", memoEpoch, ports.CompressionGzip, pruneutils.PruneOptions{KeepPatterns: []string{"b", "a"}}, true),
	}
	for name, key := range cases {
		if key == base() {
			t.Errorf("memo key does not distinguish %s", name)
		}
	}
	// The same inputs must produce the same key, or the memo never hits.
	if base() != base() {
		t.Error("memo key is not stable for identical inputs")
	}
	// Two spellings of one directory must share a key.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	a := treeLayerMemoKey(wd, "/app/vendor", memoEpoch, ports.CompressionGzip, pruneutils.PruneOptions{}, true)
	b := treeLayerMemoKey(filepath.Join(wd, "sub", ".."), "/app/vendor", memoEpoch, ports.CompressionGzip, pruneutils.PruneOptions{}, true)
	if a != b {
		t.Error("memo key does not normalise two spellings of the same directory")
	}
}

// TestStripTreeOnceRunsOncePerBuildAndNeverDuringAWalk is the guard on change
// 2's correctness half.
//
// The fake strip records how many strips are IN FLIGHT. Each platform strips
// and then samples that counter, standing in for the tar walk that follows the
// strip in Packager.Build. With the memo, only the first platform strips at
// all and every later one samples zero. Without it — calling
// striputils.StripDirectory directly, as the code did before — the second
// platform's rewrite runs while the first is already walking, and the sample
// catches it.
func TestStripTreeOnceRunsOncePerBuildAndNeverDuringAWalk(t *testing.T) {
	dir := t.TempDir()
	writeMemoTree(t, dir)

	var calls atomic.Int64
	var inFlight atomic.Int64
	var overlaps atomic.Int64

	orig := stripDirectoryFn
	stripDirectoryFn = func(ctx context.Context, d string, modTime time.Time) (int, []string, error) {
		calls.Add(1)
		inFlight.Add(1)
		defer inFlight.Add(-1)
		time.Sleep(150 * time.Millisecond)
		return 2, []string{filepath.Join(d, "addon.node"), filepath.Join(d, "other.so")}, fmt.Errorf("no strip tool")
	}
	t.Cleanup(func() { stripDirectoryFn = orig })

	ctx, cleanup := NewBuildContext(context.Background())
	defer cleanup()

	const platforms = 3
	type reply struct {
		stripped int
		skipped  []string
		err      error
	}
	replies := make([]reply, platforms)

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < platforms; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			stripped, skipped, err := stripTreeOnce(ctx, dir, memoEpoch)
			// Stands in for the tar walk that immediately follows the strip
			// in Packager.Build: no strip may still be writing at this point.
			if inFlight.Load() > 0 {
				overlaps.Add(1)
			}
			replies[i] = reply{stripped: stripped, skipped: skipped, err: err}
		}()
	}
	close(start)
	wg.Wait()

	if got := calls.Load(); got != 1 {
		t.Errorf("StripDirectory ran %d times for %d platforms, want 1", got, platforms)
	}
	if got := overlaps.Load(); got != 0 {
		t.Errorf("%d platform(s) began their tree walk while a strip was still rewriting the tree", got)
	}

	// The replay must be the real result, not a blank one: warnUnstripped
	// depends on every platform seeing the same honest counts.
	for i, r := range replies {
		if r.stripped != 2 {
			t.Errorf("platform %d got stripped=%d, want 2", i, r.stripped)
		}
		if len(r.skipped) != 2 {
			t.Errorf("platform %d got skipped=%v, want 2 entries", i, r.skipped)
		}
		if r.err == nil {
			t.Errorf("platform %d lost the strip error the first platform saw", i)
		}
	}

	// Each caller owns its slice.
	replies[0].skipped[0] = "MUTATED"
	if replies[1].skipped[0] == "MUTATED" {
		t.Error("the skipped list is shared between platforms")
	}
}

// TestStripTreeOnceWithoutBuildContextStillStrips pins the fallback: a context
// that never went through NewBuildContext (every test in this package, and any
// direct caller) must still get the work done rather than silently skipping it.
func TestStripTreeOnceWithoutBuildContextStillStrips(t *testing.T) {
	var calls atomic.Int64
	orig := stripDirectoryFn
	stripDirectoryFn = func(ctx context.Context, d string, modTime time.Time) (int, []string, error) {
		calls.Add(1)
		return 1, nil, nil
	}
	t.Cleanup(func() { stripDirectoryFn = orig })

	for i := 0; i < 2; i++ {
		if _, _, err := stripTreeOnce(context.Background(), t.TempDir(), memoEpoch); err != nil {
			t.Fatalf("stripTreeOnce: %v", err)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("StripDirectory ran %d times without a build context, want 2 (no memo to share)", got)
	}
}

// TestTreeLayerBytesDoNotDependOnPlatform pins the PREMISE the memo rests on.
//
// The memo key deliberately omits ports.Platform, on the ground that a tree
// layer's bytes are a function of the host directory, the target prefix, the
// pinned modTime, the compression and the pruning options — and of nothing
// else. If that ever stopped being true, the memo would silently hand one
// platform another platform's layer, which is the worst failure available
// here because it lands inside a published image. So it is asserted directly,
// with each platform built in its OWN build context so no memo is involved.
func TestTreeLayerBytesDoNotDependOnPlatform(t *testing.T) {
	dir := t.TempDir()
	writeMemoTree(t, dir)

	type built struct {
		digest  v1.Hash
		diffID  v1.Hash
		size    int64
		pruned  int
		records int
	}
	seen := map[ports.Platform]built{}

	for _, p := range []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64} {
		ctx, cleanup := NewBuildContext(context.Background())
		layer, pruned, records, err := BuildDirectoryTreeLayerWithPruning(
			ctx, p, dir, ports.AppVendorDirPrefix, memoEpoch, ports.CompressionGzip, pruneutils.PruneOptions{})
		if err != nil {
			cleanup()
			t.Fatalf("%s: %v", p, err)
		}
		d, id, sz := hashesOf(t, layer)
		seen[p] = built{digest: d, diffID: id, size: sz, pruned: pruned.FilesPruned, records: len(records)}
		cleanup()
	}

	a, b := seen[ports.LinuxAMD64], seen[ports.LinuxARM64]
	if a != b {
		t.Fatalf("a tree layer's bytes depend on the platform: %s -> %+v, %s -> %+v\nthe per-build memo omits platform from its key and would now serve the wrong bytes",
			ports.LinuxAMD64, a, ports.LinuxARM64, b)
	}
	if a.records == 0 || a.pruned == 0 {
		t.Fatal("fixture produced no records or pruned nothing: the comparison proves little")
	}
}

// BenchmarkTreeLayerAcrossPlatforms measures what the per-build memo is
// actually for, which BenchmarkBuildDirectoryTreeLayerWithPruning cannot show:
// that one is a single-platform measurement with a fresh build context per
// iteration, so it prices one tree build no matter what the memo does.
//
// Here one build context is shared by N platforms, exactly as the core
// pipeline's fan-out shares it. Before the memo the cost was linear in N —
// every platform walked, hashed, tarred and compressed the same tree. After
// it, N is almost free.
func BenchmarkTreeLayerAcrossPlatforms(b *testing.B) {
	dir := b.TempDir()
	writeBenchVendorTree(b, dir, 2000, 30<<20)

	platforms := []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64, {OS: "linux", Arch: "arm", Variant: "v7"}}

	for _, n := range []int{1, 2, 3} {
		b.Run(fmt.Sprintf("%d_platforms", n), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				ctx, cleanup := NewBuildContext(context.Background())
				var wg sync.WaitGroup
				errs := make([]error, n)
				for j := 0; j < n; j++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						_, _, _, err := BuildDirectoryTreeLayerWithPruning(
							ctx, platforms[j], dir, ports.AppVendorDirPrefix, memoEpoch, ports.CompressionGzip, pruneutils.PruneOptions{})
						errs[j] = err
					}()
				}
				wg.Wait()
				b.StopTimer()
				for _, err := range errs {
					if err != nil {
						cleanup()
						b.Fatalf("build: %v", err)
					}
				}
				cleanup()
				b.StartTimer()
			}
		})
	}
}
