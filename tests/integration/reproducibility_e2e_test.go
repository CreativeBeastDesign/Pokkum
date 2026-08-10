package integration

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunexec"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registry"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestRealBuildIsReproducibleAcrossRuns is the one test in this package that
// actually exercises the toolchain Pokkum's byte-reproducibility promise
// depends on: it runs `bun run build` and `bun build --compile` against the
// real testdata/fixtures/sveltekit-basic project, through the real
// internal/adapters/bunexec.Compiler and internal/adapters/packager.Packager,
// several times in a row, and asserts the resulting multi-platform image
// index digest never changes.
//
// This is deliberately not what TestPackagingIsDeterministicAcrossRuns and
// TestMultiArchIndexPackagingIsDeterministic (reproducibility_test.go) check:
// those drive the packager directly against a fixed synthetic artifact and
// never invoke bun, so they cannot see nondeterminism introduced upstream, in
// the SvelteKit build or the Bun compile step. There was exactly such a bug:
// @jesterkit/exe-sveltekit's generated assets.generated.ts lists static
// assets in whatever order its filesystem walk happened to yield, which is
// not stable across otherwise-identical builds, so two runs of the identical
// project could compile to two different binaries even with
// SOURCE_DATE_EPOCH held fixed. See internal/adapters/bunexec/assets.go
// (normalizeGeneratedAssetsFile, called from Compiler.Prepare) for the fix
// and internal/adapters/bunexec/assets_test.go for unit coverage of the sort
// itself; this test is the guarantee that the fix holds at the level the
// product actually promises it at.
//
// BaseImages and the supervisor provider are still faked, as in
// TestFixtureDrivenE2E: resolving a real base image needs network access to
// a registry, and neither port was ever implicated in the bug above, so
// faking them keeps this test hermetic and focused on the toolchain step
// that regressed. SBOM generation is disabled for the same reason — it does
// not affect the compiled artifact or the image index, and would only add
// scan time to every one of the five runs.
//
// This performs 5 real bun builds of the fixture (roughly 24s each on the
// machine this was measured on, so several minutes total). It is skipped
// under -short, and skipped with a clear message when bun is not on PATH, so
// it never burdens `make test-short` or a bun-less environment.
func TestRealBuildIsReproducibleAcrossRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real bun build reproducibility check in short mode")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not found on PATH; skipping real build reproducibility check")
	}

	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "sveltekit-basic"))
	if err != nil {
		t.Fatalf("Abs fixture path: %v", err)
	}

	// The bug this test guards against was intermittent — 2 of 5 consecutive
	// `bun run build` runs diverged when it was measured — so fewer than 5
	// runs risks a false pass by luck.
	const runs = 5
	digests := make([]string, 0, runs)

	for i := 0; i < runs; i++ {
		deps := core.Deps{
			Compiler:   bunexec.NewCompiler(testLogger()),
			BaseImages: &mockBaseResolver{t: t},
			Supervisor: &mockSupervisorProvider{},
			Packager:   packager.NewPackager(testLogger()),
			Tarballs:   registry.NewAdapter(testLogger()),
			Logger:     testLogger(),
			Version:    "0.1.0-repro-test",
		}

		req := core.BuildRequest{
			ProjectDir: fixtureDir,
			Platforms:  []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64},
			Output: core.OutputOptions{
				Mode:        core.OutputTarball,
				TarballPath: filepath.Join(t.TempDir(), "image.tar"),
			},
			SBOM:            core.SBOMOptions{Format: ports.SBOMFormatNone},
			SourceDateEpoch: testEpoch,
		}

		res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
		if err != nil {
			t.Fatalf("run %d/%d: core.Build failed: %v", i+1, runs, err)
		}
		if !res.Image.IsIndex {
			t.Fatalf("run %d/%d: expected a multi-platform image index", i+1, runs)
		}
		digest := res.Image.Digest.String()
		if digest == "" {
			t.Fatalf("run %d/%d: image index digest is empty", i+1, runs)
		}
		t.Logf("run %d/%d: index digest = %s", i+1, runs, digest)
		digests = append(digests, digest)
	}

	for i := 1; i < len(digests); i++ {
		if digests[i] != digests[0] {
			t.Errorf("image index digest is not reproducible: run 1 = %s, run %d = %s\nall digests: %v",
				digests[0], i+1, digests[i], digests)
		}
	}
}
