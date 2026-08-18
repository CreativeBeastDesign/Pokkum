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
	"sync"
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
	// canonicalRoots[i] is roots[i] with symlinks resolved once, at server
	// construction — the roots are fixed for the process lifetime, so
	// resolving them per request (as withinRoot used to) was pure redundant
	// syscall work repeated on every single request.
	canonicalRoots []string
	// fallbackPath is the resolved in-image path of the opt-in SPA-fallback
	// file, non-empty only when a fallback was configured AND successfully
	// validated at construction (a regular file within one of the served
	// roots). Unmatched GET/HEAD routes are served this file with 200.
	fallbackPath string
	// fallbackRel is the path of the fallback relative to the served root it
	// lives in (informational, used for logging).
	fallbackRel string
	// fallbackCanonicalRoot is the canonical root (see canonicalizeRoot) that
	// fallbackPath was validated to lie within at construction time. Retained
	// so fallbackFileOK can re-run that same containment check on every
	// serve-time miss, not just once at startup — see fallbackFileOK.
	fallbackCanonicalRoot string
	// fallbackWarnOnce gates the one-per-server (one-per-process) discovery
	// log emitted on the first 404 while fallback mode is unset entirely.
	fallbackWarnOnce sync.Once
	// fallbackBrokenWarnOnce gates a distinct, one-per-server log emitted the
	// first time a *configured* fallback fails its serve-time validity check
	// (fallbackFileOK) — a real operational regression, not the "you might
	// want this feature" discovery message fallbackWarnOnce covers.
	fallbackBrokenWarnOnce sync.Once
	log                    *slog.Logger
}

// newStaticServer builds a static server serving the given roots in order. A
// nil logger discards output. fallback is the opt-in SPA-fallback file (an
// in-image path); empty disables it. If fallback is non-empty but cannot be
// resolved to a regular file within one of the roots, it is rejected with a
// construction-time Warn and disabled — the server keeps running and keeps
// returning honest 404s rather than ever serving content outside the roots.
func newStaticServer(roots []string, fallback string, log *slog.Logger) *staticServer {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	rootsCopy := append([]string(nil), roots...)
	canonicalRoots := make([]string, len(rootsCopy))
	for i, root := range rootsCopy {
		canonicalRoots[i] = canonicalizeRoot(root)
	}
	s := &staticServer{roots: rootsCopy, canonicalRoots: canonicalRoots, log: log}
	if fallback != "" {
		s.configureFallback(fallback)
	}
	return s
}

// configureFallback resolves and validates the configured fallback file and
// stores it on s. A path that cannot be resolved to a regular file lying within
// one of the served roots is rejected and disables the fallback with a Warn —
// security-critical: an attacker-controlled fallback path must never serve
// bytes from outside the roots. Resolving and checking once at construction
// (not per request) mirrors canonicalizeRoot's approach for the roots.
func (s *staticServer) configureFallback(fallback string) {
	resolved, err := filepath.EvalSymlinks(fallback)
	if err != nil {
		s.log.Warn("SPA fallback path could not be resolved; disabled",
			"fallback", fallback, "error", err)
		return
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.Mode().IsRegular() {
		s.log.Warn("SPA fallback path is not a regular file; disabled",
			"fallback", fallback, "resolved", resolved)
		return
	}
	for i, root := range s.roots {
		if !withinRoot(s.canonicalRoots[i], resolved) {
			continue
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil {
			s.log.Warn("SPA fallback path could not be made relative to a root; disabled",
				"fallback", fallback, "root", root, "error", err)
			return
		}
		s.fallbackPath = resolved
		s.fallbackRel = rel
		s.fallbackCanonicalRoot = s.canonicalRoots[i]
		s.log.Info("SPA fallback enabled", "path", fallback, "root", root, "rel", rel)
		return
	}
	s.log.Warn("SPA fallback path is outside every served root; disabled",
		"fallback", fallback)
}

// canonicalizeRoot resolves root's symlinks once. If root doesn't exist yet
// or EvalSymlinks otherwise fails, it falls back to an absolute, cleaned form
// — the same fallback withinRoot used to apply per call.
func canonicalizeRoot(root string) string {
	if rc, err := filepath.EvalSymlinks(root); err == nil {
		return rc
	}
	rc := filepath.Clean(root)
	if a, err := filepath.Abs(root); err == nil {
		rc = a
	}
	return rc
}

// handler returns the http.Handler serving the static tree.
func (s *staticServer) handler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

// serveHTTP resolves the request path against each root in order and serves the
// first hit. Requests that resolve to no file get the configured SPA fallback
// (if any), else a plain 404.
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

	for i, root := range s.roots {
		if handled := s.tryServe(w, r, root, s.canonicalRoots[i], rel); handled {
			return
		}
	}

	// No root served the path. In opt-in SPA-fallback mode (a fallback file
	// was configured and validated at construction), serve that shell for any
	// unmatched GET/HEAD route instead of a 404. This is intentionally a
	// per-request re-entry into serveFile so the fallback gets exactly the
	// same ETag / Content-Encoding / Range negotiation as any other file.
	// The fallback is re-validated — as a regular file AND as still safely
	// contained within its root — here so that if it is removed, replaced,
	// or (security-critical) swapped for a symlink escaping the root after
	// construction, we fall through to an honest 404 (a miss), never a 500
	// or, worse, serving bytes from outside every served root.
	if s.fallbackPath != "" {
		if resolved, ok := s.fallbackFileOK(); ok {
			s.serveFile(w, r, resolved, true)
			return
		}
		// Configured, but broke at serve time: a real operational
		// regression, distinct from "fallback mode isn't configured at all".
		s.warnFallbackBrokenOnce()
		http.NotFound(w, r)
		return
	}
	// Only suggest the SPA fallback when it is actually a plausible remedy:
	// an extensionless path looks like a client-side route that might need
	// one, but a path with a file extension (.js, .css, .png, ...) looks
	// like a missing static asset, which no SPA fallback fixes. Before the
	// ".html" candidate existed, this hint fired for a prerendered page that
	// was simply never looked for (see tryServe) — that specific case no
	// longer reaches here at all now that the candidate exists, but the
	// extension check still guards against suggesting the wrong remedy for
	// a genuinely-missing asset.
	if filepath.Ext(rel) == "" {
		s.warnFallbackUnconfiguredOnce()
	}
	http.NotFound(w, r)
}

// fallbackFileOK reports whether the configured fallback path still resolves,
// right now, to a regular file safely within the root it was validated
// against at construction, returning the resolved path to serve. This
// re-runs the SAME EvalSymlinks + containment check configureFallback
// performed once at startup — not just an os.Stat — because the file at
// fallbackPath could have been replaced by a symlink after construction
// (e.g. an emptyDir mount swap in a deployment where
// readOnlyRootFilesystem isn't actually enforced); os.Stat alone follows
// symlinks silently, which would let such a swap serve arbitrary filesystem
// content with 200 for every unmatched route.
//
// This runs on every unmatched route (a miss), not the common-case hit
// path for a working SPA, so the extra EvalSymlinks syscall per miss is an
// accepted, deliberate cost — caching the result would reintroduce exactly
// the TOCTOU gap this re-check exists to close, and misses are not expected
// to be the hot path for a correctly configured site.
func (s *staticServer) fallbackFileOK() (resolved string, ok bool) {
	resolved, err := filepath.EvalSymlinks(s.fallbackPath)
	if err != nil {
		return "", false
	}
	if !withinRoot(s.fallbackCanonicalRoot, resolved) {
		s.log.Warn("SPA fallback path resolved outside its root at serve time; refusing",
			"path", s.fallbackPath, "resolved", resolved)
		return "", false
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.Mode().IsRegular() {
		return "", false
	}
	return resolved, true
}

// warnFallbackUnconfiguredOnce logs (once) that the opt-in SPA-fallback mode
// exists, only when no fallback was configured at all (s.fallbackPath == "").
// It never mutates the response. The per-server once gates the log so a
// developer who hits an unexpected route on an SPA site is told the opt-in
// mode exists without spamming every 404; the 404 response body itself stays
// clean — no dev-marker HTML is ever injected into production responses.
//
// Callers must only invoke this for a miss that an SPA fallback could
// plausibly fix — an extensionless path that looks like a client-side
// route, not a path with a file extension that looks like a missing static
// asset (see serveHTTP's filepath.Ext(rel) == "" gate). Suggesting an SPA
// fallback for a missing .js/.css/.png is the wrong remedy and was the
// original motivation for gating this at all.
func (s *staticServer) warnFallbackUnconfiguredOnce() {
	s.fallbackWarnOnce.Do(func() {
		s.log.Warn("unmatched route returned 404; SPA fallback is available via " +
			"POKKUM_STATIC_FALLBACK / -fallback (see Vocabulary.md) when adapter-static " +
			"emits a fallback page for this site")
	})
}

// warnFallbackBrokenOnce logs (once) that a fallback WAS configured and
// passed construction-time validation, but its file no longer resolves to a
// valid, safely-contained regular file at serve time — a real operational
// problem (the shell disappeared, or the mount underneath it changed), not
// the "you might want this feature" discovery message
// warnFallbackUnconfiguredOnce covers. It is deliberately its own sync.Once,
// separate from warnFallbackUnconfiguredOnce, so the two conditions never
// produce a misleading message for one another.
//
// Gated once-per-process rather than logged on every miss: the condition is
// persistent once it occurs (a deleted/swapped file does not un-break
// itself), so a single clear log line is sufficient operator signal, and
// repeating it on every subsequent unmatched request would reproduce the
// same log-flooding problem the original shared sync.Once was designed to
// avoid.
func (s *staticServer) warnFallbackBrokenOnce() {
	s.fallbackBrokenWarnOnce.Do(func() {
		s.log.Warn("configured SPA fallback no longer resolves to a valid file within its root; serving plain 404",
			"fallback", s.fallbackPath)
	})
}

// resolveInRoot resolves candidate's symlinks and verifies the result is
// contained within canonicalRoot (root with symlinks pre-resolved once at
// construction — see canonicalizeRoot). It returns ("", false) for anything
// that doesn't exist, can't be resolved, or escapes the root.
//
// Every path candidate tryServe considers — the exact request path, the
// "<rel>.html" sibling, and a directory's index.html — MUST go through this
// exact same EvalSymlinks + withinRoot pair. A candidate that skipped it
// would be a path-traversal regression (Serena mem:self_review_checklist
// row 22): a raw request path can be clean (cleanRelPath already rejects
// "." / ".." segments) while still resolving, via a symlink planted in a
// served root, to a target outside every root — only re-resolving and
// re-checking at each candidate catches that.
func (s *staticServer) resolveInRoot(root, canonicalRoot, candidate, what string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", false // absent, or a broken symlink, in this root
	}
	if !withinRoot(canonicalRoot, resolved) {
		s.log.Warn("refusing to serve "+what+" outside root", "root", root, "path", candidate)
		return "", false
	}
	return resolved, true
}

// tryServe attempts to serve rel from root (canonicalRoot is root with
// symlinks pre-resolved, see canonicalizeRoot). It reports whether it
// produced a response. Any move that is not a successful stream returns
// false so the caller can fall through to the next root; the final 404 (or
// SPA fallback) happens only after every root has been tried.
//
// Three candidates are considered, in this precedence order:
//
//  1. An exact regular file at the request path. Highest precedence: if a
//     real file is literally sitting at this exact path, it wins over
//     whatever else exists alongside it.
//  2. "<rel>.html" — @sveltejs/adapter-static's default (trailingSlash:
//     'never') convention: route /about is emitted as a flat about.html
//     file, not a directory. Tried BEFORE candidate 3 deliberately: a real
//     multi-page adapter-static site can legitimately have both an
//     about.html file and an about/ directory at once (e.g. a child route
//     "/about/team" needs the directory to hold team.html, but that
//     directory has no index.html of its own) — candidate 2 must win that
//     case, not fail through to a doomed candidate-3 lookup first.
//  3. A directory at the request path containing index.html —
//     adapter-static's trailingSlash: 'always' convention, and how the site
//     root ("/") is served. Lowest precedence: it depends on an extra,
//     implicit filename, and is only reached once a more specific exact
//     match and the adapter's default flat-file convention have both been
//     ruled out.
//
// Every candidate is resolved and containment-checked via the same
// resolveInRoot call (see its doc comment) — none of them get a weaker
// check than the others.
func (s *staticServer) tryServe(w http.ResponseWriter, r *http.Request, root, canonicalRoot, rel string) bool {
	full := filepath.Join(root, rel)
	resolved, resolvedOK := s.resolveInRoot(root, canonicalRoot, full, "path")

	// Candidate 1: exact regular file.
	if resolvedOK {
		if fi, err := os.Stat(resolved); err == nil && !fi.IsDir() {
			s.serveFile(w, r, resolved, false)
			return true
		}
	}

	// Candidate 2: "<rel>.html" sibling file. Skipped for the root path
	// itself (rel == "") — "" + ".html" is meaningless, and "/" is already
	// covered by candidate 3 below.
	if rel != "" {
		if htmlResolved, ok := s.resolveInRoot(root, canonicalRoot, full+".html", "html path"); ok {
			if fi, err := os.Stat(htmlResolved); err == nil && !fi.IsDir() {
				s.serveFile(w, r, htmlResolved, false)
				return true
			}
		}
	}

	// Candidate 3: directory + index.html.
	if !resolvedOK {
		return false // request path doesn't exist in this root at all
	}
	fi, err := os.Stat(resolved)
	if err != nil || !fi.IsDir() {
		return false // exists, but is neither a servable file nor a directory
	}
	// Re-run the same containment check on index.html specifically: it can
	// itself be a symlink, and os.Stat alone would follow it without
	// verifying it stays within root.
	idx, idxOK := s.resolveInRoot(root, canonicalRoot, filepath.Join(resolved, indexFile), "index path")
	if !idxOK {
		return false
	}
	ifi, err := os.Stat(idx)
	if err != nil || ifi.IsDir() {
		return false
	}
	s.serveFile(w, r, idx, false)
	return true
}

// serveFile streams one regular file (resolved path) with negotiation.
// isFallback marks a call serving the configured opt-in SPA-fallback shell
// (see cachePolicy's isFallback parameter for why this needs to be threaded
// through explicitly rather than inferred from path).
func (s *staticServer) serveFile(w http.ResponseWriter, r *http.Request, path string, isFallback bool) {
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
	h.Set("Cache-Control", cachePolicy(path, isFallback))

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

// fileETagCache caches fileETag results by resolved path. Every served path is
// immutable for the container's whole lifetime (baked into the image at build
// time), so a strong ETag computed once never needs recomputing — this avoids
// a full sequential read+SHA-256 pass on every single request, including a
// byte-Range request for a few bytes of a large file.
var fileETagCache sync.Map // path string -> fileETagEntry

type fileETagEntry struct {
	etag string
	size int64
}

// fileETag computes a strong ETag (hex SHA-256) and the byte size of the
// file, caching the result for the life of the process.
func fileETag(path string) (string, int64, error) {
	if v, ok := fileETagCache.Load(path); ok {
		e := v.(fileETagEntry)
		return e.etag, e.size, nil
	}
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
	etag := hex.EncodeToString(h.Sum(nil))
	fileETagCache.Store(path, fileETagEntry{etag: etag, size: n})
	return etag, n, nil
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
//
// isFallback forces no-cache regardless of the filename-shape heuristic below.
// The configured SPA-fallback shell is an entry point that must be
// re-fetched on every deploy so clients pick up new asset references — same
// as index.html, which already gets no-cache "naturally" because its name
// doesn't look hash-suffixed. A fallback filename that happens to look
// hash-suffixed (e.g. a project configuring fallback: 'fallback-abc123.html')
// must not fall through to looksHashed's immutable, year-long caching: that
// would pin a stale deploy's shell in every intermediate cache.
func cachePolicy(path string, isFallback bool) string {
	if isFallback {
		return "no-cache"
	}
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

// withinRoot reports whether the resolved path p is inside canonicalRoot — an
// already-canonicalized root (see canonicalizeRoot, computed once at server
// construction rather than per request). p is still canonicalised here
// (symlinks resolved, fallback to abs+clean) so that a root like
// /var/folders/... on macOS, whose symlink resolves to /private/var/folders/...,
// still matches a candidate EvalSymlinks already resolved.
func withinRoot(canonicalRoot, p string) bool {
	c := filepath.Clean(p)
	if a, aerr := filepath.Abs(p); aerr == nil {
		c = a
	}
	rel, err := filepath.Rel(canonicalRoot, c)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
