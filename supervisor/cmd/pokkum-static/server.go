package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

	// maxSidecarIndexEntries bounds the startup sidecar index. A SvelteKit
	// build has a few thousand assets, not a few hundred thousand; a tree big
	// enough to pass this is one where a resident index would cost more memory
	// than the stat calls it saves, so the server degrades to probing with
	// root.Stat rather than growing without bound.
	maxSidecarIndexEntries = 100_000

	// maxCachedDirHandles bounds the per-root directory-handle cache (see
	// servedRoot.dirs). Each entry costs one file descriptor, so this stays
	// well under a container's typical 1024 fd limit while comfortably
	// covering a real build tree, which has tens of directories, not hundreds.
	// Past the cap, paths simply resolve from the top-level root again —
	// slower, never wrong.
	maxCachedDirHandles = 128
)

// Bits of the per-asset sidecar bitmask the startup index stores. One bitmask
// per asset costs one map entry and one map lookup per request, rather than one
// entry and one lookup per encoding.
const (
	sidecarBrotli uint8 = 1 << iota
	sidecarGzip
	sidecarZstd
)

// sidecarCandidates is the Content-Encoding preference order: brotli, then
// gzip, then zstd — unchanged from the sequence pickEncoding used to probe
// with os.Stat.
var sidecarCandidates = [...]struct {
	enc string
	ext string
	bit uint8
}{
	{"br", ".br", sidecarBrotli},
	{"gzip", ".gz", sidecarGzip},
	{"zstd", ".zst", sidecarZstd},
}

// servedRoot is one configured serve root, opened once and held for the
// process lifetime.
type servedRoot struct {
	// path is the configured root exactly as given. Used for logging and as
	// the ETag cache-key prefix; never joined to a request path.
	path string

	// root is the containment handle every request is served through. It is
	// nil when the directory could not be opened at startup (absent, or not a
	// directory), in which case this root matches nothing — the same
	// observable an unresolvable root had before.
	//
	// os.Root is a security control here, not an optimisation that happens to
	// be safe. It resolves each path component with openat/O_NOFOLLOW and the
	// kernel refuses any component that leaves the root, so the handle that
	// comes back IS the object served: there is nothing left to re-resolve and
	// therefore no TOCTOU window. The sequence it replaces —
	// EvalSymlinks, then withinRoot on the resolved string, then os.Stat on
	// it, then os.Open on it — had two such windows (between resolve and stat,
	// and between stat and open) and cost a full lstat chain per candidate.
	// The same primitive already guards the supervisor's attestation reads
	// (pokkum-init/attest.go, openAttestFile).
	//
	// os.Root is documented safe for concurrent use by multiple goroutines,
	// which is what lets a single handle serve every request.
	root *os.Root

	// sidecars maps an asset's root-relative slash path to the bitmask of
	// pre-compressed sidecars sitting next to it, built once at startup by
	// buildSidecarIndex.
	sidecars map[string]uint8

	// sidecarsIndexed distinguishes "indexed, and this asset has none" from
	// "no index available" — a nil or empty map cannot say which. When false,
	// pickEncoding falls back to probing with root.Stat, exactly as before.
	sidecarsIndexed bool

	// dirs caches an *os.Root per directory that has successfully served a
	// file, keyed by that directory's root-relative slash path.
	//
	// Why: os.Root resolves a path one component at a time (Go's os package
	// does not use openat2), so "_app/immutable/chunks/x.js" costs four
	// openat calls, and it costs them again on every request for every file in
	// that directory. Holding the directory open turns the steady state into a
	// single openat for the final component. Measured on darwin, where syscall
	// overhead is high, that is a ~4.5x difference on a deep path.
	//
	// Containment is not weakened. A cached sub-root is produced by
	// sr.root.OpenRoot(dir), i.e. by walking down from the already-contained
	// root, so it can never name a directory outside it; and opening a single
	// component through it still refuses ".." and any escaping symlink,
	// exactly as opening the full path through the top-level root does. The
	// only thing cached is *which directory*, never a decision about a file.
	//
	// Entries are added ONLY after a file in that directory has actually been
	// opened successfully, and never for a failed lookup. That matters: the
	// keys would otherwise be attacker-chosen (any 404 path would mint an
	// entry) and the map would grow without bound under a flood of distinct
	// missing paths. As written, the key space is the set of directories that
	// really exist in the image, and maxCachedDirHandles bounds it regardless.
	//
	// Entries are never evicted, so a handle can never be closed while a
	// request is using it.
	//
	// Staleness: a directory handle follows the inode, so if a served
	// directory were replaced wholesale after startup we would keep serving
	// the original image's contents. The served trees are read-only for the
	// container's life (the same premise fileETagCache and the sidecar index
	// rest on), and in the event it were violated, continuing to serve the
	// attested build is the safe direction, not the dangerous one.
	dirs     sync.Map // string -> *os.Root
	dirCount atomic.Int64
}

// openAt opens rel under sr, one component deep when rel's directory is already
// cached and by full walk otherwise. It is the only place sr.root is opened
// through.
func (sr *servedRoot) openAt(rel string) (*os.File, error) {
	dir, name := path.Split(rel)
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" || name == "" {
		// A file at the top of the root, or a directory request with a
		// trailing slash: nothing to shortcut.
		return sr.root.Open(relOrDot(rel))
	}
	if h := sr.cachedDir(dir); h != nil {
		// name is a single component (path.Split guarantees it contains no
		// "/"), and os.Root refuses ".." and escaping symlinks in it anyway.
		return h.Open(name)
	}
	f, err := sr.root.Open(rel)
	if err != nil {
		return nil, err // a miss caches nothing and costs nothing extra
	}
	sr.cacheDir(dir)
	return f, nil
}

// cachedDir returns the cached handle for dir, or nil when there is none.
func (sr *servedRoot) cachedDir(dir string) *os.Root {
	v, ok := sr.dirs.Load(dir)
	if !ok {
		return nil
	}
	h, _ := v.(*os.Root)
	return h
}

// cacheDir opens and stores a handle for dir, if there is room. Called only
// after a file inside dir has been served, so it never caches on a miss.
func (sr *servedRoot) cacheDir(dir string) {
	if _, ok := sr.dirs.Load(dir); ok {
		return
	}
	if sr.dirCount.Load() >= maxCachedDirHandles {
		return
	}
	h, err := sr.root.OpenRoot(dir)
	if err != nil {
		return // not cacheable; the full-walk path keeps working
	}
	if _, loaded := sr.dirs.LoadOrStore(dir, h); loaded {
		_ = h.Close() // lost the race; the winner's handle is the live one
		return
	}
	sr.dirCount.Add(1)
}

// closeHandles releases the root handle and every cached directory handle.
func (sr *servedRoot) closeHandles() {
	sr.dirs.Range(func(k, v any) bool {
		if h, ok := v.(*os.Root); ok && h != nil {
			_ = h.Close()
		}
		sr.dirs.Delete(k)
		return true
	})
	sr.dirCount.Store(0)
	if sr.root != nil {
		_ = sr.root.Close()
		sr.root = nil
	}
}

// etagKey is the process-global ETag cache key for one file under this root.
// The NUL separator cannot occur in a path, so no two (root, rel) pairs can
// collide (mem:self_review_checklist row 38).
func (sr *servedRoot) etagKey(rel string) string {
	return sr.path + "\x00" + rel
}

// staticServer serves a set of read-only root directories with ETag, Range,
// If-Range and Content-Encoding negotiation against pre-generated .gz/.br/.zst
// sidecars. It never compresses at runtime — the sidecars are produced by
// precompressutils at build time.
type staticServer struct {
	// roots are the configured serve roots in lookup order, each holding an
	// open os.Root containment handle (see servedRoot).
	roots []*servedRoot

	// fallbackRoot indexes into roots, or -1 when no SPA fallback is enabled
	// (either none was configured, or the configured one failed validation at
	// construction). This is the single "is the fallback on?" flag; nothing
	// else may stand in for it.
	fallbackRoot int
	// fallbackRel is the fallback file's path relative to roots[fallbackRoot],
	// slash-separated. Every serve of the fallback goes through that root's
	// containment handle with this path, so the fallback gets exactly the same
	// kernel-enforced containment as any other served file.
	fallbackRel string
	// fallbackPath is the configured path exactly as given, retained for log
	// messages only. It is never opened directly.
	fallbackPath string

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
// opened as a regular file within one of the roots, it is rejected with a
// construction-time Warn and disabled — the server keeps running and keeps
// returning honest 404s rather than ever serving content outside the roots.
func newStaticServer(roots []string, fallback string, log *slog.Logger) *staticServer {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &staticServer{fallbackRoot: -1, log: log}
	for _, root := range roots {
		s.roots = append(s.roots, openServedRoot(root, log))
	}
	if fallback != "" {
		s.configureFallback(fallback)
	}
	return s
}

// close releases the roots' containment handles. Production never calls it —
// the roots are held for the whole process lifetime by design, which is what
// makes one openat per request possible — but a test process that builds many
// servers should, so file descriptors do not accumulate across a suite.
func (s *staticServer) close() {
	for _, sr := range s.roots {
		sr.closeHandles()
	}
}

// openServedRoot opens one configured root and indexes its sidecars.
//
// A root that cannot be opened is not fatal: a configured-but-absent root
// (/app/prerendered on a site with no prerendered pages, say) simply matches
// nothing, which is exactly what happened before when every candidate under it
// failed to resolve.
func openServedRoot(root string, log *slog.Logger) *servedRoot {
	sr := &servedRoot{path: root}
	h, err := os.OpenRoot(root)
	if err != nil {
		log.Info("served root is unavailable; it will match no request",
			"root", root, "error", err)
		return sr
	}
	sr.root = h
	sr.sidecars, sr.sidecarsIndexed = buildSidecarIndex(h, root, log)
	return sr
}

// buildSidecarIndex walks root once at startup and records, per asset, which
// pre-compressed sidecars sit beside it. It reports the index and whether it is
// usable at all.
//
// Why a startup snapshot is safe here, stated rather than assumed: the served
// trees are baked into the image's layers and are read-only for the whole
// container lifetime — the same premise fileETagCache already relies on to
// cache a content hash per path forever, and the same premise the startup
// attestation in pokkum-init enforces for /app. Nothing in the image writes to
// them.
//
// The index is nevertheless treated as a cache of filesystem state, not as
// truth, and it is deliberately scoped so that being wrong cannot produce a
// wrong answer:
//
//   - It records ONLY sidecars, never the existence of a primary asset. A file
//     that appears after startup is still found by tryServe's openat, so a
//     stale index can never turn a present file into a 404.
//   - A sidecar recorded here that has since vanished is caught at the point of
//     use: serveFile opens it and falls back to identity when the open fails,
//     rather than 5xx-ing (mem:self_review_checklist row 9).
//   - A sidecar that appears after startup is simply not used, and identity is
//     served. Suboptimal, never incorrect.
//
// Only regular files are indexed. A symlink named "x.js.br" is skipped here,
// and would in any case be refused by root.Open at serve time if it escaped.
func buildSidecarIndex(root *os.Root, rootPath string, log *slog.Logger) (map[string]uint8, bool) {
	idx := make(map[string]uint8, 64)
	overflow := false

	err := fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subdirectory costs coverage, not correctness:
			// assets under it fall back to identity. Keep walking.
			return nil //nolint:nilerr // deliberate: skip, do not abort
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		ext := path.Ext(p)
		var bit uint8
		for _, cand := range sidecarCandidates {
			if ext == cand.ext {
				bit = cand.bit
				break
			}
		}
		if bit == 0 {
			return nil
		}
		base := p[:len(p)-len(ext)]
		if !isPrecompressibleExt(base) {
			return nil // a ".br" next to something precompression never emits
		}
		if _, seen := idx[base]; !seen && len(idx) >= maxSidecarIndexEntries {
			overflow = true
			return fs.SkipAll
		}
		idx[base] |= bit
		return nil
	})
	if err != nil {
		log.Warn("sidecar index could not be built; falling back to per-request probing",
			"root", rootPath, "error", err)
		return nil, false
	}
	if overflow {
		log.Warn("sidecar index exceeded its entry cap; falling back to per-request probing",
			"root", rootPath, "cap", maxSidecarIndexEntries)
		return nil, false
	}
	log.Debug("sidecar index built", "root", rootPath, "assets_with_sidecars", len(idx))
	return idx, true
}

// configureFallback locates the configured fallback file inside one of the
// served roots and stores its (root, rel) coordinates on s. A path that is not
// a regular file within a served root is rejected and disables the fallback
// with a Warn — security-critical: an attacker-controlled fallback path must
// never serve bytes from outside the roots.
//
// Containment is established by opening the file through the root's os.Root
// handle, not by resolving a string and comparing prefixes. The lexical
// filepath.Rel below only decides WHICH root to try; it is not the safety
// check. The safety check is root.Open, which the kernel refuses if any
// component of rel leaves the root — and which fallbackFileOK repeats, in
// full, on every serve.
func (s *staticServer) configureFallback(fallback string) {
	s.fallbackPath = fallback
	underSomeRoot := false
	for i, sr := range s.roots {
		if sr.root == nil {
			continue
		}
		rel, err := filepath.Rel(sr.path, fallback)
		if err != nil || !relIsUnderRoot(rel) {
			continue // lexically not under this root; try the next
		}
		underSomeRoot = true
		relSlash := filepath.ToSlash(rel)
		f, fi, oerr := openInRoot(sr, relSlash)
		if oerr != nil {
			continue
		}
		regular := fi.Mode().IsRegular()
		_ = f.Close()
		if !regular {
			continue
		}
		s.fallbackRoot = i
		s.fallbackRel = relSlash
		s.log.Info("SPA fallback enabled", "path", fallback, "root", sr.path, "rel", relSlash)
		return
	}
	// Two distinct diagnoses, kept distinct: "you pointed at something outside
	// the roots" is a configuration mistake with a different remedy from "it
	// is inside a root but is not a readable regular file".
	if underSomeRoot {
		s.log.Warn("SPA fallback path could not be opened as a regular file within its served root; disabled",
			"fallback", fallback)
		return
	}
	s.log.Warn("SPA fallback path is outside every served root; disabled",
		"fallback", fallback)
}

// relIsUnderRoot reports whether a filepath.Rel result stays inside the root it
// was computed against. It is a lexical pre-filter for choosing a root, never a
// containment guarantee — os.Root provides that.
func relIsUnderRoot(rel string) bool {
	if filepath.IsAbs(rel) || rel == ".." {
		return false
	}
	return !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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

	for _, sr := range s.roots {
		if handled := s.tryServe(w, r, sr, rel); handled {
			return
		}
	}

	// No root served the path. In opt-in SPA-fallback mode (a fallback file
	// was configured and validated at construction), serve that shell for any
	// unmatched GET/HEAD route instead of a 404. This is intentionally a
	// per-request re-entry into serveFile so the fallback gets exactly the
	// same ETag / Content-Encoding / Range negotiation as any other file.
	// The fallback is re-opened — and therefore re-contained — here so that if
	// it is removed, replaced, or (security-critical) swapped for a symlink
	// escaping the root after construction, we fall through to an honest 404
	// (a miss), never a 500 or, worse, bytes from outside every served root.
	if s.fallbackRoot >= 0 {
		if f, size, ok := s.fallbackFileOK(); ok {
			s.serveFile(w, r, s.roots[s.fallbackRoot], s.fallbackRel, f, size, true)
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
	if path.Ext(rel) == "" {
		s.warnFallbackUnconfiguredOnce()
	}
	http.NotFound(w, r)
}

// fallbackFileOK re-opens the configured fallback through its root's
// containment handle and reports whether it is, right now, a regular file
// safely inside that root — returning the open handle and its size for
// serveFile to stream.
//
// This re-runs the full containment check on every unmatched route, not just
// once at construction (mem:self_review_checklist row 9), because the file at
// fallbackRel could have been replaced by a symlink after construction (e.g.
// an emptyDir mount swap in a deployment where readOnlyRootFilesystem is not
// actually enforced). os.Stat alone follows symlinks silently, which would let
// such a swap serve arbitrary filesystem content with 200 for every unmatched
// route.
//
// Under os.Root this check is strictly stronger than the EvalSymlinks +
// prefix-comparison it replaces: containment is decided by the kernel during
// the same openat that produces the handle we then serve, so the resolved
// object cannot be swapped between the check and the read.
//
// It runs on every unmatched route (a miss), not on the common-case hit path
// for a working SPA, and it is now a single openat rather than a symlink walk
// plus a stat.
func (s *staticServer) fallbackFileOK() (*os.File, int64, bool) {
	sr := s.roots[s.fallbackRoot]
	if sr.root == nil {
		return nil, 0, false
	}
	f, fi, err := openInRoot(sr, s.fallbackRel)
	if err != nil {
		return nil, 0, false
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, 0, false
	}
	return f, fi.Size(), true
}

// warnFallbackUnconfiguredOnce logs (once) that the opt-in SPA-fallback mode
// exists, only when no fallback is in effect (s.fallbackRoot < 0 — the single
// "is the fallback on?" test; s.fallbackPath is set whenever one was
// CONFIGURED, including when it was then rejected, so it must never be used
// for this decision).
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

// openInRoot opens rel under sr's containment handle and fstats the resulting
// handle. It is the single door every candidate path in this server goes
// through — the exact request path, the "<rel>.html" sibling, a directory's
// index.html, a Content-Encoding sidecar, and the SPA fallback.
//
// Two properties matter and both come from os.Root rather than from anything
// written here:
//
//   - Containment. Each component of rel is resolved with openat/O_NOFOLLOW
//     against the root descriptor; a symlink (or a "..") that would leave the
//     root makes the call fail with "path escapes from parent". Absolute
//     symlink targets are refused outright. That is a superset of what the
//     previous EvalSymlinks + withinRoot pair rejected.
//   - No TOCTOU. The FileInfo comes from fstat on the SAME handle that will be
//     read, not from a stat of a path string that is then re-opened, so no
//     swap between the check and the read is possible. The old sequence
//     re-derived the path twice after checking it.
//
// A candidate that skipped this function would be a path-traversal regression
// (mem:self_review_checklist row 22).
func openInRoot(sr *servedRoot, rel string) (*os.File, os.FileInfo, error) {
	f, err := sr.openAt(rel)
	if err != nil {
		return nil, nil, err
	}
	fi, serr := f.Stat()
	if serr != nil {
		_ = f.Close()
		return nil, nil, serr
	}
	return f, fi, nil
}

// relOrDot maps the site-root request ("" after cleanRelPath) onto the path
// os.Root uses for the root directory itself.
func relOrDot(rel string) string {
	if rel == "" {
		return "."
	}
	return rel
}

// tryServe attempts to serve rel from sr. It reports whether it produced a
// response. Any move that is not a successful stream returns false so the
// caller can fall through to the next root; the final 404 (or SPA fallback)
// happens only after every root has been tried.
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
// Every candidate is opened through the same openInRoot call (see its doc
// comment) — none of them gets a weaker check than the others. Each candidate
// now costs ONE openat instead of a symlink walk plus a stat plus, on a hit, a
// second open of the same path.
func (s *staticServer) tryServe(w http.ResponseWriter, r *http.Request, sr *servedRoot, rel string) bool {
	if sr.root == nil {
		return false // configured root unavailable; it matches nothing
	}

	// Candidate 1: exact regular file. The handle opened here is the handle
	// serveFile streams, so a hit costs exactly one openat.
	dirAtRel := false
	if f, fi, err := openInRoot(sr, relOrDot(rel)); err == nil {
		if fi.Mode().IsRegular() {
			s.serveFile(w, r, sr, rel, f, fi.Size(), false)
			return true
		}
		dirAtRel = fi.IsDir()
		_ = f.Close()
	}

	// Candidate 2: "<rel>.html" sibling file. Skipped for the root path
	// itself (rel == "") — "" + ".html" is meaningless, and "/" is already
	// covered by candidate 3 below.
	if rel != "" {
		htmlRel := rel + ".html"
		if f, fi, err := openInRoot(sr, htmlRel); err == nil {
			if fi.Mode().IsRegular() {
				s.serveFile(w, r, sr, htmlRel, f, fi.Size(), false)
				return true
			}
			_ = f.Close()
		}
	}

	// Candidate 3: directory + index.html. index.html can itself be a
	// symlink, and it goes through openInRoot like everything else, so a link
	// escaping the root is refused rather than followed.
	if !dirAtRel {
		return false // request path is not a directory in this root
	}
	idxRel := path.Join(rel, indexFile)
	f, fi, err := openInRoot(sr, idxRel)
	if err != nil {
		return false
	}
	if !fi.Mode().IsRegular() {
		_ = f.Close()
		return false
	}
	s.serveFile(w, r, sr, idxRel, f, fi.Size(), false)
	return true
}

// serveFile streams one regular file with negotiation. f is an already-open,
// already-contained handle for rel under sr (see openInRoot) positioned at
// offset 0, and serveFile takes ownership of it: it is closed on every return
// path. size is f's size from the same fstat that produced the handle.
//
// isFallback marks a call serving the configured opt-in SPA-fallback shell
// (see cachePolicy's isFallback parameter for why this needs to be threaded
// through explicitly rather than inferred from path).
func (s *staticServer) serveFile(w http.ResponseWriter, r *http.Request, sr *servedRoot, rel string, f *os.File, size int64, isFallback bool) {
	// Closes whatever f refers to at return time — the identity handle, or
	// the sidecar handle that replaced it below.
	defer func() { _ = f.Close() }()

	// Content-Encoding negotiation: prefer a pre-built sidecar the client
	// accepts, falling back to identity (the source file itself).
	bodyRel, enc := rel, ""
	if encRel, e := s.pickEncoding(r, sr, rel); e != "" {
		bf, bfi, err := openInRoot(sr, encRel)
		switch {
		case err != nil:
			// The startup index said this sidecar was here and it no longer
			// is (deleted, or replaced by something that escapes the root).
			// Degrade to identity rather than 5xx: the index is a cache of
			// filesystem state and the only correct answer to a stale entry
			// is the real file (mem:self_review_checklist row 9).
			s.log.Debug("indexed sidecar could not be opened; serving identity",
				"root", sr.path, "sidecar", encRel, "error", err)
		case !bfi.Mode().IsRegular():
			_ = bf.Close()
		default:
			_ = f.Close()
			f, bodyRel, enc, size = bf, encRel, e, bfi.Size()
		}
	}

	// Content-Type comes from the IDENTITY path's extension, never the
	// sidecar's: a ".js.br" body is still application/javascript, described by
	// Content-Encoding: br.
	ctype := mime.TypeByExtension(path.Ext(rel))
	if ctype == "" {
		ctype = "application/octet-stream"
	}

	h := w.Header()
	if enc != "" {
		h.Set("Content-Encoding", enc)
	}
	// Vary: Accept-Encoding on every sidecar-ELIGIBLE file, not only on the
	// responses that actually carried a sidecar.
	//
	// Setting it only alongside Content-Encoding was a cache-correctness bug.
	// The ETag is computed over the bytes actually served, so the identity and
	// brotli representations of one asset have different ETags; without Vary
	// on the identity response a shared cache or CDN is entitled to hand that
	// stored identity response (and its identity ETag) to a brotli-capable
	// client. The client then revalidates with an If-None-Match the origin
	// cannot match against the brotli body it would serve, and gets a full 200
	// where a 304 was available — on every request, for as long as the entry
	// lives.
	//
	// It is gated on eligibility rather than set unconditionally so assets
	// that can never have a sidecar (images, .woff2, video) are not needlessly
	// split across cache keys.
	if isPrecompressibleExt(rel) {
		h.Set("Vary", "Accept-Encoding")
	}
	h.Set("Content-Type", ctype)
	h.Set("Cache-Control", cachePolicy(rel, isFallback))
	// Range is fully implemented below (parseSingleRange/writeRange), so say
	// so. Without this header a client that probes for range support before
	// using it — video players, resumable downloaders, some CDNs — falls back
	// to refetching whole files.
	h.Set("Accept-Ranges", "bytes")

	// Strong ETag over the bytes actually served, so Range, Content-Encoding
	// and precondition matches stay consistent with the payload.
	etag, size, err := fileETagFrom(sr.etagKey(bodyRel), f, size)
	if err != nil {
		s.log.Warn("could not hash file for ETag", "root", sr.path, "path", bodyRel, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.Set("ETag", `"`+etag+`"`)

	// Conditional GET: RFC 9110 §13.1.2 places If-None-Match evaluation
	// BEFORE Range/If-Range in request precedence order, so a matching
	// conditional request must short-circuit to 304 here rather than fall
	// into the Range handling below and produce a 206. This runs for both
	// GET and HEAD — serveFile is only ever reached for those two methods
	// (serveHTTP rejects everything else with 405 before resolving a file),
	// so no extra method gate is needed here.
	//
	// A 304 must carry the same cache-relevant headers a 200 would (ETag,
	// Cache-Control, and Vary, all already set above) but no body and no
	// Content-Length describing one. WriteHeader(304) without writing a body
	// satisfies that — Content-Length is only set later, below this check,
	// so on this path it's never added to the header map in the first place.
	if inm := r.Header.Get("If-None-Match"); inm != "" && ifNoneMatchMatches(inm, etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// No If-Modified-Since support, deliberately: this server never sends
	// Last-Modified at all, and adding one would be actively misleading
	// rather than merely unused. In-image file mtimes are pinned to a fixed
	// epoch for build reproducibility (see packager.pinnedImmutableBinaryEpoch,
	// commit 1675d4c) — so a Last-Modified derived from on-disk mtime would be
	// the same constant timestamp across every build of every version of a
	// file, making it useless (and worse than useless: a client caching on
	// that basis could treat two genuinely different builds as identical).
	// The content-derived strong ETag is the only meaningful validator here.

	if r.Method == http.MethodGet {
		if rng, sat, unsat := parseSingleRange(r.Header.Get("Range"), size); sat {
			if ifRange := r.Header.Get("If-Range"); ifRange == "" || ifRangeMatches(ifRange, etag) {
				start, end := rng[0], rng[1]
				h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
				h.Set("Content-Length", strconv.FormatInt(end-start+1, 10))
				w.WriteHeader(http.StatusPartialContent)
				if err := writeRange(w, f, start, end); err != nil {
					s.log.Warn("range write failed", "path", bodyRel, "error", err)
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
	// f is positioned at offset 0: it was freshly opened, and fileETagFrom
	// guarantees it rewinds whenever it reads.
	if _, err := io.Copy(w, f); err != nil {
		s.log.Warn("write failed", "path", bodyRel, "error", err)
	}
}

// pickEncoding chooses a pre-compressed sidecar to serve based on
// Accept-Encoding, preferring brotli > gzip > zstd. It returns the sidecar's
// root-relative path and its encoding token, or ("", "") for identity.
//
// The extension gate comes FIRST and is the bulk of the win. Precompression
// only ever emits sidecars for a closed set of extensions
// (isPrecompressibleExt), so every image, font and video request used to pay
// three guaranteed-miss os.Stat calls plus three string concatenations to
// discover a .br/.gz/.zst that could not exist. Now those requests do a single
// map-free extension lookup and stop.
//
// For an eligible asset the startup index answers with one map lookup and no
// syscall at all. Only when the index is unavailable (see buildSidecarIndex)
// does this fall back to probing with root.Stat — the old behaviour, minus the
// misses the extension gate already removed.
func (s *staticServer) pickEncoding(r *http.Request, sr *servedRoot, rel string) (sidecarRel, encoding string) {
	if !isPrecompressibleExt(rel) {
		return "", ""
	}
	accept := r.Header.Get("Accept-Encoding")
	if accept == "" {
		return "", ""
	}
	mask := sr.sidecars[rel] // zero when absent; only consulted if indexed
	for _, cand := range sidecarCandidates {
		if !acceptsEncoding(accept, cand.enc) {
			continue
		}
		sp := rel + cand.ext
		if sr.sidecarsIndexed {
			if mask&cand.bit != 0 {
				return sp, cand.enc
			}
			continue
		}
		if fi, err := sr.root.Stat(sp); err == nil && !fi.IsDir() {
			return sp, cand.enc
		}
	}
	return "", ""
}

// acceptsEncoding reports whether the client's Accept-Encoding header accepts
// enc, honouring q=0 exclusions. Identity is always available and is never
// asked about here.
//
// Allocation-free: it scans the header in place rather than splitting it into
// a fresh map per request, which is what the previous parseAcceptEncoding did
// on every single request including the ones that went on to serve identity.
//
// Duplicate tokens are last-wins, matching the map-building implementation this
// replaces ("gzip;q=0, gzip" accepts gzip), and "*" is deliberately not
// expanded — also matching the previous behaviour, which recorded "*" in its
// map and then never looked it up.
func acceptsEncoding(header, enc string) bool {
	found, accepted := false, false
	for header != "" {
		part := header
		if i := strings.IndexByte(header, ','); i >= 0 {
			part, header = header[:i], header[i+1:]
		} else {
			header = ""
		}
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		token, params := part, ""
		if j := strings.IndexByte(part, ';'); j >= 0 {
			token = strings.TrimSpace(part[:j])
			params = part[j+1:]
		}
		if token == "" || !strings.EqualFold(token, enc) {
			continue
		}
		found = true
		accepted = qValueIsPositive(params)
	}
	return found && accepted
}

// qValueIsPositive parses the parameter tail of one Accept-Encoding element
// and reports whether it leaves the encoding acceptable. An absent or
// unparseable q is treated as acceptable, matching the previous behaviour.
func qValueIsPositive(params string) bool {
	if params == "" {
		return true
	}
	qv := strings.TrimSpace(params)
	if len(qv) >= 2 && (qv[0] == 'q' || qv[0] == 'Q') && qv[1] == '=' {
		qv = qv[2:]
	} else {
		qv = strings.TrimSpace(strings.TrimPrefix(qv, "q="))
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(qv), 64)
	if err != nil {
		return true
	}
	return f > 0
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

// fileETagFrom computes a strong ETag (hex SHA-256) and the byte size of the
// already-open file f, caching the result under key for the life of the
// process.
//
// It takes the open handle rather than a path so the bytes hashed are
// guaranteed to be the bytes served: there is no second path resolution
// between hashing and streaming that could land on a different file. On a
// cache hit it performs no I/O at all and returns the cached size (fallbackSize
// is used only if the cached entry is somehow absent by then, which it cannot
// be — see below).
//
// Contract: f is positioned at offset 0 on return, on every path, so the
// caller can stream it without a redundant seek.
func fileETagFrom(key string, f *os.File, fallbackSize int64) (string, int64, error) {
	if v, ok := fileETagCache.Load(key); ok {
		e := v.(fileETagEntry)
		return e.etag, e.size, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", fallbackSize, err
	}
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", fallbackSize, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", fallbackSize, err
	}
	etag := hex.EncodeToString(h.Sum(nil))
	fileETagCache.Store(key, fileETagEntry{etag: etag, size: n})
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

// ifNoneMatchMatches reports whether the If-None-Match request header
// matches the server's current entity-tag (etag: unquoted hex; this server
// only ever emits strong tags). Per RFC 9110 §13.1.2:
//
//   - The header carries a comma-separated LIST of entity-tags, any one of
//     which may match — not just a single exact tag. Each list element is
//     scanned off in turn via scanETagToken.
//   - "*" matches any current representation, regardless of value, and short-
//     circuits the whole header (RFC 9110 §13.1.2: "or if '*' is given").
//   - Comparison is WEAK: a request tag prefixed "W/" matches an otherwise
//     equal tag value. This server itself never emits a weak tag, so in
//     practice the only thing this needs to tolerate is a W/-prefixed tag
//     arriving FROM the client (e.g. a cache or proxy that downgraded a
//     stored strong tag to weak) — hence stripping "W/" and comparing the
//     remaining quoted value is sufficient; there is no strong tag on the
//     request side that also needs downgrading.
func ifNoneMatchMatches(header, etag string) bool {
	for {
		header = strings.TrimSpace(header)
		if header == "" {
			return false
		}
		if header[0] == ',' {
			header = header[1:]
			continue
		}
		if header[0] == '*' {
			return true
		}
		tag, remain := scanETagToken(header)
		if tag == "" {
			return false // malformed remainder: nothing left worth matching
		}
		tag = strings.TrimPrefix(tag, "W/")
		if strings.Trim(tag, `"`) == etag {
			return true
		}
		header = remain
	}
}

// scanETagToken scans one entity-tag off the front of s, per the grammar in
// RFC 9110 §8.8.3: a strong tag is a quoted string; a weak tag is "W/"
// followed by a quoted string. Returns the raw token (including any "W/"
// prefix and surrounding quotes) and the unconsumed remainder, or ("", "")
// if s does not begin with a well-formed entity-tag.
func scanETagToken(s string) (tag, remain string) {
	start := 0
	if strings.HasPrefix(s, "W/") {
		start = 2
	}
	if len(s) < start+2 || s[start] != '"' {
		return "", ""
	}
	for i := start + 1; i < len(s); i++ {
		if s[i] == '"' {
			return s[:i+1], s[i+1:]
		}
	}
	return "", "" // unterminated quoted string
}

// writeRange streams bytes [start,end] of the already-open f to w. It takes the
// handle rather than a path so no second open (and no second path resolution)
// happens between the ETag/precondition evaluation and the bytes sent.
func writeRange(w io.Writer, f *os.File, start, end int64) error {
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return err
	}
	_, err := io.CopyN(w, f, end-start+1)
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
func cachePolicy(rel string, isFallback bool) string {
	if isFallback {
		return "no-cache"
	}
	// rel is root-relative and carries no leading slash, so "/immutable/" is
	// matched against "/"+rel — otherwise an immutable directory sitting at
	// the top of a root would stop matching, which it did when the served path
	// was the absolute resolved one.
	if strings.Contains("/"+rel, immutableAssetMarker) {
		return "public, max-age=" + strconv.Itoa(immutableMaxAge) + ", immutable"
	}
	if looksHashed(path.Base(rel)) {
		return "public, max-age=" + strconv.Itoa(immutableMaxAge) + ", immutable"
	}
	return "no-cache"
}

// looksHashed reports whether base matches SvelteKit's "name-<hex>" convention
// (e.g. "app-abc123.js", "page.svelte-9f2a3b.css"). A heuristic guard only; the
// /immutable/ marker is authoritative.
func looksHashed(base string) bool {
	ext := path.Ext(base)
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

// cleanRelPath converts a URL path into a clean, slash-separated relative path
// safe to resolve inside an os.Root. It rejects traversal and absolute paths.
//
// It does NOT percent-decode. Callers pass r.URL.Path, which net/http has
// already decoded; decoding a second time was both wrong and, in the only
// direction that mattered, user-hostile: an asset legitimately named
// "a%2e.js" (requested as /a%252e.js, arriving here already decoded to
// "a%2e.js") was decoded again into "a..js", tripped the ".." check and
// returned 400 for a file that exists. Removing the second decode also removes
// a decoding step from the path an attacker controls, so it can only tighten
// traversal handling — a request for /..%2Fx still arrives here as "/../x",
// because net/http decoded %2F, and is rejected by the segment check below
// exactly as before. Every traversal check is unchanged.
func cleanRelPath(rawPath string) (string, error) {
	p := rawPath
	if p == "" {
		p = "/"
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." || part == "." {
			return "", errors.New("invalid path: traversal")
		}
	}
	clean := strings.TrimPrefix(p, "/")
	if clean == "." {
		clean = ""
	}
	if strings.Contains(clean, "..") {
		return "", errors.New("invalid path: traversal")
	}
	return clean, nil
}
