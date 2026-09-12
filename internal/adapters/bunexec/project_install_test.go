package bunexec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Automatic dependency install — the fix for `pokkum build` failing on every
// fresh CI checkout.
//
// Prepare relied on <ProjectDir>/node_modules already existing. It always does
// on a developer machine and never does on a clean CI checkout, so a real
// project following docs/GITHUB_ACTION.md's Quickstart failed on its first run
// with UNRESOLVED_IMPORT for '@sveltejs/kit/vite'. Nothing in the suite
// exercised a project without node_modules: the live fixtures symlink a real
// tree back in, and newProjectDir now models an installed project explicitly.
// These tests are the missing coverage, built on a fixture that deliberately
// has no node_modules.

// incidentPackageJSON is the shape of the real project that hit this: build
// tooling entirely under devDependencies and no "dependencies" key at all —
// as `sv create` scaffolds it. Not a synthetic mix.
const incidentPackageJSON = `{
	"name": "creativehomepage",
	"private": true,
	"type": "module",
	"scripts": {
		"build": "vite build"
	},
	"devDependencies": {
		"@sveltejs/adapter-node": "^5.5.4",
		"@sveltejs/kit": "^2.63.0",
		"svelte": "^5.56.1",
		"vite": "^8.0.16"
	}
}`

// newUninstalledProjectDir is newProjectDir without the node_modules it
// creates: a project exactly as a CI runner sees it after actions/checkout.
func newUninstalledProjectDir(t *testing.T, pkgJSON, svelteConfig string) string {
	t.Helper()
	dir := newProjectDir(t, pkgJSON, svelteConfig)
	if err := os.Remove(filepath.Join(dir, "node_modules")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// recordingBun is a fake bun that appends "<cwd>|<argv>" to logPath on every
// invocation, materialises a node_modules for `install` (in whatever directory
// it was run in, as real bun would) and produces a layered build's output for
// anything else. install succeeds unless installFails is set.
func recordingBun(logPath string, installFails bool) string {
	installBody := `mkdir -p node_modules; exit 0`
	if installFails {
		installBody = `echo "error: lockfile had changes, but lockfile is frozen" >&2; exit 1`
	}
	return `echo "$PWD|$*" >> ` + logPath + `
if [ "$1" = "install" ]; then
  ` + installBody + `
fi
` + succeedingBuildScript
}

// invocations returns the recorded "<cwd>|<argv>" lines, in order.
func invocations(t *testing.T, logPath string) []string {
	t.Helper()
	data, err := os.ReadFile(logPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// projectInstalls filters invocations down to `bun install ...` runs whose cwd
// is the project directory itself — as opposed to the production vendor
// install, which runs in .pokkum/vendor and is not what these tests are about.
//
// Directories are compared after resolving symlinks: on macOS t.TempDir()
// lives under /var, which is a symlink to /private/var, and the shell's $PWD
// may report either spelling.
func projectInstalls(lines []string, projectDir string) []string {
	var out []string
	for _, l := range lines {
		cwd, argv, _ := strings.Cut(l, "|")
		if samePath(cwd, projectDir) && strings.HasPrefix(argv, "install") {
			out = append(out, argv)
		}
	}
	return out
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	if errA != nil || errB != nil {
		return a == b
	}
	return ra == rb
}

func TestPrepare_InstallsDependenciesWhenNodeModulesMissing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lockfile string
		wantArgv string
	}{
		// The incident project had a bun.lock: the install must be pinned to
		// it, never a fresh resolution.
		{name: "with lockfile", lockfile: "bun.lock", wantArgv: "install --no-save --frozen-lockfile"},
		// Without one there is nothing to freeze against; the install still
		// happens (a warning says what it costs) rather than refusing.
		{name: "without lockfile", lockfile: "", wantArgv: "install --no-save"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := newUninstalledProjectDir(t, incidentPackageJSON, fmt.Sprintf(svCreateSvelteConfigFmt, "@sveltejs/adapter-node"))
			if tc.lockfile != "" {
				if err := os.WriteFile(filepath.Join(dir, tc.lockfile), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			logPath := filepath.Join(t.TempDir(), "bun.log")
			putFakeBunOnPath(t, recordingBun(logPath, false))
			c := NewCompiler(discardLogger())

			_, err := c.Prepare(context.Background(), ports.PrepareRequest{
				ProjectDir:      dir,
				Strategy:        ports.StrategyLayered,
				SourceDateEpoch: time.Unix(0, 0),
			})
			if err != nil {
				t.Fatalf("Prepare() error = %v, want nil: the missing node_modules should have been installed, not fatal", err)
			}

			lines := invocations(t, logPath)
			installs := projectInstalls(lines, dir)
			if len(installs) != 1 {
				t.Fatalf("project-dir `bun install` invocations = %d, want exactly 1:\n%s", len(installs), strings.Join(lines, "\n"))
			}
			if installs[0] != tc.wantArgv {
				t.Errorf("install argv = %q, want %q", installs[0], tc.wantArgv)
			}
			// Ordering is the whole point: the build needs the tree before it
			// starts, not merely by the time it finishes.
			if cwd, argv, _ := strings.Cut(lines[0], "|"); !samePath(cwd, dir) || !strings.HasPrefix(argv, "install") {
				t.Errorf("first bun invocation = %q, want the project install to run before anything else", lines[0])
			}
			if !projectNodeModulesPresent(dir) {
				t.Error("node_modules is still absent after Prepare")
			}
		})
	}
}

func TestPrepare_NoInstallFailsFastWhenNodeModulesMissing(t *testing.T) {
	dir := newUninstalledProjectDir(t, incidentPackageJSON, fmt.Sprintf(svCreateSvelteConfigFmt, "@sveltejs/adapter-node"))
	logPath := filepath.Join(t.TempDir(), "bun.log")
	putFakeBunOnPath(t, recordingBun(logPath, false))
	c := NewCompiler(discardLogger())

	_, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir:      dir,
		Strategy:        ports.StrategyLayered,
		NoInstall:       true,
		SourceDateEpoch: time.Unix(0, 0),
	})
	if !errors.Is(err, core.ErrDependenciesMissing) {
		t.Fatalf("err = %v, want wrapping core.ErrDependenciesMissing", err)
	}
	for _, want := range []string{"--no-install", "bun install --frozen-lockfile", "node_modules"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to mention %q", err, want)
		}
	}
	if lines := invocations(t, logPath); len(lines) != 0 {
		t.Errorf("bun was invoked with --no-install and no node_modules; must fail before any subprocess:\n%s", strings.Join(lines, "\n"))
	}
}

// TestPrepare_ExistingNodeModulesIsNeverReinstalled pins the policy that an
// existing tree — even an empty directory — is the user's and is left alone.
// Turning the gate into a staleness check would make Pokkum rewrite `bun
// link`s and CI-cached trees, and would write through the symlinked
// node_modules this repo's own live-fixture harnesses use.
func TestPrepare_ExistingNodeModulesIsNeverReinstalled(t *testing.T) {
	dir := newProjectDir(t, incidentPackageJSON, fmt.Sprintf(svCreateSvelteConfigFmt, "@sveltejs/adapter-node"))
	if err := os.WriteFile(filepath.Join(dir, "bun.lock"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "bun.log")
	putFakeBunOnPath(t, recordingBun(logPath, false))
	c := NewCompiler(discardLogger())

	if _, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir:      dir,
		Strategy:        ports.StrategyLayered,
		SourceDateEpoch: time.Unix(0, 0),
	}); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if installs := projectInstalls(invocations(t, logPath), dir); len(installs) != 0 {
		t.Errorf("`bun install` ran in the project directory although node_modules was present: %q", installs)
	}
}

// TestPrepare_HermeticStillRequiresPrepopulatedNodeModules is a regression
// guard, not new coverage: --hermetic promises zero network egress, so it must
// keep failing fast at Preflight on a missing node_modules exactly as before,
// and Prepare must never run the install for it either — this is the one
// property the automatic install must not weaken.
func TestPrepare_HermeticStillRequiresPrepopulatedNodeModules(t *testing.T) {
	dir := newUninstalledProjectDir(t, validPackageJSON, validSvelteConfig)
	logPath := filepath.Join(t.TempDir(), "bun.log")
	putFakeBunOnPath(t, `echo "$PWD|$*" >> `+logPath+`
case "$1" in
  --version) echo "1.3.14"; exit 0;;
  install) mkdir -p node_modules; exit 0;;
esac
exit 1`)
	c := NewCompiler(discardLogger())

	_, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir, Strategy: ports.StrategyExe, Hermetic: true})
	if !errors.Is(err, core.ErrHermeticViolation) {
		t.Fatalf("Preflight err = %v, want wrapping core.ErrHermeticViolation (unchanged hermetic fail-fast)", err)
	}

	// A caller that skips Preflight (or whose Preflight ran against a tree
	// that has since been deleted) must still get no install out of Prepare.
	_, err = c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir:      dir,
		Strategy:        ports.StrategyExe,
		Hermetic:        true,
		SourceDateEpoch: time.Unix(0, 0),
	})
	if err == nil {
		t.Fatal("Prepare() succeeded for a hermetic build with no node_modules")
	}
	if installs := projectInstalls(invocations(t, logPath), dir); len(installs) != 0 {
		t.Errorf("a hermetic build ran `bun install` in the project directory — that is network egress: %q", installs)
	}
	if projectNodeModulesPresent(dir) {
		t.Error("node_modules appeared during a hermetic build; nothing may install it")
	}
}

func TestPrepare_InstallFailureIsFramedAndStopsBeforeTheBuild(t *testing.T) {
	dir := newUninstalledProjectDir(t, incidentPackageJSON, fmt.Sprintf(svCreateSvelteConfigFmt, "@sveltejs/adapter-node"))
	if err := os.WriteFile(filepath.Join(dir, "bun.lock"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "bun.log")
	putFakeBunOnPath(t, recordingBun(logPath, true))
	c := NewCompiler(discardLogger())

	_, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir:      dir,
		Strategy:        ports.StrategyLayered,
		SourceDateEpoch: time.Unix(0, 0),
	})
	if !errors.Is(err, core.ErrDependencyInstallFailed) {
		t.Fatalf("err = %v, want wrapping core.ErrDependencyInstallFailed", err)
	}
	if errors.Is(err, core.ErrPrepareFailed) {
		t.Errorf("err = %v; an install failure must not be reported as a SvelteKit build failure — the build never ran", err)
	}
	// Framed, not a raw stderr dump: it says Pokkum ran the install and why,
	// and still carries bun's own diagnosis.
	for _, want := range []string{"Pokkum ran `bun install --no-save --frozen-lockfile`", "lockfile had changes, but lockfile is frozen", "--no-install"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to contain %q", err, want)
		}
	}
	for _, l := range invocations(t, logPath) {
		if _, argv, _ := strings.Cut(l, "|"); !strings.HasPrefix(argv, "install") {
			t.Errorf("the build ran after the install failed: %q", l)
		}
	}
}

// TestPrepare_InstallHonoursCancellation: the install is a subprocess Prepare
// now owns before the build; cancelling the build must tear it down promptly
// and report the cancellation, not a dependency failure.
func TestPrepare_InstallHonoursCancellation(t *testing.T) {
	dir := newUninstalledProjectDir(t, incidentPackageJSON, fmt.Sprintf(svCreateSvelteConfigFmt, "@sveltejs/adapter-node"))
	putFakeBunOnPath(t, `if [ "$1" = "install" ]; then sleep 5; fi; exit 0`)
	c := NewCompiler(discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Prepare(ctx, ports.PrepareRequest{
		ProjectDir:      dir,
		Strategy:        ports.StrategyLayered,
		SourceDateEpoch: time.Unix(0, 0),
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want wrapping context.DeadlineExceeded", err)
	}
	if errors.Is(err, core.ErrDependencyInstallFailed) {
		t.Errorf("err = %v; a cancelled install was reclassified as an install failure", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("Prepare took %v to return after cancellation; the install was not torn down", elapsed)
	}
}
