package precompressutils_test

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/precompressutils"
)

// The synthetic client asset tree: roughly what `vite build` emits for a
// mid-sized SvelteKit app — a couple of hundred immutable chunks totalling
// around 10 MB, plus images that are already compressed and must be skipped.
const (
	benchAssetFiles = 200
	benchAssetBytes = 10 << 20
)

// benchAssetPool returns a block of minified-JS-shaped text. Compression
// ratio is the whole point of this benchmark, so the content has to behave
// like real bundle output: zero-filled files would compress ~1000:1 and make
// brotli look free, while crypto-random bytes would compress not at all and
// make it look uniformly expensive.
func benchAssetPool(size int) []byte {
	fragments := []string{
		"function n(e,t){return e&&e.__esModule?e:{default:e}}",
		"const r=Object.freeze({__proto__:null,get default(){return o}});",
		"export{a as default,b as hydrate,c as mount};",
		"var i=/^[a-z0-9!#$%&'*+/=?^_`{|}~-]+$/i;",
		"t.exports=function(e){return e.replace(/[\\s\\uFEFF\\xA0]+$/g,\"\")};",
		"class s extends HTMLElement{connectedCallback(){this.render()}}",
		"if(typeof window!==\"undefined\"&&window.__SVELTEKIT_DEV__){",
		"await import(\"./chunks/entry.client.js\").then(m=>m.start(a,b));",
		".btn{display:inline-flex;align-items:center;gap:.5rem;border-radius:.375rem}",
		"@media (prefers-color-scheme:dark){:root{--bg:#0b0b0f;--fg:#e7e7ea}}",
	}
	rng := rand.New(rand.NewSource(0x5EED17))
	var b strings.Builder
	b.Grow(size + 256)
	for b.Len() < size {
		b.WriteString(fragments[rng.Intn(len(fragments))])
		fmt.Fprintf(&b, "//#%d\n", rng.Intn(1<<20))
	}
	return []byte(b.String()[:size])
}

// benchImagePool returns bytes standing in for already-compressed media
// (webp/png/jpg). These files carry non-compressible extensions, so
// PrecompressDirectory must walk past them without compressing — which is
// itself part of the cost being measured.
func benchImagePool(size int) []byte {
	buf := make([]byte, size)
	rng := rand.New(rand.NewSource(0x1A6E5))
	_, _ = rng.Read(buf)
	return buf
}

// writeBenchAssetTree materialises a client asset tree under root: immutable
// chunks nested three to four directories deep, a handful of top-level HTML
// entry points, and a static/images subtree of already-compressed media.
func writeBenchAssetTree(tb testing.TB, root string) {
	tb.Helper()

	pool := benchAssetPool(2 << 20)
	rng := rand.New(rand.NewSource(0xA55E75))

	// ~15% of the files are images; the rest are compressible text sharing
	// the byte budget.
	images := benchAssetFiles * 15 / 100
	text := benchAssetFiles - images
	avg := benchAssetBytes / text

	slice := func(n int) []byte {
		if n >= len(pool) {
			n = len(pool) - 1
		}
		off := rng.Intn(len(pool) - n)
		return pool[off : off+n]
	}

	write := func(rel string, content []byte) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			tb.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			tb.Fatalf("write %s: %v", p, err)
		}
	}

	exts := []string{".js", ".js", ".js", ".mjs", ".css", ".json", ".svg", ".html"}
	for i := 0; i < text; i++ {
		ext := exts[i%len(exts)]
		// Size varies by roughly an order of magnitude around the mean, the
		// way real chunk output does: many small route chunks, a few large
		// vendor bundles.
		size := avg/2 + rng.Intn(avg)
		if i%40 == 0 {
			size = avg * 8
		}
		var rel string
		switch ext {
		case ".html":
			rel = fmt.Sprintf("route-%d/index.html", i)
		case ".css":
			rel = fmt.Sprintf("_app/immutable/assets/style-%d.css", i)
		default:
			rel = fmt.Sprintf("_app/immutable/chunks/nested/chunk-%d%s", i, ext)
		}
		write(rel, slice(size))
	}

	imgPool := benchImagePool(512 << 10)
	for i := 0; i < images; i++ {
		size := 8<<10 + rng.Intn(48<<10)
		write(fmt.Sprintf("static/images/gallery/photo-%d.webp", i), imgPool[:size])
	}
}

// countSidecars reports how many .gz/.br/.zst sidecars exist under root, which
// is what proves the benchmark below actually compressed something.
func countSidecars(tb testing.TB, root string) int {
	tb.Helper()
	var n int
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".gz", ".br", ".zst":
			n++
		}
		return nil
	})
	if err != nil {
		tb.Fatalf("walk %s: %v", root, err)
	}
	return n
}

// benchModTime is the pinned build epoch a real build passes through. It never
// reaches a sidecar's mtime (see PrecompressFile's doc comment) but it is what
// the production call site supplies.
var benchModTime = time.Unix(1700000000, 0).UTC()

// BenchmarkPrecompressDirectory measures generating gzip/brotli/zstd sidecars
// for a client asset tree, in the two states a real build hits:
//
//	cold  — a freshly built output directory with no sidecars, which is what
//	        the first platform of a multi-platform build pays. The tree is
//	        rebuilt from scratch between iterations with the timer stopped so
//	        every measured iteration really is cold.
//	fresh — every sidecar already present and newer than its source, which is
//	        what every subsequent platform of a multi-platform build pays. It
//	        writes nothing, but it is NOT free: PrecompressFile os.ReadFile's
//	        each source in full before it consults isStale, so this path still
//	        moves the whole tree through memory. That is precisely the sort of
//	        thing this baseline exists to make visible.
//
// The cold path is dominated by brotli at BestCompression (~0.5 MB/s), so one
// iteration takes tens of seconds and b.N will be 1. That is the real cost a
// build pays, not a fixture artefact — do not shrink the tree to make the
// number look better.
func BenchmarkPrecompressDirectory(b *testing.B) {
	opts := precompressutils.PrecompressOptions{Gzip: true, Brotli: true, Zstd: true}

	b.Run("cold", func(b *testing.B) {
		root := filepath.Join(b.TempDir(), "client")
		writeBenchAssetTree(b, root)

		b.SetBytes(benchAssetBytes)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := precompressutils.PrecompressDirectory(root, benchModTime, opts); err != nil {
				b.Fatalf("PrecompressDirectory: %v", err)
			}
			b.StopTimer()
			if n := countSidecars(b, root); n == 0 {
				b.Fatal("degenerate fixture: cold run produced no sidecars")
			}
			// A fresh, sidecar-free tree for the next iteration. Building it
			// under a stopped timer keeps fixture setup out of the measurement.
			if err := os.RemoveAll(root); err != nil {
				b.Fatalf("reset tree: %v", err)
			}
			writeBenchAssetTree(b, root)
			b.StartTimer()
		}
	})

	b.Run("already_fresh", func(b *testing.B) {
		root := filepath.Join(b.TempDir(), "client")
		writeBenchAssetTree(b, root)

		// Warm the tree once, outside the measurement, so every measured
		// iteration takes the "sidecar already fresh, rewrite nothing" path.
		if err := precompressutils.PrecompressDirectory(root, benchModTime, opts); err != nil {
			b.Fatalf("warm PrecompressDirectory: %v", err)
		}
		before := countSidecars(b, root)
		if before == 0 {
			b.Fatal("degenerate fixture: warm-up produced no sidecars")
		}
		b.Logf("warmed tree: %d sidecars", before)

		b.SetBytes(benchAssetBytes)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := precompressutils.PrecompressDirectory(root, benchModTime, opts); err != nil {
				b.Fatalf("PrecompressDirectory: %v", err)
			}
		}
		b.StopTimer()
		if after := countSidecars(b, root); after != before {
			b.Fatalf("already-fresh path rewrote the tree: %d sidecars before, %d after", before, after)
		}
	})
}
