package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunexec"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunruntime"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/nativeinspect"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registry"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestRealBuild_StrategyLayered_SvelteKit3 is the first test in this repo to
// drive the real bunexec.Compiler against a SvelteKit 3 project
// (testdata/fixtures/sveltekit-kit3 — see its README for exact pinned
// versions and the SvelteKit 3 breaking changes it depends on) rather than
// only via ad-hoc manual runs. Everything else in this package targets
// SvelteKit 2 fixtures (sveltekit-basic, sveltekit-adapter-node).
//
// The fixture's adapter is already correctly configured in vite.config.ts
// (SvelteKit 3 has no svelte.config.js at all — it is a hard error if one is
// present), so bunexec.Compiler.Prepare takes the
// PrepareVirtualViteConfigPassthrough path, not the PrepareVirtualViteConfig
// injection path — this is the first fixture in the repo to exercise
// Passthrough with a real build assertion; every existing vite.config.ts
// fixture (sveltekit-basic) instead configures its adapter via
// svelte.config.js and takes the injection path's svelte.config.js branch.
// See the fixture's README for a currently-uncovered gap this surfaced:
// Passthrough never pins kit.version.name, unlike the injection path's
// injectViteVersionPin — this fixture's own vite.config.ts pins it directly
// to stay reproducible, working around the gap rather than proving it fixed.
//
// This test also exercises kit.experimental.remoteFunctions: true end to
// end through a real build: the fixture's three .remote.ts files (query,
// command, prerender, form) must survive real Vite/Rollup bundling and land
// in the packaged image under app/server/, which is asserted below by
// looking for the real, content-hashed *.remote-*.js chunk names Vite
// actually produces (verified empirically against a real build's output
// before being hardcoded here — see the fixture's README).
//
// Skipped under -short and when bun isn't on PATH or the fixture's
// dependencies aren't installed, matching every other real-bun test in this
// package.
func TestRealBuild_StrategyLayered_SvelteKit3(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real bun SvelteKit 3 build in short mode")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not found on PATH; skipping real SvelteKit 3 build")
	}

	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "sveltekit-kit3"))
	if err != nil {
		t.Fatalf("Abs fixture path: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(fixtureDir, "node_modules")); statErr != nil {
		t.Skipf("fixture dependencies not installed (run `bun install` in %s): %v", fixtureDir, statErr)
	}

	// Isolated scratch copy, per mem:self_review_checklist and Lessons.md's
	// shared-fixture-mutation entry — a real build must never write into the
	// checked-out fixture directly.
	projectDir := copyFixtureProject(t, fixtureDir)

	tarballPath := filepath.Join(t.TempDir(), "image.tar")
	deps := core.Deps{
		Compiler:        bunexec.NewCompiler(testLogger()),
		BaseImages:      &mockBaseResolver{t: t},
		Supervisor:      &mockSupervisorProvider{},
		Packager:        packager.NewPackager(testLogger()),
		BunRuntime:      bunruntime.NewResolver("", nil),
		Tarballs:        registry.NewAdapter(testLogger()),
		NativeInspector: nativeinspect.NewClosuredAdapter(),
		Logger:          testLogger(),
		Version:         "0.1.0-sveltekit3-test",
	}

	req := core.BuildRequest{
		ProjectDir: projectDir,
		Platforms:  []ports.Platform{ports.LinuxAMD64},
		Compile:    core.CompileOptions{Strategy: core.StrategyLayered},
		Output: core.OutputOptions{
			Mode:        core.OutputTarball,
			TarballPath: tarballPath,
		},
		SBOM:            core.SBOMOptions{Format: ports.SBOMFormatNone},
		SourceDateEpoch: testEpoch,
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "no such file or directory") && strings.Contains(err.Error(), "node_modules") {
			t.Skipf("fixture dependencies not installed (run `bun install` in %s): %v", fixtureDir, err)
		}
		if strings.Contains(err.Error(), "bunruntime:") && strings.Contains(err.Error(), "checksum mismatch") {
			// Unrelated, pre-existing issue: a pinned embedded bun runtime
			// checksum no longer matching what bun.sh serves, independent of
			// this test's actual regression guard (SvelteKit 3 + real
			// adapter-node@6.x + remote functions building end to end). See
			// the identical skip in layered_prerendered_e2e_test.go.
			t.Skipf("bun runtime checksum pin appears stale, unrelated to this test's regression guard: %v", err)
		}
		t.Fatalf("core.Build failed for a real SvelteKit 3 project (kit@3.0.0-next.25, adapter-node@6.0.0-next.10, remote functions) with --strategy=layered: %v", err)
	}
	if res.Image.IsIndex {
		t.Fatalf("single-platform request produced an index; want a single manifest")
	}

	img, err := tarball.ImageFromPath(tarballPath, nil)
	if err != nil {
		t.Fatalf("tarball.ImageFromPath: %v", err)
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("Layers: %v", err)
	}

	allNames := map[string]bool{}
	var entrypointFound bool
	var remoteChunkFound bool
	var remoteManifestFound bool
	for _, layer := range layers {
		for name, content := range tarEntries(t, layer) {
			allNames[name] = true
			if name == "app/server/index.js" {
				entrypointFound = true
			}
			// Vite names bundled remote-function chunks after their source
			// file with a content hash suffix, e.g.
			// "server/chunks/counter.remote-sKAMakyE.js" — verified against
			// this fixture's own real build output before being hardcoded
			// here (see the fixture's README).
			if strings.HasPrefix(name, "app/server/") && strings.Contains(name, ".remote-") {
				remoteChunkFound = true
			}
			if strings.HasPrefix(name, "app/server/") && strings.Contains(content, "remotes: {") {
				remoteManifestFound = true
			}
		}
	}

	if !entrypointFound {
		t.Errorf("app/server/index.js not found in any layer; all names: %v", sortedKeys(allNames))
	}
	if !remoteChunkFound {
		t.Error("no app/server/ file matches a bundled *.remote-<hash>.js chunk name; kit.experimental.remoteFunctions did not survive the real build")
	}
	if !remoteManifestFound {
		t.Error("no app/server/ file contains a \"remotes: {\" manifest; the remote-function registry did not survive packaging")
	}
}
