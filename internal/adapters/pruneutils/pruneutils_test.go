package pruneutils_test

import (
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/pruneutils"
)

func TestIsJunk_Defaults(t *testing.T) {
	opts := pruneutils.PruneOptions{}

	junkCases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"index.d.ts", false, true},
		{"index.d.ts.map", false, true},
		{"index.d.mts", false, true},
		{"tsconfig.json", false, true},
		{"tsconfig.build.json", false, true},
		{"bundle.js.map", false, true},
		{"README.md", false, true},
		{"readme.markdown", false, true},
		{"CHANGELOG.md", false, true},
		{"LICENSE", false, true},
		{"LICENSE.txt", false, true},
		{".npmignore", false, true},
		{".eslintrc.json", false, true},
		{".prettierrc", false, true},
		{"__tests__/util.test.js", false, true},
		{"__tests__", true, true},
		{"test/suite.js", false, true},
		{"tests/spec.ts", false, true},
		{"component.test.js", false, true},
		{"component.spec.ts", false, true},
		{".github/workflows/ci.yml", false, true},
		{".github", true, true},
		{"Makefile", false, true},
		{"Dockerfile", false, true},

		// Valid runtime production files (must NOT be marked junk)
		{"index.js", false, false},
		{"package.json", false, false},
		{"server.mjs", false, false},
		{"runtime.cjs", false, false},
		{"styles.css", false, false},
		{"image.png", false, false},
		{"fonts/inter.woff2", false, false},
		{"lib/helper.js", false, false},
		{"bin/cli.js", false, false},
	}

	for _, tc := range junkCases {
		got := pruneutils.IsJunk(tc.path, tc.isDir, opts)
		if got != tc.want {
			t.Errorf("IsJunk(%q, isDir=%v) = %v; want %v", tc.path, tc.isDir, got, tc.want)
		}
	}
}

func TestIsJunk_KeepSourcemap(t *testing.T) {
	opts := pruneutils.PruneOptions{KeepSourcemap: true}

	if pruneutils.IsJunk("bundle.js.map", false, opts) {
		t.Errorf("expected bundle.js.map to be kept when KeepSourcemap is true")
	}
	// TypeScript source map should still be pruned
	if !pruneutils.IsJunk("index.d.ts.map", false, opts) {
		t.Errorf("expected index.d.ts.map to be pruned even when KeepSourcemap is true")
	}
}

func TestIsJunk_KeepPatterns(t *testing.T) {
	opts := pruneutils.PruneOptions{
		KeepPatterns: []string{"*.md", "docs/**"},
	}

	if pruneutils.IsJunk("README.md", false, opts) {
		t.Errorf("expected README.md to be kept when matched by KeepPatterns")
	}
	if pruneutils.IsJunk("docs/guide.html", false, opts) {
		t.Errorf("expected docs/guide.html to be kept when matched by KeepPatterns")
	}
	if !pruneutils.IsJunk("index.d.ts", false, opts) {
		t.Errorf("expected index.d.ts to still be pruned")
	}
}

func TestIsJunk_NoPrune(t *testing.T) {
	opts := pruneutils.PruneOptions{NoPrune: true}

	if pruneutils.IsJunk("index.d.ts", false, opts) {
		t.Errorf("expected index.d.ts to NOT be junk when NoPrune is true")
	}
	if pruneutils.IsJunk("bundle.js.map", false, opts) {
		t.Errorf("expected bundle.js.map to NOT be junk when NoPrune is true")
	}
}

// TestIsJunk_DocFilesNotFalsePositive pins the fix for a false-positive bug: the old
// "**/README*"-style wildcard prefixes matched any file whose name merely started with
// a doc word, so real runtime source files like readme.js or license-checker.js were
// silently deleted from the vendor layer (surfacing as a require()/import
// module-not-found crash at container startup). Genuine doc files must still be pruned.
func TestIsJunk_DocFilesNotFalsePositive(t *testing.T) {
	opts := pruneutils.PruneOptions{}

	notJunk := []string{
		"src/readme.js",
		"readme.js",
		"lib/license-checker.js",
		"license-checker.js",
		"authors.ts",
		"contributors-list.mjs",
		"changelogger.js",
		"historybook.js",
		"noticeboard.js",
	}
	for _, p := range notJunk {
		if pruneutils.IsJunk(p, false, opts) {
			t.Errorf("IsJunk(%q) = true; want false (runtime source file, not a doc file)", p)
		}
	}

	stillJunk := []string{
		"README.md",
		"README",
		"readme.txt",
		"Readme.markdown",
		"ReadMe.md", // mixed case (finding 4)
		"LICENSE",
		"LICENSE.txt",
		"license",
		"LICENCE",
		"CHANGELOG.md",
		"CHANGES",
		"HISTORY.md",
		"AUTHORS",
		"CONTRIBUTORS.md",
		"NOTICE",
		"lib/README.md",
	}
	for _, p := range stillJunk {
		if !pruneutils.IsJunk(p, false, opts) {
			t.Errorf("IsJunk(%q) = false; want true (genuine doc file)", p)
		}
	}
}

// TestIsJunk_NewJunkPatterns pins finding 2: common junk that DefaultJunkPatterns
// previously missed entirely (.DS_Store, lockfiles, editor directories, log files, ...).
// Each new pattern is also checked against a lookalike runtime filename to confirm it
// doesn't over-match.
func TestIsJunk_NewJunkPatterns(t *testing.T) {
	opts := pruneutils.PruneOptions{}

	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{".DS_Store", false, true},
		{"sub/.DS_Store", false, true},
		{".gitignore", false, true},
		{".gitattributes", false, true},
		{".npmrc", false, true},
		{"yarn.lock", false, true},
		{"package-lock.json", false, true},
		{"pnpm-lock.yaml", false, true},
		{".vscode/settings.json", false, true},
		{".vscode", true, true},
		{".idea/workspace.xml", false, true},
		{".idea", true, true},
		{".yarn/cache/foo-1.0.0.zip", false, true},
		{"npm-debug.log", false, true},
		{"yarn-error.log", false, true},
		{"CODEOWNERS", false, true},
		{".github/CODEOWNERS", false, true},

		// Lookalikes that must NOT be over-matched by the new patterns.
		{"dialogHandler.js", false, false},
		{"catalog.js", false, false},
		{"lockfile.js", false, false},
		{"vscode-languageclient.js", false, false},
	}

	for _, tc := range cases {
		got := pruneutils.IsJunk(tc.path, tc.isDir, opts)
		if got != tc.want {
			t.Errorf("IsJunk(%q, isDir=%v) = %v; want %v", tc.path, tc.isDir, got, tc.want)
		}
	}
}
