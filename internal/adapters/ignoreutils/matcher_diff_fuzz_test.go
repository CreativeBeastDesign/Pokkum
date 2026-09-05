package ignoreutils

import (
	"strings"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
)

// FuzzRuleDifferential is the target aimed squarely at the fast-path
// classification in compile: it fuzzes BOTH the pattern line and the
// candidate path, and asserts that the optimised matchGlob returns exactly
// what doublestar returns for the same compiled glob.
//
// This is the one that can find a misclassification, because it is the only
// target free to invent pattern shapes. TestMatchDifferential is bounded by
// two hand-written pattern sets, so a shape nobody thought of — an escape, a
// stray ']', a pattern that is not valid UTF-8, a '?' where a literal was
// assumed — is only reachable from here.
func FuzzRuleDifferential(f *testing.F) {
	patterns := []string{
		"", "#c", "node_modules/.cache", ".git/", "*.map", ".env*",
		"storybook-static/", "!important.env", "/build/", "**/*.tmp",
		"src/**/generated", "[abc]*.ts", "{one,two}/c.ts", "?.ts", "*mixed*",
		"*", "**", "***", "*a*b*", "a**", "**a", "\\*literal", "a\\b",
		"]", "}", "a]b", "a}b", "\xff\xfe", "�", "a�b",
		"trailing ", " leading", "üñï", "空", "a b",
	}
	candidates := []string{
		"", "a", "a.ts", "空.ts", "a/b/c", ".env", ".env.local", "x.map",
		".git", ".git/HEAD", "node_modules/.cache", "storybook-static/x.js",
		"/leading", "trailing/", "a//b", "\xff", "\xff\xfe", "�",
		"a�b", "*", "]", "}", "üñï/空", strings.Repeat("a/", 50) + "z",
	}
	for _, p := range patterns {
		for _, c := range candidates {
			f.Add(p, c)
		}
	}

	f.Fuzz(func(t *testing.T, line, candidate string) {
		got, gotOK, gotErr := compile(line)
		want, wantOK, wantErr := oracleCompile(line)

		if (gotErr != nil) != (wantErr != nil) || gotOK != wantOK {
			t.Fatalf("compile(%q) = (ok %v, err %v); oracle = (ok %v, err %v)",
				line, gotOK, gotErr, wantOK, wantErr)
		}
		if gotErr != nil || !gotOK {
			return
		}
		if got.glob != want.glob || got.negate != want.negate ||
			got.dirOnly != want.dirOnly || got.anchored != want.anchored {
			t.Fatalf("compile(%q) parsed to %+v; oracle %+v", line, got, want)
		}
		if g, w := got.matchGlob(candidate), want.matchGlob(candidate); g != w {
			t.Fatalf("pattern %q (glob %q, kind %s, lit %q) vs %q: got %v, doublestar %v",
				line, got.glob, kindNames[got.kind], got.lit, candidate, g, w)
		}
	})
}

// FuzzMatchDifferential fuzzes the whole Matcher — cleaning, the ancestor
// walk and last-match-wins precedence together — against the pre-optimisation
// implementation, over the pattern set a real build evaluates. Where
// FuzzRuleDifferential can vary the rules but sees one candidate at a time,
// this one fixes the rules and varies the path, which is what exercises the
// index-sliced ancestor walk against Split/Join on separator shapes no
// hand-written corpus enumerates.
func FuzzMatchDifferential(f *testing.F) {
	patterns := append(DefaultPatterns(),
		"!important.env", "/build/", "**/*.tmp", "src/**/generated",
		"!src/keep/generated", "[abc]*.ts", "?.ts", "*mixed*", "coverage/",
	)
	got, err := New(patterns)
	if err != nil {
		f.Fatalf("New: %v", err)
	}
	want, err := newOracle(patterns)
	if err != nil {
		f.Fatalf("newOracle: %v", err)
	}

	for _, s := range differentialCorpus() {
		f.Add(s, false)
		f.Add(s, true)
	}

	f.Fuzz(func(t *testing.T, relPath string, isDir bool) {
		if g, w := got.Match(relPath, isDir), want.Match(relPath, isDir); g != w {
			t.Fatalf("Match(%q, %v) = %v, oracle = %v", relPath, isDir, g, w)
		}
	})
}

// BenchmarkResidualGlobValidation measures the ONLY third-party performance
// claim this optimisation rests on: that doublestar.MatchUnvalidated is no
// slower than doublestar.Match for the residual globby patterns matchGlob
// still hands to the library. doublestar's own doc hedges the saving ("there's
// really only one case where this performance improvement is realized"), and
// checklist row 61(b) says a library's stated performance property is an
// experiment, not a fact — so it is measured here rather than assumed.
//
// The correctness half is not a measurement at all: v4.10.0's validate flag
// only decides whether a non-match returns ErrBadPattern or a plain false, so
// the matched bool is identical for every input. FuzzRuleDifferential asserts
// that empirically against doublestar.Match.
func BenchmarkResidualGlobValidation(b *testing.B) {
	globs := []string{"**/*.stories.svelte", "src/**/generated", "**/[abc]*.ts", "**/?.ts", "{one,two}/c.ts"}
	paths := []string{
		"src/lib/components/Button.stories.svelte", "src/routes/+page.svelte",
		"src/lib/generated", "src/a.ts", "one/c.ts",
		"build/client/_app/immutable/chunks/entry.12.js",
	}
	b.Run("Match", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			benchMatchSink, _ = doublestar.Match(globs[i%len(globs)], paths[i%len(paths)])
		}
	})
	b.Run("MatchUnvalidated", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			benchMatchSink = doublestar.MatchUnvalidated(globs[i%len(globs)], paths[i%len(paths)])
		}
	})
}
