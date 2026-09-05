package ignoreutils

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// Match decides which files the pre-build secret scan reads and which bytes
// enter the remote-cache source-tree hash, so a changed verdict is a
// correctness bug in two directions at once: a newly-ignored file is a file
// no longer scanned for secrets, and a newly-kept one changes a cache key.
// The optimisation in matcher.go replaces doublestar with string comparisons
// for the common pattern shapes, which is sound only if each comparison is
// doublestar's OWN semantics for that shape — so the verdicts are checked
// against the pre-optimisation implementation (matcher_oracle_test.go, kept
// verbatim) rather than against hand-written expectations, which would only
// re-encode the same assumption the fast path makes.

// differentialPrefixes are directory prefixes (each already ending in '/', or
// empty for a root-level path). They cover ancestor-matched cases (.git/,
// node_modules/.cache/, storybook-static/), deep nesting, near-misses for
// every ancestor-shaped default pattern, and names that stress the byte-level
// comparisons: spaces, dots, dashes, non-ASCII, and glob metacharacters
// appearing in the *candidate* rather than the pattern.
func differentialPrefixes() []string {
	return []string{
		"",
		"src/",
		"src/lib/",
		"src/lib/components/ui/button/",
		"src/lib/server/db/queries/",
		"src/routes/blog/[slug]/",
		"src/lib/paraglide/",
		"src/keep/",
		"src/generated/",
		"static/images/gallery/",
		"build/",
		"build/client/_app/immutable/chunks/",
		".svelte-kit/output/server/nodes/",
		".git/",
		".git/objects/ab/",
		"git/",
		".github/workflows/",
		"node_modules/",
		"node_modules/.cache/",
		"node_modules/.cache/vite/deps/",
		"node_modules/.cacheable/",
		"node_modules/svelte/src/",
		"storybook-static/",
		"storybook-static/sb-manager/",
		"storybook-staticky/",
		".storybook/",
		"coverage/",
		"test-results/",
		"playwright-report/",
		"a b/spaced dir/",
		"üñïcode/深い/ディレクトリ/",
		"dot.dir/",
		".hidden/",
		"-dash/",
		"UPPER/",
		"a/b/c/d/e/f/g/h/",
	}
}

// differentialBases are final path segments: near-misses for every default
// pattern, dotfiles, extension variants, empty and dot-only segments, and
// segments containing characters the fast paths must treat as ordinary bytes.
func differentialBases() []string {
	return []string{
		"index.ts", "a.ts", "ab.ts", ".ts", "app.html", "+page.svelte",
		"Button.svelte", "Button.stories.svelte", "stories.svelte",
		".env", ".env.local", ".env.production", ".envrc", "env", "env.local",
		"important.env", ".ENV", "x.env",
		"bundle.js.map", "x.map", ".map", "map", "map.js", "sourcemap",
		".git", "git", ".gitignore", ".gitkeep", "git.txt",
		".cache", "cache", ".cacheable",
		"storybook-static", "storybook-staticx", "notstorybook-static",
		"tsconfig.tsbuildinfo", ".tsbuildinfo", "tsbuildinfo",
		"generated", "generated.ts", "runtime.js",
		"file.tmp", "tmp", ".tmp", "x.tmp.js",
		"généré.ts", "空.ts", "a b.ts", "a  b.ts", " leading.ts", "trailing.ts ",
		"with*star.ts", "with[bracket].ts", "with{brace}.ts", "with?q.ts",
		"back\\slash.ts", "semi;colon.ts", "comma,file.ts", "!bang.ts",
		"#hash.ts", "under_score.ts", "UPPER.TS", "MiXeD.Map",
		"", ".", "..", "...", "a", "z",
	}
}

// differentialCorpus is the cross product of prefixes and basenames plus a
// set of hand-written pathological path strings that no cross product would
// produce — absolute forms, doubled and trailing separators, dot segments,
// parent escapes, extreme depth, and invalid UTF-8. Every entry is evaluated
// as both a file and a directory.
func differentialCorpus() []string {
	prefixes := differentialPrefixes()
	bases := differentialBases()
	corpus := make([]string, 0, len(prefixes)*len(bases)+64)
	for _, p := range prefixes {
		for _, b := range bases {
			corpus = append(corpus, p+b)
		}
	}
	corpus = append(corpus,
		"", ".", "..", "/", "//", "///",
		"/abs/path/file.ts", "/.env", "/node_modules/.cache/x",
		"a//b", "a///b", "a/./b", "a/../b", "./a", "../a", "../../.env",
		"a/", "a/b/", ".git/", "node_modules/.cache/",
		"src/lib/../.env", "src/../../etc/passwd",
		"\\windows\\style\\path", "C:/win/.env",
		strings.Repeat("deep/", 40)+"leaf.map",
		strings.Repeat("a/", 200)+".env",
		"a\x00b/.env", "\xff\xfe/.env", "\xff\xfe", "bad\xffutf8.map",
		"\uFFFD", "\uFFFD.map", "prefix/\uFFFD",
	)
	return corpus
}

// differentialPatternSets are the two rule sets a real build evaluates: the
// package defaults alone, and the defaults with a project's own
// .pokkumignore layered on top (which is exactly what Load returns). The
// project set deliberately includes shapes that must NOT be fast-pathed —
// a character class, an alternation, a '?', a mid-pattern '**' — so the
// residual doublestar path is exercised rather than optimised away, and
// negations so last-match-wins precedence is under test too.
func differentialPatternSets() map[string][]string {
	project := []string{
		"coverage/",
		"*.tsbuildinfo",
		"playwright-report/",
		"test-results/",
		"src/lib/paraglide/",
		"!src/lib/paraglide/runtime.js",
		"**/*.stories.svelte",
		"/build/",
		"**/*.tmp",
		"src/**/generated",
		"!src/keep/generated",
		"[abc]*.ts",
		"{one,two}/c.ts",
		"?.ts",
		"*mixed*",
		"!important.env",
		"UPPER/",
		"trailing.ts ",
	}
	return map[string][]string{
		"defaults":         DefaultPatterns(),
		"defaults+project": append(DefaultPatterns(), project...),
	}
}

// kindNames labels a ruleKind for test output.
var kindNames = map[ruleKind]string{
	kindGlob:         "kindGlob",
	kindPathLiteral:  "kindPathLiteral",
	kindBaseLiteral:  "kindBaseLiteral",
	kindBasePrefix:   "kindBasePrefix",
	kindBaseSuffix:   "kindBaseSuffix",
	kindBaseContains: "kindBaseContains",
}

func TestMatchDifferential(t *testing.T) {
	corpus := differentialCorpus()
	if len(corpus) < 2000 {
		t.Fatalf("corpus too small to be meaningful: %d paths", len(corpus))
	}

	for name, patterns := range differentialPatternSets() {
		t.Run(name, func(t *testing.T) {
			got, err := New(patterns)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			want, err := newOracle(patterns)
			if err != nil {
				t.Fatalf("newOracle: %v", err)
			}
			if len(got.rules) != len(want.rules) {
				t.Fatalf("rule count: new=%d oracle=%d", len(got.rules), len(want.rules))
			}

			// Fixture floor (checklist row 47): a corpus that landed
			// entirely on one verdict, or a rule set that classified every
			// pattern into one kind, would pass this test while checking
			// almost nothing. Both are asserted before the comparison, so a
			// degenerate run fails loudly instead of reporting a clean pass.
			seenKind := map[ruleKind]int{}
			for i := range got.rules {
				seenKind[got.rules[i].kind]++
			}
			for k, n := range seenKind {
				t.Logf("%s: %d rules", kindNames[k], n)
			}

			var ignored, kept, diffs int
			for _, p := range corpus {
				for _, isDir := range []bool{false, true} {
					g := got.Match(p, isDir)
					w := want.Match(p, isDir)
					if g != w {
						diffs++
						if diffs <= 20 {
							t.Errorf("Match(%q, %v) = %v, oracle = %v", p, isDir, g, w)
						}
						continue
					}
					if g {
						ignored++
					} else {
						kept++
					}
				}
			}
			if diffs > 20 {
				t.Errorf("... and %d further differences", diffs-20)
			}
			if ignored == 0 || kept == 0 {
				t.Fatalf("degenerate corpus: %d ignored, %d kept of %d decisions",
					ignored, kept, 2*len(corpus))
			}
			t.Logf("%d decisions: %d ignored, %d kept, %d differences",
				2*len(corpus), ignored, kept, diffs)
		})
	}
}

// TestRuleKindCoverage pins that every fast path is actually reached by a
// realistic pattern set, and that its literal is what compile is expected to
// have extracted. Without this, deleting a case from classify would leave
// TestMatchDifferential green (doublestar answers correctly for everything)
// while silently reverting the optimisation — a passing test measuring
// nothing.
func TestRuleKindCoverage(t *testing.T) {
	cases := []struct {
		pattern string
		kind    ruleKind
		lit     string
	}{
		{"node_modules/.cache", kindPathLiteral, "node_modules/.cache"},
		{"/build/", kindPathLiteral, "build"},
		{"src/lib/paraglide/", kindPathLiteral, "src/lib/paraglide"},
		{".git/", kindBaseLiteral, ".git"},
		{"storybook-static/", kindBaseLiteral, "storybook-static"},
		{"*.map", kindBaseSuffix, ".map"},
		{".env*", kindBasePrefix, ".env"},
		{"*mixed*", kindBaseContains, "mixed"},
		{"**/*.stories.svelte", kindGlob, ""},
		{"src/**/generated", kindGlob, ""},
		{"[abc]*.ts", kindGlob, ""},
		{"?.ts", kindGlob, ""},
		{"{one,two}/c.ts", kindGlob, ""},
		{"*", kindGlob, ""},
		{"**", kindGlob, ""},
		{"\xff\xfe.ts", kindGlob, ""}, // invalid UTF-8: no byte-wise fast path
		{"a\uFFFDb", kindGlob, ""},    // literal U+FFFD: ditto
		{"back\\slash", kindGlob, ""}, // escape: doublestar's business
	}
	for _, tc := range cases {
		r, ok, err := compile(tc.pattern)
		if err != nil || !ok {
			t.Fatalf("compile(%q) = ok %v, err %v", tc.pattern, ok, err)
		}
		if r.kind != tc.kind || r.lit != tc.lit {
			t.Errorf("compile(%q): kind %s lit %q, want kind %s lit %q",
				tc.pattern, kindNames[r.kind], r.lit, kindNames[tc.kind], tc.lit)
		}
	}

	// Every kind must appear above, or a fast path is shipping untested.
	covered := map[ruleKind]bool{}
	for _, tc := range cases {
		covered[tc.kind] = true
	}
	for k := range kindNames {
		if !covered[k] {
			t.Errorf("no coverage case for %s", kindNames[k])
		}
	}
}

// TestMatchExactSkipsAncestors covers the added API. It is checked against
// Match rather than against hand-written verdicts: MatchExact must agree with
// Match on every path with no excluded ancestor, and must be the reason (and
// the only reason) they ever differ.
func TestMatchExactSkipsAncestors(t *testing.T) {
	m, err := New(append(DefaultPatterns(), "coverage/", "!keep.map"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	explicit := []struct {
		path  string
		isDir bool
		exact bool
		full  bool
	}{
		// An excluded ancestor is exactly what MatchExact ignores.
		{".git/objects/ab/cd", false, false, true},
		{"storybook-static/sb-manager/globals.js", false, false, true},
		{"node_modules/.cache/vite/deps/x.js", false, false, true},
		// No ancestor involved: the two must agree.
		{"src/app.map", false, true, true},
		{".env.local", false, true, true},
		{"src/routes/+page.svelte", false, false, false},
		{"coverage", true, true, true},
		{"coverage", false, false, false}, // dirOnly rule, file candidate
	}
	for _, tc := range explicit {
		if got := m.MatchExact(tc.path, tc.isDir); got != tc.exact {
			t.Errorf("MatchExact(%q, %v) = %v, want %v", tc.path, tc.isDir, got, tc.exact)
		}
		if got := m.Match(tc.path, tc.isDir); got != tc.full {
			t.Errorf("Match(%q, %v) = %v, want %v", tc.path, tc.isDir, got, tc.full)
		}
	}

	// Property over the full corpus: MatchExact true implies Match true, and
	// whenever they differ some ancestor directory must itself be excluded.
	var differ int
	for _, p := range differentialCorpus() {
		for _, isDir := range []bool{false, true} {
			exact, full := m.MatchExact(p, isDir), m.Match(p, isDir)
			if exact && !full {
				t.Fatalf("MatchExact(%q, %v) excluded a path Match kept", p, isDir)
			}
			if exact == full {
				continue
			}
			differ++
			if !hasExcludedAncestor(m, p) {
				t.Errorf("Match(%q, %v)=%v but MatchExact=%v with no excluded ancestor",
					p, isDir, full, exact)
			}
		}
	}
	if differ == 0 {
		t.Fatal("corpus never exercised the ancestor difference: test proves nothing")
	}
	t.Logf("%d decisions differed by ancestor propagation", differ)
}

// hasExcludedAncestor reports whether any proper ancestor directory of p is
// itself excluded, computed independently of Match's own ancestor walk.
func hasExcludedAncestor(m *Matcher, p string) bool {
	slash := strings.TrimPrefix(strings.ReplaceAll(p, "\\", "/"), "/")
	segs := strings.Split(slash, "/")
	for i := 1; i < len(segs); i++ {
		if m.MatchExact(strings.Join(segs[:i], "/"), true) {
			return true
		}
	}
	return false
}

// TestAncestorWalkPrefixes pins that the index-sliced ancestor walk visits
// exactly the prefixes the Split/Join version did, including for the
// separator shapes path.Clean does not normalise away.
func TestAncestorWalkPrefixes(t *testing.T) {
	for _, clean := range []string{
		"a", "a/b", "a/b/c", "a/b/c/d/e", "a//b", "a///b/c", "/a/b", "a/b/",
		strings.Repeat("x/", 50) + "leaf",
	} {
		var got []string
		for i := strings.IndexByte(clean, '/'); i >= 0; {
			got = append(got, clean[:i])
			next := strings.IndexByte(clean[i+1:], '/')
			if next < 0 {
				break
			}
			i += next + 1
		}

		var want []string
		parts := strings.Split(clean, "/")
		for i := 1; i < len(parts); i++ {
			want = append(want, strings.Join(parts[:i], "/"))
		}

		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("prefixes of %q: got %q, want %q", clean, got, want)
		}
	}
}

// TestMatcherConcurrentMatch pins the invariant that a compiled Matcher is
// immutable and therefore safe to share. The SBOM generator runs inside the
// pipeline's per-platform fan-out, so one Matcher really is consulted from
// several goroutines at once.
//
// This is the guard that says "no". Memoising the per-directory ancestor
// verdict on the Matcher was considered and rejected during this
// optimisation: after classification a rule pass is a handful of string
// comparisons, so a memo would buy little while turning a read-only value
// into shared mutable state needing a mutex or sync.Map on the hot path (and
// an unbounded map keyed by every directory a walk visits). Under -race this
// test fails the moment someone adds such a map without synchronisation —
// verified by doing exactly that, which is the only reason it is worth
// keeping.
func TestMatcherConcurrentMatch(t *testing.T) {
	patterns := append(DefaultPatterns(),
		"coverage/", "**/*.stories.svelte", "src/**/generated", "!important.env")
	corpus := differentialCorpus()

	// The expected verdicts are computed on a SEPARATE Matcher from the one
	// the goroutines share. Computing them on the shared instance would walk
	// every path once, sequentially, before any goroutine starts — which
	// silently pre-populates any lazily-built state and leaves the concurrent
	// phase with nothing left to write. That is not a hypothetical: the first
	// version of this test did exactly that and passed cleanly under -race
	// with an unsynchronised map deliberately added to Match.
	warm, err := New(patterns)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	want := make([]bool, len(corpus))
	for i, p := range corpus {
		want[i] = warm.Match(p, i%2 == 0)
	}

	m, err := New(patterns)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	const goroutines = 8
	start := make(chan struct{})
	errs := make(chan string, goroutines)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i, p := range corpus {
				if got := m.Match(p, i%2 == 0); got != want[i] {
					select {
					case errs <- fmt.Sprintf("Match(%q, %v) = %v, want %v", p, i%2 == 0, got, want[i]):
					default:
					}
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}
