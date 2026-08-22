package routefilterutils

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

func TestRouteForFile(t *testing.T) {
	cases := map[string]string{
		"index.html":            "/",
		"about.html":            "/about",
		"storybook/index.html":  "/storybook",
		"storybook/button.html": "/storybook/button",
		"sitemap.xml":           "/sitemap.xml",
		// A precompressed sibling is the same route — it is what a browser
		// sending Accept-Encoding: br actually gets.
		"index.html.br":           "/",
		"storybook/index.html.gz": "/storybook",
		"about.html.zst":          "/about",
		"api/data.json":           "/api/data.json",
	}
	for rel, want := range cases {
		if got := RouteForFile(rel); got != want {
			t.Errorf("RouteForFile(%q) = %q, want %q", rel, got, want)
		}
	}
}

func TestMatchesAny(t *testing.T) {
	cases := []struct {
		route   string
		pattern string
		want    bool
	}{
		{"/storybook", "/storybook", true},
		{"/storybook/button", "/storybook", true}, // bare pattern covers the subtree
		{"/administration", "/admin", false},      // and stops at a segment boundary
		{"/admin/users", "/admin", true},
		{"/storybook/button", "/storybook/*", true},
		{"/storybook/a/b", "/storybook/*", false}, // single-segment wildcard
		{"/storybook/a/b", "/storybook/**", true},
		{"/blog/post", "/nope", false},
		{"/", "/", true},
		{"/about", "/", false}, // root must not swallow the whole site
	}
	for _, c := range cases {
		if got := MatchesAny(c.route, []string{c.pattern}); got != c.want {
			t.Errorf("MatchesAny(%q, %q) = %v, want %v", c.route, c.pattern, got, c.want)
		}
	}
}

func TestValidatePatterns(t *testing.T) {
	if err := ValidatePatterns([]string{"/storybook", "/admin/**"}); err != nil {
		t.Errorf("ValidatePatterns(valid) = %v, want nil", err)
	}
	for _, bad := range []string{"", "storybook", "https://example.com/storybook"} {
		if err := ValidatePatterns([]string{"/ok", bad}); err == nil {
			t.Errorf("ValidatePatterns(%q) = nil, want an error", bad)
		}
	}
}

// writeTree lays out a prerendered output tree with several distinct routes,
// so exclusion is exercised against a multi-item collection whose members have
// differing outcomes (self_review_checklist row 3) rather than one page.
func writeTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"index.html":              `<a href="/about">about</a><a href="/storybook">gallery</a>`,
		"about.html":              `<a href="/">home</a><a href="/storybook/button">btn</a><a href="https://x.test/storybook">ext</a>`,
		"storybook/index.html":    `<h1>gallery</h1>`,
		"storybook/button.html":   `<h1>button</h1>`,
		"administration/idx.html": `<h1>keep me</h1>`,
		"sitemap.xml":             `<urlset/>`,
	}
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestApplyExclusions_RemovesSubtreeAndLeavesTheRest(t *testing.T) {
	dir := writeTree(t)

	res, err := ApplyExclusions(dir, []string{"/storybook"})
	if err != nil {
		t.Fatalf("ApplyExclusions() error = %v", err)
	}

	wantRemoved := []string{"storybook/button.html", "storybook/index.html"}
	if !equal(res.RemovedFiles, wantRemoved) {
		t.Errorf("RemovedFiles = %v, want %v", res.RemovedFiles, wantRemoved)
	}
	// The files must actually be gone from disk, not merely reported: the
	// packager reads the tree, not this Result.
	for _, rel := range wantRemoved {
		if _, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); !os.IsNotExist(statErr) {
			t.Errorf("%s still on disk after exclusion, stat err = %v", rel, statErr)
		}
	}
	for _, keep := range []string{"index.html", "about.html", "administration/idx.html", "sitemap.xml"} {
		if _, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(keep))); statErr != nil {
			t.Errorf("%s was removed but should have been kept: %v", keep, statErr)
		}
	}
	if len(res.UnmatchedPatterns) != 0 {
		t.Errorf("UnmatchedPatterns = %v, want none", res.UnmatchedPatterns)
	}
}

func TestApplyExclusions_ReportsUnmatchedPattern(t *testing.T) {
	dir := writeTree(t)

	// Two patterns, only one of which matches — a single-pattern test cannot
	// distinguish "reported the unmatched one" from "reported them all".
	res, err := ApplyExclusions(dir, []string{"/storybook", "/does-not-exist"})
	if err != nil {
		t.Fatalf("ApplyExclusions() error = %v", err)
	}
	if !equal(res.UnmatchedPatterns, []string{"/does-not-exist"}) {
		t.Errorf("UnmatchedPatterns = %v, want [/does-not-exist]", res.UnmatchedPatterns)
	}
}

// TestApplyExclusions_DoesNotDeleteThroughASymlink guards the deletion path
// against a symlinked entry pointing outside the output tree. A build tree is
// attacker-influenced whenever a dependency's build step can write into it, so
// "it matched the pattern" must never be enough to follow a link out.
func TestApplyExclusions_DoesNotDeleteThroughASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on windows")
	}
	dir := writeTree(t)
	outside := filepath.Join(t.TempDir(), "precious.html")
	if err := os.WriteFile(outside, []byte("do not delete"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "storybook", "linked.html")); err != nil {
		t.Fatal(err)
	}

	res, err := ApplyExclusions(dir, []string{"/storybook"})
	if err != nil {
		t.Fatalf("ApplyExclusions() error = %v", err)
	}
	if _, statErr := os.Stat(outside); statErr != nil {
		t.Fatalf("symlink target outside the tree was deleted: %v", statErr)
	}
	if !equal(res.SkippedSymlinks, []string{"storybook/linked.html"}) {
		t.Errorf("SkippedSymlinks = %v, want [storybook/linked.html]", res.SkippedSymlinks)
	}
}

func TestFindDeadLinks(t *testing.T) {
	dir := writeTree(t)
	if _, err := ApplyExclusions(dir, []string{"/storybook"}); err != nil {
		t.Fatal(err)
	}

	links, err := FindDeadLinks(dir, []string{"/storybook"})
	if err != nil {
		t.Fatalf("FindDeadLinks() error = %v", err)
	}
	// about.html links to /storybook/button, index.html to /storybook; the
	// absolute external https://x.test/storybook must not be reported.
	want := []DeadLink{
		{FromPage: "about.html", Href: "/storybook/button", Route: "/storybook/button"},
		{FromPage: "index.html", Href: "/storybook", Route: "/storybook"},
	}
	if len(links) != len(want) {
		t.Fatalf("FindDeadLinks() = %+v, want %+v", links, want)
	}
	for i := range want {
		if links[i] != want[i] {
			t.Errorf("link[%d] = %+v, want %+v", i, links[i], want[i])
		}
	}
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}
