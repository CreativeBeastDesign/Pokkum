package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/precompressutils"
)

// This file exercises staticServer against a fixture tree that faithfully
// mirrors the real static-strategy build output shape (client/app.js,
// client/immutable/chunks/<hash>.js, client/favicon.svg,
// prerendered/index.html) with REAL .gz/.br/.zst sidecars produced by the
// same production compression code the build pipeline uses
// (internal/adapters/precompressutils), rather than hand-faked bytes.
//
// Design note: tests/integration (which drives the full core.Build pipeline
// and could extract real layer bytes via ExtractLayerFiles) cannot be
// reused here directly — it's a separate package/process, and
// t.TempDir() output from one `go test` binary doesn't survive into
// another. Splitting this into two test binaries synchronized via a fixed
// on-disk path would add an ordering dependency (this package's test only
// passing if the other package's test ran first) for no real benefit.
// Building an equivalent fixture directly in this package, using the real
// precompressutils code (which does NOT import internal/ports, so it's
// safe for this deliberately-isolated binary's *test-only* dependency
// graph — it never ships in the production pokkum-static binary, since
// _test.go files aren't compiled into non-test builds), is simpler, has no
// cross-process ordering dependency, and stays entirely within this
// package's existing testing conventions (see writeTree/newTestServer in
// server_test.go, reused below).

// fixtureModTime is a fixed, non-"now" timestamp used only to make
// PrecompressDirectory's mtime-based staleness check deterministic across
// test runs. It has no bearing on OCI reproducibility (this is test-fixture
// generation, not a build adapter producing image layers).
var fixtureModTime = time.Unix(1735689600, 0).UTC() // 2025-01-01T00:00:00Z

// staticIntegrationFixture holds a real, on-disk static-server fixture tree
// (two roots: client + prerendered) plus the original, uncompressed content
// of each file, keyed by its URL path, for round-trip assertions.
type staticIntegrationFixture struct {
	clientRoot      string
	prerenderedRoot string

	appJSContent     []byte // served at /app.js
	immutableContent []byte // served at /immutable/chunks/app-a1b2c3d4.js
	faviconContent   []byte // served at /favicon.svg
	indexContent     []byte // served at /index.html (and at / via multi-root fallthrough)
}

// buildStaticIntegrationFixture writes a real fixture tree under t.TempDir()
// and precompresses it with the actual production precompressutils code
// (real gzip/brotli/zstd, not hand-rolled bytes), then sanity-checks that
// every expected sidecar was actually produced before handing control to
// the caller.
func buildStaticIntegrationFixture(t *testing.T) staticIntegrationFixture {
	t.Helper()

	root := t.TempDir()
	clientRoot := filepath.Join(root, "client")
	prerenderedRoot := filepath.Join(root, "prerendered")
	immutableDir := filepath.Join(clientRoot, "immutable", "chunks")

	if err := os.MkdirAll(immutableDir, 0o755); err != nil {
		t.Fatalf("mkdir immutable dir: %v", err)
	}
	if err := os.MkdirAll(prerenderedRoot, 0o755); err != nil {
		t.Fatalf("mkdir prerendered dir: %v", err)
	}

	// Content must be >=64 bytes with genuine repetition/redundancy —
	// precompressutils.PrecompressFile skips files under 64 bytes outright
	// and only keeps a sidecar that achieves real positive savings
	// (buf.Len() < origSize). Random/incompressible content of this size
	// would silently produce no sidecars and the fixture would be wrong.
	appJS := bytes.Repeat([]byte("console.log('fixture client bundle');\n"), 20)
	immutableJS := bytes.Repeat([]byte("export const CHUNK_ID = 'a1b2c3d4'; console.log('pokkum immutable chunk');\n"), 25)
	favicon := bytes.Repeat([]byte("<svg xmlns=\"http://www.w3.org/2000/svg\"><circle r=\"1\"/></svg>\n"), 20)
	indexHTML := bytes.Repeat([]byte("<!doctype html><html><body><h1>Pokkum Static Fixture</h1></body></html>\n"), 20)

	writeFile := func(p string, content []byte) {
		t.Helper()
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}

	appJSPath := filepath.Join(clientRoot, "app.js")
	immutableJSPath := filepath.Join(immutableDir, "app-a1b2c3d4.js")
	faviconPath := filepath.Join(clientRoot, "favicon.svg")
	indexPath := filepath.Join(prerenderedRoot, "index.html")

	writeFile(appJSPath, appJS)
	writeFile(immutableJSPath, immutableJS)
	writeFile(faviconPath, favicon)
	writeFile(indexPath, indexHTML)

	// Real compression via the same production code path the build pipeline
	// uses (internal/adapters/precompressutils), not hand-faked bytes.
	if err := precompressutils.PrecompressDirectory(clientRoot, fixtureModTime); err != nil {
		t.Fatalf("PrecompressDirectory(client): %v", err)
	}
	if err := precompressutils.PrecompressDirectory(prerenderedRoot, fixtureModTime); err != nil {
		t.Fatalf("PrecompressDirectory(prerendered): %v", err)
	}

	for _, base := range []string{appJSPath, immutableJSPath, faviconPath, indexPath} {
		for _, ext := range []string{".gz", ".br", ".zst"} {
			if fi, err := os.Stat(base + ext); err != nil {
				t.Fatalf("expected real sidecar %s to exist (precompressutils produced none): %v", base+ext, err)
			} else if fi.Size() == 0 {
				t.Fatalf("sidecar %s is empty", base+ext)
			}
		}
	}

	return staticIntegrationFixture{
		clientRoot:       clientRoot,
		prerenderedRoot:  prerenderedRoot,
		appJSContent:     appJS,
		immutableContent: immutableJS,
		faviconContent:   favicon,
		indexContent:     indexHTML,
	}
}

// mustGunzip, mustUnbrotli and mustUnzstd decompress a real sidecar's bytes
// so callers can assert the round trip against the original content, not
// just check headers.
func mustGunzip(t *testing.T, data []byte) []byte {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer r.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("gunzip read: %v", err)
	}
	return out
}

func mustUnbrotli(t *testing.T, data []byte) []byte {
	t.Helper()
	out, err := io.ReadAll(brotli.NewReader(bytes.NewReader(data)))
	if err != nil {
		t.Fatalf("brotli read: %v", err)
	}
	return out
}

func mustUnzstd(t *testing.T, data []byte) []byte {
	t.Helper()
	dec, err := zstd.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("zstd.NewReader: %v", err)
	}
	defer dec.Close()
	out, err := io.ReadAll(dec)
	if err != nil {
		t.Fatalf("zstd read: %v", err)
	}
	return out
}

func TestStaticServerIntegration_PlainGETServesRealUncompressedContent(t *testing.T) {
	fx := buildStaticIntegrationFixture(t)
	srv := newTestServer(t, fx.clientRoot, fx.prerenderedRoot)

	// GET /app.js with no Accept-Encoding: identity content, byte-for-byte
	// equal to the real source file (not a sidecar).
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /app.js = %d, want 200", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), fx.appJSContent) {
		t.Errorf("GET /app.js body does not match real source content")
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want empty for identity response", enc)
	}
	if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len(fx.appJSContent)) {
		t.Errorf("Content-Length = %q, want %d", cl, len(fx.appJSContent))
	}

	// GET / falls through client root (no index.html there) to the
	// prerendered root's index.html — the real multi-root convention
	// (POKKUM_STATIC_ROOTS default /app/client:/app/prerendered).
	rec2 := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec2.Code)
	}
	if !bytes.Equal(rec2.Body.Bytes(), fx.indexContent) {
		t.Errorf("GET / body does not match real prerendered index.html content")
	}
}

func TestStaticServerIntegration_ContentEncodingRoundTrip(t *testing.T) {
	fx := buildStaticIntegrationFixture(t)
	srv := newTestServer(t, fx.clientRoot, fx.prerenderedRoot)

	cases := []struct {
		name           string
		acceptEncoding string
		wantEnc        string
		sidecarExt     string
		decompress     func(*testing.T, []byte) []byte
	}{
		{"gzip", "gzip", "gzip", ".gz", mustGunzip},
		{"brotli", "br", "br", ".br", mustUnbrotli},
		{"zstd", "zstd", "zstd", ".zst", mustUnzstd},
	}

	appJSOnDisk := filepath.Join(fx.clientRoot, "app.js")

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
			req.Header.Set("Accept-Encoding", c.acceptEncoding)
			srv.handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("GET /app.js (Accept-Encoding: %s) = %d, want 200", c.acceptEncoding, rec.Code)
			}
			if got := rec.Header().Get("Content-Encoding"); got != c.wantEnc {
				t.Fatalf("Content-Encoding = %q, want %q", got, c.wantEnc)
			}
			if v := rec.Header().Get("Vary"); !strings.Contains(v, "Accept-Encoding") {
				t.Errorf("Vary = %q, want to include Accept-Encoding", v)
			}

			// The response body must be the REAL sidecar bytes written to
			// disk by precompressutils — not a re-derived or synthetic
			// value.
			wantSidecarBytes, err := os.ReadFile(appJSOnDisk + c.sidecarExt)
			if err != nil {
				t.Fatalf("read real sidecar %s: %v", appJSOnDisk+c.sidecarExt, err)
			}
			if !bytes.Equal(rec.Body.Bytes(), wantSidecarBytes) {
				t.Errorf("response body does not match the real on-disk %s sidecar bytes", c.sidecarExt)
			}

			// Round-trip proof: decompressing the served bytes must
			// reproduce the exact original, uncompressed source content.
			got := c.decompress(t, rec.Body.Bytes())
			if !bytes.Equal(got, fx.appJSContent) {
				t.Errorf("decompressed (%s) body does not round-trip to the original app.js content", c.name)
			}
		})
	}
}

func TestStaticServerIntegration_RangeRequestOnRealFixtureFile(t *testing.T) {
	fx := buildStaticIntegrationFixture(t)
	srv := newTestServer(t, fx.clientRoot, fx.prerenderedRoot)

	size := int64(len(fx.immutableContent))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/immutable/chunks/app-a1b2c3d4.js", nil)
	req.Header.Set("Range", "bytes=0-9")
	srv.handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("GET with Range = %d, want 206", rec.Code)
	}
	wantBody := fx.immutableContent[0:10]
	if !bytes.Equal(rec.Body.Bytes(), wantBody) {
		t.Errorf("range body = %q, want %q", rec.Body.Bytes(), wantBody)
	}
	wantContentRange := "bytes 0-9/" + strconv.Itoa(int(size))
	if cr := rec.Header().Get("Content-Range"); cr != wantContentRange {
		t.Errorf("Content-Range = %q, want %q", cr, wantContentRange)
	}
	if cl := rec.Header().Get("Content-Length"); cl != "10" {
		t.Errorf("Content-Length = %q, want 10", cl)
	}
}

func TestStaticServerIntegration_StrongETagAndIfRange(t *testing.T) {
	fx := buildStaticIntegrationFixture(t)
	srv := newTestServer(t, fx.clientRoot, fx.prerenderedRoot)

	// Obtain the real ETag from a full GET.
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /app.js = %d, want 200", rec.Code)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("expected an ETag header")
	}
	if strings.HasPrefix(etag, "W/") {
		t.Errorf("ETag = %q, want a strong ETag (no W/ prefix)", etag)
	}

	// Matching If-Range against a Range request -> 206 with the real partial
	// content.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	req2.Header.Set("Range", "bytes=0-3")
	req2.Header.Set("If-Range", etag)
	srv.handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusPartialContent {
		t.Errorf("matching If-Range = %d, want 206", rec2.Code)
	}
	if !bytes.Equal(rec2.Body.Bytes(), fx.appJSContent[0:4]) {
		t.Errorf("If-Range partial body = %q, want %q", rec2.Body.Bytes(), fx.appJSContent[0:4])
	}

	// Mismatched If-Range -> full 200 with the real, complete source
	// content (precondition fails, so the Range is ignored).
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	req3.Header.Set("Range", "bytes=0-3")
	req3.Header.Set("If-Range", `"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"`)
	srv.handler().ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("mismatched If-Range = %d, want 200", rec3.Code)
	}
	if !bytes.Equal(rec3.Body.Bytes(), fx.appJSContent) {
		t.Errorf("full body after If-Range miss does not match real source content")
	}
}

func TestStaticServerIntegration_ImmutableCacheControlOnlyForImmutableAsset(t *testing.T) {
	fx := buildStaticIntegrationFixture(t)
	srv := newTestServer(t, fx.clientRoot, fx.prerenderedRoot)

	const immutable = "public, max-age=31536000, immutable"

	cases := []struct {
		path string
		want string
	}{
		{"/immutable/chunks/app-a1b2c3d4.js", immutable},
		{"/favicon.svg", "no-cache"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", c.path, rec.Code)
		}
		if got := rec.Header().Get("Cache-Control"); got != c.want {
			t.Errorf("GET %s Cache-Control = %q, want %q", c.path, got, c.want)
		}
	}
}
