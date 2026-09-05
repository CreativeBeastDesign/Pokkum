package main

import (
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Baseline benchmarks for the per-request static-serving path.
//
// Every request today pays: cleanRelPath, then for each configured root up to
// three filepath.EvalSymlinks calls (exact path, "<rel>.html" sibling,
// directory index.html) each followed by an os.Stat, then pickEncoding's up to
// three more os.Stat calls probing .br/.gz/.zst sidecars, then the ETag lookup
// and the os.Open + copy. The planned change replaces the EvalSymlinks/Stat
// pair with an os.Root-scoped open and the sidecar probes with a startup index,
// so the shapes below are chosen to separate those two costs:
//
//   - a hit in the FIRST root with a sidecar present   (1 probe, hits)
//   - the same file with no Accept-Encoding            (0 probes)
//   - a .png, an extension that never has a sidecar    (3 probes, all miss)
//   - a hit in the SECOND root                         (a full miss then a hit)
//   - a 404                                            (every candidate misses)
//   - a 304                                            (resolve + negotiate, no body)
//
// The fixture mirrors how Pokkum lays a real image out: two roots, /app/client
// (hashed immutable assets, with .br/.gz sidecars for text and none for images)
// and /app/prerendered (flat adapter-static HTML with sidecars), a few hundred
// files in total so any per-request directory work is visible.

// benchStaticFixture is the built two-root tree plus the request shapes.
type benchStaticFixture struct {
	clientRoot      string
	prerenderedRoot string
	files           int

	jsPath   string // request path of a hashed .js asset that HAS a .br sidecar
	pngPath  string // request path of a .png (never has a sidecar)
	htmlPath string // extensionless route served from the SECOND root
	jsETag   string // ETag of the js asset as served with full Accept-Encoding
}

// benchFullAccept is what a current browser actually sends.
const benchFullAccept = "gzip, deflate, br, zstd"

// benchStaticName produces a stable, content-hash-looking basename.
func benchStaticName(prefix string, i int) string {
	return fmt.Sprintf("%s-%08x", prefix, uint32(i)*2654435761)
}

// benchStaticWrite writes size bytes at path (creating parents), with a
// per-file seed prefix so no two files are byte-identical.
func benchStaticWrite(b *testing.B, path string, size int, filler []byte, seed uint64) {
	b.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if size < 8 {
		size = 8
	}
	buf := make([]byte, size)
	binary.LittleEndian.PutUint64(buf[:8], seed)
	for off := 8; off < size; off += len(filler) {
		copy(buf[off:], filler)
	}
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		b.Fatalf("write %s: %v", path, err)
	}
}

// buildBenchStaticFixture materialises the two-root tree. Sidecars are written
// for text assets only (js/css/html), at ~30% of the source size — the server
// never decodes them, it only stats and streams them, so plausible bytes are
// enough and real compression would only slow fixture setup.
func buildBenchStaticFixture(b *testing.B, base string) benchStaticFixture {
	b.Helper()

	clientRoot := filepath.Join(base, "client")
	prerenderedRoot := filepath.Join(base, "prerendered")

	filler := make([]byte, 8192)
	rng := rand.New(rand.NewPCG(0x73746174, 0x69630001)) // fixed seed
	for i := range filler {
		filler[i] = byte(rng.Uint32())
	}

	files := 0
	seed := uint64(0)
	write := func(path string, size int, sidecars bool) {
		seed++
		benchStaticWrite(b, path, size, filler, seed)
		files++
		if !sidecars {
			return
		}
		for _, ext := range []string{".br", ".gz", ".zst"} {
			seed++
			benchStaticWrite(b, path+ext, size*3/10, filler, seed)
			files++
		}
	}

	// client/_app/immutable — hashed JS chunks, entry points, CSS (all with
	// sidecars) and images (never sidecars).
	var jsRel, pngRel string
	for i := 0; i < 150; i++ {
		rel := filepath.Join("_app", "immutable", "chunks", benchStaticName("chunk", i)+".js")
		write(filepath.Join(clientRoot, rel), 4096+i*137, true)
		if i == 75 {
			jsRel = filepath.ToSlash(rel)
		}
	}
	for i := 0; i < 10; i++ {
		write(filepath.Join(clientRoot, "_app", "immutable", "entry", benchStaticName("entry", i)+".js"), 20000+i*311, true)
	}
	for i := 0; i < 30; i++ {
		write(filepath.Join(clientRoot, "_app", "immutable", "assets", benchStaticName("style", i)+".css"), 12000+i*97, true)
	}
	for i := 0; i < 40; i++ {
		rel := filepath.Join("_app", "immutable", "assets", benchStaticName("image", i)+".png")
		write(filepath.Join(clientRoot, rel), 40000+i*1024, false)
		if i == 20 {
			pngRel = filepath.ToSlash(rel)
		}
	}
	write(filepath.Join(clientRoot, "favicon.png"), 5000, false)
	write(filepath.Join(clientRoot, "robots.txt"), 200, false)

	// prerendered — adapter-static's flat "<route>.html" output.
	write(filepath.Join(prerenderedRoot, "index.html"), 9000, true)
	write(filepath.Join(prerenderedRoot, "about.html"), 7000, true)
	for i := 0; i < 100; i++ {
		write(filepath.Join(prerenderedRoot, "blog", fmt.Sprintf("post-%d.html", i)), 6000+i*53, true)
	}

	return benchStaticFixture{
		clientRoot:      clientRoot,
		prerenderedRoot: prerenderedRoot,
		files:           files,
		jsPath:          "/" + jsRel,
		pngPath:         "/" + pngRel,
		// Extensionless: candidate 1 misses in both roots, candidate 2
		// ("about.html") hits in the second — the real adapter-static shape.
		htmlPath: "/about",
	}
}

// benchDiscardWriter is an http.ResponseWriter that keeps a real per-request
// header map (so header work is measured) but throws the body away. Used
// instead of httptest.ResponseRecorder inside the timed loop deliberately: the
// recorder's bytes.Buffer growth for a 40 KB asset would dominate B/op and
// dilute exactly the syscall-level cost these benchmarks exist to track. The
// file is still opened and fully read — io.Copy runs for real.
type benchDiscardWriter struct {
	header http.Header
	code   int
}

func newBenchDiscardWriter() *benchDiscardWriter {
	return &benchDiscardWriter{header: make(http.Header, 8)}
}

func (w *benchDiscardWriter) Header() http.Header         { return w.header }
func (w *benchDiscardWriter) Write(p []byte) (int, error) { return len(p), nil }
func (w *benchDiscardWriter) WriteHeader(code int)        { w.code = code }

// BenchmarkStaticServer_Request measures one full trip through the handler for
// each request shape the planned optimisation affects differently.
func BenchmarkStaticServer_Request(b *testing.B) {
	fx := buildBenchStaticFixture(b, b.TempDir())
	// "a few hundred files" is part of the point: per-request directory work
	// only shows up against a realistically populated tree.
	if fx.files < 300 {
		b.Fatalf("fixture has %d files, want at least 300", fx.files)
	}
	srv := newStaticServer([]string{fx.clientRoot, fx.prerenderedRoot}, "", nil)
	defer srv.close()
	h := srv.handler()

	// Grab the ETag the server actually serves for the js asset under full
	// Accept-Encoding (i.e. the ETag of the .br sidecar), so the 304 case below
	// is a genuine match rather than a fabricated tag that would 200.
	probe := httptest.NewRecorder()
	probeReq := httptest.NewRequest(http.MethodGet, fx.jsPath, nil)
	probeReq.Header.Set("Accept-Encoding", benchFullAccept)
	h.ServeHTTP(probe, probeReq)
	if probe.Code != http.StatusOK {
		b.Fatalf("fixture probe GET %s = %d, want 200", fx.jsPath, probe.Code)
	}
	fx.jsETag = probe.Header().Get("ETag")
	if fx.jsETag == "" {
		b.Fatal("fixture probe returned no ETag")
	}
	if enc := probe.Header().Get("Content-Encoding"); enc != "br" {
		b.Fatalf("fixture probe Content-Encoding = %q, want br (sidecar not being picked up)", enc)
	}

	cases := []struct {
		name     string
		path     string
		headers  map[string]string
		wantCode int
	}{
		{
			// First root, sidecar present and accepted: one .br probe, hits.
			name:     "immutable_js_br_sidecar",
			path:     fx.jsPath,
			headers:  map[string]string{"Accept-Encoding": benchFullAccept},
			wantCode: http.StatusOK,
		},
		{
			// Same file, identity: pickEncoding does zero stats.
			name:     "immutable_js_no_accept_encoding",
			path:     fx.jsPath,
			wantCode: http.StatusOK,
		},
		{
			// PNG: three sidecar stats, all guaranteed misses.
			name:     "png_three_sidecar_misses",
			path:     fx.pngPath,
			headers:  map[string]string{"Accept-Encoding": benchFullAccept},
			wantCode: http.StatusOK,
		},
		{
			// Miss in root 1, "<rel>.html" hit in root 2.
			name:     "prerendered_html_second_root",
			path:     fx.htmlPath,
			headers:  map[string]string{"Accept-Encoding": benchFullAccept},
			wantCode: http.StatusOK,
		},
		{
			// Every candidate in every root misses. Path carries an extension
			// so the 404 does not also take the fallback-hint logging branch.
			name:     "not_found",
			path:     "/no/such/asset-deadbeef.js",
			headers:  map[string]string{"Accept-Encoding": benchFullAccept},
			wantCode: http.StatusNotFound,
		},
		{
			// Full resolve + negotiate + ETag, short-circuited before any body.
			name:     "conditional_304",
			path:     fx.jsPath,
			headers:  map[string]string{"Accept-Encoding": benchFullAccept, "If-None-Match": fx.jsETag},
			wantCode: http.StatusNotModified,
		},
	}

	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			// The request is built once and reused: the handler only reads it,
			// and per-iteration httptest.NewRequest allocation is request
			// plumbing, not server work.
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}

			// Prove the shape really is the shape claimed before timing it —
			// a 404 silently standing in for an intended 200 would make this
			// benchmark measure nothing.
			check := newBenchDiscardWriter()
			h.ServeHTTP(check, req)
			if check.code != c.wantCode {
				b.Fatalf("GET %s = %d, want %d", c.path, check.code, c.wantCode)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				h.ServeHTTP(newBenchDiscardWriter(), req)
			}
		})
	}
}
