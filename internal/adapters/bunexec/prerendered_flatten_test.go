package bunexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixtureFile creates path (and its parent directories) with content.
func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestFlattenPrerenderedOutput_PagesOnly is the common real-world shape (the
// one this project's committed real fixture,
// testdata/fixtures/sveltekit-static, actually has): only "pages" is
// populated, nested one level deep including a subdirectory (matching a
// non-root route). "dependencies" and "data" are entirely absent, which must
// not be treated as an error.
func TestFlattenPrerenderedOutput_PagesOnly(t *testing.T) {
	dir := t.TempDir()
	prerenderedDir := filepath.Join(dir, "prerendered")
	writeFixtureFile(t, filepath.Join(prerenderedDir, "pages", "index.html"), "<h1>index</h1>")
	writeFixtureFile(t, filepath.Join(prerenderedDir, "pages", "about.html"), "<h1>about</h1>")
	writeFixtureFile(t, filepath.Join(prerenderedDir, "pages", "blog", "post-1.html"), "<h1>post 1</h1>")

	if err := FlattenPrerenderedOutput(prerenderedDir); err != nil {
		t.Fatalf("FlattenPrerenderedOutput() error = %v, want nil", err)
	}

	for _, want := range []string{
		filepath.Join(prerenderedDir, "index.html"),
		filepath.Join(prerenderedDir, "about.html"),
		filepath.Join(prerenderedDir, "blog", "post-1.html"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("expected flattened file %s to exist: %v", want, err)
		}
	}
	if _, err := os.Stat(filepath.Join(prerenderedDir, "pages")); !os.IsNotExist(err) {
		t.Errorf("expected prerendered/pages to be removed after flattening, stat err = %v", err)
	}
}

// TestFlattenPrerenderedOutput_AllThreeCategoriesMerge proves the merge
// covers "dependencies" and "data" too — not just "pages" — matching
// @sveltejs/kit's own Builder.writePrerendered, which copies all three (see
// FlattenPrerenderedOutput's doc comment for where this was confirmed against
// the real, vendored @sveltejs/kit source).
func TestFlattenPrerenderedOutput_AllThreeCategoriesMerge(t *testing.T) {
	dir := t.TempDir()
	prerenderedDir := filepath.Join(dir, "prerendered")
	writeFixtureFile(t, filepath.Join(prerenderedDir, "pages", "index.html"), "<h1>index</h1>")
	writeFixtureFile(t, filepath.Join(prerenderedDir, "dependencies", "api", "posts.json"), `{"posts":[]}`)
	writeFixtureFile(t, filepath.Join(prerenderedDir, "data", "posts", "_data.json"), `{"remote":true}`)

	if err := FlattenPrerenderedOutput(prerenderedDir); err != nil {
		t.Fatalf("FlattenPrerenderedOutput() error = %v, want nil", err)
	}

	for _, want := range []string{
		filepath.Join(prerenderedDir, "index.html"),
		filepath.Join(prerenderedDir, "api", "posts.json"),
		filepath.Join(prerenderedDir, "posts", "_data.json"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("expected flattened file %s to exist: %v", want, err)
		}
	}
	for _, gone := range []string{"pages", "dependencies", "data"} {
		if _, err := os.Stat(filepath.Join(prerenderedDir, gone)); !os.IsNotExist(err) {
			t.Errorf("expected prerendered/%s to be removed after flattening, stat err = %v", gone, err)
		}
	}
}

// TestFlattenPrerenderedOutput_NoCategoriesIsNotAnError covers a prerendered/
// directory that exists (Prepare's own os.Stat check already passed) but has
// no pages/dependencies/data subdirectories at all — e.g. a site with zero
// prerenderable routes and only an SPA fallback. Nothing to flatten is not a
// failure.
func TestFlattenPrerenderedOutput_NoCategoriesIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	prerenderedDir := filepath.Join(dir, "prerendered")
	if err := os.MkdirAll(prerenderedDir, 0o755); err != nil {
		t.Fatalf("mkdir prerenderedDir: %v", err)
	}

	if err := FlattenPrerenderedOutput(prerenderedDir); err != nil {
		t.Fatalf("FlattenPrerenderedOutput() error = %v, want nil", err)
	}
}

// TestFlattenPrerenderedOutput_CollisionIsHardError proves that if two
// categories somehow produced the same relative path, FlattenPrerenderedOutput
// refuses to silently pick one over the other (mem:self_review_checklist's
// "never silently overwrite" family) — it fails loudly instead.
func TestFlattenPrerenderedOutput_CollisionIsHardError(t *testing.T) {
	dir := t.TempDir()
	prerenderedDir := filepath.Join(dir, "prerendered")
	writeFixtureFile(t, filepath.Join(prerenderedDir, "dependencies", "clash.html"), "dependencies version")
	writeFixtureFile(t, filepath.Join(prerenderedDir, "pages", "clash.html"), "pages version")

	err := FlattenPrerenderedOutput(prerenderedDir)
	if err == nil {
		t.Fatal("FlattenPrerenderedOutput() error = nil, want a collision error")
	}
	if !strings.Contains(err.Error(), "clash.html") || !strings.Contains(err.Error(), "both produced") {
		t.Errorf("error = %v, want it to name the colliding path and explain the refusal", err)
	}
}

// TestFlattenPrerenderedOutput_RefusesEscapingSymlinkDestination proves the
// containment FlattenPrerenderedOutput provides is structural rather than
// incidental (gosec G122, roadmap item walk-callback-symlink-toctou).
//
// The reachable escape is on the DESTINATION side, not the walked source: rel
// is derived with filepath.Rel and so can never contain "..", but the
// destination's parent directory is looked up inside the staging tree, and a
// symlink there resolves wherever it points. Before the os.Root conversion,
// prerendered/sub being a symlink out of the staging tree meant os.Stat and
// os.Rename both followed it, and pages/sub/leak.html was deposited at
// outside/leak.html with FlattenPrerenderedOutput returning nil — a silent
// write outside the tree it is supposed to be rearranging.
//
// The assertion is empirical: it checks whether the canary bytes actually
// landed outside the staging directory, not merely that some error came back.
// The escaping entry is deliberately not the first one walked ("index.html"
// sorts before "sub"), so a legitimate flatten is proven to still happen in
// the same call (mem:self_review_checklist rows 3 and 4).
func TestFlattenPrerenderedOutput_RefusesEscapingSymlinkDestination(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	prerenderedDir := filepath.Join(dir, "prerendered")
	writeFixtureFile(t, filepath.Join(prerenderedDir, "pages", "index.html"), "<h1>index</h1>")
	writeFixtureFile(t, filepath.Join(prerenderedDir, "pages", "sub", "leak.html"), "canary-bytes")
	if err := os.Symlink(outside, filepath.Join(prerenderedDir, "sub")); err != nil {
		t.Fatalf("creating the test symlink failed; this containment test must never be silently skipped (checklist rows 39/47): %v", err)
	}

	err := FlattenPrerenderedOutput(prerenderedDir)

	// The escape check is the point of the test and runs regardless of what
	// the function returned: a nil error plus an escaped file is the exact
	// pre-conversion behaviour.
	if data, readErr := os.ReadFile(filepath.Join(outside, "leak.html")); readErr == nil {
		t.Fatalf("prerendered file escaped the staging tree: %s contains %q (FlattenPrerenderedOutput returned %v)",
			filepath.Join(outside, "leak.html"), data, err)
	}
	if err == nil {
		t.Error("FlattenPrerenderedOutput() error = nil, want a refusal to move a file through a symlink leaving the staging tree")
	}
	// The canary must still be where it started — refused, not lost.
	if data, readErr := os.ReadFile(filepath.Join(prerenderedDir, "pages", "sub", "leak.html")); readErr != nil {
		t.Errorf("refused file should be left in place, reading it failed: %v", readErr)
	} else if string(data) != "canary-bytes" {
		t.Errorf("refused file content = %q, want %q", data, "canary-bytes")
	}
	// ...and the legitimate sibling walked before it must still have been
	// flattened, so the refusal is scoped to the escaping path.
	if _, statErr := os.Stat(filepath.Join(prerenderedDir, "index.html")); statErr != nil {
		t.Errorf("legitimate in-root file should still flatten: %v", statErr)
	}
}

// TestFlattenPrerenderedOutput_FollowsRelativeSymlinkInsideTree pins the other
// half of the os.Root semantics deliberately: only symlinks that LEAVE the
// staging tree are refused. A relative one resolving back inside it is still
// followed, exactly as the plain os.Stat/os.Rename calls this replaced would
// have. This is a behaviour-preservation test, not a containment test — it
// exists so a future tightening (e.g. refusing all symlinks) cannot happen
// silently.
//
// The target is deliberately RELATIVE. os.Root refuses an absolute symlink
// target outright, even one naming a path inside the root, because openat-based
// resolution cannot verify an absolute target belongs to the root — see
// TestFlattenPrerenderedOutput_RefusesAbsoluteSymlinkTarget, which records that
// as an accepted, deliberate difference from the previous os.Rename behaviour.
func TestFlattenPrerenderedOutput_FollowsRelativeSymlinkInsideTree(t *testing.T) {
	dir := t.TempDir()
	prerenderedDir := filepath.Join(dir, "prerendered")
	inner := filepath.Join(prerenderedDir, "real")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatalf("mkdir inner: %v", err)
	}
	writeFixtureFile(t, filepath.Join(prerenderedDir, "pages", "sub", "ok.html"), "inside")
	if err := os.Symlink("real", filepath.Join(prerenderedDir, "sub")); err != nil {
		t.Fatalf("creating the test symlink failed; this containment test must never be silently skipped (checklist rows 39/47): %v", err)
	}

	if err := FlattenPrerenderedOutput(prerenderedDir); err != nil {
		t.Fatalf("FlattenPrerenderedOutput() error = %v, want nil for a relative symlink resolving inside the tree", err)
	}
	data, err := os.ReadFile(filepath.Join(inner, "ok.html"))
	if err != nil {
		t.Fatalf("expected the file to land through the in-tree symlink at %s: %v", filepath.Join(inner, "ok.html"), err)
	}
	if string(data) != "inside" {
		t.Errorf("content = %q, want %q", data, "inside")
	}
}

// TestFlattenPrerenderedOutput_RefusesAbsoluteSymlinkTarget documents the one
// behaviour the os.Root conversion deliberately changes: an ABSOLUTE symlink
// target is refused even when it names a directory inside the staging tree,
// because os.Root's openat-based resolution has no way to prove an absolute
// path lies within the root. The previous os.Rename would have followed it.
//
// This is accepted rather than worked around: SvelteKit's own prerender
// postbuild step writes plain files and directories into this staging tree, so
// an absolute symlink appearing inside it is not a shape any supported build
// produces, and refusing it fails loudly (a Prepare error naming the path)
// rather than silently writing somewhere unexpected. The test exists so the
// next reader finds this recorded instead of rediscovering it as a bug.
func TestFlattenPrerenderedOutput_RefusesAbsoluteSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	prerenderedDir := filepath.Join(dir, "prerendered")
	inner := filepath.Join(prerenderedDir, "real")
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatalf("mkdir inner: %v", err)
	}
	writeFixtureFile(t, filepath.Join(prerenderedDir, "pages", "sub", "ok.html"), "inside")
	if err := os.Symlink(inner, filepath.Join(prerenderedDir, "sub")); err != nil {
		t.Fatalf("creating the test symlink failed; this containment test must never be silently skipped (checklist rows 39/47): %v", err)
	}

	err := FlattenPrerenderedOutput(prerenderedDir)
	if err == nil {
		t.Fatal("FlattenPrerenderedOutput() error = nil, want a refusal for an absolute symlink target")
	}
	if !strings.Contains(err.Error(), "sub") {
		t.Errorf("error = %v, want it to name the refused path", err)
	}
	// Nothing was moved through it.
	if _, statErr := os.Stat(filepath.Join(inner, "ok.html")); statErr == nil {
		t.Error("file was moved through an absolute symlink target; it should have been refused")
	}
}
