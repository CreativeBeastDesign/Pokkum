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
