package ignore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatch_BasenameGlob(t *testing.T) {
	m, err := New([]string{"*.map"})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"app.js.map":         true,
		"src/app.js.map":     true,
		"deep/nested/x.map":  true,
		"app.js":             false,
		"mapreduce/index.js": false,
	}
	for p, want := range cases {
		if got := m.Match(p, false); got != want {
			t.Errorf("Match(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestMatch_DotEnvStar(t *testing.T) {
	m, err := New([]string{".env*"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".env", ".env.local", "server/.env.production"} {
		if !m.Match(p, false) {
			t.Errorf("expected %q to be excluded", p)
		}
	}
	if m.Match("environment.ts", false) {
		t.Error("environment.ts should not match .env*")
	}
}

func TestMatch_TrailingSlashDirectoryOnly(t *testing.T) {
	m, err := New([]string{"logs/"})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match("logs", true) {
		t.Error("directory 'logs' should be excluded by 'logs/'")
	}
	if m.Match("logs", false) {
		t.Error("a FILE named 'logs' should not be excluded by the directory-only pattern 'logs/'")
	}
	if !m.Match("logs/2024-01-01.log", false) {
		t.Error("a file inside excluded directory 'logs' should be excluded")
	}
}

func TestMatch_LeadingSlashAnchors(t *testing.T) {
	m, err := New([]string{"/build"})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match("build", true) {
		t.Error("root-level 'build' should match anchored pattern '/build'")
	}
	if m.Match("src/build", true) {
		t.Error("'src/build' should NOT match the root-anchored pattern '/build'")
	}
}

func TestMatch_MiddleSlashIsImplicitlyAnchored(t *testing.T) {
	m, err := New([]string{"node_modules/.cache"})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match("node_modules/.cache", true) {
		t.Error("'node_modules/.cache' should match its own literal pattern")
	}
	if !m.Match("node_modules/.cache/foo.tmp", false) {
		t.Error("files under an excluded directory should be excluded")
	}
	if m.Match("packages/foo/node_modules/.cache", true) {
		t.Error("a pattern containing a slash must be anchored to the root, not match at any depth")
	}
}

func TestMatch_DoubleStarCrossesSegments(t *testing.T) {
	m, err := New([]string{"**/fixtures/**"})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match("test/fixtures/a/b.json", false) {
		t.Error("** should cross multiple directory segments")
	}
	if !m.Match("fixtures/a.json", false) {
		t.Error("**/ should also match zero leading segments")
	}
}

func TestMatch_NegationOverridesEarlierExclusion(t *testing.T) {
	m, err := New([]string{"*.log", "!important.log"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Match("important.log", false) {
		t.Error("important.log should be un-excluded by the later negation")
	}
	if !m.Match("debug.log", false) {
		t.Error("debug.log should still be excluded")
	}
}

func TestMatch_LastMatchingRuleWins(t *testing.T) {
	// A later rule re-excludes a path that an earlier negation had spared.
	m, err := New([]string{"!keep.txt", "keep.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match("keep.txt", false) {
		t.Error("the later, non-negated rule should win over the earlier negation")
	}
}

func TestMatch_NegationInsideExcludedDirectoryDoesNotReinclude(t *testing.T) {
	// Matches real git: once a directory is excluded, git does not look
	// inside it for a deeper negation.
	m, err := New([]string{"build/", "!build/keep.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match("build/keep.txt", false) {
		t.Error("a negation for a path under an excluded directory must not re-include it")
	}
}

func TestMatch_CommentsAndBlankLinesIgnored(t *testing.T) {
	m, err := New([]string{
		"# a comment",
		"",
		"   ",
		"*.tmp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match("a.tmp", false) {
		t.Error("*.tmp should still be compiled despite surrounding comments/blanks")
	}
	if m.Match("# a comment", false) {
		t.Error("comment lines must not become literal patterns")
	}
}

func TestMatch_NoRulesMatchesNothing(t *testing.T) {
	m, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	if m.Match("anything/at/all.txt", false) {
		t.Error("an empty matcher should exclude nothing")
	}
	var nilMatcher *Matcher
	if nilMatcher.Match("x", false) {
		t.Error("a nil *Matcher should exclude nothing")
	}
}

func TestLoad_MissingFileUsesDefaultsOnly(t *testing.T) {
	dir := t.TempDir()
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Match(".git", true) {
		t.Error("default patterns should exclude .git even with no .pokkumignore present")
	}
	if !m.Match("app.js.map", false) {
		t.Error("default patterns should exclude *.map")
	}
	if !m.Match(".env.local", false) {
		t.Error("default patterns should exclude .env*")
	}
	if !m.Match("node_modules/.cache", true) {
		t.Error("default patterns should exclude node_modules/.cache")
	}
	// node_modules itself (without /.cache) must NOT be excluded by the
	// defaults -- only the cache subdirectory is.
	if m.Match("node_modules", true) {
		t.Error("bare node_modules must not be excluded by the built-in defaults")
	}
}

func TestLoad_ProjectFileOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	content := "!*.map\nsecrets/\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Match("app.js.map", false) {
		t.Error("the project's negation of *.map should override the built-in default")
	}
	if !m.Match("secrets", true) {
		t.Error("the project's own pattern should be applied")
	}
	// Defaults not touched by the project file still apply.
	if !m.Match(".env.local", false) {
		t.Error(".env* default should still apply when untouched by the project file")
	}
}
