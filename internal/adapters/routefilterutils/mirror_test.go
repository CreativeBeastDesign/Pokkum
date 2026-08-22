package routefilterutils

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

// writeRoutes lays out a routes tree with the shapes that actually occur:
// a plain route, a nested route under a directory carrying its own +layout,
// a SvelteKit group directory, and a dynamic segment.
func writeRoutes(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlink mirror is POSIX-only in these tests")
	}
	root := t.TempDir()
	files := []string{
		"+layout.svelte",
		"+page.svelte",
		"about/+page.svelte",
		"dev/+page.svelte",
		"dev/+layout.svelte",
		"dev/widgets/+page.svelte",
		"admin/+layout.svelte",
		"admin/panel/+page.svelte",
		"admin/audit/+page.svelte",
		"(marketing)/pricing/+page.svelte",
		"blog/[slug]/+page.svelte",
	}
	for _, rel := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("<h1>"+rel+"</h1>"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// mirrorRoutes lists every route file reachable through the mirror, following
// symlinks — which is what SvelteKit's route walker sees.
func mirrorRoutes(t *testing.T, mirror string) []string {
	t.Helper()
	var out []string
	var walk func(dir, rel string)
	walk = func(dir, rel string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, e := range entries {
			childRel := e.Name()
			if rel != "" {
				childRel = rel + "/" + e.Name()
			}
			p := filepath.Join(dir, e.Name())
			info, err := os.Stat(p) // Stat, not Lstat: follow the symlinks.
			if err != nil {
				t.Fatalf("stat %s: %v", p, err)
			}
			if info.IsDir() {
				walk(p, childRel)
				continue
			}
			out = append(out, childRel)
		}
	}
	walk(mirror, "")
	sort.Strings(out)
	return out
}

func TestBuildFilteredRoutesMirror_ExcludesTheRouteAndItsSubtree(t *testing.T) {
	root := writeRoutes(t)
	mirror := filepath.Join(t.TempDir(), "routes")

	res, err := BuildFilteredRoutesMirror(root, mirror, []string{"/dev"})
	if err != nil {
		t.Fatalf("BuildFilteredRoutesMirror() error = %v", err)
	}

	got := mirrorRoutes(t, mirror)
	for _, gone := range []string{"dev/+page.svelte", "dev/+layout.svelte", "dev/widgets/+page.svelte"} {
		for _, g := range got {
			if g == gone {
				t.Errorf("%s is still reachable through the mirror", gone)
			}
		}
	}
	// Everything else must survive, including the group and dynamic segments.
	for _, keep := range []string{
		"+layout.svelte", "+page.svelte", "about/+page.svelte",
		"admin/+layout.svelte", "admin/panel/+page.svelte", "admin/audit/+page.svelte",
		"(marketing)/pricing/+page.svelte", "blog/[slug]/+page.svelte",
	} {
		found := false
		for _, g := range got {
			if g == keep {
				found = true
			}
		}
		if !found {
			t.Errorf("%s vanished from the mirror; got %v", keep, got)
		}
	}
	if len(res.ExcludedRoutes) != 1 || res.ExcludedRoutes[0] != "/dev" {
		t.Errorf("ExcludedRoutes = %v, want [/dev]", res.ExcludedRoutes)
	}
}

// TestBuildFilteredRoutesMirror_PartialDirectoryKeepsItsLayout is the silent
// failure this design exists to prevent. Excluding /admin/audit leaves
// /admin/panel behind, and /admin/panel inherits admin/+layout.svelte. A mirror
// that recreated admin/ without carrying the layout across still builds, still
// serves the page, and silently wraps it in the root layout instead — verified
// against a real SvelteKit build. Nothing warns, so the mirror must be right.
func TestBuildFilteredRoutesMirror_PartialDirectoryKeepsItsLayout(t *testing.T) {
	root := writeRoutes(t)
	mirror := filepath.Join(t.TempDir(), "routes")

	if _, err := BuildFilteredRoutesMirror(root, mirror, []string{"/admin/audit"}); err != nil {
		t.Fatalf("BuildFilteredRoutesMirror() error = %v", err)
	}

	got := mirrorRoutes(t, mirror)
	var haveLayout, havePanel, haveAudit bool
	for _, g := range got {
		switch g {
		case "admin/+layout.svelte":
			haveLayout = true
		case "admin/panel/+page.svelte":
			havePanel = true
		case "admin/audit/+page.svelte":
			haveAudit = true
		}
	}
	if !haveLayout {
		t.Error("admin/+layout.svelte was dropped; /admin/panel would silently render in the root layout")
	}
	if !havePanel {
		t.Error("admin/panel/+page.svelte was dropped, but only /admin/audit was excluded")
	}
	if haveAudit {
		t.Error("admin/audit/+page.svelte survived the exclusion")
	}
}

// TestBuildFilteredRoutesMirror_MatchesThroughGroupDirectories: a group
// directory contributes no URL segment, so the pattern is written the way the
// route is addressed.
func TestBuildFilteredRoutesMirror_MatchesThroughGroupDirectories(t *testing.T) {
	root := writeRoutes(t)
	mirror := filepath.Join(t.TempDir(), "routes")

	res, err := BuildFilteredRoutesMirror(root, mirror, []string{"/pricing"})
	if err != nil {
		t.Fatalf("BuildFilteredRoutesMirror() error = %v", err)
	}
	for _, g := range mirrorRoutes(t, mirror) {
		if g == "(marketing)/pricing/+page.svelte" {
			t.Error("/pricing was not excluded through its (marketing) group directory")
		}
	}
	if len(res.UnmatchedPatterns) != 0 {
		t.Errorf("UnmatchedPatterns = %v, want none", res.UnmatchedPatterns)
	}
}

func TestBuildFilteredRoutesMirror_ReportsUnmatched(t *testing.T) {
	root := writeRoutes(t)
	mirror := filepath.Join(t.TempDir(), "routes")

	res, err := BuildFilteredRoutesMirror(root, mirror, []string{"/dev", "/nope"})
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if len(res.UnmatchedPatterns) != 1 || res.UnmatchedPatterns[0] != "/nope" {
		t.Errorf("UnmatchedPatterns = %v, want [/nope]", res.UnmatchedPatterns)
	}
}

// TestBuildFilteredRoutesMirror_RelativeEscapesStillResolve pins the property
// that makes symlinks mandatory rather than a copy: a mirrored route file's
// real path is its original location, so `../../lib/x` still points at the
// project's lib directory.
func TestBuildFilteredRoutesMirror_RelativeEscapesStillResolve(t *testing.T) {
	root := writeRoutes(t)
	// src/lib sits beside src/routes; create it so the escape has a target.
	lib := filepath.Join(filepath.Dir(root), "lib")
	if err := os.MkdirAll(lib, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, "thing.js"), []byte("export const T=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	mirror := filepath.Join(t.TempDir(), "routes")
	if _, err := BuildFilteredRoutesMirror(root, mirror, []string{"/dev"}); err != nil {
		t.Fatal(err)
	}

	// Resolve the mirrored file the way Vite does — through its real path.
	mirrored := filepath.Join(mirror, "about", "+page.svelte")
	real, err := filepath.EvalSymlinks(mirrored)
	if err != nil {
		t.Fatalf("resolving %s: %v", mirrored, err)
	}
	target := filepath.Join(filepath.Dir(real), "..", "..", "lib", "thing.js")
	if _, err := os.Stat(target); err != nil {
		t.Errorf("a relative import escaping the routes tree does not resolve from the mirrored file's real path: %v", err)
	}
}

func TestRouteForDir(t *testing.T) {
	cases := map[string]string{
		"dev":                 "/dev",
		"admin/panel":         "/admin/panel",
		"(marketing)/pricing": "/pricing",
		"blog/[slug]":         "/blog/[slug]",
		"":                    "/",
	}
	for in, want := range cases {
		if got := RouteForDir(in); got != want {
			t.Errorf("RouteForDir(%q) = %q, want %q", in, got, want)
		}
	}
}
