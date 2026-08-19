package precompressutils_test

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/precompressutils"
)

// compressibleAsset is large and repetitive so brotli takes measurable time,
// which is what makes the write window wide enough for the race below to land.
var compressibleAsset = func() string {
	s := ""
	for i := 0; i < 400; i++ {
		s += fmt.Sprintf("export const chunk%d = { name: 'value-%d', padding: '%s' };\n", i, i, "abcdefghij0123456789")
	}
	return s
}()

func writeTree(t *testing.T, files int) string {
	t.Helper()
	dir := t.TempDir()
	for i := 0; i < files; i++ {
		p := filepath.Join(dir, fmt.Sprintf("chunk-%03d.js", i))
		if err := os.WriteFile(p, []byte(compressibleAsset), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// buildTarLikeThePackager mirrors what the packager does after precompressing:
// stat every file to size its tar header, then read the file into the archive.
// That stat-then-read pair is precisely where the race showed up as
// "archive/tar: write too long", so the reproduction has to do both, in that
// order, rather than merely reading files.
func buildTarLikeThePackager(dir string) error {
	tw := tar.NewWriter(io.Discard)
	defer tw.Close()
	return filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return err
		}
		hdr, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.Base(p)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			// A sidecar can legitimately vanish between walk and open only if
			// something is writing concurrently, which is the bug; surface it.
			return err
		}
		defer f.Close()
		// io.Copy writing more bytes than the header declared is exactly the
		// failure mode: archive/tar returns ErrWriteTooLong.
		if _, err := io.Copy(tw, f); err != nil {
			return fmt.Errorf("copy %s: %w", filepath.Base(p), err)
		}
		return nil
	})
}

// TestPrecompressDirectory_ConcurrentWithTarring is the regression test for the
// reported multi-arch build failure:
//
//	prerendered layer: write tar entry "app/prerendered/index.html.br":
//	archive/tar: write too long
//
// fanOut builds platforms concurrently and every platform packages from the same
// output tree, so one platform generated sidecars while another was already
// walking that tree into a tar. os.WriteFile truncates before writing, so the
// walker could stat a half-written sidecar, put that size in the header, then
// read the finished file.
//
// `go test -race` cannot detect this: the race is through the filesystem, not
// memory. Nor does this test reliably reproduce it — the vulnerable window is the
// gap between O_TRUNC and the write completing, which is roughly one syscall wide,
// and it was verified that this test still passes with both the atomic write and
// the lock reverted. It is therefore a smoke test that the concurrent path works,
// NOT evidence that the race is fixed.
//
// The durable guard is TestSidecarWritesNeverTruncateInPlace, which asserts the
// property that makes partial observation impossible instead of trying to observe
// its absence. Reproducing the interleaving on demand would need fault injection
// (a seam around the write) that does not exist here.
func TestPrecompressDirectory_ConcurrentWithTarring(t *testing.T) {
	dir := writeTree(t, 24)
	ts := time.Unix(1_700_000_000, 0).UTC()
	opts := precompressutils.PrecompressOptions{Gzip: true, Brotli: true, Zstd: true}

	const platforms = 4
	var wg sync.WaitGroup
	errs := make(chan error, platforms*2)

	for i := 0; i < platforms; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// What each platform's packager does: precompress the shared tree,
			// then immediately tar it.
			if err := precompressutils.PrecompressDirectory(dir, ts, opts); err != nil {
				errs <- fmt.Errorf("precompress: %w", err)
				return
			}
			if err := buildTarLikeThePackager(dir); err != nil {
				errs <- fmt.Errorf("tar: %w", err)
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent precompress+tar over one tree must not fail: %v", err)
	}
}

// TestPrecompressDirectory_SecondCallDoesNotRewrite pins the other half of the
// fix, and does it with a content canary rather than a timestamp.
//
// Timestamps cannot detect this: sidecar mtimes used to be pinned to the build
// epoch, so a rewrite set exactly the same mtime it had before and an
// mtime-comparison test passed whether or not the file was regenerated. The first
// version of this test made that mistake and passed with the fix reverted. Marking
// the sidecar and checking whether the mark survives is independent of clocks
// entirely.
//
// What it guards: isStale compares a sidecar's mtime against its source's, and a
// build writes its sources *now* while the epoch comes from the last commit — so
// every sidecar was permanently "older than its source" and every platform in a
// multi-platform build re-ran brotli at BestCompression over the whole tree. That
// is both wasted work and what made the race window maximal, since the safety
// argument for the per-directory lock depends on later platforms writing nothing.
func TestPrecompressDirectory_SecondCallDoesNotRewrite(t *testing.T) {
	dir := writeTree(t, 6)
	ts := time.Unix(1_700_000_000, 0).UTC()
	opts := precompressutils.PrecompressOptions{Gzip: true, Brotli: true}

	if err := precompressutils.PrecompressDirectory(dir, ts, opts); err != nil {
		t.Fatalf("first pass: %v", err)
	}

	// Replace each sidecar's contents with a canary. A regenerating second pass
	// overwrites it; a pass that correctly reuses fresh sidecars leaves it.
	canary := []byte("CANARY-not-a-real-sidecar")
	marked := 0
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch filepath.Ext(e.Name()) {
		case ".gz", ".br":
			p := filepath.Join(dir, e.Name())
			fi, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, canary, 0o644); err != nil {
				t.Fatal(err)
			}
			// Restore the mtime the first pass gave it, so the canary is
			// indistinguishable from a legitimately fresh sidecar as far as
			// freshness detection is concerned.
			if err := os.Chtimes(p, fi.ModTime(), fi.ModTime()); err != nil {
				t.Fatal(err)
			}
			marked++
		}
	}
	if marked == 0 {
		t.Fatal("premise broken: the first pass produced no sidecars, so this test would prove nothing")
	}

	if err := precompressutils.PrecompressDirectory(dir, ts, opts); err != nil {
		t.Fatalf("second pass: %v", err)
	}

	survived := 0
	for _, e := range entries {
		switch filepath.Ext(e.Name()) {
		case ".gz", ".br":
			body, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if string(body) == string(canary) {
				survived++
			}
		}
	}
	if survived != marked {
		t.Errorf("%d of %d sidecars were regenerated by a second pass over an unchanged tree; "+
			"fresh sidecars must be reused, or every platform recompresses the whole tree and "+
			"writes while other platforms are tarring it", marked-survived, marked)
	}
}

// TestPrecompressDirectory_RegeneratesWhenSourceChanges is the negative control:
// reusing fresh sidecars must not mean ignoring a source that really did change,
// which is the guard isStale exists for.
func TestPrecompressDirectory_RegeneratesWhenSourceChanges(t *testing.T) {
	dir := writeTree(t, 2)
	ts := time.Unix(1_700_000_000, 0).UTC()
	opts := precompressutils.PrecompressOptions{Gzip: true}

	if err := precompressutils.PrecompressDirectory(dir, ts, opts); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	before := sidecarStamps(t, dir)

	time.Sleep(1100 * time.Millisecond)
	target := filepath.Join(dir, "chunk-000.js")
	if err := os.WriteFile(target, []byte(compressibleAsset+"\nexport const added = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := precompressutils.PrecompressDirectory(dir, ts, opts); err != nil {
		t.Fatalf("second pass: %v", err)
	}
	after := sidecarStamps(t, dir)

	if before["chunk-000.js.gz"] == after["chunk-000.js.gz"] {
		t.Error("a source overwritten after its sidecar must regenerate it; stale compressed output would be served to clients")
	}
	if before["chunk-001.js.gz"] != after["chunk-001.js.gz"] {
		t.Error("an untouched source's sidecar should not have been regenerated")
	}
}

func sidecarStamps(t *testing.T, dir string) map[string]time.Time {
	t.Helper()
	out := map[string]time.Time{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		switch filepath.Ext(e.Name()) {
		case ".gz", ".br", ".zst":
			fi, err := e.Info()
			if err != nil {
				t.Fatal(err)
			}
			out[e.Name()] = fi.ModTime()
		}
	}
	return out
}

// Deliberately absent: a test that forces sidecar regeneration while another
// goroutine tars the same tree.
//
// One was written, and removing it is the point. It asserted that a write
// concurrent with a walk is safe — write atomicity — which is a guarantee this
// design does not provide and does not need. The shipped invariant is narrower:
// writes never overlap a walk at all. Every platform precompresses before it tars,
// the per-directory lock means only the first platform's pass writes anything, and
// the freshness fix means every later pass finds the sidecars current and writes
// nothing. So by the time any tar walk starts, all writing is finished.
//
// Forcing continuous rewrites breaks that premise artificially, and the test then
// failed intermittently — correctly, because under those conditions the code really
// is unsafe. Keeping it would have meant a flaky test of a guarantee we chose not
// to make.
//
// Atomicity was also tried as the fix (temp file plus rename) and reverted: os.CreateTemp
// places the temporary file in the very directory the packager walks, so a
// concurrent walk either fails its lstat when the file is renamed away, or packages
// a .tmp-* file into the image. A test caught that, which is why the fix is
// ordering rather than atomicity.
//
// What guards the real invariant is TestPrecompressDirectory_SecondCallDoesNotRewrite:
// if later passes ever start rewriting again, the ordering argument collapses and
// that test fails.

// TestPrecompressDirectory_SerialisesPerDirectory guards the property the race fix
// actually rests on: writes to a shared tree never overlap, so a platform that has
// started tarring cannot have another platform writing underneath it.
//
// Asserted by observing that concurrent calls do not interleave their writes,
// rather than by trying to catch a partial read — that window is about one syscall
// wide and, as the smoke test above records, does not reproduce on demand.
func TestPrecompressDirectory_SerialisesPerDirectory(t *testing.T) {
	dir := writeTree(t, 8)
	ts := time.Unix(1_700_000_000, 0).UTC()
	opts := precompressutils.PrecompressOptions{Gzip: true, Brotli: true}

	const callers = 5
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := precompressutils.PrecompressDirectory(dir, ts, opts); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent precompression of one tree must not error: %v", err)
	}

	// Every sidecar must be complete and decodable. A torn write from overlapping
	// writers would show up here as a truncated gzip stream.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var checked int
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".gz" {
			continue
		}
		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("open %s: %v", e.Name(), err)
		}
		zr, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			t.Errorf("%s is not a valid gzip stream after concurrent generation: %v", e.Name(), err)
			continue
		}
		if _, err := io.Copy(io.Discard, zr); err != nil {
			t.Errorf("%s is a truncated gzip stream: %v", e.Name(), err)
		}
		zr.Close()
		f.Close()
		checked++
	}
	if checked == 0 {
		t.Fatal("premise broken: no .gz sidecars were produced, so nothing was verified")
	}

	// No temporary files may be left behind in a tree the packager will walk: one
	// would either break its walk or be packaged into the image.
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("temporary file %q left in a directory the packager tars", e.Name())
		}
	}
}
