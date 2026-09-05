package pruneutils

import (
	"fmt"
	"math/rand"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
)

// This file is the differential guard for the IsJunk rewrite.
//
// IsJunk's verdict decides layer membership: a file it calls junk is omitted
// from the tar stream, so a single changed verdict changes the layer's bytes
// and therefore the image digest. The rewrite replaced 84 doublestar.Match
// calls per file with map lookups, suffix tests and a segment scan, and the
// only honest way to land that is to keep the original implementation here as
// an oracle and prove the two agree over a corpus large enough to exercise
// every pattern's positive case, its near-misses, and the awkward inputs
// (case variants, "..", unicode, spaces, embedded separators).

// legacyIsDocFile is the pre-rewrite isDocFile, verbatim.
func legacyIsDocFile(base string) bool {
	lower := strings.ToLower(base)
	for _, name := range docBaseNames {
		if !strings.HasPrefix(lower, name) {
			continue
		}
		suffix := lower[len(name):]
		for _, ext := range docExtensions {
			if suffix == ext {
				return true
			}
		}
	}
	return false
}

// legacyIsJunk is the pre-rewrite IsJunk, verbatim, against which the new
// implementation is diffed.
func legacyIsJunk(relPath string, _ bool, opts PruneOptions) bool {
	if opts.NoPrune {
		return false
	}

	normPath := filepath.ToSlash(relPath)
	normPath = strings.TrimPrefix(normPath, "/")
	normPath = strings.TrimPrefix(normPath, "./")

	base := filepath.Base(normPath)

	for _, keep := range opts.KeepPatterns {
		if matched, _ := doublestar.Match(keep, normPath); matched {
			return false
		}
		if matched, _ := doublestar.Match(keep, base); matched {
			return false
		}
	}

	if opts.KeepSourcemap && strings.HasSuffix(normPath, ".map") && !strings.HasSuffix(normPath, ".d.ts.map") {
		return false
	}

	if legacyIsDocFile(base) {
		return true
	}

	for _, pattern := range DefaultJunkPatterns {
		if !opts.KeepSourcemap && pattern == "**/*.map" && strings.HasSuffix(normPath, ".map") {
			return true
		}
		if matched, _ := doublestar.Match(pattern, normPath); matched {
			return true
		}
		if matched, _ := doublestar.Match(pattern, base); matched {
			return true
		}
	}

	return false
}

// differentialCorpus builds the path corpus both implementations are run over.
//
// It is generated rather than hand-listed so that every entry of
// DefaultJunkPatterns contributes its own positive case and its own
// near-misses automatically: a pattern added later is covered without anyone
// remembering to extend a literal table.
func differentialCorpus() []string {
	dirs := []string{
		"",
		"pkg/",
		"@scope/pkg/",
		"pkg/dist/",
		"pkg/dist/esm/internal/",
		".hidden/",
		"a/./b/",
		"a/../b/",
		"dir with spaces/",
		"ünïcødé/",
		"pkg/test/",
		"pkg/tests/",
		"pkg/__tests__/",
		"pkg/.github/workflows/",
		"pkg/.git/objects/",
		"pkg/.yarn/cache/",
		"pkg/.yarn/",
		"pkg/yarn/cache/",
		"pkg/.vscode/",
		"pkg/.idea/",
		"pkg/.circleci/",
	}

	bases := []string{
		// Ordinary runtime files that must survive.
		"index.js", "index.mjs", "index.cjs", "package.json", "chunk-a1b2.js",
		"binding-linux-x64.node", "style.css", "logo.svg", "data.json",
		"readme.js", "license-checker.js", "licences.js", "notice.mjs",
		"test.js", "atest", "testing", "tests.js", "contest",
		"tsconfig", "tsconfig.jsonc", "Dockerfil", "docker-compose",
		"makefile", "MAKEFILE", "eslintrc", "prettierrc.js",
		"file.d.tsx", "file.dts", "map", ".mapp", "notamap",
		// Exercise every literal / wildcard pattern's positive case.
		"a.d.ts", "a.d.ts.map", "a.d.mts", "a.d.mts.map", "a.d.cts", "a.d.cts.map",
		".d.ts", ".map", "a.map", "a.log", ".log",
		"tsconfig.json", "tsconfig.build.json", "tsconfig..json", "tsconfig.a.b.json",
		".npmignore", ".eslintrc", ".eslintrc.json", ".eslintrcX",
		".prettierrc", ".prettierrc.yaml", ".editorconfig", "CODEOWNERS", "codeowners",
		".DS_Store", ".gitignore", ".gitattributes", ".npmrc",
		"yarn.lock", "package-lock.json", "pnpm-lock.yaml",
		"a.test.js", "a.spec.js", "a.test.mjs", "a.spec.mjs", "a.test.ts", "a.spec.ts",
		"a.test.d.ts", ".travis.yml", "Makefile", "Dockerfile", "Dockerfile.dev",
		"docker-compose.yml", "docker-compose.prod.yml", "docker-composeX.yml",
		"docker-compose.yaml",
		// Documentation basenames and their near-misses.
		"README", "README.md", "readme.txt", "ReadMe.MARKDOWN", "readme.1st",
		"CHANGELOG.md", "CHANGES", "HISTORY.rst", "LICENSE", "LICENCE.txt",
		"AUTHORS", "CONTRIBUTORS.md", "NOTICE", "notice.org", "readmes",
		"license.js", "changelogger.md", "authors-list.js",
		// Directory-shaped basenames, since "**/test/**" matches a plain file
		// literally named "test".
		"test", "tests", "__tests__", ".github", ".git", ".vscode", ".idea", ".circleci",
		// Awkward shapes.
		"weird\\name.log", "a b.d.ts", "..", ".", "a.MAP", "A.D.TS",
	}

	seen := make(map[string]struct{})
	corpus := make([]string, 0, len(dirs)*len(bases)+64)
	add := func(p string) {
		if _, ok := seen[p]; ok {
			return
		}
		seen[p] = struct{}{}
		corpus = append(corpus, p)
	}
	for _, d := range dirs {
		for _, b := range bases {
			add(d + b)
		}
	}

	// Absolute and "./"-prefixed forms, which IsJunk trims before matching.
	for _, b := range bases {
		add("/" + b)
		add("./" + b)
		add("/pkg/test/" + b)
	}

	// A pseudo-random pile of deep paths built from the same segment
	// vocabulary, to catch reductions that hold at depth 1-3 and break deeper.
	segs := []string{"pkg", "dist", "test", "tests", "__tests__", ".git", ".github",
		".yarn", "cache", "src", "lib", "esm", "cjs", ".vscode", "node_modules", "a", ""}
	rng := rand.New(rand.NewSource(0xC0FFEE))
	for i := 0; i < 4000; i++ {
		depth := 1 + rng.Intn(5)
		parts := make([]string, 0, depth+1)
		for j := 0; j < depth; j++ {
			parts = append(parts, segs[rng.Intn(len(segs))])
		}
		parts = append(parts, bases[rng.Intn(len(bases))])
		add(strings.Join(parts, "/"))
	}

	return corpus
}

// differentialOptions are the PruneOptions permutations the corpus is run
// under. KeepPatterns matter because the rewrite classifies literal keeps
// separately from globby ones.
func differentialOptions() []struct {
	name string
	opts PruneOptions
} {
	return []struct {
		name string
		opts PruneOptions
	}{
		{"defaults", PruneOptions{}},
		{"keep_sourcemap", PruneOptions{KeepSourcemap: true}},
		{"no_prune", PruneOptions{NoPrune: true}},
		{"keep_glob", PruneOptions{KeepPatterns: []string{"**/*.node", "**/dist/**/*.mjs", "**/LICENSE"}}},
		{"keep_literal", PruneOptions{KeepPatterns: []string{"README.md", "pkg/test/index.js", "yarn.lock"}}},
		{"keep_mixed", PruneOptions{KeepPatterns: []string{"tsconfig.json", "**/*.d.ts", "pkg/*/a.log"}}},
		{"keep_sourcemap_and_keep", PruneOptions{KeepSourcemap: true, KeepPatterns: []string{"**/*.d.ts.map"}}},
	}
}

func TestIsJunkMatchesLegacyOracle(t *testing.T) {
	corpus := differentialCorpus()
	if len(corpus) < 2000 {
		t.Fatalf("degenerate corpus: only %d paths", len(corpus))
	}

	for _, tc := range differentialOptions() {
		t.Run(tc.name, func(t *testing.T) {
			m := NewMatcher(tc.opts)
			var junk, kept, mismatches int
			for _, p := range corpus {
				want := legacyIsJunk(p, false, tc.opts)
				got := m.IsJunk(p, false)
				if got != want {
					mismatches++
					if mismatches <= 20 {
						t.Errorf("verdict differs for %q: legacy=%v new=%v", p, want, got)
					}
				}
				// Package-level IsJunk must agree with the Matcher it wraps.
				if fn := IsJunk(p, false, tc.opts); fn != got {
					t.Errorf("IsJunk(%q) = %v but Matcher.IsJunk = %v", p, fn, got)
				}
				if want {
					junk++
				} else {
					kept++
				}
			}
			if mismatches > 20 {
				t.Errorf("... and %d further mismatches", mismatches-20)
			}
			// Fixture floor: a corpus that produced a single verdict would
			// compare one branch of each implementation and pass regardless.
			if !tc.opts.NoPrune && (junk == 0 || kept == 0) {
				t.Fatalf("degenerate corpus for %s: %d junk / %d kept of %d", tc.name, junk, kept, len(corpus))
			}
			t.Logf("%s: %d paths, %d junk, %d kept", tc.name, len(corpus), junk, kept)
		})
	}
}

func TestIsDocFileMatchesLegacyOracle(t *testing.T) {
	var checked int
	for _, name := range append([]string{}, docBaseNames...) {
		for _, ext := range docExtensions {
			for _, variant := range []string{name + ext, strings.ToUpper(name + ext), strings.ToUpper(name) + ext} {
				if got, want := isDocFile(variant), legacyIsDocFile(variant); got != want {
					t.Errorf("isDocFile(%q) = %v, legacy = %v", variant, got, want)
				}
				checked++
			}
		}
	}
	for _, near := range []string{"readme.js", "license-checker.js", "readmes", "notice.d.ts",
		"changelogger.md", "", "read", "licence", "LICENCE", "history.org", "authorsx"} {
		if got, want := isDocFile(near), legacyIsDocFile(near); got != want {
			t.Errorf("isDocFile(%q) = %v, legacy = %v", near, got, want)
		}
		checked++
	}
	if checked < 100 {
		t.Fatalf("degenerate coverage: only %d names checked", checked)
	}
}

// TestCompileJunkRulesClassifiesEveryDefaultPattern asserts that the
// classification actually reduced the default blocklist rather than silently
// dumping patterns into the doublestar residual, which would still be correct
// but would give up the entire performance win without failing anything.
func TestCompileJunkRulesClassifiesEveryDefaultPattern(t *testing.T) {
	if n := len(defaultJunkRules.residual); n != 0 {
		t.Errorf("%d default patterns fell through to the doublestar residual: %v", n, defaultJunkRules.residual)
	}
	total := len(defaultJunkRules.exactBases) + len(defaultJunkRules.suffixes) +
		len(defaultJunkRules.prefixSuffix) + len(defaultJunkRules.dirSegments) +
		len(defaultJunkRules.dirSequences) + len(defaultJunkRules.residual)
	if total != len(DefaultJunkPatterns) {
		t.Errorf("compiled %d rules from %d patterns", total, len(DefaultJunkPatterns))
	}
	t.Logf("exact=%d suffix=%d prefixSuffix=%d dirSeg=%d dirSeq=%d residual=%d",
		len(defaultJunkRules.exactBases), len(defaultJunkRules.suffixes),
		len(defaultJunkRules.prefixSuffix), len(defaultJunkRules.dirSegments),
		len(defaultJunkRules.dirSequences), len(defaultJunkRules.residual))
}

// TestCompileJunkRulesResidualFallback proves the residual arm is live: a
// pattern shape the classifier cannot reduce must still be matched with
// doublestar rather than dropped.
func TestCompileJunkRulesResidualFallback(t *testing.T) {
	r := compileJunkRules([]string{"**/{a,b}/*.junk", "vendor/**/*.tmp"})
	if len(r.residual) != 2 {
		t.Fatalf("expected 2 residual patterns, got %v", r.residual)
	}
	for _, tc := range []struct {
		path string
		want bool
	}{
		{"x/a/f.junk", true},
		{"x/b/f.junk", true},
		{"x/c/f.junk", false},
		{"vendor/deep/f.tmp", true},
		{"other/deep/f.tmp", false},
	} {
		if got := r.matches(tc.path, filepath.Base(tc.path)); got != tc.want {
			t.Errorf("residual match %q = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// benchOracleSink keeps the oracle honest about being callable.
var benchOracleSink bool

func TestLegacyOracleIsNotTriviallyConstant(t *testing.T) {
	// The oracle is only meaningful if it disagrees with itself across inputs;
	// a constant oracle would make TestIsJunkMatchesLegacyOracle vacuous.
	benchOracleSink = legacyIsJunk("pkg/README.md", false, PruneOptions{})
	if !benchOracleSink {
		t.Fatal("oracle says pkg/README.md is not junk")
	}
	if legacyIsJunk("pkg/index.js", false, PruneOptions{}) {
		t.Fatal("oracle says pkg/index.js is junk")
	}
	_ = fmt.Sprint(benchOracleSink)
}
