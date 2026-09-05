// Package ignore implements a small, dependency-light matcher for
// .pokkumignore files: gitignore-syntax exclude lists read from a project
// root so that files like .env.local, test fixtures and source maps never
// reach a scan.
//
// This package is deliberately free of any dependency on internal/core or
// internal/ports, and it does not know about SBOMs, syft, or the compiler.
// internal/adapters/sbom is its first consumer, but the package was placed
// here rather than nested under sbom/ specifically so that a future
// consumer — the SvelteKit compiler (W2), which also needs to decide which
// project files matter — can import it without creating a dependency on the
// SBOM adapter.
//
// # Supported syntax
//
// Matcher implements the common subset of gitignore(5) syntax:
//
//   - Blank lines and lines starting with '#' are ignored (comments).
//   - A leading '!' negates a pattern: a later, matching negation
//     un-excludes a path that an earlier pattern excluded.
//   - A trailing '/' restricts a pattern to directories.
//   - A pattern containing a '/' anywhere but at the end (whether leading
//     or in the middle) is anchored to the root passed to Load/New; a
//     pattern with no '/' at all matches the basename at any depth.
//   - '*' matches any run of characters within one path segment; '**'
//     matches across segment boundaries, including zero segments.
//
// # Precedence
//
// Patterns are evaluated in the order given, and — matching git — the last
// pattern that matches a given path wins, so a later negation overrides an
// earlier exclusion. Load seeds the list with DefaultPatterns() before the
// project's own .pokkumignore, so a project's rules always have the final
// say over the built-in defaults.
//
// # Directory pruning
//
// Once an ancestor directory of a path matches an (non-negated) exclusion,
// the path is excluded outright: Match does not look inside an excluded
// directory for a deeper negation. This mirrors real git, which documents
// the same limitation ("It is not possible to re-include a file if a
// parent directory of that file is excluded") — Matcher does not attempt
// to be more capable than the syntax it implements.
package ignoreutils

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
)

// IsUtilityPackage marks this as a reusable utility, not a port adapter.
const IsUtilityPackage = true

// FileName is the exclude file Load reads from a project root.
const FileName = ".pokkumignore"

// DefaultPatterns are applied before any patterns from a project's
// .pokkumignore, so that a project's own rules — including negations — can
// always override them.
func DefaultPatterns() []string {
	return []string{
		"node_modules/.cache",
		".git/",
		"*.map",
		".env*",
		// Storybook's built output. It is generated, minified, and not source,
		// but it sits at the project root where the pre-build source scan finds
		// it — a real project produced 11 "hardcoded secret" findings in one
		// storybook-static/sb-manager/globals-runtime.js and could not build.
		// The advice the guard prints does not help there either: a
		// pokkum:allow-secret comment cannot be added to generated output, so
		// the only route was a regex against machine-generated code.
		//
		// Deliberately narrow. Only the OUTPUT directory is excluded, not
		// .storybook/, which holds hand-written configuration and belongs in the
		// scan. Other build outputs (build/, dist/) are left in for now: they
		// are conventional enough to be a policy decision rather than an
		// obvious omission, and a project's own .pokkumignore overrides these
		// defaults either way.
		"storybook-static/",
	}
}

// ruleKind records the shape compile recognised in a pattern, so matchGlob
// can answer with a string comparison instead of re-entering doublestar.
//
// doublestar has no compiled-pattern form: doublestar.Match re-parses its
// pattern string on every call (v4.10.0 match.go). Match evaluates every rule
// against every ancestor prefix of a path, so the parse cost is paid
// depth x len(rules) times per file, on the hot path of four separate
// directory walks per build (the pre-build secret scan, the SBOM project
// scan, the remote-cache source-tree hash and the post-build output scan).
// Nearly every pattern a real .pokkumignore holds -- and every entry of
// DefaultPatterns -- is one of the shapes below, none of which needs a glob
// engine.
//
// Every fast path is a claim about doublestar's OWN semantics for that
// pattern shape, not about what the shape intuitively means; see the
// soundness notes on each constant. TestMatchDifferential and
// FuzzRuleDifferential check the claims against the pre-optimisation
// implementation kept verbatim as an oracle, which is the only reason these
// are trustworthy.
type ruleKind uint8

const (
	// kindGlob is the residual case: hand the pattern to doublestar.
	kindGlob ruleKind = iota

	// kindPathLiteral is an anchored pattern with no glob metacharacter, e.g.
	// "node_modules/.cache" or "/build". doublestar's matcher decodes pattern
	// and name one rune at a time and compares them, requiring both to be
	// exhausted together, so a metacharacter-free pattern matches exactly the
	// identical string -- provided both decode unambiguously, which is what
	// the utf8 guard in classify exists for.
	kindPathLiteral

	// kindBaseLiteral is "**/<lit>" with lit free of metacharacters and of
	// '/'. A leading "**/" can only end at a segment boundary (or at position
	// 0), and lit contains no '/', so lit must match the whole final segment:
	// equivalent to basename(candidate) == lit.
	kindBaseLiteral

	// kindBasePrefix is "**/<lit>*" -- the final segment must start with lit,
	// since '*' never crosses a '/'.
	kindBasePrefix

	// kindBaseSuffix is "**/*<lit>".
	kindBaseSuffix

	// kindBaseContains is "**/*<lit>*".
	kindBaseContains
)

// rule is one compiled pattern line.
type rule struct {
	glob     string   // the doublestar pattern to match against a candidate path
	lit      string   // the literal kind compares against; empty for kindGlob
	kind     ruleKind // which comparison matchGlob uses
	negate   bool
	dirOnly  bool
	anchored bool
}

// Matcher matches project-relative paths against a compiled, ordered set of
// gitignore-style rules. The zero value matches nothing; construct with New
// or Load.
type Matcher struct {
	rules []rule
}

// New compiles patterns in order. Later patterns take precedence over
// earlier ones when both match the same path (see the package doc). An
// invalid pattern (only possible via a malformed doublestar glob) is
// reported with the offending line.
func New(patterns []string) (*Matcher, error) {
	m := &Matcher{}
	for i, raw := range patterns {
		r, ok, err := compile(raw)
		if err != nil {
			return nil, fmt.Errorf("ignore: pattern %d %q: %w", i+1, raw, err)
		}
		if !ok {
			continue
		}
		m.rules = append(m.rules, r)
	}
	return m, nil
}

// Load reads dir/.pokkumignore (gitignore syntax, see the package doc) and
// returns a Matcher seeded with DefaultPatterns() followed by the file's
// own patterns. A missing file is not an error — Load returns a Matcher
// with just the defaults, which is the documented behaviour of a project
// that has not opted into any excludes of its own.
func Load(dir string) (*Matcher, error) {
	filePatterns, err := ReadPatterns(dir)
	if err != nil {
		return nil, err
	}
	return New(append(DefaultPatterns(), filePatterns...))
}

// ReadPatterns reads dir/.pokkumignore and returns its lines verbatim, in
// file order, with no interpretation applied yet — comments and blank
// lines are still present, since compile (called from New) is what skips
// them. A missing file returns (nil, nil), not an error.
//
// This is exposed separately from Load so a caller that needs to compose
// the project's own patterns with additional rules from elsewhere — such
// as internal/adapters/sbom, which layers .pokkumignore's contents on top
// of a request-level exclude list rather than under DefaultPatterns alone
// — can do so without re-implementing file reading.
func ReadPatterns(dir string) ([]string, error) {
	f, err := os.Open(filepath.Join(dir, FileName))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("ignore: reading %s: %w", FileName, err)
	}
	defer f.Close()

	var patterns []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		patterns = append(patterns, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ignore: reading %s: %w", FileName, err)
	}
	return patterns, nil
}

// compile parses one pattern line. ok is false for a blank line or comment,
// which the caller should skip without adding a rule.
func compile(line string) (r rule, ok bool, err error) {
	// gitignore trims trailing whitespace unless escaped; the escaped case
	// is rare enough in practice that we do not support it.
	s := strings.TrimRight(line, " \t")
	s = strings.TrimLeft(s, " \t")
	if s == "" || strings.HasPrefix(s, "#") {
		return rule{}, false, nil
	}

	if strings.HasPrefix(s, "\\#") || strings.HasPrefix(s, "\\!") {
		s = s[1:]
	} else if strings.HasPrefix(s, "!") {
		r.negate = true
		s = s[1:]
	}
	if s == "" {
		return rule{}, false, nil
	}

	if strings.HasSuffix(s, "/") {
		r.dirOnly = true
		s = strings.TrimSuffix(s, "/")
	}
	if s == "" {
		return rule{}, false, nil
	}

	if strings.HasPrefix(s, "/") {
		r.anchored = true
		s = strings.TrimPrefix(s, "/")
	} else if strings.Contains(s, "/") {
		r.anchored = true
	}

	unanchored := !r.anchored
	if unanchored {
		s = "**/" + s
	}

	if _, err := doublestar.Match(s, "probe"); err != nil {
		return rule{}, false, fmt.Errorf("invalid glob: %w", err)
	}

	r.glob = s
	r.classify(unanchored)
	return r, true, nil
}

// globMeta reports whether s holds any byte doublestar treats as anything
// other than a literal. Deliberately over-broad: ']' and '}' outside a class
// or alternation are literals to doublestar, but counting them as meta only
// costs a rule its fast path, whereas missing one would be a wrong verdict.
func globMeta(s string) bool {
	return strings.ContainsAny(s, `*?[]{}\`)
}

// literalSafe reports whether lit can be compared with ==, HasPrefix,
// HasSuffix or Contains and agree with doublestar's rune-by-rune comparison.
//
// doublestar decodes both pattern and candidate with utf8.DecodeRuneInString,
// which maps every invalid byte to U+FFFD with size 1. Two DIFFERENT invalid
// bytes therefore compare equal inside doublestar while byte comparison says
// they differ -- so a pattern that is not valid UTF-8, or that contains a
// literal U+FFFD, cannot use a byte-wise fast path. Once lit is valid UTF-8
// and U+FFFD-free, every rune it holds has exactly one valid encoding, so
// doublestar agreeing rune-for-rune is the same statement as the bytes being
// equal. It also means a valid-UTF-8 lit can never begin at a continuation
// byte of the candidate, so a HasSuffix/Contains hit is always on a rune
// boundary -- which is what makes the '*' kinds sound too.
func literalSafe(lit string) bool {
	return lit != "" && utf8.ValidString(lit) && !strings.ContainsRune(lit, utf8.RuneError)
}

// classify records which of the cheap comparisons in ruleKind, if any, is
// exactly equivalent to running doublestar against r.glob. unanchored says
// the pattern had no '/' at all and so was rewritten to "**/"+pattern by the
// caller, which is what lets the basename kinds assume no '/' in the literal.
//
// Anything not recognised keeps kindGlob and goes to doublestar as before, so
// an unrecognised shape is slow, never wrong.
func (r *rule) classify(unanchored bool) {
	if !unanchored {
		if !globMeta(r.glob) && literalSafe(r.glob) {
			r.kind, r.lit = kindPathLiteral, r.glob
		}
		return
	}

	// r.glob is "**/" + the original pattern, and the original had no '/'.
	core := strings.TrimPrefix(r.glob, "**/")
	leadingStar := strings.HasPrefix(core, "*")
	if leadingStar {
		core = core[1:]
	}
	trailingStar := strings.HasSuffix(core, "*")
	if trailingStar {
		core = core[:len(core)-1]
	}
	// core == "" covers the degenerate "*", "**" and "***" patterns, whose
	// zero-length-match semantics are doublestar's business, not ours.
	if globMeta(core) || strings.Contains(core, "/") || !literalSafe(core) {
		return
	}

	switch {
	case !leadingStar && !trailingStar:
		r.kind = kindBaseLiteral
	case leadingStar && !trailingStar:
		r.kind = kindBaseSuffix
	case !leadingStar && trailingStar:
		r.kind = kindBasePrefix
	default:
		r.kind = kindBaseContains
	}
	r.lit = core
}

// baseName returns the final path segment of candidate, without allocating.
// Unlike path.Base it does not special-case the empty or trailing-slash
// forms ("" and "a/" both yield ""), because those are what doublestar's own
// segment matching sees: a pattern ending in a non-empty literal segment
// cannot match a name whose last segment is empty.
func baseName(candidate string) string {
	if i := strings.LastIndexByte(candidate, '/'); i >= 0 {
		return candidate[i+1:]
	}
	return candidate
}

// matchGlob reports whether candidate (a slash-separated path, relative to
// the matcher's root, with no leading slash) matches the rule's compiled
// glob.
//
// For every kind but kindGlob this is a string comparison that is exactly
// equivalent to doublestar's answer for that pattern shape (see ruleKind).
// The residual globs use MatchUnvalidated rather than Match: compile has
// already run the pattern through doublestar.Match once, and -- more to the
// point -- doublestar's validate flag only ever decides whether a non-match
// is reported as ErrBadPattern or as a plain false (v4.10.0 match.go, the
// two `if validate` sites). The matched bool is identical for every input,
// valid pattern or not, and matchGlob discards the error either way.
func (r *rule) matchGlob(candidate string) bool {
	switch r.kind {
	case kindPathLiteral:
		return candidate == r.lit
	case kindBaseLiteral:
		return baseName(candidate) == r.lit
	case kindBasePrefix:
		return strings.HasPrefix(baseName(candidate), r.lit)
	case kindBaseSuffix:
		return strings.HasSuffix(baseName(candidate), r.lit)
	case kindBaseContains:
		return strings.Contains(baseName(candidate), r.lit)
	default:
		return doublestar.MatchUnvalidated(r.glob, candidate)
	}
}

// Match reports whether relPath — slash- or OS-separated, relative to the
// project root, with no leading path separator — should be excluded.
// isDir reports whether relPath names a directory.
//
// Match evaluates every ancestor directory of relPath first: once one of
// them is excluded, relPath is excluded outright and the deeper rules are
// never consulted, matching real git's directory-pruning behaviour (see the
// package doc). This makes Match's cost proportional to the depth of
// relPath, not the number of rules times the depth, which is irrelevant at
// the sizes a project's ignore file reaches but keeps the implementation
// honest about what it is doing.
func (m *Matcher) Match(relPath string, isDir bool) bool {
	if m == nil || len(m.rules) == 0 {
		return false
	}
	clean := path.Clean(filepath.ToSlash(relPath))
	if clean == "." || clean == "" || clean == "/" {
		return false
	}
	clean = strings.TrimPrefix(clean, "/")

	// Walk the ancestor prefixes by index into clean rather than by
	// Split+Join: the prefixes are literally substrings of clean, so slicing
	// yields identical strings for zero allocations, where Join copied
	// O(depth^2) bytes per path.
	for i := strings.IndexByte(clean, '/'); i >= 0; {
		if m.matchRules(clean[:i], true) {
			return true
		}
		next := strings.IndexByte(clean[i+1:], '/')
		if next < 0 {
			break
		}
		i += next + 1
	}
	return m.matchRules(clean, isDir)
}

// MatchExact reports whether relPath itself matches, WITHOUT the ancestor
// propagation Match performs -- an excluded parent directory does not
// exclude relPath here.
//
// This exists for callers that already prune excluded directories during a
// walk (returning fs.SkipDir when Match reports a directory excluded), for
// which Match's ancestor loop re-derives a verdict the walk has already
// acted on. Such a caller can use MatchExact and pay one rule pass per entry
// instead of depth-plus-one passes. A caller that does NOT prune -- one that
// keeps descending into an excluded directory -- must keep using Match, or
// it will collect files under a directory it was told to exclude.
//
// relPath is cleaned exactly as Match cleans it.
func (m *Matcher) MatchExact(relPath string, isDir bool) bool {
	if m == nil || len(m.rules) == 0 {
		return false
	}
	clean := path.Clean(filepath.ToSlash(relPath))
	if clean == "." || clean == "" || clean == "/" {
		return false
	}
	return m.matchRules(strings.TrimPrefix(clean, "/"), isDir)
}

// matchRules evaluates every rule against exactly one candidate path (no
// ancestor propagation), applying gitignore's last-matching-rule-wins
// precedence. Rules are visited in compiled order -- the file's own order --
// which is what makes a later negation win; the loop indexes m.rules rather
// than ranging by value only to avoid copying each rule struct per candidate.
func (m *Matcher) matchRules(candidate string, isDir bool) bool {
	ignored := false
	for i := range m.rules {
		r := &m.rules[i]
		if r.dirOnly && !isDir {
			continue
		}
		if r.matchGlob(candidate) {
			ignored = !r.negate
		}
	}
	return ignored
}
