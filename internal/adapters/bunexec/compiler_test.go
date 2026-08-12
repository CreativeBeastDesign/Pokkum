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

func TestPreflight_MissingSvelteConfig(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, "")
	c := NewCompiler(discardLogger())

	_, err := c.Preflight(context.Background(), ports.PreflightRequest{ProjectDir: dir})
	if !errors.Is(err, core.ErrProjectNotFound) {
		t.Fatalf("err = %v, want wrapping core.ErrProjectNotFound", err)
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
	_, err := c.Prepare(ctx, ports.PrepareRequest{ProjectDir: dir, SourceDateEpoch: time.Unix(0, 0)})
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

	_, err := c.Prepare(context.Background(), ports.PrepareRequest{ProjectDir: dir, SourceDateEpoch: time.Unix(0, 0)})
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

	_, err := c.Prepare(context.Background(), ports.PrepareRequest{ProjectDir: dir, SourceDateEpoch: time.Unix(0, 0)})
	if !errors.Is(err, core.ErrPrepareFailed) {
		t.Fatalf("err = %v, want wrapping core.ErrPrepareFailed", err)
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

