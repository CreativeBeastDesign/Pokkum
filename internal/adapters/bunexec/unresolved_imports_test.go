package bunexec

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// stageTree writes a package.json and a node_modules tree, returning both
// paths. Packages named here are created as resolvable packages (a directory
// with its own package.json), which is what Node and Bun actually require.
func stageTree(t *testing.T, manifest string, installed ...string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	modules := filepath.Join(dir, "node_modules")
	for _, name := range installed {
		pkgDir := filepath.Join(modules, filepath.FromSlash(name))
		if err := os.MkdirAll(pkgDir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pkgDir, "package.json"), []byte(`{"name":"`+name+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if len(installed) == 0 {
		return dir, ""
	}
	return dir, modules
}

// TestVerify_ReportsOnlyTheMissingOne uses three production dependencies with
// differing outcomes, so the test cannot pass by reporting all of them or none
// (self_review_checklist row 3), and the missing one is not the first
// alphabetically (row 4).
func TestVerify_ReportsOnlyTheMissingOne(t *testing.T) {
	manifest := `{"name":"app","dependencies":{"alpha":"1.0.0","zeta":"1.0.0","mid":"1.0.0"}}`
	dir, modules := stageTree(t, manifest, "alpha", "zeta")

	missing, err := verifyProductionDependenciesResolvable(dir, modules)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if len(missing) != 1 || missing[0] != "mid" {
		t.Errorf("missing = %v, want [mid]", missing)
	}
}

// TestVerify_IgnoresDevDependencies is the guard's most important negative
// case. adapter-node bundles every devDependency into the server output, so a
// devDependency absent from the staged production tree is correct and
// expected — reporting it is precisely what got the previous static-analysis
// attempt reverted for failing working builds.
func TestVerify_IgnoresDevDependencies(t *testing.T) {
	manifest := `{"name":"app","dependencies":{"katex":"1.0.0"},"devDependencies":{"@sveltejs/kit":"2.0.0","vite":"8.0.0","svelte":"5.0.0"}}`
	dir, modules := stageTree(t, manifest, "katex")

	missing, err := verifyProductionDependenciesResolvable(dir, modules)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none — devDependencies are bundled into the output, not resolved at runtime", missing)
	}
}

func TestVerify_ScopedPackages(t *testing.T) {
	manifest := `{"name":"app","dependencies":{"@scope/present":"1.0.0","@scope/absent":"1.0.0"}}`
	dir, modules := stageTree(t, manifest, "@scope/present")

	missing, err := verifyProductionDependenciesResolvable(dir, modules)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if len(missing) != 1 || missing[0] != "@scope/absent" {
		t.Errorf("missing = %v, want [@scope/absent]", missing)
	}
}

// TestVerify_DirectoryWithoutPackageJSONDoesNotResolve covers a half-written
// install: the directory exists, so a presence check would pass, but Node
// resolves a bare specifier through the package's own package.json and would
// fail at runtime.
func TestVerify_DirectoryWithoutPackageJSONDoesNotResolve(t *testing.T) {
	manifest := `{"name":"app","dependencies":{"hollow":"1.0.0"}}`
	dir, modules := stageTree(t, manifest, "other")
	if err := os.MkdirAll(filepath.Join(modules, "hollow"), 0o750); err != nil {
		t.Fatal(err)
	}

	missing, err := verifyProductionDependenciesResolvable(dir, modules)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if len(missing) != 1 || missing[0] != "hollow" {
		t.Errorf("missing = %v, want [hollow] — a directory without package.json is not a resolvable package", missing)
	}
}

// TestVerify_NothingStagedAtAll is the original bug in its purest form: the
// manifest declares production dependencies and the staging step produced no
// tree, so every one of them is missing.
func TestVerify_NothingStagedAtAll(t *testing.T) {
	manifest := `{"name":"app","dependencies":{"katex":"1.0.0","clsx":"2.0.0"}}`
	dir, _ := stageTree(t, manifest)

	missing, err := verifyProductionDependenciesResolvable(dir, "")
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if len(missing) != 2 {
		t.Errorf("missing = %v, want both declared dependencies", missing)
	}
}

func TestVerify_NoProductionDependenciesIsClean(t *testing.T) {
	manifest := `{"name":"app","devDependencies":{"vite":"8.0.0"}}`
	dir, _ := stageTree(t, manifest)

	missing, err := verifyProductionDependenciesResolvable(dir, "")
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none", missing)
	}
}

// TestVerify_RejectsTraversalInPackageName guards a malformed manifest: a
// package name cannot contain "..", and following one would stat outside the
// staged tree.
func TestVerify_RejectsTraversalInPackageName(t *testing.T) {
	manifest := `{"name":"app","dependencies":{"../../etc":"1.0.0"}}`
	dir, modules := stageTree(t, manifest, "real")

	missing, err := verifyProductionDependenciesResolvable(dir, modules)
	if err != nil {
		t.Fatalf("error = %v, want nil", err)
	}
	if len(missing) != 1 || missing[0] != "../../etc" {
		t.Errorf("missing = %v, want the traversal name reported as unresolvable", missing)
	}
}

func TestFormatMissingDependencies_NamesThePackagesAndTheFix(t *testing.T) {
	msg := formatMissingDependencies([]string{"katex", "clsx"}, "/tmp/stage/node_modules")
	for _, want := range []string{"katex", "clsx", "/tmp/stage/node_modules", "devDependencies"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message does not mention %q:\n%s", want, msg)
		}
	}
}

// bunThatInstalls emulates the two invocations Prepare makes: `bun install
// --production ...` inside the staging directory, and `bun run build` in the
// project. Whether the install actually produces a tree is the variable under
// test — a real `bun install --production` exits 0 having installed nothing
// when the lockfile disagrees with the manifest, which is the failure this
// guard exists to catch.
func bunThatInstalls(pkgs string) string {
	return `set -e
if [ "$1" = "install" ]; then
  for p in ` + pkgs + `; do
    mkdir -p "node_modules/$p"
    printf '{"name":"%s"}' "$p" > "node_modules/$p/package.json"
  done
  exit 0
fi
mkdir -p build
printf 'export default {};\n' > build/index.js
cat > build/handler.js <<'HANDLEREOF'
` + validHandlerJS + `HANDLEREOF
exit 0
`
}

// TestPrepare_FailsWhenAProductionDependencyWasNotStaged drives the real
// Prepare rather than the checker, because a guard that is never reached is
// indistinguishable from one that passes (self_review_checklist row 27).
func TestPrepare_FailsWhenAProductionDependencyWasNotStaged(t *testing.T) {
	pkg := `{"name":"app","dependencies":{"katex":"^0.18.1"},"devDependencies":{"@sveltejs/kit":"^2.5.0"}}`
	dir := newProjectDir(t, pkg, fmt.Sprintf(svCreateSvelteConfigFmt, "@sveltejs/adapter-node"))
	// The install "succeeds" but stages nothing — exactly the silent
	// under-delivery the guard is for.
	putFakeBunOnPath(t, bunThatInstalls(""))
	c := NewCompiler(discardLogger())

	_, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir:      dir,
		Strategy:        ports.StrategyLayered,
		SourceDateEpoch: time.Unix(1700000000, 0),
	})
	if err == nil {
		t.Fatal("Prepare() succeeded with katex missing from the image; the first route importing it would 500 in production")
	}
	if !strings.Contains(err.Error(), "katex") {
		t.Errorf("error does not name the missing package: %v", err)
	}
}

// TestPrepare_SucceedsWhenProductionDependenciesAreStaged is the control. Both
// halves are needed: without it, a guard that rejected every layered build
// would still pass the test above.
func TestPrepare_SucceedsWhenProductionDependenciesAreStaged(t *testing.T) {
	pkg := `{"name":"app","dependencies":{"katex":"^0.18.1"},"devDependencies":{"@sveltejs/kit":"^2.5.0"}}`
	dir := newProjectDir(t, pkg, fmt.Sprintf(svCreateSvelteConfigFmt, "@sveltejs/adapter-node"))
	putFakeBunOnPath(t, bunThatInstalls("katex"))
	c := NewCompiler(discardLogger())

	if _, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir:      dir,
		Strategy:        ports.StrategyLayered,
		SourceDateEpoch: time.Unix(1700000000, 0),
	}); err != nil {
		t.Fatalf("Prepare() error = %v, want nil — katex was staged and resolves", err)
	}
}
