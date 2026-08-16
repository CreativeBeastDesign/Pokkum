package bunexec

import (
	"context"
	"errors"
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

	_, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir})
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

	_, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPreflight_BunNotFound(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
	putNoBunOnPath(t)
	c := NewCompiler(discardLogger())

	_, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir})
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

	_, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir})
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

	_, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir, MinBunVersion: "1.4.0"})
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

	result, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir})
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
	// svelte.config.js real-shaped but naming the wrong adapter for
	// StrategyLayered (@sveltejs/adapter-node); no vite.config.ts, so
	// svelte.config.js governs and simply doesn't configure what's needed.
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

	_, err := c.Prepare(context.Background(), ports.PrepareRequest{ProjectDir: dir, Strategy: ports.StrategyLayered, SourceDateEpoch: time.Unix(0, 0)})
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
	// The compounding bug: svelte.config.js is entirely absent (current
	// `sv create` scaffolds don't generate one), and the real vite.config.ts
	// that governs configures @sveltejs/adapter-auto, not the
	// StrategyLayered target @sveltejs/adapter-node.
	dir := newProjectDirWithVite(t, validPackageJSON, "", realSvCreateDefaultViteConfigTS)
	bunSentinel := filepath.Join(t.TempDir(), "bun-was-invoked")
	putFakeBunOnPath(t, `touch `+bunSentinel+`; exit 0`)
	c := NewCompiler(discardLogger())

	_, err := c.Prepare(context.Background(), ports.PrepareRequest{ProjectDir: dir, Strategy: ports.StrategyLayered, SourceDateEpoch: time.Unix(0, 0)})
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

	// Create node_modules -> passes
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	_, err = c.Preflight(context.Background(), ports.PreflightRequest{
		ProjectDir: dir,
		Hermetic:   true,
	})
	if err != nil {
		t.Fatalf("expected Preflight with node_modules present to pass, got %v", err)
	}
}
