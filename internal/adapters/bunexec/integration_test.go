package bunexec_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunexec"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestLiveFixture_PreflightAndCompile is a real, non-mocked smoke test: it
// shells out to an actual bun on PATH against
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
func TestLiveFixture_PreflightAndCompile(t *testing.T) {
	projectDir, err := filepath.Abs(filepath.Join("..", "..", "..", "testdata", "fixtures", "sveltekit-basic"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, "package.json")); err != nil {
		t.Skip("testdata/fixtures/sveltekit-basic not present; skipping live smoke test")
	}
	entrypoint := filepath.Join(projectDir, ".svelte-kit", "jesterkit-sveltekit", "temp-server", "index.ts")
	if _, err := os.Stat(entrypoint); err != nil {
		t.Skip("fixture has not been prepared (no temp-server/index.ts yet); skipping live smoke test")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("no bun on PATH; skipping live smoke test")
	}

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
