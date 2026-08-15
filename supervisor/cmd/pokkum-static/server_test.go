package main

import (
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
	return newStaticServer(roots, nil)
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
