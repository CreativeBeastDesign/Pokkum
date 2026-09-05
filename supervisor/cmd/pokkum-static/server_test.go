package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	return newTestServerFallback(t, roots, "", nil)
}

// newTestServerFallback builds a test server with an opt-in SPA fallback file
// path configured.
//
// Every server built here is closed on test cleanup: a staticServer now holds
// one open os.Root descriptor per served root for its whole life (that is what
// makes a request cost one openat), so a suite building dozens of servers
// would otherwise accumulate descriptors. Production never closes — the roots
// are meant to outlive every request.
func newTestServerFallback(t *testing.T, roots []string, fallback string, log *slog.Logger) *staticServer {
	t.Helper()
	s := newStaticServer(roots, fallback, log)
	t.Cleanup(s.close)
	return s
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

// TestStaticServer_IfNoneMatch_MatchingReturns304NoBody is the direct
// regression/repro test for the reported bug: the server computes and sends
// a strong ETag on every response but had no conditional-GET path at all, so
// a client presenting the current ETag via If-None-Match still got a full
// 200 with a body every time. This test must fail with 200 against the
// pre-fix code (confirmed before implementing) and pass with 304 and an
// empty body afterward.
func TestStaticServer_IfNoneMatch_MatchingReturns304NoBody(t *testing.T) {
	root := writeTree(t, map[string]string{"data.txt": "0123456789"})
	srv := newTestServer(t, root)

	// First request to learn the current strong ETag.
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/data.txt", nil))
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on first response")
	}

	// Re-request presenting that exact ETag via If-None-Match.
	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/data.txt", nil)
	req.Header.Set("If-None-Match", etag)
	srv.handler().ServeHTTP(rec2, req)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("matching If-None-Match = %d, want 304", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", rec2.Body.String())
	}
	if got := rec2.Header().Get("ETag"); got != etag {
		t.Errorf("304 ETag = %q, want %q (must still carry the validator)", got, etag)
	}
	if cl := rec2.Header().Get("Content-Length"); cl != "" {
		t.Errorf("304 Content-Length = %q, want absent", cl)
	}
}

// TestStaticServer_IfNoneMatch_NonMatchingReturns200WithBody verifies a
// non-matching If-None-Match value is simply ignored: the client gets the
// normal full 200 response with its body, exactly as if the header were
// absent.
func TestStaticServer_IfNoneMatch_NonMatchingReturns200WithBody(t *testing.T) {
	root := writeTree(t, map[string]string{"data.txt": "0123456789"})
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/data.txt", nil)
	req.Header.Set("If-None-Match", `"deadbeef"`)
	srv.handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("non-matching If-None-Match = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "0123456789" {
		t.Errorf("body = %q, want the full content", rec.Body.String())
	}
}

// TestStaticServer_IfNoneMatch_StarMatchesExistingFile verifies the "*"
// wildcard matches any current representation, so a request for a file that
// does exist gets 304 regardless of the (irrelevant) actual ETag value.
func TestStaticServer_IfNoneMatch_StarMatchesExistingFile(t *testing.T) {
	root := writeTree(t, map[string]string{"data.txt": "0123456789"})
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/data.txt", nil)
	req.Header.Set("If-None-Match", "*")
	srv.handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotModified {
		t.Fatalf(`If-None-Match: * on an existing file = %d, want 304`, rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", rec.Body.String())
	}
}

// TestStaticServer_IfNoneMatch_ListContainingCurrentTagMatches verifies
// If-None-Match's list form: a comma-separated set of entity-tags where any
// one matching is sufficient, not just a single exact match.
func TestStaticServer_IfNoneMatch_ListContainingCurrentTagMatches(t *testing.T) {
	root := writeTree(t, map[string]string{"data.txt": "0123456789"})
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/data.txt", nil))
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/data.txt", nil)
	req.Header.Set("If-None-Match", `"aaaaaaaa", `+etag+`, "bbbbbbbb"`)
	srv.handler().ServeHTTP(rec2, req)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("list containing current tag = %d, want 304", rec2.Code)
	}
}

// TestStaticServer_IfNoneMatch_WeakPrefixedRequestTagStillMatches verifies
// weak comparison: a request tag prefixed "W/" must still match an otherwise
// equal tag value, even though this server itself only ever emits strong
// tags — the tolerance is for what arrives FROM the client (e.g. a cache or
// proxy that downgraded a stored strong tag to weak).
func TestStaticServer_IfNoneMatch_WeakPrefixedRequestTagStillMatches(t *testing.T) {
	root := writeTree(t, map[string]string{"data.txt": "0123456789"})
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/data.txt", nil))
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/data.txt", nil)
	req.Header.Set("If-None-Match", "W/"+etag)
	srv.handler().ServeHTTP(rec2, req)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("weak-prefixed matching If-None-Match = %d, want 304", rec2.Code)
	}
}

// TestStaticServer_IfNoneMatch_BeatsRange is the ordering regression test
// mandated by RFC 9110 §13.1.2: If-None-Match is evaluated BEFORE Range/
// If-Range. A request carrying both a matching conditional and a Range
// header must 304, never fall through to a 206.
func TestStaticServer_IfNoneMatch_BeatsRange(t *testing.T) {
	root := writeTree(t, map[string]string{"data.txt": "0123456789"})
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/data.txt", nil))
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/data.txt", nil)
	req.Header.Set("Range", "bytes=2-5")
	req.Header.Set("If-None-Match", etag)
	srv.handler().ServeHTTP(rec2, req)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("matching If-None-Match + Range = %d, want 304 (not 206)", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", rec2.Body.String())
	}
	if cr := rec2.Header().Get("Content-Range"); cr != "" {
		t.Errorf("304 Content-Range = %q, want absent", cr)
	}
}

// TestStaticServer_IfNoneMatch_HeadConsistentWithGet verifies HEAD gets the
// same conditional-GET treatment as GET: a matching If-None-Match on a HEAD
// request must also 304 with no body.
func TestStaticServer_IfNoneMatch_HeadConsistentWithGet(t *testing.T) {
	root := writeTree(t, map[string]string{"data.txt": "0123456789"})
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/data.txt", nil))
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	rec2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodHead, "/data.txt", nil)
	req.Header.Set("If-None-Match", etag)
	srv.handler().ServeHTTP(rec2, req)

	if rec2.Code != http.StatusNotModified {
		t.Fatalf("HEAD with matching If-None-Match = %d, want 304", rec2.Code)
	}
	if rec2.Body.Len() != 0 {
		t.Errorf("304 body = %q, want empty", rec2.Body.String())
	}

	// A non-matching HEAD still gets 200 with an empty body (HEAD never
	// carries a body regardless of status), matching TestStaticServer_HeadRequest.
	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest(http.MethodHead, "/data.txt", nil)
	req3.Header.Set("If-None-Match", `"deadbeef"`)
	srv.handler().ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Errorf("HEAD with non-matching If-None-Match = %d, want 200", rec3.Code)
	}
}

// TestStaticServer_IfNoneMatch_RangeAndIfRangeUnaffectedWhenAbsent is a
// non-regression check for the pre-existing, tested Range/If-Range behaviour
// (see TestStaticServer_RangeRequest and TestStaticServer_IfRange): with no
// If-None-Match header at all, Range and If-Range must behave exactly as
// before this change.
func TestStaticServer_IfNoneMatch_RangeAndIfRangeUnaffectedWhenAbsent(t *testing.T) {
	root := writeTree(t, map[string]string{"data.txt": "0123456789"})
	srv := newTestServer(t, root)

	// Plain Range, no conditional headers at all -> 206.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/data.txt", nil)
	req.Header.Set("Range", "bytes=2-5")
	srv.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("plain Range = %d, want 206", rec.Code)
	}
	if rec.Body.String() != "2345" {
		t.Errorf("range body = %q, want 2345", rec.Body.String())
	}

	// Range + matching If-Range, no If-None-Match -> still 206.
	etag := rec.Header().Get("ETag")
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/data.txt", nil)
	req2.Header.Set("Range", "bytes=2-5")
	req2.Header.Set("If-Range", etag)
	srv.handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusPartialContent {
		t.Fatalf("Range + matching If-Range (no If-None-Match) = %d, want 206", rec2.Code)
	}
}

// TestStaticServer_IfNoneMatch_NonMatchingWithRangeStill206 proves the two
// checks compose correctly rather than one unconditionally overriding the
// other: a non-matching If-None-Match must fall through to normal Range
// handling, not force a 200 (or any other status) regardless of Range.
func TestStaticServer_IfNoneMatch_NonMatchingWithRangeStill206(t *testing.T) {
	root := writeTree(t, map[string]string{"data.txt": "0123456789"})
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/data.txt", nil)
	req.Header.Set("If-None-Match", `"deadbeef"`)
	req.Header.Set("Range", "bytes=2-5")
	srv.handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("non-matching If-None-Match + Range = %d, want 206", rec.Code)
	}
	if got := rec.Body.String(); got != "2345" {
		t.Errorf("range body = %q, want 2345", got)
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
	srv := newTestServerFallback(t, []string{root}, "", logger)

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
// back) that the ".html" candidate goes through the exact same containment
// check as the other candidates — openInRoot, i.e. os.Root's per-component
// openat (it went through EvalSymlinks + withinRoot when written). A
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
	srv := newTestServerFallback(t, []string{root}, "", logger)

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
	srv := newTestServerFallback(t, []string{root}, "", logger)

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

// --- os.Root containment (Change 2) --------------------------------------

// TestStaticServer_SymlinkEscapeShapesAllRejected is the containment proof for
// the switch from EvalSymlinks + withinRoot to os.Root. It enumerates the
// shapes a symlink can take rather than the single absolute-target case the
// pre-existing TestStaticServer_SymlinkEscapeRejected covered, because os.Root
// refuses them by a different mechanism (per-component openat/O_NOFOLLOW in
// the kernel) and each shape has to be shown to still be refused:
//
//   - an absolute symlink to a file outside the root
//   - a RELATIVE symlink climbing out with ".."
//   - a symlink to a DIRECTORY outside the root, requested through
//   - the same, reached through the "<rel>.html" candidate
//   - the same, reached through a directory's index.html candidate
//
// Every case asserts the outside content is not in the body — an empirical
// containment check, not merely "an error came back"
// (mem:self_review_checklist row 22).
func TestStaticServer_SymlinkEscapeShapesAllRejected(t *testing.T) {
	outside := writeTree(t, map[string]string{
		"leak.txt":       "OUTSIDE-SECRET",
		"secret.html":    "OUTSIDE-SECRET",
		"sub/index.html": "OUTSIDE-SECRET",
	})
	root := writeTree(t, map[string]string{"ok.txt": "fine"})

	links := map[string]string{
		"abs.txt":     filepath.Join(outside, "leak.txt"),           // absolute target
		"rel.txt":     "../" + filepath.Base(outside) + "/leak.txt", // relative climb-out
		"escape.html": filepath.Join(outside, "secret.html"),        // reached via ".html"
		"outdir":      outside,                                      // directory symlink
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
			t.Fatalf("creating symlink %s: this containment test must never be silently skipped (checklist rows 39/47): %v", name, err)
		}
	}

	srv := newTestServer(t, root)
	// The relative link's target only resolves if outside is a sibling of
	// root; both come from t.TempDir() under the same parent, so it is.
	for _, p := range []string{
		"/abs.txt",
		"/rel.txt",
		"/escape",      // the "<rel>.html" candidate
		"/escape.html", // the exact-path candidate
		"/outdir/leak.txt",
		"/outdir/sub", // directory symlink + index.html candidate
		"/outdir/sub/index.html",
	} {
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("GET %s = 200, want the escape to be refused", p)
		}
		if strings.Contains(rec.Body.String(), "OUTSIDE-SECRET") {
			t.Errorf("GET %s leaked content from outside the root: %q", p, rec.Body.String())
		}
	}

	// And the root still serves its own files, so the refusals above are not
	// a server that simply serves nothing (checklist row 47).
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ok.txt", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "fine" {
		t.Fatalf("GET /ok.txt = %d %q, want 200 \"fine\" — the escape assertions above prove nothing if the server serves nothing", rec.Code, rec.Body.String())
	}
}

// TestStaticServer_CachedDirHandleStillRefusesEscape guards the directory
// handle cache specifically (servedRoot.dirs). Once a directory has served a
// file, later requests resolve one component through the cached sub-root
// instead of walking from the top-level root — a different code path, and
// therefore a place a containment bypass could hide. A symlink escaping the
// root from inside a WARMED directory must still be refused.
func TestStaticServer_CachedDirHandleStillRefusesEscape(t *testing.T) {
	outside := writeTree(t, map[string]string{"leak.txt": "OUTSIDE-SECRET"})
	root := writeTree(t, map[string]string{"deep/a/b/real.txt": "real"})
	if err := os.Symlink(filepath.Join(outside, "leak.txt"), filepath.Join(root, "deep", "a", "b", "esc.txt")); err != nil {
		t.Fatalf("creating the test symlink failed; this containment test must never be silently skipped: %v", err)
	}
	srv := newTestServer(t, root)

	// Warm the cache for deep/a/b by serving a real file out of it.
	rec := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/deep/a/b/real.txt", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("warming request = %d, want 200", rec.Code)
	}
	if h := srv.roots[0].cachedDir("deep/a/b"); h == nil {
		t.Fatal("directory handle was not cached after a successful serve; this test would then exercise the uncached path and prove nothing (checklist row 45)")
	}

	// Now the escape, resolved through that cached handle.
	rec2 := httptest.NewRecorder()
	srv.handler().ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/deep/a/b/esc.txt", nil))
	if rec2.Code == http.StatusOK {
		t.Errorf("escaping symlink served 200 through a cached directory handle")
	}
	if strings.Contains(rec2.Body.String(), "OUTSIDE-SECRET") {
		t.Errorf("cached directory handle leaked outside-root content: %q", rec2.Body.String())
	}

	// A ".." inside the final component cannot be smuggled either: os.Root
	// refuses it even one component deep.
	if _, err := srv.roots[0].cachedDir("deep/a/b").Open("../real.txt"); err == nil {
		t.Error("cached directory handle followed \"..\" out of its directory")
	}
}

// TestStaticServer_DirHandleCacheOnlyPopulatedByHits pins the property that
// keeps the cache's key space bounded and out of an attacker's reach: a 404
// must never mint an entry.
//
// Two shapes, because only one of them is ordering-sensitive and the first
// alone would be a decoration:
//
//   - a miss into a directory that does not exist. This cannot cache under any
//     ordering, since OpenRoot on the missing directory fails — pinned because
//     it is the property, not because the code could plausibly break it.
//   - a miss into a directory that DOES exist. This is the real one: caching
//     before knowing the open succeeded would mint an entry (and pay an extra
//     OpenRoot) for every 404 under any real directory.
func TestStaticServer_DirHandleCacheOnlyPopulatedByHits(t *testing.T) {
	root := writeTree(t, map[string]string{"real/a.txt": "a"})
	srv := newTestServer(t, root)
	sr := srv.roots[0]

	get := func(p string, wantCode int) {
		t.Helper()
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != wantCode {
			t.Fatalf("GET %s = %d, want %d", p, rec.Code, wantCode)
		}
	}

	for i := 0; i < 50; i++ {
		get("/nope"+strconv.Itoa(i)+"/deep"+strconv.Itoa(i)+"/x.js", http.StatusNotFound)
	}
	if n := sr.dirCount.Load(); n != 0 {
		t.Errorf("misses into absent directories cached %d handles, want 0", n)
	}

	// Misses inside a directory that really exists.
	for i := 0; i < 50; i++ {
		get("/real/missing"+strconv.Itoa(i)+".js", http.StatusNotFound)
	}
	if n := sr.dirCount.Load(); n != 0 {
		t.Errorf("misses inside an existing directory cached %d handles, want 0 — entries must be minted only by a successful open", n)
	}

	// A hit does populate it, so the assertions above are not just "the cache
	// never works" (checklist row 47).
	get("/real/a.txt", http.StatusOK)
	if n := sr.dirCount.Load(); n != 1 {
		t.Errorf("after one hit dirCount = %d, want 1", n)
	}
	if sr.cachedDir("real") == nil {
		t.Error("hit did not cache a handle for its directory")
	}
}

// TestStaticServer_DirHandleCacheRespectsCap pins the file-descriptor bound.
// Every cached entry costs an fd, and a container's limit is commonly 1024, so
// the cache must stop growing at maxCachedDirHandles — and, past the cap, must
// keep serving correctly by resolving from the top-level root again.
func TestStaticServer_DirHandleCacheRespectsCap(t *testing.T) {
	const dirs = maxCachedDirHandles + 20
	files := make(map[string]string, dirs)
	for i := 0; i < dirs; i++ {
		files["d"+strconv.Itoa(i)+"/f.txt"] = "body" + strconv.Itoa(i)
	}
	root := writeTree(t, files)
	srv := newTestServer(t, root)
	sr := srv.roots[0]

	for i := 0; i < dirs; i++ {
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/d"+strconv.Itoa(i)+"/f.txt", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /d%d/f.txt = %d, want 200 (serving must not depend on the cache having room)", i, rec.Code)
		}
		if want := "body" + strconv.Itoa(i); rec.Body.String() != want {
			t.Fatalf("GET /d%d/f.txt body = %q, want %q — past the cap the uncached path must still serve the RIGHT file", i, rec.Body.String(), want)
		}
	}
	n := sr.dirCount.Load()
	if n > maxCachedDirHandles {
		t.Errorf("dirCount = %d, exceeds the cap of %d; each entry is a file descriptor", n, maxCachedDirHandles)
	}
	if n != maxCachedDirHandles {
		t.Errorf("dirCount = %d, want the cap %d to have actually been reached — otherwise this test never exercised the over-cap path", n, maxCachedDirHandles)
	}
}

// --- Sidecar index (Change 3) --------------------------------------------

// TestStaticServer_SidecarIndexServesIndexedSidecar is the positive case the
// staleness tests below need in order to mean anything: an indexed sidecar is
// actually chosen, and its bytes are what the ETag describes.
func TestStaticServer_SidecarIndexServesIndexedSidecar(t *testing.T) {
	root := writeTree(t, map[string]string{
		"app.js":    "IDENTITY-BYTES",
		"app.js.br": "BROTLI-BYTES",
	})
	srv := newTestServer(t, root)
	if !srv.roots[0].sidecarsIndexed {
		t.Fatal("sidecar index was not built; every assertion below would then be testing the stat-probe fallback")
	}
	if got := srv.roots[0].sidecars["app.js"]; got&sidecarBrotli == 0 {
		t.Fatalf("index mask for app.js = %b, want the brotli bit set", got)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	req.Header.Set("Accept-Encoding", "br, gzip")
	srv.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /app.js = %d, want 200", rec.Code)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "br" {
		t.Errorf("Content-Encoding = %q, want br", enc)
	}
	if rec.Body.String() != "BROTLI-BYTES" {
		t.Errorf("body = %q, want the sidecar bytes", rec.Body.String())
	}
}

// TestStaticServer_StaleSidecarIndexFallsBackToIdentity is the correctness
// guard for making the index a startup snapshot. If a sidecar the index
// recorded has since gone, the request must serve the IDENTITY file — the
// right bytes, no Content-Encoding, and an ETag over what was actually sent —
// never a 500 and never a truncated or mislabelled body.
//
// This is the failure mode a filesystem cache has to be shown to survive: the
// index says "there is a .br here" and there is not.
func TestStaticServer_StaleSidecarIndexFallsBackToIdentity(t *testing.T) {
	root := writeTree(t, map[string]string{
		"app.js":    "IDENTITY-BYTES",
		"app.js.br": "BROTLI-BYTES",
	})
	srv := newTestServer(t, root)

	// The index recorded the sidecar at startup...
	if got := srv.roots[0].sidecars["app.js"]; got&sidecarBrotli == 0 {
		t.Fatal("fixture did not index the .br sidecar; the staleness assertion below would be vacuous")
	}
	// ...and now it is gone, without the index knowing.
	if err := os.Remove(filepath.Join(root, "app.js.br")); err != nil {
		t.Fatal(err)
	}
	if got := srv.roots[0].sidecars["app.js"]; got&sidecarBrotli == 0 {
		t.Fatal("index self-healed; this test must exercise a genuinely stale entry")
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	req.Header.Set("Accept-Encoding", "br, gzip")
	srv.handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /app.js with a stale index entry = %d, want 200 serving identity", rec.Code)
	}
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want empty — a stale index entry must not label an identity body as compressed", enc)
	}
	if rec.Body.String() != "IDENTITY-BYTES" {
		t.Errorf("body = %q, want the identity bytes", rec.Body.String())
	}
	// The ETag must describe the bytes actually served, or a cache will pair
	// this body with the sidecar's validator.
	sum := sha256.Sum256([]byte("IDENTITY-BYTES"))
	if want := `"` + hex.EncodeToString(sum[:]) + `"`; rec.Header().Get("ETag") != want {
		t.Errorf("ETag = %s, want %s (the hash of the bytes served)", rec.Header().Get("ETag"), want)
	}
	if cl := rec.Header().Get("Content-Length"); cl != strconv.Itoa(len("IDENTITY-BYTES")) {
		t.Errorf("Content-Length = %q, want %d", cl, len("IDENTITY-BYTES"))
	}
}

// TestStaticServer_IndexNeverGatesPrimaryAssetExistence pins the scoping rule
// that makes a startup snapshot safe at all: the index records SIDECARS only.
// A primary asset that appears after startup must still be served, because its
// existence is decided by an openat at request time, not by the index.
//
// A sidecar appearing after startup is, symmetrically, simply not used —
// suboptimal, never incorrect.
func TestStaticServer_IndexNeverGatesPrimaryAssetExistence(t *testing.T) {
	root := writeTree(t, map[string]string{"seed.js": "seed"})
	srv := newTestServer(t, root)

	// A primary asset created after the index was built.
	if err := os.WriteFile(filepath.Join(root, "late.js"), []byte("LATE"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/late.js", nil)
	req.Header.Set("Accept-Encoding", "br, gzip")
	srv.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /late.js = %d, want 200 — the sidecar index must never turn a present file into a 404", rec.Code)
	}
	if rec.Body.String() != "LATE" {
		t.Errorf("body = %q, want LATE", rec.Body.String())
	}

	// A sidecar created after the index was built: identity, not a 500.
	if err := os.WriteFile(filepath.Join(root, "late.js.br"), []byte("BR"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/late.js", nil)
	req2.Header.Set("Accept-Encoding", "br")
	srv.handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || rec2.Body.String() != "LATE" {
		t.Errorf("GET /late.js after a late sidecar = %d %q, want 200 with the identity body", rec2.Code, rec2.Body.String())
	}
	if enc := rec2.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want empty for an unindexed sidecar", enc)
	}
}

// TestStaticServer_NonCompressibleExtensionSkipsSidecarProbe pins the
// extension gate, which is a deliberate behaviour change as well as the
// optimisation: precompression only ever emits sidecars for
// precompressibleExtensions, so a ".png.br" cannot exist in an image Pokkum
// built. A hand-placed one is therefore NOT served — the request stops at the
// extension check before any lookup happens.
func TestStaticServer_NonCompressibleExtensionSkipsSidecarProbe(t *testing.T) {
	root := writeTree(t, map[string]string{
		"pic.png":    "PNGBYTES",
		"pic.png.br": "BRBYTES",
		"app.js":     "JSBYTES",
		"app.js.br":  "JSBR",
	})
	srv := newTestServer(t, root)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/pic.png", nil)
	req.Header.Set("Accept-Encoding", "br, gzip, zstd")
	srv.handler().ServeHTTP(rec, req)
	if enc := rec.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("png Content-Encoding = %q, want empty: .png is not an extension precompression emits sidecars for", enc)
	}
	if rec.Body.String() != "PNGBYTES" {
		t.Errorf("png body = %q, want the identity bytes", rec.Body.String())
	}

	// The gate must not be "never negotiate": an eligible extension still does.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	req2.Header.Set("Accept-Encoding", "br")
	srv.handler().ServeHTTP(rec2, req2)
	if enc := rec2.Header().Get("Content-Encoding"); enc != "br" {
		t.Fatalf("js Content-Encoding = %q, want br — the extension gate has swallowed everything", enc)
	}
}

// --- Cache-correctness headers (Change 4) --------------------------------

// TestStaticServer_VaryOnEveryEligibleAsset is the cache-correctness guard.
// Vary: Accept-Encoding used to be sent only when a sidecar was actually
// chosen, so the identity representation of a compressible asset went out
// without it. A shared cache is then entitled to serve that stored identity
// response — and its identity ETag — to a brotli-capable client, whose
// If-None-Match can never match the brotli body the origin would serve, so
// every revalidation costs a full 200 instead of a 304.
//
// The header must therefore be present on every sidecar-ELIGIBLE response
// (compressed, identity, and 304 alike) and absent on assets that can never
// have a sidecar, so those are not needlessly split across cache keys.
func TestStaticServer_VaryOnEveryEligibleAsset(t *testing.T) {
	root := writeTree(t, map[string]string{
		"app.js":    "JSBYTES",
		"app.js.br": "JSBR",
		"plain.js":  "NOSIDECAR",
		"pic.png":   "PNGBYTES",
	})
	srv := newTestServer(t, root)

	cases := []struct {
		name     string
		path     string
		accept   string
		wantVary bool
	}{
		{"compressible served compressed", "/app.js", "br", true},
		{"compressible served identity (no Accept-Encoding)", "/app.js", "", true},
		{"compressible served identity (encoding not accepted)", "/app.js", "identity", true},
		{"compressible with no sidecar at all", "/plain.js", "br, gzip", true},
		{"never-compressible asset", "/pic.png", "br, gzip", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			if c.accept != "" {
				req.Header.Set("Accept-Encoding", c.accept)
			}
			rec := httptest.NewRecorder()
			srv.handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", c.path, rec.Code)
			}
			got := rec.Header().Values("Vary")
			has := len(got) > 0 && strings.Contains(strings.Join(got, ","), "Accept-Encoding")
			if has != c.wantVary {
				t.Errorf("Vary = %v, want Accept-Encoding present = %v", got, c.wantVary)
			}
			if len(got) > 1 {
				t.Errorf("Vary sent %d times (%v); it must be set once, not appended per representation", len(got), got)
			}
		})
	}

	// A 304 carries the same cache-relevant headers a 200 would.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	srv.handler().ServeHTTP(rec, req)
	etag := rec.Header().Get("ETag")
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/app.js", nil)
	req2.Header.Set("If-None-Match", etag)
	srv.handler().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusNotModified {
		t.Fatalf("conditional GET = %d, want 304", rec2.Code)
	}
	if v := rec2.Header().Get("Vary"); !strings.Contains(v, "Accept-Encoding") {
		t.Errorf("304 Vary = %q, want to include Accept-Encoding", v)
	}
}

// TestStaticServer_AcceptRangesAdvertised pins that a server which fully
// implements Range says so. Clients that probe for the header before issuing a
// ranged request (video players, resumable downloaders, some CDNs) otherwise
// refetch whole files even though 206 would have worked — which the second
// half of this test proves it does.
func TestStaticServer_AcceptRangesAdvertised(t *testing.T) {
	root := writeTree(t, map[string]string{"data.bin": "0123456789"})
	srv := newTestServer(t, root)

	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, httptest.NewRequest(method, "/data.bin", nil))
		if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
			t.Errorf("%s Accept-Ranges = %q, want \"bytes\"", method, got)
		}
	}

	// And the advertisement is honest.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/data.bin", nil)
	req.Header.Set("Range", "bytes=2-5")
	srv.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent {
		t.Fatalf("ranged GET = %d, want 206 — advertising Accept-Ranges without honouring it is worse than not advertising", rec.Code)
	}
	if rec.Body.String() != "2345" {
		t.Errorf("range body = %q, want 2345", rec.Body.String())
	}
	if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("206 Accept-Ranges = %q, want \"bytes\"", got)
	}
}

// --- cleanRelPath double-decode (Change 5) -------------------------------

// TestStaticServer_PercentInFilenameIsNotDecodedTwice covers the bug removing
// url.PathUnescape from cleanRelPath fixes. net/http has already decoded
// r.URL.Path by the time the handler runs, so decoding again turned a file
// legitimately named "a%2e.js" into "a..js", which then tripped the traversal
// check and returned 400 for a file that exists.
func TestStaticServer_PercentInFilenameIsNotDecodedTwice(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a%2e.js":     "PERCENT-DOT",
		"b%2Fc.js":    "PERCENT-SLASH",
		"plain%20.js": "PERCENT-SPACE",
	})
	srv := newTestServer(t, root)

	// The client double-encodes so that after net/http's decode the path is
	// the literal filename.
	for target, want := range map[string]string{
		"/a%252e.js":     "PERCENT-DOT",
		"/b%252Fc.js":    "PERCENT-SLASH",
		"/plain%2520.js": "PERCENT-SPACE",
	} {
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 for a file whose name legitimately contains a percent sign", target, rec.Code)
			continue
		}
		if rec.Body.String() != want {
			t.Errorf("GET %s body = %q, want %q", target, rec.Body.String(), want)
		}
	}
}

// TestCleanRelPath_TraversalStillRejectedWithoutSecondDecode enumerates the
// traversal shapes directly against cleanRelPath, after removing its second
// percent-decode, and pins that every one is still refused. Removing a decode
// can only reduce what an attacker can express, but "can only" is exactly the
// kind of reasoning this table exists to replace with evidence.
//
// Note the %2e / %2E rows: those arrive at cleanRelPath already decoded by
// net/http (see the handler-level test below), so what is asserted here is the
// literal-string behaviour of the function itself.
func TestCleanRelPath_TraversalStillRejectedWithoutSecondDecode(t *testing.T) {
	rejected := []string{
		"/../etc/passwd",
		"/..",
		"/a/../../b",
		"/./x",
		"/a/./b",
		"..",
		"/a/..",
		"/foo/../../../etc/shadow",
		"/a..b/c", // the pre-existing conservative substring check
	}
	for _, p := range rejected {
		if got, err := cleanRelPath(p); err == nil {
			t.Errorf("cleanRelPath(%q) = %q, nil; want a traversal rejection", p, got)
		}
	}

	accepted := map[string]string{
		"/":                    "",
		"":                     "",
		"/index.html":          "index.html",
		"/a/b/c.js":            "a/b/c.js",
		"/a%2e.js":             "a%2e.js", // NOT decoded into "a..js" any more
		"/a%2E.js":             "a%2E.js",
		"/%2e%2e/x":            "%2e%2e/x", // literal, not "../x"
		"/name with space.txt": "name with space.txt",
	}
	for p, want := range accepted {
		got, err := cleanRelPath(p)
		if err != nil {
			t.Errorf("cleanRelPath(%q) error = %v, want %q", p, err, want)
			continue
		}
		if got != want {
			t.Errorf("cleanRelPath(%q) = %q, want %q", p, got, want)
		}
	}
}

// TestStaticServer_EncodedTraversalStillRejectedEndToEnd is the handler-level
// half: %2e%2e and %2F traversal attempts must still be refused after the
// second decode is gone, because net/http's own decode already turns them into
// real "../" segments before cleanRelPath sees them.
func TestStaticServer_EncodedTraversalStillRejectedEndToEnd(t *testing.T) {
	outside := writeTree(t, map[string]string{"passwd": "OUTSIDE-SECRET"})
	root := writeTree(t, map[string]string{"index.html": "home"})
	sibling := filepath.Base(outside)

	for _, p := range []string{
		"/../" + sibling + "/passwd",
		"/%2e%2e/" + sibling + "/passwd",
		"/%2E%2E/" + sibling + "/passwd",
		"/..%2F" + sibling + "%2Fpasswd",
		"/%2e%2e%2f" + sibling + "%2fpasswd",
		"/a/%2e%2e/%2e%2e/" + sibling + "/passwd",
		"/....//" + sibling + "/passwd",
	} {
		rec := httptest.NewRecorder()
		srv := newTestServer(t, root)
		srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("GET %q = 200, want the traversal refused", p)
		}
		if strings.Contains(rec.Body.String(), "OUTSIDE-SECRET") {
			t.Errorf("GET %q leaked outside-root content: %q", p, rec.Body.String())
		}
	}
}

// TestStaticServer_ConcurrentRequestsShareStateSafely exercises the state this
// change made shared and mutable — the per-root directory-handle cache
// (sync.Map + atomic counter, written on the first hit in each directory) and
// the process-wide ETag cache — from many goroutines at once, which is how a
// real server uses them. Run under -race it is the guard for the fan-in half
// of this change; run without, it still catches a handle closed out from under
// a concurrent request or a wrong body served under contention.
//
// Every response is checked for the RIGHT body, not merely a 200: a cache that
// handed one request another's directory handle would still return 200
// (mem:self_review_checklist row 38 — a coarse key yields wrong output, not a
// visible miss).
func TestStaticServer_ConcurrentRequestsShareStateSafely(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 24; i++ {
		d := "d" + strconv.Itoa(i)
		files[d+"/app.js"] = "BODY-" + d
		files[d+"/app.js.br"] = "BR-" + d
		files[d+"/sub/page.html"] = "PAGE-" + d
	}
	root := writeTree(t, files)
	srv := newTestServer(t, root)
	h := srv.handler()

	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for n := 0; n < 40; n++ {
				i := (g*7 + n) % 24
				d := "d" + strconv.Itoa(i)

				rec := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/"+d+"/app.js", nil)
				req.Header.Set("Accept-Encoding", "br")
				h.ServeHTTP(rec, req)
				if rec.Code != http.StatusOK || rec.Body.String() != "BR-"+d {
					t.Errorf("GET /%s/app.js = %d %q, want 200 %q", d, rec.Code, rec.Body.String(), "BR-"+d)
					return
				}

				rec2 := httptest.NewRecorder()
				h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/"+d+"/sub/page", nil))
				if rec2.Code != http.StatusOK || rec2.Body.String() != "PAGE-"+d {
					t.Errorf("GET /%s/sub/page = %d %q, want 200 %q", d, rec2.Code, rec2.Body.String(), "PAGE-"+d)
					return
				}

				rec3 := httptest.NewRecorder()
				h.ServeHTTP(rec3, httptest.NewRequest(http.MethodGet, "/"+d+"/missing-"+strconv.Itoa(n)+".js", nil))
				if rec3.Code != http.StatusNotFound {
					t.Errorf("GET /%s/missing = %d, want 404", d, rec3.Code)
					return
				}
			}
		}(g)
	}
	wg.Wait()

	// The concurrent run must actually have populated the shared state, or
	// this test raced nothing (checklist row 47).
	if n := srv.roots[0].dirCount.Load(); n < 24 {
		t.Errorf("dirCount = %d after concurrent serving, want at least 24 (one per directory) — the shared state under test was never exercised", n)
	}
}

// TestStaticServer_ExplicitTrailingSlashServesIndex covers a request shape no
// existing test exercised and that the os.Root rewrite routes differently: a
// URL with an explicit trailing slash. cleanRelPath keeps the slash, so the
// path has an empty final component, which bypasses the directory-handle
// shortcut in servedRoot.openAt and resolves from the top-level root. It must
// still find the directory and serve its index.html.
func TestStaticServer_ExplicitTrailingSlashServesIndex(t *testing.T) {
	root := writeTree(t, map[string]string{
		"index.html":           "<h1>home</h1>",
		"blog/index.html":      "<h1>blog</h1>",
		"blog/post/index.html": "<h1>post</h1>",
	})
	srv := newTestServer(t, root)

	for p, want := range map[string]string{
		"/":           "<h1>home</h1>",
		"/blog/":      "<h1>blog</h1>",
		"/blog":       "<h1>blog</h1>",
		"/blog/post/": "<h1>post</h1>",
		"/blog/post":  "<h1>post</h1>",
	} {
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", p, rec.Code)
			continue
		}
		if rec.Body.String() != want {
			t.Errorf("GET %s body = %q, want %q", p, rec.Body.String(), want)
		}
	}
}

// TestStaticServer_SidecarProbeFallbackWhenIndexUnavailable drives the branch
// pickEncoding takes when the startup index could not be built at all (an
// unreadable tree, or one past maxSidecarIndexEntries): it must fall back to
// probing the filesystem with root.Stat and still negotiate correctly.
//
// Nothing in the ordinary test fixtures reaches that branch, so without this it
// would ship never having executed once (mem:self_review_checklist row 27a:
// assert an effect that exists only inside the new branch). The index is
// disabled directly on the constructed server, which is the only way to reach
// the state deterministically.
func TestStaticServer_SidecarProbeFallbackWhenIndexUnavailable(t *testing.T) {
	root := writeTree(t, map[string]string{
		"app.js":     "IDENTITY",
		"app.js.gz":  "GZIPPED",
		"plain.js":   "NOSIDECAR",
		"pic.png":    "PNG",
		"pic.png.br": "PNGBR",
	})
	srv := newTestServer(t, root)

	// Simulate buildSidecarIndex having failed or overflowed.
	sr := srv.roots[0]
	sr.sidecars, sr.sidecarsIndexed = nil, false

	get := func(p, accept string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, p, nil)
		req.Header.Set("Accept-Encoding", accept)
		rec := httptest.NewRecorder()
		srv.handler().ServeHTTP(rec, req)
		return rec
	}

	// The probe finds the real sidecar.
	rec := get("/app.js", "br, gzip")
	if enc := rec.Header().Get("Content-Encoding"); enc != "gzip" {
		t.Errorf("Content-Encoding = %q, want gzip from the stat-probe fallback", enc)
	}
	if rec.Body.String() != "GZIPPED" {
		t.Errorf("body = %q, want the sidecar bytes", rec.Body.String())
	}

	// No sidecar: identity, not an error.
	rec2 := get("/plain.js", "br, gzip, zstd")
	if enc := rec2.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("Content-Encoding = %q, want empty", enc)
	}
	if rec2.Body.String() != "NOSIDECAR" {
		t.Errorf("body = %q, want NOSIDECAR", rec2.Body.String())
	}

	// The extension gate still runs first, so a never-compressible type does
	// not probe at all even with the index gone.
	rec3 := get("/pic.png", "br, gzip, zstd")
	if enc := rec3.Header().Get("Content-Encoding"); enc != "" {
		t.Errorf("png Content-Encoding = %q, want empty: the extension gate must precede the probe fallback", enc)
	}
	if rec3.Body.String() != "PNG" {
		t.Errorf("png body = %q, want PNG", rec3.Body.String())
	}
}
