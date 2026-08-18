package bunexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- project fixture helpers -------------------------------------------------

const validSvelteConfig = `
import adapter from "@jesterkit/exe-sveltekit";

export default {
	kit: {
		adapter: adapter({ target: "linux-x64", embedStatic: true })
	}
};
`

const validPackageJSON = `{
	"name": "sveltekit-basic",
	"dependencies": {
		"@sveltejs/kit": "^2.5.0"
	},
	"devDependencies": {
		"@jesterkit/exe-sveltekit": "^0.4.0"
	}
}`

// viteBuildPackageJSON is validPackageJSON plus a "build": "vite build"
// script — the shape checkEffectiveAdapter's Option B guard requires before
// it will swap `bun run build` for `bun x vite build --config ...`, and the
// real script a genuine `sv create` scaffold's package.json has.
const viteBuildPackageJSON = `{
	"name": "sveltekit-basic",
	"dependencies": {
		"@sveltejs/kit": "^2.5.0"
	},
	"devDependencies": {
		"@jesterkit/exe-sveltekit": "^0.4.0"
	},
	"scripts": {
		"build": "vite build"
	}
}`

// newProjectDir writes package.json and svelte.config.js (when non-empty)
// into a fresh temp directory and returns its path.
func newProjectDir(t *testing.T, pkgJSON, svelteConfig string) string {
	t.Helper()
	dir := t.TempDir()
	if pkgJSON != "" {
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if svelteConfig != "" {
		if err := os.WriteFile(filepath.Join(dir, "svelte.config.js"), []byte(svelteConfig), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// newProjectDirWithVite is newProjectDir plus an optional vite.config.ts, for
// tests of checkEffectiveAdapter's vite.config.ts-governs branch.
func newProjectDirWithVite(t *testing.T, pkgJSON, svelteConfig, viteConfig string) string {
	t.Helper()
	dir := newProjectDir(t, pkgJSON, svelteConfig)
	if viteConfig != "" {
		if err := os.WriteFile(filepath.Join(dir, "vite.config.ts"), []byte(viteConfig), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// putFakeBunOnPath writes an executable shell script named "bun" into a fresh
// temp directory, prepends that directory to PATH for the duration of the
// test (t.Setenv restores it automatically), and returns the script's path.
// script is the body of the shell script after the shebang line.
func putFakeBunOnPath(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake bun shell script fixture is POSIX-shell only")
	}
	dir := t.TempDir()
	bunPath := filepath.Join(dir, "bun")
	content := "#!/bin/sh\n" + script + "\n"
	if err := os.WriteFile(bunPath, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return bunPath
}

// putNoBunOnPath points PATH at an empty directory so exec.LookPath("bun")
// fails, without disturbing any real bun installed on the host.
func putNoBunOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PATH", dir)
}

// --- Preflight: sentinel classification -------------------------------------

func TestPreflight_MissingPackageJSON(t *testing.T) {
	dir := newProjectDir(t, "", validSvelteConfig)
	c := NewCompiler(discardLogger())

	_, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir})
	if !errors.Is(err, core.ErrProjectNotFound) {
		t.Fatalf("err = %v, want wrapping core.ErrProjectNotFound", err)
	}
}

// TestPreflight_MissingSvelteConfig_NotAnError proves a missing
// svelte.config.js does not, by itself, fail Preflight (via the
// package.json-dependency fallback, same as
// TestPreflight_AdapterInPackageJSONOnly_NotAnError) — regression coverage
// for a real bug found by running the actual pipeline against a real `sv
// create` scaffold: current sv create projects (sv@0.17.0 /
// @sveltejs/kit@2.63.0+) generate no svelte.config.js at all, configuring the
// adapter entirely via vite.config.ts instead, so treating the file's mere
// absence as "not a SvelteKit project" hard-failed every real build before
// Prepare's own, strategy-aware checkEffectiveAdapter ever got a chance to
// run. See Roadmap.md's "Layered-Strategy Real-Build Correctness" and
// tests/integration/layered_prerendered_e2e_test.go, which caught this by
// actually running core.Build end to end against such a project.
func TestPreflight_MissingSvelteConfig_NotAnError(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, "")
	putFakeBunOnPath(t, `
case "$1" in
  --version) echo "1.3.14"; exit 0;;
esac
exit 1
`)
	c := NewCompiler(discardLogger())

	// Strategy is stated explicitly (StrategyExe, matching validPackageJSON's
	// @jesterkit/exe-sveltekit devDependency) now that Preflight's adapter
	// check is strategy-aware: the zero value resolves to the layered
	// default's @sveltejs/adapter-node requirement, which this fixture does
	// not declare.
	_, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir, Strategy: ports.StrategyExe})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPreflight_AdapterMissing(t *testing.T) {
	noAdapterPkgJSON := `{"name": "app", "dependencies": {"@sveltejs/kit": "^2.5.0"}}`
	noAdapterSvelteConfig := `
import adapter from "@sveltejs/adapter-auto";
export default { kit: { adapter: adapter() } };
`
	dir := newProjectDir(t, noAdapterPkgJSON, noAdapterSvelteConfig)
	c := NewCompiler(discardLogger())

	_, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir})
	if !errors.Is(err, core.ErrAdapterMissing) {
		t.Fatalf("err = %v, want wrapping core.ErrAdapterMissing", err)
	}
}

func TestPreflight_AdapterInPackageJSONOnly_NotAnError(t *testing.T) {
	// The adapter is a devDependency but svelte.config.js's text does not
	// literally contain the package string (e.g. re-exported from a shared
	// config module). Preflight must still pass via the package.json fallback.
	svelteConfig := `
import adapter from "./shared-adapter.js";
export default { kit: { adapter: adapter({ target: "linux-x64" }) } };
`
	dir := newProjectDir(t, validPackageJSON, svelteConfig)
	putFakeBunOnPath(t, `
case "$1" in
  --version) echo "1.3.14"; exit 0;;
esac
exit 1
`)
	c := NewCompiler(discardLogger())

	// Strategy is stated explicitly (StrategyExe): the fixture's
	// svelte.config.js re-exports the adapter from a local module (so
	// AdapterConfigured's literal text match can't see it), and it is
	// package.json's @jesterkit/exe-sveltekit devDependency the fallback
	// must match against — which only happens for StrategyExe now that the
	// check is strategy-aware.
	_, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir, Strategy: ports.StrategyExe})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPreflight_BunNotFound(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	putNoBunOnPath(t)
	c := NewCompiler(discardLogger())

	_, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir, Strategy: ports.StrategyExe})
	if !errors.Is(err, core.ErrBunNotFound) {
		t.Fatalf("err = %v, want wrapping core.ErrBunNotFound", err)
	}
}

func TestPreflight_BunTooOld(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	putFakeBunOnPath(t, `
case "$1" in
  --version) echo "1.0.0"; exit 0;;
esac
exit 1
`)
	c := NewCompiler(discardLogger())

	_, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir, Strategy: ports.StrategyExe})
	if !errors.Is(err, core.ErrBunTooOld) {
		t.Fatalf("err = %v, want wrapping core.ErrBunTooOld", err)
	}
}

func TestPreflight_BunTooOld_CustomMinVersion(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	putFakeBunOnPath(t, `
case "$1" in
  --version) echo "1.3.0"; exit 0;;
esac
exit 1
`)
	c := NewCompiler(discardLogger())

	_, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir, MinBunVersion: "1.4.0", Strategy: ports.StrategyExe})
	if !errors.Is(err, core.ErrBunTooOld) {
		t.Fatalf("err = %v, want wrapping core.ErrBunTooOld", err)
	}
}

func TestPreflight_Success(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	putFakeBunOnPath(t, `
case "$1" in
  --version) echo "1.3.14"; exit 0;;
esac
exit 1
`)
	c := NewCompiler(discardLogger())

	result, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir, Strategy: ports.StrategyExe})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.BunVersion != "1.3.14" {
		t.Errorf("BunVersion = %q, want %q", result.BunVersion, "1.3.14")
	}
	if result.BunPath == "" {
		t.Error("BunPath is empty")
	}
	// node_modules was never installed in this fixture, so ResolveVersion
	// should fall back to the package.json range rather than error.
	if result.AdapterVersion != "^0.4.0" {
		t.Errorf("AdapterVersion = %q, want fallback %q", result.AdapterVersion, "^0.4.0")
	}
	if result.SvelteKitVersion != "^2.5.0" {
		t.Errorf("SvelteKitVersion = %q, want fallback %q", result.SvelteKitVersion, "^2.5.0")
	}
}

// --- Preflight: strategy-aware adapter requirement --------------------------
//
// Before this fix, Preflight unconditionally required @jesterkit/exe-
// sveltekit or @sveltejs/adapter-node regardless of req.Strategy, so a real,
// correctly-configured adapter-static-only project (what --strategy=static
// actually needs) failed here before Prepare's own, already strategy-aware
// checkEffectiveAdapter ever got a chance to run — see Lessons.md's
// "Preflight is not strategy-aware" entry and
// mem:self_review_checklist row 13 (a well-tested fix to one function in a
// call chain does not prove the chain works if an earlier check in the same
// chain makes its own, independent, untested assumption about the same
// input).

// staticOnlyPackageJSON declares only @sveltejs/adapter-static — no jesterkit,
// no adapter-node — matching a real adapter-static project.
const staticOnlyPackageJSON = `{
	"name": "sveltekit-static",
	"dependencies": {
		"@sveltejs/kit": "^2.5.0"
	},
	"devDependencies": {
		"@sveltejs/adapter-static": "^3.0.10"
	}
}`

// staticSvelteConfig configures @sveltejs/adapter-static directly in
// svelte.config.js.
const staticSvelteConfig = `
import adapter from "@sveltejs/adapter-static";
export default { kit: { adapter: adapter() } };
`

// nodeOnlyPackageJSON declares only @sveltejs/adapter-node — no jesterkit, no
// adapter-static.
const nodeOnlyPackageJSON = `{
	"name": "sveltekit-node",
	"dependencies": {
		"@sveltejs/kit": "^2.5.0"
	},
	"devDependencies": {
		"@sveltejs/adapter-node": "^5.5.7"
	}
}`

// TestPreflight_StrategyAware_StaticAcceptsAdapterStatic is the direct
// regression test for bug 1: a real adapter-static-only project, built with
// --strategy=static, must pass Preflight. Fails against the pre-fix code
// (confirmed by temporarily reverting the strategy-aware check during
// development of this fix): the pre-fix code required jesterkit or
// adapter-node unconditionally, and this fixture has neither.
func TestPreflight_StrategyAware_StaticAcceptsAdapterStatic(t *testing.T) {
	dir := newProjectDir(t, staticOnlyPackageJSON, staticSvelteConfig)
	putFakeBunOnPath(t, `
case "$1" in
  --version) echo "1.3.14"; exit 0;;
esac
exit 1
`)
	c := NewCompiler(discardLogger())

	_, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir, Strategy: ports.StrategyStatic})
	if err != nil {
		t.Fatalf("Preflight() error = %v, want nil for a real adapter-static-only project built with --strategy=static", err)
	}
}

// TestPreflight_StrategyAware_StaticRejectsAdapterNodeOnly proves the fix
// actually discriminates by strategy rather than having been loosened into
// accepting any known adapter: an adapter-node-only project (no
// adapter-static anywhere) requested with --strategy=static must still fail,
// the same way requesting --strategy=static against a real adapter-node
// project would.
func TestPreflight_StrategyAware_StaticRejectsAdapterNodeOnly(t *testing.T) {
	dir := newProjectDir(t, nodeOnlyPackageJSON, fmt.Sprintf(svCreateSvelteConfigFmt, "@sveltejs/adapter-node"))
	c := NewCompiler(discardLogger())

	_, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir, Strategy: ports.StrategyStatic})
	if !errors.Is(err, core.ErrAdapterMissing) {
		t.Fatalf("err = %v, want wrapping core.ErrAdapterMissing (adapter-node does not satisfy --strategy=static)", err)
	}
}

// TestPreflight_StrategyAware_EmptyStrategyDefaultsToLayered proves the zero
// value behaves like DefaultBuildStrategy (layered), matching
// core.BuildRequest.Normalize()'s own default-before-Preflight behavior —
// an adapter-static-only project with no Strategy set must fail the same way
// a real layered build against it would (adapter-static's output shape isn't
// what the layered packaging path expects at all).
func TestPreflight_StrategyAware_EmptyStrategyDefaultsToLayered(t *testing.T) {
	dir := newProjectDir(t, staticOnlyPackageJSON, staticSvelteConfig)
	c := NewCompiler(discardLogger())

	_, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir})
	if !errors.Is(err, core.ErrAdapterMissing) {
		t.Fatalf("err = %v, want wrapping core.ErrAdapterMissing (empty Strategy must behave like the layered default, which this fixture does not satisfy)", err)
	}
}

func TestPreflight_AlreadyCancelled(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	c := NewCompiler(discardLogger())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Preflight(ctx, ports.PreflightRequest{ProjectDir: dir})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want wrapping context.Canceled", err)
	}
}

// --- context cancellation actually kills the child process ------------------

func TestPrepare_ContextCancellationKillsChild(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	// A fake bun whose "run build" subcommand sleeps far longer than the
	// context we give it, so the test proves cancellation - not a fast exit -
	// is what ends the call.
	putFakeBunOnPath(t, `sleep 5`)
	c := NewCompiler(discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	// StrategyExe matches validSvelteConfig's @jesterkit/exe-sveltekit: the
	// strategy is stated explicitly rather than left at the zero value, which
	// only reached this point by resolving to a different adapter than the
	// fixture configures (harmless before Prepare gated on that, now not).
	_, err := c.Prepare(ctx, ports.PrepareRequest{ProjectDir: dir, Strategy: ports.StrategyExe, SourceDateEpoch: time.Unix(0, 0)})
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want wrapping context.DeadlineExceeded", err)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("Prepare took %s to return after context deadline; child process was not killed promptly", elapsed)
	}
}

func TestCompile_ContextCancellationKillsChild(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	putFakeBunOnPath(t, `sleep 5`)
	c := NewCompiler(discardLogger())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	req := ports.CompileRequest{
		ProjectDir:      dir,
		EntrypointPath:  filepath.Join(dir, ".svelte-kit", "jesterkit-sveltekit", "temp-server", "index.ts"),
		Platform:        ports.LinuxAMD64,
		OutputPath:      filepath.Join(t.TempDir(), "app-linux-amd64"),
		SourceDateEpoch: time.Unix(0, 0),
	}

	start := time.Now()
	_, err := c.Compile(ctx, req)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want wrapping context.DeadlineExceeded", err)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("Compile took %s to return after context deadline; child process was not killed promptly", elapsed)
	}
}

// --- Compile: platform / error classification -------------------------------

func TestCompile_UnsupportedPlatform(t *testing.T) {
	c := NewCompiler(discardLogger())
	req := ports.CompileRequest{
		ProjectDir:      t.TempDir(),
		EntrypointPath:  "/does/not/matter.ts",
		Platform:        ports.Platform{OS: "windows", Arch: "amd64"},
		OutputPath:      filepath.Join(t.TempDir(), "out"),
		SourceDateEpoch: time.Unix(0, 0),
	}

	_, err := c.Compile(context.Background(), req)
	if !errors.Is(err, core.ErrUnsupportedPlatform) {
		t.Fatalf("err = %v, want wrapping core.ErrUnsupportedPlatform", err)
	}
}

func TestCompile_BunExitNonZero(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	putFakeBunOnPath(t, `echo "boom: syntax error" 1>&2; exit 1`)
	c := NewCompiler(discardLogger())

	req := ports.CompileRequest{
		ProjectDir:      dir,
		EntrypointPath:  filepath.Join(dir, ".svelte-kit", "jesterkit-sveltekit", "temp-server", "index.ts"),
		Platform:        ports.LinuxAMD64,
		OutputPath:      filepath.Join(t.TempDir(), "app-linux-amd64"),
		SourceDateEpoch: time.Unix(0, 0),
	}

	_, err := c.Compile(context.Background(), req)
	if !errors.Is(err, core.ErrCompileFailed) {
		t.Fatalf("err = %v, want wrapping core.ErrCompileFailed", err)
	}
	if !strings.Contains(err.Error(), "boom: syntax error") {
		t.Errorf("err = %v, want captured stderr in message", err)
	}
}

// TestCompile_HermeticStripsSocketBearingEnvVars mirrors
// TestPrepare_HermeticStripsSocketBearingEnvVars for the Compile stage —
// req.Hermetic strips socket-bearing env vars there too, matching Compile's
// identical hermetic-sandbox treatment.
func TestCompile_HermeticStripsSocketBearingEnvVars(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	t.Setenv("SSH_AUTH_SOCK", "/tmp/should-not-be-inherited.sock")
	putFakeBunOnPath(t, `
if [ -n "$SSH_AUTH_SOCK" ]; then
	echo "SSH_AUTH_SOCK_PRESENT" 1>&2
else
	echo "SSH_AUTH_SOCK_ABSENT" 1>&2
fi
exit 1
`)
	c := NewCompiler(discardLogger())

	req := ports.CompileRequest{
		ProjectDir:      dir,
		EntrypointPath:  filepath.Join(dir, ".svelte-kit", "jesterkit-sveltekit", "temp-server", "index.ts"),
		Platform:        ports.LinuxAMD64,
		OutputPath:      filepath.Join(t.TempDir(), "app-linux-amd64"),
		SourceDateEpoch: time.Unix(0, 0),
		Hermetic:        true,
	}

	_, err := c.Compile(context.Background(), req)
	if err == nil {
		t.Fatal("expected Compile to fail — the fake bun script always exits 1")
	}
	if strings.Contains(err.Error(), "SSH_AUTH_SOCK_PRESENT") {
		t.Fatalf("expected SSH_AUTH_SOCK to be stripped from a hermetic build's subprocess env, got: %v", err)
	}
	if strings.Contains(err.Error(), "failed to start inside the hermetic network sandbox") {
		t.Skipf("hermetic sandbox unavailable in this environment: %v", err)
	}
	if !strings.Contains(err.Error(), "SSH_AUTH_SOCK_ABSENT") {
		t.Fatalf("expected the probe result in the error, got: %v", err)
	}
}

func TestPrepare_BunExitNonZero(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	putFakeBunOnPath(t, `echo "adapter crashed" 1>&2; exit 1`)
	c := NewCompiler(discardLogger())

	// StrategyExe matches validSvelteConfig's configured adapter, so Prepare
	// reaches the bun invocation this test is about.
	_, err := c.Prepare(context.Background(), ports.PrepareRequest{ProjectDir: dir, Strategy: ports.StrategyExe, SourceDateEpoch: time.Unix(0, 0)})
	if !errors.Is(err, core.ErrPrepareFailed) {
		t.Fatalf("err = %v, want wrapping core.ErrPrepareFailed", err)
	}
	if !strings.Contains(err.Error(), "adapter crashed") {
		t.Errorf("err = %v, want captured stderr in message", err)
	}
}

// TestPrepare_HermeticStripsSocketBearingEnvVars is PR-2's residual-gap
// regression guard, platform-independent (unlike the netns sandbox tests in
// hermetic_linux_test.go): a hermetic build's subprocess must not see
// SSH_AUTH_SOCK even though the parent process has it set — see
// hermeticStrippedEnvVars's doc comment (env.go) for exactly what this does
// and doesn't close.
func TestPrepare_HermeticStripsSocketBearingEnvVars(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	t.Setenv("SSH_AUTH_SOCK", "/tmp/should-not-be-inherited.sock")
	putFakeBunOnPath(t, `
if [ -n "$SSH_AUTH_SOCK" ]; then
	echo "SSH_AUTH_SOCK_PRESENT" 1>&2
else
	echo "SSH_AUTH_SOCK_ABSENT" 1>&2
fi
exit 1
`)
	c := NewCompiler(discardLogger())

	_, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir: dir, Strategy: ports.StrategyExe, SourceDateEpoch: time.Unix(0, 0), Hermetic: true,
	})
	if err == nil {
		t.Fatal("expected Prepare to fail — the fake bun script always exits 1")
	}
	if strings.Contains(err.Error(), "SSH_AUTH_SOCK_PRESENT") {
		t.Fatalf("expected SSH_AUTH_SOCK to be stripped from a hermetic build's subprocess env, got: %v", err)
	}
	if strings.Contains(err.Error(), "failed to start inside the hermetic network sandbox") {
		t.Skipf("hermetic sandbox unavailable in this environment: %v", err)
	}
	if !strings.Contains(err.Error(), "SSH_AUTH_SOCK_ABSENT") {
		t.Fatalf("expected the probe result in the error, got: %v", err)
	}
}

// TestPrepare_NonHermeticKeepsSocketBearingEnvVars is the control for the
// test above: a non-hermetic build has no reason to strip anything, and
// this proves stripHermeticSensitiveEnv is only reached when req.Hermetic is
// actually set, not unconditionally.
func TestPrepare_NonHermeticKeepsSocketBearingEnvVars(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	t.Setenv("SSH_AUTH_SOCK", "/tmp/should-be-inherited.sock")
	putFakeBunOnPath(t, `
if [ -n "$SSH_AUTH_SOCK" ]; then
	echo "SSH_AUTH_SOCK_PRESENT" 1>&2
else
	echo "SSH_AUTH_SOCK_ABSENT" 1>&2
fi
exit 1
`)
	c := NewCompiler(discardLogger())

	_, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir: dir, Strategy: ports.StrategyExe, SourceDateEpoch: time.Unix(0, 0), Hermetic: false,
	})
	if err == nil {
		t.Fatal("expected Prepare to fail — the fake bun script always exits 1")
	}
	if !strings.Contains(err.Error(), "SSH_AUTH_SOCK_PRESENT") {
		t.Fatalf("expected a non-hermetic build to keep inheriting SSH_AUTH_SOCK, got: %v", err)
	}
}

// validAdapterNodeSvelteConfig configures @sveltejs/adapter-node, the real
// adapter StrategyLayered expects — used by the telemetry gating tests
// below, which need Prepare to reach the strategy-gated telemetry block
// rather than fail fast on adapter misconfiguration.
const validAdapterNodeSvelteConfig = `
import adapter from "@sveltejs/adapter-node";

export default {
	kit: {
		adapter: adapter()
	}
};
`

// TestPrepare_Telemetry_StrategyExeWiresWrapper is PR-5's strategy-scope
// regression guard (mem:self_review_checklist row 11): confirms the
// telemetry wrapper is only wired for StrategyExe, the one strategy whose
// PrepareResult.EntrypointPath is actually read downstream (Compile's
// ports.CompileRequest construction) — a project misconfigured with the
// wrong adapter for StrategyExe would fail before reaching this, so the
// real signal here is that a fake bun script that creates the expected exe
// output still finds a live .pokkum/telemetry-entry.ts wrapper afterward.
func TestPrepare_Telemetry_StrategyExeWiresWrapper(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	tempServerDir := filepath.Join(dir, ".svelte-kit", "jesterkit-sveltekit", "temp-server")
	entrypoint := filepath.Join(tempServerDir, "index.ts")
	assetsPath := filepath.Join(tempServerDir, assetsGeneratedFilename)
	putFakeBunOnPath(t, fmt.Sprintf(
		`mkdir -p %q && touch %q && cat > %q <<'EOF'
%s
EOF
exit 0`, tempServerDir, entrypoint, assetsPath, validAssetsGenerated))
	c := NewCompiler(discardLogger())

	res, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir: dir, Strategy: ports.StrategyExe, SourceDateEpoch: time.Unix(0, 0),
		Telemetry: ports.TelemetryOptions{Enabled: true},
	})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	wantWrapper := filepath.Join(dir, ".pokkum", "telemetry-entry.ts")
	if res.EntrypointPath != wantWrapper {
		t.Errorf("EntrypointPath = %q, want the telemetry wrapper %q", res.EntrypointPath, wantWrapper)
	}
	if _, err := os.Stat(wantWrapper); err != nil {
		t.Errorf("expected the telemetry wrapper to actually be written at %s: %v", wantWrapper, err)
	}
}

// TestPrepare_Telemetry_StrategyLayeredDoesNotWireWrapper is the other half
// of the same regression guard: StrategyLayered never calls Compile at all
// (it packages prep.OutputDir directly — see internal/core/pipeline.go), so
// a wrapper wired via EntrypointPath would be a real file nothing ever
// reads. Confirms Prepare does NOT swap EntrypointPath and does NOT write a
// wrapper for this strategy, even with telemetry enabled — silently
// generating unread output would itself be a "looks wired, isn't" bug.
func TestPrepare_Telemetry_StrategyLayeredDoesNotWireWrapper(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, validAdapterNodeSvelteConfig)
	entrypoint := filepath.Join(dir, "build", "index.js")
	// handler.js's content must match one of patchPrerenderedHandler's
	// recognized patterns (StrategyLayered's post-build step) — reusing the
	// exact fixture content TestPrepare_ZeroConfigAutoInjection_EngagesViteWrapper
	// already established as valid, above.
	putFakeBunOnPath(t, `mkdir -p build && touch build/index.js && echo 'path.join(dir, "prerendered")' > build/handler.js && exit 0`)
	c := NewCompiler(discardLogger())

	res, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir: dir, Strategy: ports.StrategyLayered, SourceDateEpoch: time.Unix(0, 0),
		Telemetry: ports.TelemetryOptions{Enabled: true},
	})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}

	if res.EntrypointPath != entrypoint {
		t.Errorf("EntrypointPath = %q, want the unwrapped real entrypoint %q (StrategyLayered telemetry is not yet supported)", res.EntrypointPath, entrypoint)
	}
	wrapperPath := filepath.Join(dir, ".pokkum", "telemetry-entry.ts")
	if _, err := os.Stat(wrapperPath); err == nil {
		t.Errorf("expected no telemetry wrapper to be written for StrategyLayered (nothing would ever read it), but found one at %s", wrapperPath)
	}
}

// TestPrepare_Telemetry_StrategyStaticIsExplicitNoOp is the third leg of the
// telemetry-wiring switch's case coverage (see the two tests above for
// StrategyExe/StrategyLayered). A static site has no server-side runtime to
// instrument, so StrategyStatic must stay a documented no-op — this pins that
// behavior with its own case (added alongside the switch's new `default`, see
// mem:self_review_checklist row 11) rather than leaving it to fall through
// silently, and proves enabling telemetry for a static build is neither an
// error nor a silently-generated-but-unread wrapper file.
func TestPrepare_Telemetry_StrategyStaticIsExplicitNoOp(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, fmt.Sprintf(svCreateSvelteConfigFmt, "@sveltejs/adapter-static"))
	putFakeBunOnPath(t, staticBuildScript(""))
	c := NewCompiler(discardLogger())

	res, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir: dir, Strategy: ports.StrategyStatic, SourceDateEpoch: time.Unix(0, 0),
		Telemetry: ports.TelemetryOptions{Enabled: true},
	})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if res.TelemetryPreloadRelPath != "" {
		t.Errorf("TelemetryPreloadRelPath = %q, want empty for StrategyStatic (nothing packages or reads a preload for a static site)", res.TelemetryPreloadRelPath)
	}
	wrapperPath := filepath.Join(dir, ".pokkum", "telemetry-entry.ts")
	if _, err := os.Stat(wrapperPath); err == nil {
		t.Errorf("expected no telemetry wrapper to be written for StrategyStatic, but found one at %s", wrapperPath)
	}
}

// TestPrepare_Telemetry_UnrecognizedStrategyErrors proves the telemetry-wiring
// switch's new `default` arm actually fires and fails closed, rather than
// silently skipping telemetry wiring the way the pre-fix code (and the
// negative-check predecessor this same switch already shipped once, per
// Lessons.md's 2026-08-18 entry) would have. req.Strategy is normally
// validated by core.BuildRequest.Validate (BuildStrategy.Valid()) before
// core.Build ever calls Prepare, so this exact input cannot occur via the
// pokkum CLI today — this test exercises bunexec.Compiler.Prepare directly
// with a value that check does not run for, to prove the switch's own
// defense-in-depth actually works rather than assuming it from reading the
// code.
func TestPrepare_Telemetry_UnrecognizedStrategyErrors(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, fmt.Sprintf(svCreateSvelteConfigFmt, "@sveltejs/adapter-node"))
	putFakeBunOnPath(t, "set -e\nmkdir -p build\nprintf 'export default {};\\n' > build/index.js\nexit 0\n")
	c := NewCompiler(discardLogger())

	_, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir: dir, Strategy: ports.BuildStrategy("bogus-strategy"), SourceDateEpoch: time.Unix(0, 0),
		Telemetry: ports.TelemetryOptions{Enabled: true},
	})
	if err == nil {
		t.Fatal("Prepare succeeded, want an error for an unrecognized build strategy reaching the telemetry-wiring switch")
	}
	if !strings.Contains(err.Error(), "unrecognized build strategy") {
		t.Errorf("error = %v, want it to name the unrecognized build strategy", err)
	}
	if !errors.Is(err, core.ErrPrepareFailed) {
		t.Errorf("error = %v, want it to wrap core.ErrPrepareFailed", err)
	}
}

func TestPrepare_MissingEntrypointAfterSuccessfulBuild(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	// bun exits 0 but never actually wrote .svelte-kit/jesterkit-sveltekit/...
	putFakeBunOnPath(t, `exit 0`)
	c := NewCompiler(discardLogger())

	// StrategyExe matches validSvelteConfig's configured adapter, so the
	// failure under test is the missing entrypoint and nothing earlier.
	_, err := c.Prepare(context.Background(), ports.PrepareRequest{ProjectDir: dir, Strategy: ports.StrategyExe, SourceDateEpoch: time.Unix(0, 0)})
	if !errors.Is(err, core.ErrPrepareFailed) {
		t.Fatalf("err = %v, want wrapping core.ErrPrepareFailed", err)
	}
}

// --- Prepare: checkEffectiveAdapter fail-fast (Option C) --------------------
//
// These prove Prepare rejects a misconfigured project before doing any real
// work — no subprocess spawned, no .pokkum/ written — for both real,
// confirmed failure shapes: (1) svelte.config.js governs and doesn't name the
// strategy's adapter, and (2) vite.config.ts governs (it passes options to
// its sveltekit() plugin call) and doesn't name it, regardless of what
// svelte.config.js says. See sveltekitutils.EffectiveAdapterConfigured and
// its tests for the underlying detector; these exercise it wired into Prepare.

// realSvCreateDefaultViteConfigTS is vite.config.ts exactly as emitted by
// `bunx sv create --template minimal --types ts --no-add-ons` (sv@0.17.0,
// @sveltejs/kit@2.63.0 range), captured 2026-08-17 — the totally
// unconfigured default scaffold: no svelte.config.js at all, adapter is
// @sveltejs/adapter-auto. See sveltekitutils/project_test.go's
// realSvCreateDefaultViteConfig for the same fixture with fuller provenance
// notes; duplicated here (rather than exported) since the two packages don't
// share test-only code.
const realSvCreateDefaultViteConfigTS = `import adapter from '@sveltejs/adapter-auto';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [
		sveltekit({
			adapter: adapter()
		})
	]
});
`

func TestPrepare_FailsFastWhenAdapterNotConfigured(t *testing.T) {
	// With NoInject: true, svelte.config.js real-shaped but naming the wrong adapter
	// fails fast with core.ErrAdapterMisconfigured before subprocess or sandbox write.
	wrongSvelteConfig := `import adapter from '@sveltejs/adapter-auto';

export default {
	kit: {
		adapter: adapter()
	}
};
`
	dir := newProjectDir(t, validPackageJSON, wrongSvelteConfig)
	bunSentinel := filepath.Join(t.TempDir(), "bun-was-invoked")
	putFakeBunOnPath(t, `touch `+bunSentinel+`; exit 0`)
	c := NewCompiler(discardLogger())

	_, err := c.Prepare(context.Background(), ports.PrepareRequest{ProjectDir: dir, Strategy: ports.StrategyLayered, NoInject: true, SourceDateEpoch: time.Unix(0, 0)})
	if !errors.Is(err, core.ErrAdapterMisconfigured) {
		t.Fatalf("err = %v, want wrapping core.ErrAdapterMisconfigured", err)
	}
	if !strings.Contains(err.Error(), "@sveltejs/adapter-node") {
		t.Errorf("err = %v, want it to name the missing target adapter", err)
	}
	if !strings.Contains(err.Error(), "svelte.config.js") {
		t.Errorf("err = %v, want it to name the file to fix", err)
	}
	if _, statErr := os.Stat(bunSentinel); statErr == nil {
		t.Error("bun was invoked; checkEffectiveAdapter must fail before any subprocess is spawned")
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".pokkum")); statErr == nil {
		t.Error(".pokkum/ was written; checkEffectiveAdapter must fail before PrepareVirtualConfig runs")
	}
}

func TestPrepare_FailsFastWhenViteConfigOverridesAdapter(t *testing.T) {
	// With NoInject: true, vite.config.ts configuring @sveltejs/adapter-auto
	// fails fast with core.ErrAdapterMisconfigured before subprocess or sandbox write.
	dir := newProjectDirWithVite(t, validPackageJSON, "", realSvCreateDefaultViteConfigTS)
	bunSentinel := filepath.Join(t.TempDir(), "bun-was-invoked")
	putFakeBunOnPath(t, `touch `+bunSentinel+`; exit 0`)
	c := NewCompiler(discardLogger())

	_, err := c.Prepare(context.Background(), ports.PrepareRequest{ProjectDir: dir, Strategy: ports.StrategyLayered, NoInject: true, SourceDateEpoch: time.Unix(0, 0)})
	if !errors.Is(err, core.ErrAdapterMisconfigured) {
		t.Fatalf("err = %v, want wrapping core.ErrAdapterMisconfigured", err)
	}
	if !strings.Contains(err.Error(), "vite.config.ts") {
		t.Errorf("err = %v, want it to name vite.config.ts as the file that governs", err)
	}
	if !strings.Contains(err.Error(), "ignored") {
		t.Errorf("err = %v, want it to explain svelte.config.js is ignored here", err)
	}
	if _, statErr := os.Stat(bunSentinel); statErr == nil {
		t.Error("bun was invoked; checkEffectiveAdapter must fail before any subprocess is spawned")
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".pokkum")); statErr == nil {
		t.Error(".pokkum/ was written; checkEffectiveAdapter must fail before PrepareVirtualConfig runs")
	}
}

func TestPrepare_ZeroConfigAutoInjection_EngagesViteWrapper(t *testing.T) {
	// Option B: when NoInject is false, the project's build script is exactly
	// "vite build" (checkEffectiveAdapter's guard requires this — see
	// viteBuildPackageJSON), and vite.config.ts has @sveltejs/adapter-auto,
	// Prepare generates .pokkum/vite.config.ts with @sveltejs/adapter-node
	// and invokes vite build with --config.
	dir := newProjectDirWithVite(t, viteBuildPackageJSON, "", realSvCreateDefaultViteConfigTS)
	argsSentinel := filepath.Join(t.TempDir(), "bun-args.txt")
	putFakeBunOnPath(t, `echo "$@" > `+argsSentinel+`; mkdir -p build; touch build/index.js; echo 'path.join(dir, "prerendered")' > build/handler.js; exit 0`)
	c := NewCompiler(discardLogger())

	res, err := c.Prepare(context.Background(), ports.PrepareRequest{ProjectDir: dir, Strategy: ports.StrategyLayered, SourceDateEpoch: time.Unix(0, 0)})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if res.EntrypointPath == "" {
		t.Errorf("expected non-empty EntrypointPath")
	}

	virtualConfigPath := filepath.Join(dir, ".pokkum", "vite.config.ts")
	if _, err := os.Stat(virtualConfigPath); os.IsNotExist(err) {
		t.Fatalf("expected virtual vite.config.ts at %s", virtualConfigPath)
	}

	cfgBytes, err := os.ReadFile(virtualConfigPath)
	if err != nil {
		t.Fatalf("failed to read virtual config: %v", err)
	}
	if !strings.Contains(string(cfgBytes), "@sveltejs/adapter-node") {
		t.Errorf("expected virtual config to configure @sveltejs/adapter-node, got:\n%s", string(cfgBytes))
	}

	argsData, err := os.ReadFile(argsSentinel)
	if err != nil {
		t.Fatalf("failed to read captured bun args: %v", err)
	}
	argsStr := string(argsData)
	if !strings.Contains(argsStr, "--config") || !strings.Contains(argsStr, "vite") {
		t.Errorf("expected bun to invoke vite with --config, got argv: %s", argsStr)
	}
}

// TestPrepare_ViteConfigOverrideWithAdapterPresent_Succeeds proves the
// override check is not merely "vite.config.ts exists => fail": when the Vite
// config does reference the target adapter, Prepare proceeds past
// checkEffectiveAdapter and reaches the real bun invocation, using the real
// captured adapter-node scaffold shape.
func TestPrepare_ViteConfigOverrideWithAdapterPresent_Succeeds(t *testing.T) {
	realAdapterNodeViteConfig := `import adapter from '@sveltejs/adapter-node';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [
		sveltekit({
			adapter: adapter()
		})
	]
});
`
	dir := newProjectDirWithVite(t, validPackageJSON, "", realAdapterNodeViteConfig)
	bunSentinel := filepath.Join(t.TempDir(), "bun-was-invoked")
	putFakeBunOnPath(t, `touch `+bunSentinel+`; exit 0`)
	c := NewCompiler(discardLogger())

	// bun exits 0 without producing build/index.js, so Prepare still fails —
	// but past checkEffectiveAdapter, at the missing-entrypoint check. That's
	// the point: this proves the adapter check let a correctly-configured
	// project through rather than blocking on vite.config.ts's mere presence.
	_, err := c.Prepare(context.Background(), ports.PrepareRequest{ProjectDir: dir, Strategy: ports.StrategyLayered, SourceDateEpoch: time.Unix(0, 0)})
	if errors.Is(err, core.ErrAdapterMisconfigured) {
		t.Fatalf("err = %v, checkEffectiveAdapter should have passed for a real adapter-node vite.config.ts", err)
	}
	if !errors.Is(err, core.ErrPrepareFailed) {
		t.Fatalf("err = %v, want wrapping core.ErrPrepareFailed (missing entrypoint)", err)
	}
	if _, statErr := os.Stat(bunSentinel); statErr != nil {
		t.Error("bun was never invoked; checkEffectiveAdapter should have passed and let Prepare reach the build")
	}
}

func TestPreflight_HermeticViolation(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	putFakeBunOnPath(t, `echo "1.2.18"`)
	c := NewCompiler(discardLogger())

	// Without node_modules and Hermetic=true -> fails
	_, err := c.Preflight(context.Background(), ports.PreflightRequest{
		ProjectDir: dir,
		Hermetic:   true,
	})
	if !errors.Is(err, core.ErrHermeticViolation) {
		t.Fatalf("expected ErrHermeticViolation when node_modules is missing, got %v", err)
	}

	// Create node_modules -> passes. Strategy is stated explicitly
	// (StrategyExe, matching validSvelteConfig's @jesterkit/exe-sveltekit)
	// now that the adapter check is strategy-aware.
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	_, err = c.Preflight(context.Background(), ports.PreflightRequest{
		ProjectDir: dir,
		Hermetic:   true,
		Strategy:   ports.StrategyExe,
	})
	if err != nil {
		t.Fatalf("expected Preflight with node_modules present to pass, got %v", err)
	}
}
