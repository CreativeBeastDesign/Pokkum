package ignoreutils

import (
	"strings"
	"testing"
)

// FuzzCompile exercises compile against arbitrary .pokkumignore lines. A
// .pokkumignore file is project-authored, but this package's own doc
// comment (matcher.go) states the package is a general-purpose,
// dependency-light gitignore matcher with SvelteKit (W2) as a stated future
// consumer beyond its current one (internal/adapters/sbom) — nothing here
// assumes the line came from a fully-trusted source, and a malformed
// pattern must be reported as an ordinary error, never panic or hang the
// build. Also confirms matchGlob on whatever compiles is safe to call.
func FuzzCompile(f *testing.F) {
	f.Add("")
	f.Add("#a comment")
	f.Add("   ")
	f.Add("node_modules/.cache")
	f.Add(".git/")
	f.Add("*.map")
	f.Add(".env*")
	f.Add("!important.env")
	f.Add("/anchored/at/root")
	f.Add("not/anchored/**/deep")
	f.Add("**/**/**/**/**")
	f.Add(strings.Repeat("*", 5000))
	f.Add(strings.Repeat("**/", 500) + "x")
	f.Add("\\#escaped-comment")
	f.Add("\\!escaped-negate")
	f.Add("!")
	f.Add("/")
	f.Add("//")
	f.Add("a/b/c/")
	f.Add("[")
	f.Add("[a-")
	f.Add("[!]")
	f.Add("a\x00b")
	f.Add("\xff\xfe")
	f.Add("a" + strings.Repeat("/", 1000) + "b")

	f.Fuzz(func(t *testing.T, line string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("compile(%q) panicked: %v", line, r)
			}
		}()
		r, ok, err := compile(line)
		if err != nil {
			return // malformed glob: an ordinary, expected rejection
		}
		if !ok {
			return // blank/comment line: nothing further to check
		}
		// Whatever compiled must be safe to match against arbitrary
		// candidates without panicking, including pathological candidates
		// shaped to stress doublestar's matching.
		for _, candidate := range []string{
			"", "a", "a/b/c", strings.Repeat("a/", 200) + "z",
			strings.Repeat("*", 100), line,
		} {
			_ = r.matchGlob(candidate)
		}
	})
}

// FuzzMatcherMatch exercises the full Load-time pattern set (DefaultPatterns
// plus a handful of representative project rules, including negations)
// against arbitrary candidate paths, fuzzing both the path string and the
// isDir flag. Matcher.Match's contract is a TOTAL function — every input
// must yield a decision (true or false) without panicking or hanging, which
// is the property this target checks empirically rather than by inspection.
func FuzzMatcherMatch(f *testing.F) {
	patterns := append(DefaultPatterns(),
		"!important.env",
		"/build/",
		"**/*.tmp",
		"src/**/generated",
		"!src/keep/generated",
	)
	m, err := New(patterns)
	if err != nil {
		f.Fatalf("New(%v): %v", patterns, err)
	}

	seeds := []string{
		"",
		".",
		"/",
		"a",
		"a/b/c",
		".env",
		".env.local",
		"important.env",
		"node_modules/.cache",
		"node_modules/.cache/deep/file",
		".git",
		".git/HEAD",
		"src/generated",
		"src/keep/generated",
		"build",
		"build/output.js",
		"../escape",
		"a/../b",
		"a//b",
		"\\windows\\style\\path",
		strings.Repeat("a/", 300) + "leaf",
		"a\x00b",
		"\xff\xfe",
		strings.Repeat("*", 200),
	}
	for _, s := range seeds {
		f.Add(s, false)
		f.Add(s, true)
	}

	f.Fuzz(func(t *testing.T, relPath string, isDir bool) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Matcher.Match(%q, %v) panicked: %v", relPath, isDir, r)
			}
		}()
		// The only property checked is totality (no panic, terminates) — the
		// boolean result itself has no independent oracle to check against
		// without reimplementing gitignore semantics, which real unit tests
		// in matcher_test.go already cover for known, hand-picked cases.
		_ = m.Match(relPath, isDir)
	})
}
