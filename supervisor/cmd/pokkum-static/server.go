package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Static server path/traversal and negotiation constants.
const (
	// indexFile is served for directory requests that resolve to a directory
	// containing it.
	indexFile = "index.html"

	// immutableAssetMarker is a path segment that, when present, makes a file
	// eligible for immutable cache headers. SvelteKit's hashed build output lives
	// under client/_app/immutable/.
	immutableAssetMarker = "/immutable/"

	// immutableMaxAge is one year in seconds, the conventional value for
	// content-hashed, immutable assets.
	immutableMaxAge = 31536000
)

// staticServer serves a set of read-only root directories with ETag, Range,
// If-Range and Content-Encoding negotiation against pre-generated .gz/.br/.zst
// sidecars. It never compresses at runtime — the sidecars are produced by
// precompressutils at build time.
type staticServer struct {
	roots []string
	log   *slog.Logger
}

// newStaticServer builds a static server serving the given roots in order. A
// nil logger discards output.
func newStaticServer(roots []string, log *slog.Logger) *staticServer {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &staticServer{roots: append([]string(nil), roots...), log: log}
}

// handler returns the http.Handler serving the static tree.
func (s *staticServer) handler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

// serveHTTP resolves the request path against each root in order and serves the
// first hit. Requests that resolve to no file get 404.
func (s *staticServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	rel, err := cleanRelPath(r.URL.Path)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	for _, root := range s.roots {
		if handled := s.tryServe(w, r, root, rel); handled {
			return
		}
	}
	http.NotFound(w, r)
}

// tryServe attempts to serve rel from root. It reports whether it produced a
// response. Any move that is not a successful stream (file absent, broken
// symlink, symlink escaping the root, directory with no index) returns false so
// the caller can fall through to the next root; the final 404 happens only
// after every root has been tried.
func (s *staticServer) tryServe(w http.ResponseWriter, r *http.Request, root, rel string) bool {
	full := filepath.Join(root, rel)
	resolved, err := filepath.EvalSymlinks(full)
	if err != nil {
		return false // absent (or broken symlink) in this root
	}
	if !withinRoot(root, resolved) {
		s.log.Warn("refusing to serve path outside root", "root", root, "path", rel)
		return false
	}

	fi, err := os.Stat(resolved)
	if err != nil {
		return false
	}
	if fi.IsDir() {
		// Serve index.html when present; otherwise fall through (no directory
		// listing is ever generated).
		idx := filepath.Join(resolved, indexFile)
		if ifi, ierr := os.Stat(idx); ierr == nil && !ifi.IsDir() {
			resolved, fi = idx, ifi
		} else {
			return false
		}
	}

	s.serveFile(w, r, resolved)
	return true
}

// serveFile streams one regular file (resolved path) with negotiation.
func (s *staticServer) serveFile(w http.ResponseWriter, r *http.Request, path string) {
	// Content-Encoding negotiation: prefer a pre-built sidecar the client
	// accepts, falling back to identity (the source file itself).
	bodyPath, enc := s.pickEncoding(r, path)
	if enc == "" {
		bodyPath = path
	}

	ctype := mime.TypeByExtension(filepath.Ext(path))
	if ctype == "" {
		ctype = "application/octet-stream"
	}

	h := w.Header()
	if enc != "" {
		h.Set("Content-Encoding", enc)
		h.Add("Vary", "Accept-Encoding")
	}
	h.Set("Content-Type", ctype)
	h.Set("Cache-Control", cachePolicy(path))

	// Strong ETag over the bytes actually served, so Range, Content-Encoding
	// and precondition matches stay consistent with the payload.
	etag, size, err := fileETag(bodyPath)
	if err != nil {
		s.log.Warn("could not hash file for ETag", "path", bodyPath, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.Set("ETag", `"`+etag+`"`)

	if r.Method == http.MethodGet {
		if rng, sat, unsat := parseSingleRange(r.Header.Get("Range"), size); sat {
			if ifRange := r.Header.Get("If-Range"); ifRange == "" || ifRangeMatches(ifRange, etag) {
				start, end := rng[0], rng[1]
				h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
				h.Set("Content-Length", strconv.FormatInt(end-start+1, 10))
				w.WriteHeader(http.StatusPartialContent)
				if err := writeRange(w, bodyPath, start, end); err != nil {
					s.log.Warn("range write failed", "path", bodyPath, "error", err)
				}
				return
			}
			// If-Range precondition failed: fall through to full content.
		} else if unsat {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		// Multi-range or malformed (or GET with an If-Range miss): send the
		// full representation, which is always legal.
	}

	h.Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodHead {
		return
	}
	f, err := os.Open(bodyPath)
	if err != nil {
		s.log.Warn("open failed", "path", bodyPath, "error", err)
		return
	}
	defer f.Close()
	if _, err := io.Copy(w, f); err != nil {
		s.log.Warn("write failed", "path", bodyPath, "error", err)
	}
}

// pickEncoding chooses a pre-compressed sidecar to serve based on Accept-Encoding,
// preferring brotli > gzip > zstd. Returns ("", "") for identity.
func (s *staticServer) pickEncoding(r *http.Request, sourcePath string) (sidecar, encoding string) {
	accepted := parseAcceptEncoding(r.Header.Get("Accept-Encoding"))
	for _, cand := range []struct{ enc, ext string }{
		{"br", ".br"},
		{"gzip", ".gz"},
		{"zstd", ".zst"},
	} {
		if !accepted[cand.enc] {
			continue
		}
		sp := sourcePath + cand.ext
		if fi, err := os.Stat(sp); err == nil && !fi.IsDir() {
			return sp, cand.enc
		}
	}
	return "", ""
}

// parseAcceptEncoding returns the set of encodings the client explicitly
// accepts, honouring q=0 exclusions. Identity is always available and not listed.
func parseAcceptEncoding(header string) map[string]bool {
	accepted := map[string]bool{}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		token, qParam, hasQ := strings.Cut(part, ";")
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		want := true
		if hasQ {
			qv := strings.TrimSpace(strings.TrimPrefix(qParam, "q="))
			if f, err := strconv.ParseFloat(qv, 64); err == nil {
				want = f > 0
			}
		}
		accepted[strings.ToLower(token)] = want
	}
	return accepted
}

// fileETag computes a strong ETag (hex SHA-256) and the byte size of the file.
func fileETag(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// parseSingleRange parses a single "bytes=start-end" range against size.
//
// Return contract:
//   - (_, false, false): no Range header (or a multi-range / malformed header) —
//     the caller sends the full representation. Sending 200 for a multi-range
//     header is explicitly allowed by RFC 7233.
//   - ([2]int64, true, false): a valid single satisfiable range [start,end].
//   - (_, false, true): a single range that cannot be satisfied at all — the
//     caller must respond 416.
func parseSingleRange(header string, size int64) (rng [2]int64, satisfiable, unsatisfiable bool) {
	if header == "" {
		return [2]int64{}, false, false
	}
	if !strings.HasPrefix(header, "bytes=") {
		return [2]int64{}, false, false
	}
	spec := strings.TrimPrefix(header, "bytes=")
	if strings.Contains(spec, ",") {
		return [2]int64{}, false, false // multi-range: ignored, send full
	}
	startS, endS, ok := strings.Cut(spec, "-")
	if !ok {
		return [2]int64{}, false, false // malformed: ignored, send full
	}
	start, err1 := strconv.ParseInt(strings.TrimSpace(startS), 10, 64)
	end, err2 := strconv.ParseInt(strings.TrimSpace(endS), 10, 64)
	switch {
	case err1 != nil && err2 != nil:
		return [2]int64{}, false, false // malformed
	case err1 != nil: // suffix range "-N": last N bytes
		n := end
		if n > size {
			n = size
		}
		start = size - n
		end = size - 1
	case err2 != nil: // open-ended "start-"
		end = size - 1
	}
	if start < 0 || end >= size || start > end {
		return [2]int64{}, false, true // unsatisfiable
	}
	return [2]int64{start, end}, true, false
}

// ifRangeMatches evaluates the If-Range precondition against the current strong
// ETag (unquoted hex). A weak (W/) If-Range is treated as a match on tag value.
func ifRangeMatches(ifRange, etag string) bool {
	iv := strings.TrimSpace(ifRange)
	if strings.HasPrefix(iv, "W/") {
		iv = strings.TrimSpace(strings.TrimPrefix(iv, "W/"))
	}
	iv = strings.Trim(iv, `"`)
	return iv == etag
}

// writeRange streams bytes [start,end] of path to w.
func writeRange(w io.Writer, path string, start, end int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return err
	}
	_, err = io.CopyN(w, f, end-start+1)
	return err
}

// cachePolicy returns the Cache-Control header for a served file.
//
// Files under /immutable/ or matching SvelteKit's content-hashed
// "name-<hash>.<ext>" convention are served as immutable for a year; everything
// else (HTML in particular) gets no-cache so it revalidates via its ETag.
func cachePolicy(path string) string {
	if strings.Contains(path, immutableAssetMarker) {
		return "public, max-age=" + strconv.Itoa(immutableMaxAge) + ", immutable"
	}
	if looksHashed(filepath.Base(path)) {
		return "public, max-age=" + strconv.Itoa(immutableMaxAge) + ", immutable"
	}
	return "no-cache"
}

// looksHashed reports whether base matches SvelteKit's "name-<hex>" convention
// (e.g. "app-abc123.js", "page.svelte-9f2a3b.css"). A heuristic guard only; the
// /immutable/ marker is authoritative.
func looksHashed(base string) bool {
	ext := filepath.Ext(base)
	if ext == "" {
		return false
	}
	stem := strings.TrimSuffix(base, ext)
	idx := strings.LastIndexByte(stem, '-')
	if idx < 0 || idx == len(stem)-1 {
		return false
	}
	tail := stem[idx+1:]
	if len(tail) < 4 || len(tail) > 24 {
		return false
	}
	for _, c := range tail {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// cleanRelPath converts a URL path into a clean relative path safe to join to a
// root. It rejects traversal, absolute paths and backslash tricks.
func cleanRelPath(rawPath string) (string, error) {
	p, err := url.PathUnescape(rawPath)
	if err != nil {
		return "", err
	}
	if p == "" {
		p = "/"
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." {
			return "", errors.New("invalid path: traversal")
		}
	}
	clean := filepath.FromSlash(strings.TrimPrefix(p, "/"))
	if clean == "" || clean == "." {
		clean = ""
	}
	if strings.Contains(clean, "..") {
		return "", errors.New("invalid path: traversal")
	}
	return clean, nil
}

// withinRoot reports whether the resolved path p is inside root. Both sides are
// canonicalised (symlinks resolved, fallback to abs+clean) so that a root like
// /var/folders/... on macOS, whose symlink resolves to /private/var/folders/...,
// still matches a candidate EvalSymlinks already resolved.
func withinRoot(root, p string) bool {
	rc, err := filepath.EvalSymlinks(root)
	if err != nil {
		rc = filepath.Clean(root)
		if a, aerr := filepath.Abs(root); aerr == nil {
			rc = a
		}
	}
	c := filepath.Clean(p)
	if a, aerr := filepath.Abs(p); aerr == nil {
		c = a
	}
	rel, err := filepath.Rel(rc, c)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
