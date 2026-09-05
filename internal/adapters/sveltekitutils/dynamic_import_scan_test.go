package sveltekitutils

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minifiedLineWithComputedImport builds ONE line of at least minBytes that ends
// in a computed import() call — the shape every real bundler emits and no
// hand-written fixture produces by accident. This is the input class the
// previous bufio.Scanner-based implementation could not see past: its default
// token limit is 64KiB, Scan() returned false with bufio.ErrTooLong, the error
// was never checked, and the file was reported as containing no dynamic
// imports at all.
func minifiedLineWithComputedImport(minBytes int) string {
	const filler = "var _pad=1;"
	const tail = "const m=await import('./routes/'+routeName);"

	var b strings.Builder
	for b.Len() < minBytes {
		b.WriteString(filler)
	}
	b.WriteString(tail)
	return b.String()
}

// TestScanDynamicImports_ComputedImportOnLineOver64KiB is the regression guard
// for the bug this file's fix exists for. It must FAIL against the
// bufio.Scanner implementation and pass against the whole-file read.
func TestScanDynamicImports_ComputedImportOnLineOver64KiB(t *testing.T) {
	dir := t.TempDir()

	line := minifiedLineWithComputedImport(100 * 1024)
	if len(line) <= 64*1024 {
		// Fixture floor: a "long line" fixture that fits inside the limit it is
		// supposed to exceed would pass against the buggy code too.
		t.Fatalf("fixture line is %d bytes, must exceed bufio.Scanner's 64KiB default token limit", len(line))
	}

	// Written with a trailing newline so the file is unambiguously ONE line of
	// content — the exact minified-bundle shape, not a long file.
	if err := os.WriteFile(filepath.Join(dir, "bundle.js"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ScanDynamicImports(context.Background(), dir, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !res.HasUnsupportedDynamicImports {
		t.Fatalf("HasUnsupportedDynamicImports = false, want true: a %d-byte single line containing import('./routes/'+routeName) was reported clean", len(line))
	}
	if len(res.DetectedLocations) != 1 || res.DetectedLocations[0] != "bundle.js:1" {
		t.Errorf("DetectedLocations = %v, want [bundle.js:1]", res.DetectedLocations)
	}
	if len(res.SkippedFiles) != 0 {
		t.Errorf("SkippedFiles = %v, want none: the file is well under the size ceiling and was fully scanned", res.SkippedFiles)
	}
}

// TestScanDynamicImports_MinifiedLineReportsEveryComputedImport covers the
// sibling defect from the same secretguard post-mortem: dedupe keyed on
// "file:line" alone reported at most ONE finding per line, and in a minified
// bundle one line is the whole file.
func TestScanDynamicImports_MinifiedLineReportsEveryComputedImport(t *testing.T) {
	dir := t.TempDir()

	line := "const a=await import('./routes/'+x);const b=await import(`./pages/${y}`);const c=await import(z);"
	if err := os.WriteFile(filepath.Join(dir, "chunk.js"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ScanDynamicImports(context.Background(), dir, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.DetectedLocations) != 3 {
		t.Errorf("DetectedLocations = %v (len %d), want 3 findings — every computed import on the line, not just the first", res.DetectedLocations, len(res.DetectedLocations))
	}
}

// TestScanDynamicImports_OversizedFileIsReportedNotClean proves the coverage
// gap is surfaced. A file the scan never read must not be indistinguishable
// from a file it read and found clean.
func TestScanDynamicImports_OversizedFileIsReportedNotClean(t *testing.T) {
	dir := t.TempDir()

	content := minifiedLineWithComputedImport(4096)
	if err := os.WriteFile(filepath.Join(dir, "huge.js"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ScanDynamicImports(context.Background(), dir, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(res.SkippedFiles) != 1 {
		t.Fatalf("SkippedFiles = %v, want exactly one entry for huge.js — an unscanned file must be reported, never silently treated as clean", res.SkippedFiles)
	}
	if res.SkippedFiles[0].FilePath != "huge.js" {
		t.Errorf("SkippedFiles[0].FilePath = %q, want %q", res.SkippedFiles[0].FilePath, "huge.js")
	}
	if !strings.Contains(res.SkippedFiles[0].Reason, "exceeds") {
		t.Errorf("SkippedFiles[0].Reason = %q, want it to say the file exceeded the scan limit", res.SkippedFiles[0].Reason)
	}
	var reasonFound bool
	for _, r := range res.Reasons {
		if strings.Contains(r, "not scanned for dynamic imports") {
			reasonFound = true
		}
	}
	if !reasonFound {
		t.Errorf("Reasons = %v, want a human-readable line naming the unscanned file", res.Reasons)
	}
	if res.HasUnsupportedDynamicImports {
		t.Errorf("HasUnsupportedDynamicImports = true, want false: nothing was actually found — the file was never read, which is what SkippedFiles reports")
	}
}

// TestScanDynamicImports_PrunesGeneratedOutputDirectories pins the pruning
// behavior in both directions: source is scanned, generated output is not.
func TestScanDynamicImports_PrunesGeneratedOutputDirectories(t *testing.T) {
	dir := t.TempDir()

	computed := "const m = await import('./routes/' + name);\n"

	pruned := []string{
		filepath.Join(".svelte-kit", "output", "server", "index.js"),
		filepath.Join("build", "server", "chunks", "0.js"),
		filepath.Join("dist", "bundle.js"),
		filepath.Join(".output", "server", "index.mjs"),
		filepath.Join("node_modules", "pkg", "index.js"),
		filepath.Join(".git", "hooks", "x.js"),
		filepath.Join(".pokkum", "src", "instrumentation.server.ts"),
	}
	for _, rel := range pruned {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(computed), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	res, err := ScanDynamicImports(context.Background(), dir, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.HasUnsupportedDynamicImports {
		t.Fatalf("HasUnsupportedDynamicImports = true, want false: every computed import lives in a generated-output directory that must be pruned; found %v", res.DetectedLocations)
	}

	// Fixture floor: the same content in src/ MUST still be found, otherwise
	// this test would pass against a scanner that had stopped working entirely.
	srcFile := filepath.Join(dir, "src", "routes", "loader.ts")
	if err := os.MkdirAll(filepath.Dir(srcFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcFile, []byte(computed), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err = ScanDynamicImports(context.Background(), dir, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.HasUnsupportedDynamicImports {
		t.Fatal("HasUnsupportedDynamicImports = false, want true: a computed import in src/ must still be detected")
	}
	if len(res.DetectedLocations) != 1 {
		t.Errorf("DetectedLocations = %v, want only the src/ finding", res.DetectedLocations)
	}
}

// TestScanDynamicImports_ExplicitFileTargetIsNeverPruned records the escape
// hatch: pruning applies to the directory walk only, so a caller can still
// point the scan at one built bundle by path.
func TestScanDynamicImports_ExplicitFileTargetIsNeverPruned(t *testing.T) {
	dir := t.TempDir()
	buildFile := filepath.Join(dir, "build", "server", "index.js")
	if err := os.MkdirAll(filepath.Dir(buildFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(buildFile, []byte(minifiedLineWithComputedImport(80*1024)), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ScanDynamicImports(context.Background(), buildFile, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.HasUnsupportedDynamicImports {
		t.Fatal("HasUnsupportedDynamicImports = false, want true: an explicitly targeted build file must still be scanned")
	}
}

// TestScanDynamicImports_CancelledContextStopsWalk proves the walk actually
// aborts part-way rather than running the tree to completion and discarding the
// result.
//
// Cancellation is injected MID-walk (see cancelAfterNChecks) rather than before
// the call, because a pre-cancelled context is caught by the cheap guard at the
// top of ScanDynamicImports and would pass even if the check inside the walk
// callback were deleted — which is the check this test exists to hold.
func TestScanDynamicImports_CancelledContextStopsWalk(t *testing.T) {
	dir := t.TempDir()
	const routes = 50
	for i := 0; i < routes; i++ {
		sub := filepath.Join(dir, "src", fmt.Sprintf("routes%02d", i))
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "loader.js"), []byte("await import('./p/'+n);\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Fixture floor: uncancelled, this tree is loudly non-clean, so a result of
	// "found nothing" below means the walk stopped, not that there was nothing
	// to find.
	baseline, err := ScanDynamicImports(context.Background(), dir, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(baseline.DetectedLocations) != routes {
		t.Fatalf("baseline DetectedLocations = %d, want %d", len(baseline.DetectedLocations), routes)
	}

	ctx := cancelAfterNChecks(8)
	res, err := ScanDynamicImports(ctx, dir, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(res.DetectedLocations) >= routes {
		t.Errorf("DetectedLocations = %d entries, want fewer than the full %d: a cancelled scan must stop walking, not finish the tree", len(res.DetectedLocations), routes)
	}

	// And the pre-cancelled case returns before touching the filesystem at all.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	res, err = ScanDynamicImports(cancelled, dir, 0)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if len(res.DetectedLocations) != 0 {
		t.Errorf("DetectedLocations = %v, want none for an already-cancelled context", res.DetectedLocations)
	}
}

// realMinifiedChunk returns the longest single line found in the repository's
// own committed SvelteKit build output, together with the file it came from.
//
// mem:self_review_checklist rows 26 and 50 both require this: a size-bounded
// scanner must be exercised against realistic GENERATED content, because a
// hand-written fixture and the implementation encode the same mental model of
// what a bundle looks like and will happily agree with each other while both
// are wrong. This corpus is genuine Vite/Rollup output from a real build and is
// checked into the repository, so its absence is a defect rather than an
// environmental condition — hence t.Fatalf, never t.Skip.
func realMinifiedChunk(t *testing.T) (line, source string) {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	corpus := filepath.Join(root, "testdata", "fixtures", "sveltekit-adapter-node", "build")

	err = filepath.WalkDir(corpus, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".js") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, l := range strings.Split(string(data), "\n") {
			if len(l) > len(line) {
				line, source = l, path
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk real build corpus %s: %v (this fixture is committed; a missing corpus is a defect, not an environment difference)", corpus, err)
	}

	// Corpus floor: a corpus whose longest line is a few dozen characters is
	// not minified output and would make everything below prove nothing.
	if len(line) < 20_000 {
		t.Fatalf("longest line in %s is %d bytes; the real build corpus must be genuinely minified for this test to mean anything", corpus, len(line))
	}
	return line, source
}

// TestScanDynamicImports_RealMinifiedBundleShape runs the scanner against a
// single line built entirely out of real bundler output.
//
// The repository's committed chunks top out around 29KiB per line — genuinely
// minified, but under bufio.Scanner's 64KiB ceiling — so the specimen is that
// real chunk's own lines rejoined into one, which is exactly what the same
// bundler emits under different chunking settings, plus one computed import.
// Nothing about the content is invented.
func TestScanDynamicImports_RealMinifiedBundleShape(t *testing.T) {
	longest, source := realMinifiedChunk(t)
	t.Logf("longest real minified line: %d bytes, from %s", len(longest), source)

	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	for b.Len() <= 64*1024 {
		for _, l := range strings.Split(string(data), "\n") {
			b.WriteString(l)
			b.WriteString(";")
		}
	}
	b.WriteString("const m=await import('./chunks/'+id);")

	specimen := b.String()
	if len(specimen) <= 64*1024 {
		t.Fatalf("specimen is %d bytes, must exceed the 64KiB token limit it exists to cross", len(specimen))
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "chunk.js"), []byte(specimen+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ScanDynamicImports(context.Background(), dir, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.HasUnsupportedDynamicImports {
		t.Fatalf("HasUnsupportedDynamicImports = false, want true on a %d-byte single line of real bundler output", len(specimen))
	}
	t.Logf("findings on the real-shaped specimen: %v", res.DetectedLocations)
}

// TestScanDynamicImports_SkipOnLaterFileDoesNotHideEarlierFindings covers
// mem:self_review_checklist row 4: the skip path must trigger on a NON-first
// item and must not swallow what was already found.
func TestScanDynamicImports_SkipOnLaterFileDoesNotHideEarlierFindings(t *testing.T) {
	dir := t.TempDir()

	// Lexical walk order: a.js, b.js, c.js.
	if err := os.WriteFile(filepath.Join(dir, "a.js"), []byte("await import('./x/'+n);\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.js"), []byte(minifiedLineWithComputedImport(4096)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.js"), []byte("await import('./y/'+m);\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ScanDynamicImports(context.Background(), dir, 1024)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.SkippedFiles) != 1 || res.SkippedFiles[0].FilePath != "b.js" {
		t.Fatalf("SkippedFiles = %v, want exactly b.js", res.SkippedFiles)
	}
	if len(res.DetectedLocations) != 2 {
		t.Errorf("DetectedLocations = %v, want both a.js and c.js: a skip in the middle of the walk must not stop or discard the rest", res.DetectedLocations)
	}
}
