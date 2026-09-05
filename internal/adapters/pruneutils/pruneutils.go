package pruneutils

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// IsUtilityPackage marks this as a reusable utility, not a port adapter.
const IsUtilityPackage = true

// DefaultJunkPatterns defines default patterns for files in node_modules/vendor directories
// that are not needed at production runtime.
//
// Documentation/metadata files (README, LICENSE, CHANGELOG, ...) are deliberately NOT
// listed here as wildcard prefixes: see docBaseNames/isDocFile below for why, and for
// how they are matched instead.
var DefaultJunkPatterns = []string{
	// TypeScript definitions and configs
	"**/*.d.ts",
	"**/*.d.ts.map",
	"**/*.d.mts",
	"**/*.d.mts.map",
	"**/*.d.cts",
	"**/*.d.cts.map",
	"**/tsconfig.json",
	"**/tsconfig.*.json",

	// Source maps (pruned unless KeepSourcemap is true)
	"**/*.map",

	// Project metadata that isn't a "doc file" in the README/LICENSE sense but is
	// still never needed at runtime.
	"**/.npmignore",
	"**/.eslintrc*",
	"**/.prettierrc*",
	"**/.editorconfig",
	"**/CODEOWNERS",

	// OS/editor cruft, VCS files and lockfiles: never read at runtime, and their
	// names don't collide with real module names, so a plain wildcard is safe here.
	"**/.DS_Store",
	"**/.gitignore",
	"**/.gitattributes",
	"**/.npmrc",
	"**/yarn.lock",
	"**/package-lock.json",
	"**/pnpm-lock.yaml",
	"**/.vscode/**",
	"**/.idea/**",
	"**/.yarn/cache/**",
	"**/*.log",

	// Tests and tooling
	"**/__tests__/**",
	"**/test/**",
	"**/tests/**",
	"**/*.test.js",
	"**/*.spec.js",
	"**/*.test.mjs",
	"**/*.spec.mjs",
	"**/*.test.ts",
	"**/*.spec.ts",
	"**/*.test.d.ts",
	"**/.github/**",
	"**/.git/**",
	"**/.circleci/**",
	"**/.travis.yml",

	// Build scripts and manifests
	"**/Makefile",
	"**/Dockerfile*",
	"**/docker-compose*.yml",
}

// docBaseNames are the canonical, lowercase basenames of common documentation and
// project-metadata files. isDocFile compares a file's basename against these
// case-insensitively, so README/Readme/readme/ReadMe are all treated alike.
var docBaseNames = []string{
	"readme",
	"changelog",
	"changes",
	"history",
	"license",
	"licence",
	"authors",
	"contributors",
	"notice",
}

// docExtensions are the file extensions (including the leading dot) recognized as
// documentation/text formats when appended to a docBaseNames entry. The empty
// string matches the bare name (e.g. "LICENSE" with no extension at all, which is
// the most common real-world convention).
var docExtensions = []string{
	"",
	".md", ".markdown", ".mdown", ".mkd", ".mkdn",
	".txt", ".text",
	".rst", ".adoc", ".asciidoc", ".textile", ".rdoc", ".org", ".creole", ".pod",
	".wiki", ".1st", ".rtf",
}

// docFileNames is the fully expanded cross product of docBaseNames and
// docExtensions, built once at package init. It exists purely so isDocFile is
// a single map lookup instead of the 9x18 prefix/suffix scan it used to be:
// IsJunk runs once per walked file and a vendored node_modules tree has tens
// of thousands of them. The set it describes is character-for-character the
// one the old nested loop accepted — TestIsDocFileMatchesLegacyOracle pins
// that against the original implementation.
var docFileNames = func() map[string]struct{} {
	m := make(map[string]struct{}, len(docBaseNames)*len(docExtensions))
	for _, name := range docBaseNames {
		for _, ext := range docExtensions {
			m[name+ext] = struct{}{}
		}
	}
	return m
}()

// isDocFile reports whether base (a bare filename, no directory component) is a
// documentation/metadata file such as README.md or LICENSE.
//
// This is deliberately NOT a "**/README*" style wildcard prefix match: that
// pattern also matches real runtime source files that merely start with the same
// word, e.g. "readme.js" or "license-checker.js", and would silently delete them
// from the vendor layer. Instead this requires the basename (case-insensitively)
// to be exactly one of the known words, optionally followed by a recognized
// documentation extension — so "README", "README.md" and "readme.txt" match, but
// "readme.js" and "license-checker.js" do not.
func isDocFile(base string) bool {
	_, ok := docFileNames[strings.ToLower(base)]
	return ok
}

// PruneOptions configures vendor pruning behaviour.
type PruneOptions struct {
	NoPrune       bool     // Disable pruning entirely
	KeepSourcemap bool     // Preserve *.map files even when pruning is enabled
	KeepPatterns  []string // Additional glob patterns to keep/whitelist

	// ExcludeDirs names top-level subdirectories (relative to the tree being
	// walked, e.g. "client") to skip entirely — unlike NoPrune/KeepPatterns,
	// which decide file-by-file whether something is disposable junk, this
	// unconditionally omits a whole subtree regardless of NoPrune, because
	// its contents are packaged into a different layer entirely and including
	// them here would duplicate them, not just fail to prune them.
	ExcludeDirs []string
}

// PruneResult records what was pruned from a vendor directory tree: how many
// files were excluded from the packaged layer, how many bytes that saved, and
// (for callers that want the detail) which relative paths were excluded.
//
// This is populated by packager.BuildDirectoryTreeLayerWithPruning as it walks
// the tree deciding, file by file, what to include in the layer — pruning here
// never touches the host filesystem, it only omits junk files from the tar
// stream being built.
type PruneResult struct {
	FilesPruned int
	BytesSaved  int64
	PrunedPaths []string
}

// IsExcludedDir reports whether relPath (slash-separated, relative to the
// tree being walked) is one of opts.ExcludeDirs — an exact top-level
// subdirectory name, not a glob. Unlike IsJunk, this applies even when
// opts.NoPrune is set, since ExcludeDirs isn't about disposable junk, it's
// about a subtree that belongs to a different layer entirely.
func IsExcludedDir(relPath string, opts PruneOptions) bool {
	normPath := strings.TrimPrefix(filepath.ToSlash(relPath), "/")
	for _, ex := range opts.ExcludeDirs {
		if normPath == strings.Trim(ex, "/") {
			return true
		}
	}
	return false
}

// patternMeta are the glob metacharacters that make a pattern fragment
// something doublestar has to interpret rather than something a plain string
// comparison can decide.
const patternMeta = "*?[]{}"

// hasMeta reports whether s contains any glob metacharacter.
func hasMeta(s string) bool {
	return strings.ContainsAny(s, patternMeta)
}

// prefixSuffixRule matches a basename against a single-star glob such as
// "Dockerfile*" or "docker-compose*.yml". Because a basename never contains a
// separator and doublestar's "*" matches any run of non-separator characters
// (the empty run included), "prefix*suffix" is exactly "starts with prefix,
// ends with suffix, and is long enough that the two do not overlap".
type prefixSuffixRule struct {
	prefix string
	suffix string
}

func (r prefixSuffixRule) match(base string) bool {
	return len(base) >= len(r.prefix)+len(r.suffix) &&
		strings.HasPrefix(base, r.prefix) &&
		strings.HasSuffix(base, r.suffix)
}

// junkRules is DefaultJunkPatterns compiled once, at package init, into the
// cheapest form each pattern admits.
//
// The old implementation called doublestar.Match twice per pattern per file —
// 84 calls, each of which re-parses the pattern string from scratch — which
// measured at ~4.5 microseconds for a single verdict. IsJunk is called once per
// walked file and a vendored node_modules runs to tens of thousands of files
// per layer per platform, so that is real time inside the walk.
//
// Every reduction below is an exact restatement of what doublestar does for
// that pattern shape, not an approximation:
//
//   - "**/*.d.ts" and friends: "**/" consumes whole leading segments and "*"
//     cannot cross a separator, so the pattern matches iff the final segment
//     ends with ".d.ts" — a strings.HasSuffix on the basename.
//   - "**/yarn.lock" and friends: same reasoning with no wildcard left over,
//     so it is basename equality.
//   - "**/Dockerfile*": a single-star basename glob, see prefixSuffixRule.
//   - "**/test/**": doublestar's trailing "/**" matches zero or more segments,
//     so this holds iff *some* segment of the path equals "test" — including
//     the last one, which is why a file literally named "test" matches too.
//   - "**/.yarn/cache/**": the same, for a run of consecutive segments.
//
// The old implementation also matched every pattern against the basename as
// well as the full path. That arm is subsumed: for a "**/X" pattern (X free of
// separators) the basename arm is exactly the rule above, and for a
// "**/DIR/**" pattern the basename can only match when the basename itself
// equals DIR, which the segment scan already covers because the basename is
// always the path's last segment.
//
// TestIsJunkMatchesLegacyOracle runs both implementations over a large
// generated corpus and asserts identical verdicts; it is the reason this
// reduction can be trusted rather than merely believed.
type junkRules struct {
	exactBases   map[string]struct{}
	suffixes     []string
	prefixSuffix []prefixSuffixRule
	dirSegments  map[string]struct{}
	dirSequences [][]string
	residual     []string
}

// compileJunkRules classifies patterns into junkRules. Anything it cannot
// prove reducible stays in residual and is still matched with doublestar
// against both the full path and the basename, exactly as before — so adding
// an exotic pattern to DefaultJunkPatterns degrades performance rather than
// correctness.
func compileJunkRules(patterns []string) junkRules {
	r := junkRules{
		exactBases:  make(map[string]struct{}, len(patterns)),
		dirSegments: make(map[string]struct{}, len(patterns)),
	}
	for _, pattern := range patterns {
		rest, ok := strings.CutPrefix(pattern, "**/")
		if !ok {
			r.residual = append(r.residual, pattern)
			continue
		}

		if inner, ok := strings.CutSuffix(rest, "/**"); ok {
			if inner == "" || hasMeta(inner) {
				r.residual = append(r.residual, pattern)
				continue
			}
			segs := strings.Split(inner, "/")
			// An empty segment ("a//b") would make the segment scan's
			// semantics diverge from doublestar's; nothing in the defaults
			// produces one, and anything that did belongs in the residual.
			if slices.Contains(segs, "") {
				r.residual = append(r.residual, pattern)
				continue
			}
			if len(segs) == 1 {
				r.dirSegments[segs[0]] = struct{}{}
			} else {
				r.dirSequences = append(r.dirSequences, segs)
			}
			continue
		}

		if strings.Contains(rest, "/") {
			r.residual = append(r.residual, pattern)
			continue
		}

		switch {
		case !hasMeta(rest):
			r.exactBases[rest] = struct{}{}
		case strings.Count(rest, "*") == 1 && !strings.ContainsAny(rest, "?[]{}"):
			star := strings.IndexByte(rest, '*')
			prefix, suffix := rest[:star], rest[star+1:]
			if prefix == "" {
				r.suffixes = append(r.suffixes, suffix)
			} else {
				r.prefixSuffix = append(r.prefixSuffix, prefixSuffixRule{prefix: prefix, suffix: suffix})
			}
		default:
			r.residual = append(r.residual, pattern)
		}
	}
	return r
}

// defaultJunkRules is DefaultJunkPatterns compiled at init.
//
// DefaultJunkPatterns is an exported var, so a caller could in principle
// append to it after init and find the addition ignored here. That was already
// only theoretically supported (nothing in the tree does it) and IsJunk's
// contract has always been "the default blocklist", not "whatever the slice
// currently holds"; making it a compile-once value is what buys the ~100x.
var defaultJunkRules = compileJunkRules(DefaultJunkPatterns)

// matches reports whether normPath (already slash-normalised and
// trimmed) or its basename is matched by the compiled default blocklist.
func (r *junkRules) matches(normPath, base string) bool {
	if _, ok := r.exactBases[base]; ok {
		return true
	}
	for _, suffix := range r.suffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	for _, ps := range r.prefixSuffix {
		if ps.match(base) {
			return true
		}
	}
	if len(r.dirSegments) > 0 || len(r.dirSequences) > 0 {
		if r.matchesSegments(normPath) {
			return true
		}
	}
	for _, pattern := range r.residual {
		if matched, _ := doublestar.Match(pattern, normPath); matched {
			return true
		}
		if matched, _ := doublestar.Match(pattern, base); matched {
			return true
		}
	}
	return false
}

// matchesSegments walks normPath one segment at a time — without allocating a
// slice for the split, since this runs once per file — checking each against
// the single-segment directory set and each position against every multi-
// segment sequence.
func (r *junkRules) matchesSegments(normPath string) bool {
	for i := 0; i <= len(normPath); {
		end := strings.IndexByte(normPath[i:], '/')
		var seg string
		if end < 0 {
			seg = normPath[i:]
			end = len(normPath)
		} else {
			end += i
			seg = normPath[i:end]
		}
		if _, ok := r.dirSegments[seg]; ok {
			return true
		}
		for _, seq := range r.dirSequences {
			if matchSequenceAt(normPath, i, seq) {
				return true
			}
		}
		i = end + 1
	}
	return false
}

// matchSequenceAt reports whether the segments of normPath starting at byte
// offset start are exactly seq, in order.
func matchSequenceAt(normPath string, start int, seq []string) bool {
	i := start
	for _, want := range seq {
		if i > len(normPath) {
			return false
		}
		end := strings.IndexByte(normPath[i:], '/')
		var seg string
		if end < 0 {
			seg = normPath[i:]
			end = len(normPath)
		} else {
			end += i
			seg = normPath[i:end]
		}
		if seg != want {
			return false
		}
		i = end + 1
	}
	return true
}

// keepRule is one entry of PruneOptions.KeepPatterns, classified once so the
// per-file path does not re-decide whether the pattern needs doublestar at
// all. A literal pattern (no metacharacters) can only ever match itself, so it
// reduces to two string comparisons.
type keepRule struct {
	pattern string
	literal bool
}

// Matcher is PruneOptions compiled for repeated use over one directory tree.
//
// Build it once per walk and call IsJunk per file: the KeepPatterns
// classification, which is per-options rather than per-file work, then happens
// once instead of once per walked file. IsJunk (the package-level function) is
// the one-shot equivalent and stays for callers with a single path to judge.
type Matcher struct {
	noPrune       bool
	keepSourcemap bool
	keeps         []keepRule
}

// NewMatcher compiles opts into a Matcher.
func NewMatcher(opts PruneOptions) Matcher {
	m := Matcher{noPrune: opts.NoPrune, keepSourcemap: opts.KeepSourcemap}
	if len(opts.KeepPatterns) > 0 {
		m.keeps = make([]keepRule, 0, len(opts.KeepPatterns))
		for _, k := range opts.KeepPatterns {
			m.keeps = append(m.keeps, keepRule{pattern: k, literal: !hasMeta(k)})
		}
	}
	return m
}

// IsJunk returns true if relPath (slash-separated relative path) matches junk
// blocklists and is not exempted by KeepPatterns or KeepSourcemap. The isDir
// flag is accepted for parity with callers that already know whether relPath is
// a directory (matching itself is path/pattern based and works the same for
// files and directories), but is not currently needed to decide the match.
func (m *Matcher) IsJunk(relPath string, _ bool) bool {
	if m.noPrune {
		return false
	}

	normPath := filepath.ToSlash(relPath)
	normPath = strings.TrimPrefix(normPath, "/")
	normPath = strings.TrimPrefix(normPath, "./")

	base := filepath.Base(normPath)

	// Check if matching keep patterns
	for _, keep := range m.keeps {
		if keep.literal {
			if keep.pattern == normPath || keep.pattern == base {
				return false
			}
			continue
		}
		if matched, _ := doublestar.Match(keep.pattern, normPath); matched {
			return false
		}
		if matched, _ := doublestar.Match(keep.pattern, base); matched {
			return false
		}
	}

	// Preserve source maps if KeepSourcemap is true
	if m.keepSourcemap && strings.HasSuffix(normPath, ".map") && !strings.HasSuffix(normPath, ".d.ts.map") {
		return false
	}

	if isDocFile(base) {
		return true
	}

	return defaultJunkRules.matches(normPath, base)
}

// IsJunk returns true if relPath (slash-separated relative path) matches junk blocklists
// and is not exempted by KeepPatterns or KeepSourcemap. The isDir flag is accepted for
// parity with callers that already know whether relPath is a directory (matching itself
// is path/pattern based and works the same for files and directories), but is not
// currently needed to decide the match.
//
// Callers judging a whole tree should build a Matcher once with NewMatcher and
// call its IsJunk method instead; this function recompiles opts on every call.
func IsJunk(relPath string, isDir bool, opts PruneOptions) bool {
	// Checked before NewMatcher so the disabled-pruning path stays free of
	// even the keep-pattern classification it would never consult.
	if opts.NoPrune {
		return false
	}
	m := NewMatcher(opts)
	return m.IsJunk(relPath, isDir)
}
