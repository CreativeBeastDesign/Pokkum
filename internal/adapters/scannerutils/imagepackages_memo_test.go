package scannerutils

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// countingImage wraps a v1.Image and counts how many times its layers are
// actually read.
//
// It exists because "the memo works" and "the memo was never consulted" are
// indistinguishable from the return value alone — both produce the right
// packages (mem:self_review_checklist rows 28 and 47). Layers() is the first
// thing an uncached extraction does and a cached one never does, so this
// counter is the observable that separates them.
type countingImage struct {
	v1.Image
	layerReads atomic.Int64
}

func (c *countingImage) Layers() ([]v1.Layer, error) {
	c.layerReads.Add(1)
	return c.Image.Layers()
}

// failingLayersImage reports a digest normally but cannot produce layers.
type failingLayersImage struct {
	v1.Image
	calls atomic.Int64
	fail  atomic.Bool
}

var errLayersUnavailable = errors.New("layers unavailable")

func (f *failingLayersImage) Layers() ([]v1.Layer, error) {
	f.calls.Add(1)
	if f.fail.Load() {
		return nil, errLayersUnavailable
	}
	return f.Image.Layers()
}

// memoTestImage builds a distinct image whose npm catalogue is identifiable
// from its contents alone.
func memoTestImage(t *testing.T, pkgName, version string) v1.Image {
	t.Helper()
	return buildTestImage(t, map[string]string{
		"etc/os-release": fmt.Sprintf("ID=debian\nVERSION_ID=\"%s\"\n", version),
		fmt.Sprintf("app/vendor/%s/package.json", pkgName): fmt.Sprintf(
			`{"name": %q, "version": %q}`, pkgName, version),
	})
}

func TestExtractImagePackages_MemoReturnsWhatAnUncachedExtractionWould(t *testing.T) {
	resetImagePackagesMemo()
	t.Cleanup(resetImagePackagesMemo)

	base := memoTestImage(t, "lodash", "4.17.21")
	img := &countingImage{Image: base}
	ctx := context.Background()

	// The oracle: the extraction path with the memo bypassed entirely.
	wantPkgs, wantDistro, wantLayers, err := extractImagePackagesUncached(ctx, base)
	if err != nil {
		t.Fatalf("extractImagePackagesUncached: %v", err)
	}
	if len(wantPkgs) == 0 || wantLayers == 0 {
		t.Fatalf("degenerate fixture: %d packages across %d layers — a memo test over an empty result proves nothing", len(wantPkgs), wantLayers)
	}

	firstPkgs, firstDistro, err := ExtractImagePackages(ctx, img)
	if err != nil {
		t.Fatalf("first ExtractImagePackages: %v", err)
	}
	if got := img.layerReads.Load(); got != 1 {
		t.Fatalf("first call read layers %d times, want 1 — the cold path did not run", got)
	}

	secondPkgs, secondDistro, err := ExtractImagePackages(ctx, img)
	if err != nil {
		t.Fatalf("second ExtractImagePackages: %v", err)
	}
	// This is the assertion the whole change exists for. Without it the test
	// would pass identically with no memo at all.
	if got := img.layerReads.Load(); got != 1 {
		t.Errorf("second call read layers again (total %d) — the result was not memoised", got)
	}

	if got := renderPackages(secondPkgs); got != renderPackages(wantPkgs) {
		t.Errorf("memoised result differs from an uncached extraction\n--- uncached ---\n%s\n--- memoised ---\n%s",
			renderPackages(wantPkgs), got)
	}
	if got := renderPackages(firstPkgs); got != renderPackages(wantPkgs) {
		t.Errorf("first (cold) result differs from an uncached extraction\n--- uncached ---\n%s\n--- cold ---\n%s",
			renderPackages(wantPkgs), got)
	}
	if firstDistro != wantDistro || secondDistro != wantDistro {
		t.Errorf("distro: cold=%+v memoised=%+v, want %+v", firstDistro, secondDistro, wantDistro)
	}
}

func TestExtractImagePackages_MemoNeverServesOneImageForAnother(t *testing.T) {
	resetImagePackagesMemo()
	t.Cleanup(resetImagePackagesMemo)

	ctx := context.Background()
	a := &countingImage{Image: memoTestImage(t, "alpha", "1.0.0")}
	b := &countingImage{Image: memoTestImage(t, "bravo", "2.0.0")}

	da, err := a.Digest()
	if err != nil {
		t.Fatalf("digest a: %v", err)
	}
	db, err := b.Digest()
	if err != nil {
		t.Fatalf("digest b: %v", err)
	}
	if da == db {
		t.Fatalf("fixture images share digest %s — this test cannot distinguish them", da)
	}

	// Interleaved on purpose: a per-digest bug that only manifests on the
	// second distinct image would be invisible if each image were exercised
	// to completion in turn.
	gotA1, _, err := ExtractImagePackages(ctx, a)
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	gotB1, _, err := ExtractImagePackages(ctx, b)
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	gotA2, _, err := ExtractImagePackages(ctx, a)
	if err != nil {
		t.Fatalf("a again: %v", err)
	}
	gotB2, _, err := ExtractImagePackages(ctx, b)
	if err != nil {
		t.Fatalf("b again: %v", err)
	}

	assertHas := func(label string, pkgs []CatalogPackage, wantName, wantVersion string) {
		t.Helper()
		for _, p := range pkgs {
			if p.Name == wantName {
				if p.Version != wantVersion {
					t.Errorf("%s: %s@%s, want %s@%s", label, p.Name, p.Version, wantName, wantVersion)
				}
				return
			}
		}
		t.Errorf("%s: %s missing; got %s", label, wantName, renderPackages(pkgs))
	}
	assertHas("a cold", gotA1, "alpha", "1.0.0")
	assertHas("b cold", gotB1, "bravo", "2.0.0")
	assertHas("a memoised", gotA2, "alpha", "1.0.0")
	assertHas("b memoised", gotB2, "bravo", "2.0.0")

	for _, p := range gotA2 {
		if p.Name == "bravo" {
			t.Errorf("image a's memoised result contains image b's package: %s", renderPackages(gotA2))
		}
	}
	for _, p := range gotB2 {
		if p.Name == "alpha" {
			t.Errorf("image b's memoised result contains image a's package: %s", renderPackages(gotB2))
		}
	}

	if got := a.layerReads.Load(); got != 1 {
		t.Errorf("image a read layers %d times, want 1", got)
	}
	if got := b.layerReads.Load(); got != 1 {
		t.Errorf("image b read layers %d times, want 1", got)
	}
}

// TestExtractImagePackages_MemoisedSliceIsNotAliasedToCallers guards the
// classic memo defect: handing every caller the same backing array, so the
// first one to sort or truncate it corrupts every later hit.
func TestExtractImagePackages_MemoisedSliceIsNotAliasedToCallers(t *testing.T) {
	resetImagePackagesMemo()
	t.Cleanup(resetImagePackagesMemo)

	ctx := context.Background()
	img := memoTestImage(t, "lodash", "4.17.21")

	first, _, err := ExtractImagePackages(ctx, img)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	want := renderPackages(first)
	if len(first) == 0 {
		t.Fatal("degenerate fixture: no packages to mutate")
	}

	// A caller doing something entirely ordinary with the slice it was given.
	for i := range first {
		first[i].Version = "CLOBBERED"
		first[i].Name = "CLOBBERED"
	}

	second, _, err := ExtractImagePackages(ctx, img)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if got := renderPackages(second); got != want {
		t.Errorf("a caller's in-place edit leaked into the memo\n--- want ---\n%s\n--- got ---\n%s", want, got)
	}
}

// TestExtractImagePackages_ErrorIsNotMemoised proves a failed extraction
// leaves nothing behind: the retry must do real work, not be served a
// memoised empty result.
func TestExtractImagePackages_ErrorIsNotMemoised(t *testing.T) {
	resetImagePackagesMemo()
	t.Cleanup(resetImagePackagesMemo)

	ctx := context.Background()
	img := &failingLayersImage{Image: memoTestImage(t, "lodash", "4.17.21")}
	img.fail.Store(true)

	if _, _, err := ExtractImagePackages(ctx, img); !errors.Is(err, errLayersUnavailable) {
		t.Fatalf("first call err = %v, want %v", err, errLayersUnavailable)
	}

	img.fail.Store(false)
	pkgs, _, err := ExtractImagePackages(ctx, img)
	if err != nil {
		t.Fatalf("retry after transient failure: %v", err)
	}
	if len(pkgs) == 0 {
		t.Error("retry returned zero packages — the failed attempt was memoised as an empty result")
	}
	if got := img.calls.Load(); got != 2 {
		t.Errorf("Layers() called %d times, want 2 (failed attempt + real retry)", got)
	}
}

// TestExtractImagePackages_MemoHitHonoursContextCancellation pins the one
// behaviour a memo could silently change: an already-cancelled context must
// not start returning success just because an earlier call warmed the cache.
func TestExtractImagePackages_MemoHitHonoursContextCancellation(t *testing.T) {
	resetImagePackagesMemo()
	t.Cleanup(resetImagePackagesMemo)

	img := memoTestImage(t, "lodash", "4.17.21")
	if _, _, err := ExtractImagePackages(context.Background(), img); err != nil {
		t.Fatalf("warming the memo: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := ExtractImagePackages(ctx, img); !errors.Is(err, context.Canceled) {
		t.Errorf("memo hit with a cancelled context returned err = %v, want context.Canceled — the uncached path checks ctx.Err() per layer and this image has layers", err)
	}
}

// TestExtractImagePackages_MemoIsConcurrencySafe runs the fan-out shape the
// SBOM generator actually produces. Meaningful under -race.
func TestExtractImagePackages_MemoIsConcurrencySafe(t *testing.T) {
	resetImagePackagesMemo()
	t.Cleanup(resetImagePackagesMemo)

	ctx := context.Background()
	images := []v1.Image{
		memoTestImage(t, "alpha", "1.0.0"),
		memoTestImage(t, "bravo", "2.0.0"),
		memoTestImage(t, "charlie", "3.0.0"),
	}
	want := make([]string, len(images))
	for i, img := range images {
		pkgs, _, err := ExtractImagePackages(ctx, img)
		if err != nil {
			t.Fatalf("warm %d: %v", i, err)
		}
		want[i] = renderPackages(pkgs)
	}
	resetImagePackagesMemo() // race the cold path too, not only hits

	const goroutines = 24
	var wg sync.WaitGroup
	errs := make([]error, goroutines)
	got := make([]string, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			idx := g % len(images)
			pkgs, _, err := ExtractImagePackages(ctx, images[idx])
			if err != nil {
				errs[g] = err
				return
			}
			got[g] = renderPackages(pkgs)
		}(g)
	}
	wg.Wait()

	for g := 0; g < goroutines; g++ {
		if errs[g] != nil {
			t.Fatalf("goroutine %d: %v", g, errs[g])
		}
		if got[g] != want[g%len(images)] {
			t.Errorf("goroutine %d (image %d) got the wrong image's packages:\n--- want ---\n%s\n--- got ---\n%s",
				g, g%len(images), want[g%len(images)], got[g])
		}
	}
}

// TestExtractImagePackages_MemoCapNeverSubstitutesAnEntry checks the size cap
// does what its comment claims: past the cap results stop being stored, and
// no entry is ever replaced by a different image's.
func TestExtractImagePackages_MemoCapNeverSubstitutesAnEntry(t *testing.T) {
	resetImagePackagesMemo()
	t.Cleanup(resetImagePackagesMemo)

	ctx := context.Background()
	total := imagePackagesMemoMaxEntries + 3
	images := make([]v1.Image, total)
	want := make([]string, total)
	for i := range images {
		images[i] = memoTestImage(t, fmt.Sprintf("pkg-%03d", i), fmt.Sprintf("1.0.%d", i))
		pkgs, _, err := ExtractImagePackages(ctx, images[i])
		if err != nil {
			t.Fatalf("image %d: %v", i, err)
		}
		want[i] = renderPackages(pkgs)
	}
	for i, img := range images {
		pkgs, _, err := ExtractImagePackages(ctx, img)
		if err != nil {
			t.Fatalf("re-read image %d: %v", i, err)
		}
		if got := renderPackages(pkgs); got != want[i] {
			t.Fatalf("image %d past the memo cap returned different packages\n--- want ---\n%s\n--- got ---\n%s", i, want[i], got)
		}
	}
}
