package sbom

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/scannerutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// bunLockWith renders a minimal but real-shaped bun.lock naming one package,
// matching the format scannerutils.ParseBunLock actually consumes
// ("packages": { "<name>": ["<name>@<version>", ...] }).
func bunLockWith(name, version string) string {
	return `{"lockfileVersion":1,"packages":{"` + name + `":["` + name + `@` + version + `","",{},"sha512-x"]}}`
}

// catalogNames returns the package names in a scan result, for assertions that
// care about WHAT was catalogued rather than how many.
func catalogNames(pkgs []scannerutils.CatalogPackage) map[string]string {
	out := make(map[string]string, len(pkgs))
	for _, p := range pkgs {
		out[p.Name] = p.Version
	}
	return out
}

// TestScanProject_RefusesLockfileEscapingProjectDir proves the lockfile reads
// inside scanProject's WalkDir callback are contained to the project directory
// structurally, not by luck (gosec G122, roadmap item
// walk-callback-symlink-toctou).
//
// The project directory is the user's own, but a dependency's install script
// writes into it, so a lockfile-named symlink pointing out of the tree is a
// plausible shape. Before the os.Root conversion, os.ReadFile(p) followed it
// and the foreign file's packages were catalogued into the SBOM as if they
// were the project's own dependencies.
//
// The assertion is empirical and content-based: it checks which package names
// reached the catalogue, not whether an error string appeared — the read error
// is swallowed on both the old and new code paths, so an error assertion could
// not distinguish them at all. Two lockfiles with DIFFERENT package names are
// used, and the escaping one is not the first entry walked ("bun.lock" sorts
// before "nested"), so the in-root read is proven to still work in the same
// scan (mem:self_review_checklist rows 3 and 4).
func TestScanProject_RefusesLockfileEscapingProjectDir(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval base: %v", err)
	}
	outside := filepath.Join(base, "outside")
	project := filepath.Join(base, "project", "nested")
	for _, d := range []string{outside, project} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	projectDir := filepath.Join(base, "project")

	// The file the escaping symlink points at, well outside the project.
	if err := os.WriteFile(filepath.Join(outside, "foreign.lock"), []byte(bunLockWith("leaked-outside-pkg", "9.9.9")), 0o644); err != nil {
		t.Fatal(err)
	}
	// A legitimate, real, in-project lockfile.
	if err := os.WriteFile(filepath.Join(projectDir, "bun.lock"), []byte(bunLockWith("in-project-pkg", "1.0.0")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "package.json"), []byte(`{"name":"p","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// The escape: a lockfile-named symlink leaving the project tree.
	if err := os.Symlink(filepath.Join(outside, "foreign.lock"), filepath.Join(project, "bun.lock")); err != nil {
		t.Fatalf("creating the test symlink failed; this containment test must never be silently skipped (checklist rows 39/47): %v", err)
	}

	g := NewGenerator(nil)
	req := ports.SBOMRequest{ProjectDir: projectDir, Format: ports.SBOMFormatSPDXJSON, CreatedAt: time.Unix(0, 0).UTC()}
	m, err := g.buildMatcher(req)
	if err != nil {
		t.Fatalf("buildMatcher: %v", err)
	}
	pkgs, err := g.scanProject(context.Background(), projectDir, m)
	if err != nil {
		t.Fatalf("scanProject() error = %v, want nil (an unreadable lockfile is skipped, not fatal)", err)
	}

	names := catalogNames(pkgs)
	if v, ok := names["leaked-outside-pkg"]; ok {
		t.Errorf("scan read through a symlink leaving the project: catalogued leaked-outside-pkg@%s", v)
	}
	if v, ok := names["in-project-pkg"]; !ok {
		t.Errorf("legitimate in-project lockfile was not catalogued; got %v", names)
	} else if v != "1.0.0" {
		t.Errorf("in-project-pkg version = %q, want 1.0.0", v)
	}
}

// TestScanProject_FollowsRelativeLockfileSymlinkInsideProject pins the
// preserved half of the semantics: a relative symlink resolving back inside
// the project is still followed, exactly as os.ReadFile did. Workspace layouts
// legitimately do this, so the conversion must not have quietly stopped
// cataloguing them.
func TestScanProject_FollowsRelativeLockfileSymlinkInsideProject(t *testing.T) {
	projectDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval project: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectDir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "package.json"), []byte(`{"name":"p","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "shared.lock"), []byte(bunLockWith("workspace-pkg", "2.0.0")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "shared.lock"), filepath.Join(projectDir, "nested", "bun.lock")); err != nil {
		t.Fatalf("creating the test symlink failed; this containment test must never be silently skipped (checklist rows 39/47): %v", err)
	}

	g := NewGenerator(nil)
	req := ports.SBOMRequest{ProjectDir: projectDir, Format: ports.SBOMFormatSPDXJSON, CreatedAt: time.Unix(0, 0).UTC()}
	m, err := g.buildMatcher(req)
	if err != nil {
		t.Fatalf("buildMatcher: %v", err)
	}
	pkgs, err := g.scanProject(context.Background(), projectDir, m)
	if err != nil {
		t.Fatalf("scanProject() error = %v, want nil", err)
	}
	if _, ok := catalogNames(pkgs)["workspace-pkg"]; !ok {
		t.Errorf("relative in-project lockfile symlink should still be read; got %v", catalogNames(pkgs))
	}
}
