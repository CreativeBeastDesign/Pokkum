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
package ignore

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

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
	}
}

// rule is one compiled pattern line.
type rule struct {
	glob     string // the doublestar pattern to match against a candidate path
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

	if !r.anchored {
		s = "**/" + s
	}

	if _, err := doublestar.Match(s, "probe"); err != nil {
		return rule{}, false, fmt.Errorf("invalid glob: %w", err)
	}

	r.glob = s
	return r, true, nil
}

// matchGlob reports whether candidate (a slash-separated path, relative to
// the matcher's root, with no leading slash) matches the rule's compiled
// glob.
func (r rule) matchGlob(candidate string) bool {
	ok, _ := doublestar.Match(r.glob, candidate)
	return ok
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

	parts := strings.Split(clean, "/")
	for i := 1; i < len(parts); i++ {
		if m.matchExact(strings.Join(parts[:i], "/"), true) {
			return true
		}
	}
	return m.matchExact(clean, isDir)
}

// matchExact evaluates every rule against exactly one candidate path (no
// ancestor propagation), applying gitignore's last-matching-rule-wins
// precedence.
func (m *Matcher) matchExact(candidate string, isDir bool) bool {
	ignored := false
	for _, r := range m.rules {
		if r.dirOnly && !isDir {
			continue
		}
		if r.matchGlob(candidate) {
			ignored = !r.negate
		}
	}
	return ignored
}
