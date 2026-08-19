package bunexec_test

import (
	"context"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunexec"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// liveFixtureSkipDirNames are top-level entries copyLiveFixtureProject never
// copies: node_modules is symlinked back to the original instead (real,
// installed dependencies — copying them would make this already-opt-in live
// smoke test slow for no benefit), and .git/dist/build/.pokkum are build
// tool/VCS cruft irrelevant to this test. Unlike
// tests/integration/harness_test.go's copyFixtureProject, this deliberately
// does NOT skip .svelte-kit: this test's whole precondition is that the
// fixture has already been prepared (see TestLiveFixture_PreflightAndCompile's
// doc comment below), so the copy must carry the source fixture's own
// pre-generated .svelte-kit/jesterkit-sveltekit/temp-server/index.ts, not
// start fresh without it.
var liveFixtureSkipDirNames = map[string]bool{
	"node_modules": true,
	".git":         true,
	"dist":         true,
	"build":        true,
	".pokkum":      true,
}

// copyLiveFixtureProject copies fixtureDir's real source tree — including
// its currently-generated .svelte-kit output, if any — into a fresh
// t.TempDir(), symlinking node_modules back to the original, and returns the
// copy's path. This is a small, deliberately duplicated variant of
// tests/integration/harness_test.go's copyFixtureProject: bunexec's tests
// live in packages bunexec/bunexec_test, which cannot import a helper from
// tests/integration (a separate module-internal package with its own
// dependency graph and no shared import path for test helpers), and
// mem:self_review_checklist's guidance for this refactor was to prefer a
// tiny duplicated per-package helper over inventing a new shared production
// package just to host test-only code. See this file's
// TestLiveFixture_PreflightAndCompile for why an isolated copy is needed at
// all: building against the checked-out fixture directly made this test's
// pass/fail depend on what other tests or runs left on disk in
// testdata/fixtures/sveltekit-basic (Lessons.md's 2026-08-19
// shared-fixture-mutation entry; this test is incident #1 there).
func copyLiveFixtureProject(t *testing.T, fixtureDir string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("copyLiveFixtureProject: mkdir %q: %v", dst, err)
	}

	walkErr := filepath.WalkDir(fixtureDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(fixtureDir, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if liveFixtureSkipDirNames[d.Name()] {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			// The fixture is not expected to contain symlinks of its own
			// outside node_modules (skipped above); skip anything unexpected
			// rather than mishandling it.
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if walkErr != nil {
		t.Fatalf("copyLiveFixtureProject: copy %q to %q: %v", fixtureDir, dst, walkErr)
	}

	srcNodeModules := filepath.Join(fixtureDir, "node_modules")
	if _, statErr := os.Stat(srcNodeModules); statErr == nil {
		if err := os.Symlink(srcNodeModules, filepath.Join(dst, "node_modules")); err != nil {
			t.Fatalf("copyLiveFixtureProject: symlink node_modules: %v", err)
		}
	}
	return dst
}

// TestLiveFixture_PreflightAndCompile is a real, non-mocked smoke test: it
// shells out to an actual bun on PATH against a scratch copy of
// testdata/fixtures/sveltekit-basic, a real SvelteKit project wired with
// @jesterkit/exe-sveltekit. It intentionally skips (not fails) when the
// fixture or its already-generated .svelte-kit/jesterkit-sveltekit output is
// absent, since that fixture is maintained outside this package and a build
// running before it exists — or without a "bun install" having been run in
// it — must not be treated as a bunexec failure.
//
// It calls Preflight and Compile only, not Prepare: Prepare is documented as
// unsafe for concurrent use against a given ProjectDir, and the fixture may
// be shared with, or already prepared by, other tooling. Re-running `bun run
// build` here would race with that. If the fixture's temp-server/index.ts is
// missing, this test skips rather than trying to generate it itself.
//
// Preflight/Compile run against copyLiveFixtureProject's scratch copy of the
// fixture, not the checked-out fixture directory itself: this test used to
// build in place, and failed once in a full-suite run before passing in
// isolation and 3/3 on re-run (see Lessons.md's 2026-08-19 entry) — an
// observation with a mechanism (this test's `bun build --compile` runs with
// cwd set to a directory other real-build tests/packages can be concurrently
// reading or writing), not a confirmed root cause, but isolating this test's
// own ProjectDir removes the shared mutable state regardless of the exact
// mechanism.
func TestLiveFixture_PreflightAndCompile(t *testing.T) {
	origProjectDir, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "fixtures", "sveltekit-basic"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(origProjectDir, "package.json")); err != nil {
		t.Skip("testdata/fixtures/sveltekit-basic not present; skipping live smoke test")
	}
	if _, err := os.Stat(filepath.Join(origProjectDir, ".svelte-kit", "jesterkit-sveltekit", "temp-server", "index.ts")); err != nil {
		t.Skip("fixture has not been prepared (no temp-server/index.ts yet); skipping live smoke test")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("no bun on PATH; skipping live smoke test")
	}

	projectDir := copyLiveFixtureProject(t, origProjectDir)
	entrypoint := filepath.Join(projectDir, ".svelte-kit", "jesterkit-sveltekit", "temp-server", "index.ts")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	c := bunexec.NewCompiler(logger)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Strategy is stated explicitly (StrategyExe, matching this fixture's
	// @jesterkit/exe-sveltekit wiring per this test's doc comment) now that
	// Preflight's adapter check is strategy-aware.
	pf, err := c.Preflight(ctx, ports.PreflightRequest{ProjectDir: projectDir, Strategy: ports.StrategyExe})
	if err != nil {
		t.Fatalf("Preflight against live fixture failed: %v", err)
	}
	if pf.BunVersion == "" {
		t.Error("Preflight returned empty BunVersion")
	}
	t.Logf("preflight: bunPath=%s bunVersion=%s adapterVersion=%s svelteKitVersion=%s",
		pf.BunPath, pf.BunVersion, pf.AdapterVersion, pf.SvelteKitVersion)

	outDir := t.TempDir()
	for _, platform := range []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64} {
		target, _ := platform.BunTarget()
		outPath := filepath.Join(outDir, "app-"+target)

		artifact, err := c.Compile(ctx, ports.CompileRequest{
			ProjectDir:      projectDir,
			EntrypointPath:  entrypoint,
			Platform:        platform,
			OutputPath:      outPath,
			SourceDateEpoch: time.Unix(1700000000, 0),
			Minify:          true,
		})
		if err != nil {
			t.Fatalf("Compile(%s) against live fixture failed: %v", platform, err)
		}
		if artifact.SHA256 == "" || artifact.Size == 0 {
			t.Errorf("Compile(%s) returned an incomplete Artifact: %+v", platform, artifact)
		}
		if artifact.Path != outPath {
			t.Errorf("Compile(%s) Path = %q, want %q", platform, artifact.Path, outPath)
		}
		t.Logf("compiled %s: target=%s path=%s size=%d sha256=%s", platform, target, artifact.Path, artifact.Size, artifact.SHA256)
	}
}
