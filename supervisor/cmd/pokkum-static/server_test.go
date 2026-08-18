package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTree creates files under t.TempDir()/<rel> with the given contents.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

func newTestServer(t *testing.T, roots ...string) *staticServer {
	t.Helper()
	// Use a discarding logger to keep output quiet.
	return newStaticServer(roots, "", nil)
}

// newTestServerFallback builds a test server with an opt-in SPA fallback file
// path configured.
func newTestServerFallback(t *testing.T, roots []string, fallback string, log *slog.Logger) *staticServer {
	t.Helper()
	return newStaticServer(roots, fallback, log)
}

func TestStaticServer_ServesIndexFromRoot(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html":           "<h1>home</h1>",
		"about.html":           "<h1>about</h1>",
		"assets/app-abc123.js": "console.log(1)",
	})
	srv := newTestServer(t, root)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "<h1>home</h1>" {
		t.Errorf("GET / body = %q", got)
	}
	if ct := rec.Header().Get("Cache-Control"); ct != "no-cache" {
		t.Errorf("index Cache-Control = %q, want no-cache", ct)
	}
}

func TestStaticServer_ServesFile(t *testing.T) {
	root := writeTree(t, map[string]string{"about.html": "<h1>about</h1>"})
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/about.html", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /about.html = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "<h1>about</h1>" {
		t.Errorf("body = %q", rec.Body.String())
	}
	// HTML should not be immutable.
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", cc)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("expected an ETag header")
	}
}

func TestStaticServer_ImmutableHeadersForHashedAndImmutableAssets(t *testing.T) {
	root := writeTree(t, map[string]string{
		"_app/immutable/chunk-9f2a3b.css": "body{}",
		"assets/app-1234abc.js":           "x",
		"assets/plain.js":                 "y",
	})
	srv := newTestServer(t, root)
	immutable := "public, max-age=31536000, immutable"

	cases := []struct {
		path string
		want string
	}{
		{"/_app/immutable/chunk-9f2a3b.css", immutable},
		{"/assets/app-1234abc.js", immutable},
		{"/assets/plain.js", "no-cache"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if got := rec.Header().Get("Cache-Control"); got != c.want {
			t.Errorf("GET %s Cache-Control = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestStaticServer_ContentEncodingNegotiation(t *testing.T) {
	root := writeTree(t, map[string]string{
		"app.js": "hello world hello world",
	})
	// Create sidecars manually (simulating precompressutils output).
	if err := os.WriteFile(filepath.Join(root, "app.js.gz"), []byte("gzdata"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app.js.br"), []byte("brdata"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, root)

	cases := []struct {
		name    string
		header  string
		wantEnc string
		wantLen string
	}{
		{"brotli preferred", "gzip, br", "br", "6"},
		{"gzip only", "gzip", "gzip", "6"},
		{"gzip q=0 excluded", "gzip;q=0, br", "br", "6"},
		{"none accepted -> identity", "identity", "", "23"},
		{"no header -> identity", "", "", "23"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
			if c.header != "" {
				req.Header.Set("Accept-Encoding", c.header)
			}
			srv.handler().ServeHTTP(rec, req)
			if got := rec.Header().Get("Content-Encoding"); got != c.wantEnc {
				t.Errorf("Content-Encoding = %q, want %q", got, c.wantEnc)
			}
			if got := rec.Header().Get("Vary"); c.wantEnc != "" && !strings.Contains(got, "Accept-Encoding") {
				t.Errorf("Vary = %q, want to include Accept-Encoding", got)
			}
			if got := rec.Header().Get("Content-Length"); got != c.wantLen {
				t.Errorf("Content-Length = %q, want %q", got, c.wantLen)
			}
		})
	}
}

func TestStaticServer_RangeRequest(t *testing.T) {
	content := "0123456789"
	root := writeTree(t, map[string]string{"data.txt": content})
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/data.txt", nil)
	req.Header.Set("Range", "bytes=2-5")
	srv.handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("GET with Range = %d, want 206", rec.Code)
	}
	if got := rec.Body.String(); got != "2345" {
		t.Errorf("range body = %q, want 2345", got)
	}
	if cr := rec.Header().Get("Content-Range"); cr != "bytes 2-5/10" {
		t.Errorf("Content-Range = %q, want bytes 2-5/10", cr)
	}
	if got := rec.Header().Get("Content-Length"); got != "4" {
		t.Errorf("Content-Length = %q, want 4", got)
	}
}

func TestStaticServer_RangeUnsatisfiable(t *testing.T) {
	root := writeTree(t, map[string]string{"data.txt": "0123456789"})
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/data.txt", nil)
	req.Header.Set("Range", "bytes=100-200")
	srv.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("unsatisfiable range = %d, want 416", rec.Code)
	}
	if cr := rec.Header().Get("Content-Range"); cr != "bytes */10" {
		t.Errorf("Content-Range = %q, want bytes */10", cr)
	}
}

func TestStaticServer_IfRange(t *testing.T) {
	root := writeTree(t, map[string]string{"data.txt": "0123456789"})
	srv := newTestServer(t, root)

	// Obtain the ETag from a full GET.
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/data.txt", nil))
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	// Matching If-Range -> 206.
	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/data.txt", nil)
	req.Header.Set("Range", "bytes=0-3")
	req.Header.Set("If-Range", etag)
	srv.handler().ServeHTTP(rec2, req)
	if rec2.Code != http.StatusPartialContent {
		t.Errorf("matching If-Range = %d, want 206", rec2.Code)
	}

	// Mismatched If-Range -> full 200.
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodGet, "/data.txt", nil)
	req3.Header.Set("Range", "bytes=0-3")
	req3.Header.Set("If-Range", `"deadbeef"`)
	srv.handler().ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("mismatched If-Range = %d, want 200", rec3.Code)
	}
	if rec3.Body.String() != "0123456789" {
		t.Errorf("full body after If-Range miss = %q", rec3.Body.String())
	}
}

func TestStaticServer_MethodNotAllowed(t *testing.T) {
	root := writeTree(t, map[string]string{"data.txt": "x"})
	srv := newTestServer(t, root)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/data.txt", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rec.Code)
	}
}

func TestStaticServer_PathTraversalRejected(t *testing.T) {
	root := writeTree(t, map[string]string{"data.txt": "secret"})
	srv := newTestServer(t, root)

	for _, p := range []string{
		"/../data.txt",
		"/..%2Fdata.txt",
		"/a/../../data.txt",
	} {
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("traversal path %q was served (200)", p)
		}
	}
}

func TestStaticServer_SymlinkEscapeRejected(t *testing.T) {
	outside := writeTree(t, map[string]string{"leak.txt": "boom"})
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("fine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "leak.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/link.txt", nil))
	// A symlink escaping the root must not serve the outside file.
	if rec.Body.String() == "boom" {
		t.Errorf("symlink escaped root and served outside content")
	}
}

func TestStaticServer_MultipleRootsFallThrough(t *testing.T) {
	rootA := writeTree(t, map[string]string{"a.txt": "AAA"})
	rootB := writeTree(t, map[string]string{"b.txt": "BBB"})
	srv := newTestServer(t, rootA, rootB)

	for path, want := range map[string]string{"/a.txt": "AAA", "/b.txt": "BBB"} {
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Body.String() != want {
			t.Errorf("GET %s = %q, want %q", path, rec.Body.String(), want)
		}
	}
}

func TestStaticServer_HeadRequest(t *testing.T) {
	root := writeTree(t, map[string]string{"a.txt": "hello"})
	srv := newTestServer(t, root)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodHead, "/a.txt", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("HEAD = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD body should be empty, got %d bytes", rec.Body.Len())
	}
}

func TestStaticServer_404(t *testing.T) {
	root := writeTree(t, map[string]string{"a.txt": "x"})
	srv := newTestServer(t, root)
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missing.txt", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("missing = %d, want 404", rec.Code)
	}
}

func TestStaticServer_Fallback_OnMiss(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<h1>home</h1>"})
	fallback := filepath.Join(root, "index.html")
	srv := newTestServerFallback(t, []string{root}, fallback, nil)

	// An unknown route gets the fallback shell with 200, not a 404.
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/some/client/route", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown route with fallback = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "<h1>home</h1>" {
		t.Errorf("fallback body = %q, want the fallback shell content", got)
	}
}

func TestStaticServer_Fallback_NotForExistingRoute(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html": "<h1>home</h1>",
		"about.html": "<h1>about</h1>",
	})
	fallback := filepath.Join(root, "index.html")
	srv := newTestServerFallback(t, []string{root}, fallback, nil)

	// An existing route must still serve its own file, not the fallback.
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/about.html", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("existing route = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "<h1>about</h1>" {
		t.Errorf("existing route body = %q, want its own content, not the fallback", got)
	}
}

func TestStaticServer_Fallback_MethodNotAllowed(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<h1>home</h1>"})
	fallback := filepath.Join(root, "index.html")
	srv := newTestServerFallback(t, []string{root}, fallback, nil)

	// A non-GET/HEAD method to an unknown route must still be 405, never a
	// 200 fallback — the fallback concept only applies to GET/HEAD.
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, httptest.NewRequest(method, "/unknown", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s unknown route = %d, want 405", method, rec.Code)
		}
	}
}

func TestStaticServer_Fallback_TraversalRejected(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<h1>home</h1>"})
	fallback := filepath.Join(root, "index.html")
	srv := newTestServerFallback(t, []string{root}, fallback, nil)

	// A traversal-requested unknown route must never be served the fallback:
	// the fallback serves on a *clean* route miss only.
	for _, p := range []string{"/..", "/../index.html", "/..%2Findex.html", "/a/../../index.html"} {
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("traversal path %q was served fallback (200)", p)
		}
	}
}

func TestStaticServer_Fallback_OutsideRootRejected(t *testing.T) {
	outside := writeTree(t, map[string]string{"index.html": "<h1>outside</h1>"})
	root := writeTree(t, map[string]string{"index.html": "<h1>inside</h1>"})

	// A fallback path that resolves outside the served root must be rejected
	// at construction (fallback disabled, Warn logged), never served.
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	srv := newTestServerFallback(t, []string{root}, filepath.Join(outside, "index.html"), logger)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown route with out-of-root fallback = %d, want 404 (fallback rejected)", rec.Code)
	}
	if rec.Body.String() == "<h1>outside</h1>" {
		t.Errorf("served fallback file from outside the served root; must be rejected")
	}
	if !strings.Contains(buf.String(), "outside every served root") {
		t.Errorf("expected a construction-time Warn about the out-of-root fallback, got: %s", buf.String())
	}
}

func TestStaticServer_Fallback_NegotiationWithSidecar(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": strings.Repeat("SPA shell ", 8)})
	// A real gzip sidecar for the fallback, simulating precompressutils.
	if err := os.WriteFile(filepath.Join(root, "index.html.gz"), []byte("gzbytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	fallback := filepath.Join(root, "index.html")
	srv := newTestServerFallback(t, []string{root}, fallback, nil)

	// The fallback must honour Content-Encoding negotiation like any file.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/unknown/route", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	srv.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown route with fallback (gzip) = %d, want 200", rec.Code)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Errorf("fallback Content-Encoding = %q, want gzip", enc)
	}
	if got := rec.Body.String(); got != "gzbytes" {
		t.Errorf("fallback gzip body = %q, want the gzip sidecar bytes", got)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("fallback response should carry an ETag")
	}
}

func TestStaticServer_Fallback_DefaultStill404(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<h1>home</h1>"})
	// No fallback configured (default behavior preserved).
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown route without fallback = %d, want 404", rec.Code)
	}
	// The 404 body must remain the clean default — no dev-marker HTML comment.
	if body := rec.Body.String(); strings.Contains(body, "<!--") || strings.Contains(body, "SPA") {
		t.Errorf("unset-fallback 404 body leaked a dev marker: %q", body)
	}
	if got := rec.Body.String(); got == "" || strings.Contains(got, "pokkum") {
		t.Errorf("404 body = %q, want the standard clean 404 page", got)
	}
}

func TestStaticServer_Fallback_DeletedAtRequestTimeIsMiss(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<h1>home</h1>"})
	fallback := filepath.Join(root, "index.html")
	srv := newTestServerFallback(t, []string{root}, fallback, nil)

	// Serve normally while the fallback exists.
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown route with fallback = %d, want 200", rec.Code)
	}

	// Delete the fallback file: the configured fallback is no longer a regular
	// file, so an unmatched route must be an honest 404 miss, never a 500.
	if err := os.Remove(fallback); err != nil {
		t.Fatal(err)
	}
	rec2 := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	if rec2.Code != http.StatusNotFound {
		t.Errorf("unknown route after fallback deleted = %d, want 404 (miss, not 500)", rec2.Code)
	}
}

func TestStaticServer_Fallback_WarnOnceOn404WhenUnset(t *testing.T) {
	root := writeTree(t, map[string]string{"a.txt": "x"})
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	// Each server owns its own one-per-server (one-per-process) once, so a
	// fresh instance warns exactly once regardless of prior servers/tests.
	srv := newStaticServer([]string{root}, "", logger)

	// Extensionless: looks like a client-side route, so the SPA-fallback
	// hint is the relevant remedy here (see
	// TestStaticServer_404Hint_SkippedForAssetLikePath for the asset-like
	// case, which must NOT get this hint).
	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missing-route", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("iteration %d = %d, want 404", i, rec.Code)
		}
	}
	if got := strings.Count(buf.String(), "SPA fallback is available"); got != 1 {
		t.Errorf("warned %d time(s), want exactly 1 (one-per-process)", got)
	}
}

// TestStaticServer_Fallback_SymlinkSwapAfterConstructionRejected is a
// regression test for the TOCTOU gap where fallbackFileOK only re-checked
// os.Stat at serve time, not the same EvalSymlinks + root-containment check
// configureFallback performs at construction. If the file at the validated
// fallback path is later replaced by a symlink pointing outside every served
// root (e.g. an emptyDir mount swap), the server must fall back to an honest
// 404 rather than serving the escaped content.
func TestStaticServer_Fallback_SymlinkSwapAfterConstructionRejected(t *testing.T) {
	outside := writeTree(t, map[string]string{"secret.html": "<h1>secret</h1>"})
	root := writeTree(t, map[string]string{"index.html": "<h1>home</h1>"})
	fallback := filepath.Join(root, "index.html")

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	srv := newTestServerFallback(t, []string{root}, fallback, logger)

	// Sanity: the fallback works normally before the swap.
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("before swap: unknown route = %d, want 200", rec.Code)
	}

	// Simulate a mount swap after construction: the file at the validated
	// fallback path becomes a symlink escaping the served root.
	if err := os.Remove(fallback); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.html"), fallback); err != nil {
		t.Fatal(err)
	}

	rec2 := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	if rec2.Code != http.StatusNotFound {
		t.Errorf("after symlink swap outside root: unknown route = %d, want 404 (miss)", rec2.Code)
	}
	if strings.Contains(rec2.Body.String(), "secret") {
		t.Errorf("served content from outside the root after a symlink swap: %q", rec2.Body.String())
	}
}

// TestStaticServer_Fallback_BrokenLogsDistinctMessageOnce is a regression
// test for warnFallbackOnce firing the wrong ("you might want this feature")
// message when a fallback WAS configured but broke at serve time. The
// broken-fallback case must log its own distinct message, exactly once, and
// must never emit the discovery message meant for the "nothing configured"
// case.
func TestStaticServer_Fallback_BrokenLogsDistinctMessageOnce(t *testing.T) {
	root := writeTree(t, map[string]string{"index.html": "<h1>home</h1>"})
	fallback := filepath.Join(root, "index.html")
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	srv := newTestServerFallback(t, []string{root}, fallback, logger)

	if err := os.Remove(fallback); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unknown", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("iteration %d = %d, want 404", i, rec.Code)
		}
	}

	logged := buf.String()
	if got := strings.Count(logged, "no longer resolves to a valid file"); got != 1 {
		t.Errorf("broken-fallback warning logged %d time(s), want exactly 1 (one-per-process): %s", got, logged)
	}
	if strings.Contains(logged, "SPA fallback is available via") {
		t.Errorf("the unconfigured-mode discovery message fired for a broken, already-configured fallback: %s", logged)
	}
}

// TestStaticServer_Fallback_HashLookingFilenameStillNoCache is a regression
// test for the fallback shell getting a year-long immutable Cache-Control
// purely because its configured filename happens to match SvelteKit's
// "name-<hash>.ext" convention (e.g. fallback: 'fallback-abc123.html'). The
// fallback is an SPA entry point that must revalidate on every deploy.
func TestStaticServer_Fallback_HashLookingFilenameStillNoCache(t *testing.T) {
	root := writeTree(t, map[string]string{
		"fallback-abc123.html": "<h1>spa shell</h1>",
	})
	fallback := filepath.Join(root, "fallback-abc123.html")
	srv := newTestServerFallback(t, []string{root}, fallback, nil)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/some/unknown/route", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown route with fallback = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("hash-looking fallback filename Cache-Control = %q, want no-cache (never immutable)", cc)
	}
}

// TestCachePolicy_FallbackForcesNoCacheEvenWhenHashLooking directly exercises
// cachePolicy's isFallback override, and confirms the override is actually
// load-bearing (the same path without it would get immutable caching).
func TestCachePolicy_FallbackForcesNoCacheEvenWhenHashLooking(t *testing.T) {
	path := "/app/client/fallback-abc123.html"
	if got := cachePolicy(path, true); got != "no-cache" {
		t.Errorf("cachePolicy(%q, isFallback=true) = %q, want no-cache", path, got)
	}
	if got := cachePolicy(path, false); got == "no-cache" {
		t.Errorf("cachePolicy(%q, isFallback=false) = %q, expected the hash-looking heuristic to apply here (otherwise this test no longer proves the override matters)", path, got)
	}
}

// TestStaticServer_ExtensionlessHTMLFallback_BugRepro is the direct
// regression test for the reported bug: @sveltejs/adapter-static with its
// default trailingSlash: 'never' prerenders route /about to a flat file
// named about.html. A request for the extensionless route /about must find
// it via the new "<rel>.html" candidate. Before the fix, tryServe only knew
// about an exact-file match and a directory+index.html match, so this
// request 404'd despite the file being present on disk.
func TestStaticServer_ExtensionlessHTMLFallback_BugRepro(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html": "<h1>home</h1>",
		"about.html": "<h1>about</h1>",
	})
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/about", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /about = %d, want 200 (about.html should be found via the .html fallback)", rec.Code)
	}
	if got := rec.Body.String(); got != "<h1>about</h1>" {
		t.Errorf("GET /about body = %q, want %q", got, "<h1>about</h1>")
	}
	// A prerendered page is not content-hashed: it must revalidate, never be
	// cached as immutable.
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("GET /about Cache-Control = %q, want no-cache", cc)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("GET /about: expected an ETag header, same as any other served file")
	}
}

// TestStaticServer_HTMLFallback_ExactFileTakesPrecedence verifies candidate
// precedence: when a literal, extensionless file happens to exist at the
// exact request path, it wins over an "<rel>.html" sibling with different
// content — the most specific, literal match always wins.
func TestStaticServer_HTMLFallback_ExactFileTakesPrecedence(t *testing.T) {
	root := writeTree(t, map[string]string{
		"about":      "exact-file-content",
		"about.html": "<h1>about</h1>",
	})
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/about", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /about = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "exact-file-content" {
		t.Errorf("GET /about body = %q, want the exact file's content, not the .html sibling", got)
	}
}

// TestStaticServer_HTMLFallback_BeatsEmptyNestedRouteDirectory covers the
// realistic multi-page adapter-static shape: routes /about and /about/team
// both exist, so the build emits about.html AND an about/ directory (which
// exists only to hold team.html, with no index.html of its own). A request
// for /about must still serve about.html rather than 404 from a failed
// directory-index lookup — the ".html" candidate must be tried before, and
// win over, an existing-but-empty directory-index candidate.
func TestStaticServer_HTMLFallback_BeatsEmptyNestedRouteDirectory(t *testing.T) {
	root := writeTree(t, map[string]string{
		"about.html":      "<h1>about</h1>",
		"about/team.html": "<h1>team</h1>",
	})
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/about", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /about = %d, want 200 (about.html should win over the empty about/ directory)", rec.Code)
	}
	if got := rec.Body.String(); got != "<h1>about</h1>" {
		t.Errorf("GET /about body = %q, want %q", got, "<h1>about</h1>")
	}

	// Sanity: the nested route itself still resolves normally.
	rec2 := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/about/team", nil))
	if rec2.Code != http.StatusOK {
		t.Fatalf("GET /about/team = %d, want 200", rec2.Code)
	}
	if got := rec2.Body.String(); got != "<h1>team</h1>" {
		t.Errorf("GET /about/team body = %q, want %q", got, "<h1>team</h1>")
	}
}

// TestStaticServer_HTMLFallback_DirectoryIndexStillLowestPrecedence is a
// regression test for adapter-static's OTHER config, trailingSlash:
// 'always', which emits only "<route>/index.html" (no flat "<route>.html"
// sibling at all). The directory+index.html candidate must still work when
// it is the only candidate that exists.
func TestStaticServer_HTMLFallback_DirectoryIndexStillLowestPrecedence(t *testing.T) {
	root := writeTree(t, map[string]string{
		"about/index.html": "<h1>about (trailing slash)</h1>",
	})
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/about", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /about = %d, want 200 (directory index should still be found)", rec.Code)
	}
	if got := rec.Body.String(); got != "<h1>about (trailing slash)</h1>" {
		t.Errorf("GET /about body = %q, want %q", got, "<h1>about (trailing slash)</h1>")
	}
}

// TestStaticServer_HTMLFallback_MultiRootOrdering verifies the multi-root
// precedence decision: an earlier root wins in its entirety over a later
// root, regardless of which candidate type matched within it. Root A only
// has an "about.html" (.html candidate); root B has a literal "about" exact
// file. Because root A is listed first in POKKUM_STATIC_ROOTS order, its
// .html-resolved hit must win over root B's exact-file hit.
func TestStaticServer_HTMLFallback_MultiRootOrdering(t *testing.T) {
	rootA := writeTree(t, map[string]string{"about.html": "<h1>from root A</h1>"})
	rootB := writeTree(t, map[string]string{"about": "from root B (exact file)"})
	srv := newTestServer(t, rootA, rootB)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/about", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /about = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "<h1>from root A</h1>" {
		t.Errorf("GET /about body = %q, want root A's .html hit to win over root B's exact-file hit", got)
	}
}

// TestStaticServer_HTMLFallback_TraversalRejected is an empirical
// containment proof (Serena mem:self_review_checklist row 22: prove nothing
// was written/served outside the root, don't just assert an error came
// back) that the new ".html" candidate goes through the exact same
// EvalSymlinks + withinRoot checks as the pre-existing candidates. A
// symlink named "escape.html" pointing outside the served root must never
// be served through the new candidate.
func TestStaticServer_HTMLFallback_TraversalRejected(t *testing.T) {
	outside := writeTree(t, map[string]string{"secret.html": "<h1>top secret</h1>"})
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<h1>home</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.html"), filepath.Join(root, "escape.html")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/escape", nil))
	if rec.Code == http.StatusOK {
		t.Fatalf("GET /escape = 200, want the symlink escape to be refused")
	}
	if strings.Contains(rec.Body.String(), "top secret") {
		t.Fatalf("the .html candidate served content from outside the root via a symlink escape: %q", rec.Body.String())
	}

	// Also prove plain traversal segments through the new candidate's
	// construction (rel + ".html") can't escape either, e.g. a request
	// whose path, if naively suffixed, could reach outside the root.
	for _, p := range []string{"/../secret", "/..%2Fsecret", "/a/../../secret"} {
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("traversal path %q was served (200) via the .html candidate", p)
		}
		if strings.Contains(rec.Body.String(), "top secret") {
			t.Errorf("traversal path %q leaked outside-root content: %q", p, rec.Body.String())
		}
	}
}

// TestStaticServer_HTMLFallback_RangeAndETagSupported verifies a page
// resolved through the new ".html" candidate gets exactly the same
// negotiation as a directly-requested file (Range/ETag), not a
// stripped-down code path.
func TestStaticServer_HTMLFallback_RangeAndETagSupported(t *testing.T) {
	root := writeTree(t, map[string]string{"about.html": "0123456789"})
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/about", nil)
	req.Header.Set("Range", "bytes=2-5")
	srv.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("GET /about with Range = %d, want 206", rec.Code)
	}
	if got := rec.Body.String(); got != "2345" {
		t.Errorf("range body = %q, want 2345", got)
	}
	if rec.Header().Get("ETag") == "" {
		t.Error("expected an ETag header on a .html-fallback-resolved response")
	}
}

// TestStaticServer_404Hint_SkippedForAssetLikePath is a regression test for
// the misleading-hint bug: the SPA-fallback discovery hint must not fire for
// a request path that looks like a missing static asset (has a file
// extension) — an SPA fallback is never the relevant remedy for a missing
// .js/.css/.png etc, only for an unmatched extensionless client route.
func TestStaticServer_404Hint_SkippedForAssetLikePath(t *testing.T) {
	root := writeTree(t, map[string]string{"a.txt": "x"})
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	srv := newStaticServer([]string{root}, "", logger)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/missing-asset.js", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing asset = %d, want 404", rec.Code)
	}
	if strings.Contains(buf.String(), "SPA fallback is available") {
		t.Errorf("the SPA-fallback discovery hint fired for an asset-like path (has a file extension); it should only fire for an extensionless route-like miss, got log: %s", buf.String())
	}
}

// TestStaticServer_404Hint_ShownForRouteLikePath complements the above: an
// extensionless path (plausible client-side route) still gets the
// discovery hint exactly once.
func TestStaticServer_404Hint_ShownForRouteLikePath(t *testing.T) {
	root := writeTree(t, map[string]string{"a.txt": "x"})
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	srv := newStaticServer([]string{root}, "", logger)

	for i := 0; i < 3; i++ {
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/some/client/route", nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("iteration %d: missing route = %d, want 404", i, rec.Code)
		}
	}
	if got := strings.Count(buf.String(), "SPA fallback is available"); got != 1 {
		t.Errorf("route-like miss: hint logged %d time(s), want exactly 1", got)
	}
}
