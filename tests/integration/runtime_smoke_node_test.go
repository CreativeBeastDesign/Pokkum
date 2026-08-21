package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/baseimage"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunexec"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/nativeinspect"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registry"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sigstore"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/supervisor"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestRuntimeSmoke_NodeRuntime_BootsAndServes is
// TestRuntimeSmoke_LayeredStrategy_BootsAndServes's --runtime=node
// counterpart, closing the exact gap
// TestRuntimeSmoke_LayeredStrategy_BootsAndServes closed for the default
// --runtime=bun layered image. --runtime=node (commit f5229c3, Roadmap Tier
// 1.3) was proven exactly once, manually: a real build of
// testdata/fixtures/sveltekit-adapter-node was loaded into Docker,
// pokkum-init verified the startup attestation over 79 files, exec'd
// /nodejs/bin/node /app/server/index.js, and adapter-node served / and the
// probes with 200. That proof never made it into the test suite —
// runtime_smoke_test.go's two existing smoke tests cover --strategy=layered
// with the default Bun runtime and --strategy=static, but nothing boots a
// --runtime=node image, so the manual result could regress silently. This
// test is the automated, permanent version of that manual step.
//
// It drives the real pipeline end to end exactly like
// TestRuntimeSmoke_LayeredStrategy_BootsAndServes (real bun build, real
// bunexec.Compiler, real packager, the real embedded pokkum-init supervisor),
// with two differences: req.AppRuntime is core.RuntimeNode, and deps.BunRuntime
// is deliberately left nil (mirroring TestFixtureDrivenE2E_Static's identical
// omission for a strategy/runtime combination that never dereferences it —
// see internal/core/pipeline.go's fan-out, which only resolves
// deps.BunRuntime when req.AppRuntime == ports.RuntimeBun). Leaving
// BaseImage.Preset/Ref unset lets core.Build's own Normalize() default them to
// BaseImageDistrolessNode (internal/core/model.go), the real preset a plain
// `pokkum build --runtime=node` resolves to — gcr.io/distroless/nodejs24-debian12,
// same registry host the layered test already network-gates on.
//
// What this test asserts, beyond what the layered test already covers:
//
//  1. assertNodeRuntimeImage inspects the produced OCI tarball directly (via
//     tarball.ImageFromPath, the same approach layered_prerendered_e2e_test.go
//     and static_e2e_test.go use) and proves the entrypoint really is
//     ports.NodeBinaryPath — read from ports.DefaultLayeredNodeEntrypoint()
//     rather than hardcoded — and that dev.pokkum.runtime is stamped "node".
//  2. The load-bearing "it really is node" signal: no layer carries a Bun
//     runtime binary. internal/core/pipeline.go's imageLabels stamps
//     dev.pokkum.runtime ONLY for a non-default runtime, and
//     internal/adapters/packager/packager.go's layered branch appends the
//     Bun-runtime layer addendum ONLY when effectiveAppRuntime(req.AppRuntime)
//     == ports.RuntimeBun — so a node image's absence of both the label-default
//     and the Bun layer is meaningful, not an oversight. Checked two ways:
//     no image-config History entry's CreatedBy mentions ports.BunBinaryPath
//     (the packager's own addendum comment string), and no layer's actual tar
//     members contain that path — mirroring TestFixtureDrivenE2E_Static's
//     identical History-string-plus-real-tar-member double check for its own
//     no-Bun/no-supervisor assertion.
//  3. The app actually serves: reusing runContainerAndAssertServes verbatim,
//     since it already asserts exactly what's needed here (pokkum-init's real
//     /healthz and /readyz, and the SvelteKit app's own homepage content) and
//     this test uses the identical fixture
//     (testdata/fixtures/sveltekit-adapter-node) the layered test already
//     proved that assertion's homepage-text expectation against.
//
// Gating mirrors TestRuntimeSmoke_LayeredStrategy_BootsAndServes exactly
// (testing.Short, bun on PATH, fixture deps installed, a reachable container
// runtime, network path to gcr.io:443 for the real base image pull) — no new
// gate is needed since --runtime=node resolves the same fixture and the same
// base registry host.
func TestRuntimeSmoke_NodeRuntime_BootsAndServes(t *testing.T) {
	if testing.Short() {
		smokeGateSkipf(t, "skipping real bun+docker runtime smoke test in short mode")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		smokeGateSkipf(t, "bun not found on PATH: %v", err)
	}

	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "sveltekit-adapter-node"))
	if err != nil {
		t.Fatalf("Abs fixture path: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(fixtureDir, "node_modules")); statErr != nil {
		smokeGateSkipf(t, "fixture dependencies not installed (run `bun install` in %s): %v", fixtureDir, statErr)
	}

	runtimeBin, ok := requireContainerRuntime(t)
	if !ok {
		return // requireContainerRuntime already called smokeGateSkipf.
	}

	requireNetworkPathTo(t, "gcr.io:443")

	projectDir := copyFixtureProject(t, fixtureDir)
	// A --runtime=node image resolves bare imports straight off the packaged
	// tree with no bundler in front of it, so /app/node_modules matters here at
	// least as much as it does for the Bun image — and the fixture ships no
	// production dependencies of its own. See
	// runtime_smoke_nodemodules_test.go.

	tarballPath := filepath.Join(t.TempDir(), "runtime-smoke-node.tar")
	repo := "pokkum.local/runtime-smoke-node"
	tag := uniqueName("smoke")
	imageRef := repo + ":" + tag

	logger := testLogger()
	deps := core.Deps{
		Compiler: bunexec.NewCompiler(logger),
		BaseImages: baseimage.NewResolver(logger,
			baseimage.WithCosignSigner(cosign.NewSigner(logger)),
			baseimage.WithKeylessVerifier(sigstore.NewVerifier(logger))),
		Supervisor:      supervisor.New(logger),
		Packager:        packager.NewPackager(logger),
		Tarballs:        registry.NewAdapter(logger),
		NativeInspector: nativeinspect.NewClosuredAdapter(),
		Logger:          logger,
		Version:         "0.1.0-runtime-smoke-test",
		// BunRuntime is intentionally left nil: a --runtime=node build never
		// dereferences it (internal/core/pipeline.go gates every
		// deps.BunRuntime.Resolve call on req.AppRuntime == ports.RuntimeBun),
		// mirroring TestFixtureDrivenE2E_Static's identical omission for
		// StrategyStatic.
	}

	req := core.BuildRequest{
		ProjectDir: projectDir,
		Repo:       repo,
		Tags:       []string{tag},
		Platforms:  []ports.Platform{ports.LocalPlatform()},
		Compile:    core.CompileOptions{Strategy: core.StrategyLayered},
		AppRuntime: core.RuntimeNode,
		Output: core.OutputOptions{
			Mode:        core.OutputTarball,
			TarballPath: tarballPath,
		},
		SBOM: core.SBOMOptions{Format: ports.SBOMFormatNone},
		BaseImage: core.BaseImageOptions{
			// Preset/Ref deliberately left unset: core.Build's own
			// Normalize() defaults them to BaseImageDistrolessNode for
			// AppRuntime==RuntimeNode, the real preset a plain `pokkum build
			// --runtime=node` resolves to. Verifying its keyless Sigstore
			// signature is not what this test exists to guard and would add
			// unrelated Fulcio/Rekor network calls (see the layered test's
			// identical NoVerifyBase field).
			NoVerifyBase: true,
		},
		SourceDateEpoch: testEpoch,
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		if strings.Contains(err.Error(), "no such file or directory") && strings.Contains(err.Error(), "node_modules") {
			smokeGateSkipf(t, "fixture dependencies not installed (run `bun install` in %s): %v", fixtureDir, err)
		}
		if isNetworkError(err) {
			smokeGateSkipf(t, "base image resolution could not reach the network: %v", err)
		}
		t.Fatalf("core.Build failed for a real --runtime=node build: %v", err)
	}
	if res.Image.IsIndex {
		t.Fatalf("single-platform request produced an index; want a single manifest")
	}

	assertNodeRuntimeImage(t, tarballPath)

	loadImageIntoRuntime(t, runtimeBin, tarballPath, imageRef)
	containerName := runContainerAndAssertServes(t, runtimeBin, imageRef)
	assertNodeModulesAttestedAtRuntime(t, tarballPath, containerLogs(t, runtimeBin, containerName))
}

// assertNodeRuntimeImage inspects the OCI image written to tarballPath
// directly from disk (tarball.ImageFromPath — never trusting the build log)
// and proves the three things distinctive to --runtime=node: the real
// entrypoint, the runtime label, and — the load-bearing signal — that no
// layer carries a Bun runtime binary. See this file's package doc comment
// for why each of these three checks exists.
func assertNodeRuntimeImage(t *testing.T, tarballPath string) {
	t.Helper()

	img, err := tarball.ImageFromPath(tarballPath, nil)
	if err != nil {
		t.Fatalf("tarball.ImageFromPath: %v", err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("img.ConfigFile: %v", err)
	}

	wantEntrypoint := ports.DefaultLayeredNodeEntrypoint()
	if !slices.Equal(cfg.Config.Entrypoint, wantEntrypoint) {
		t.Errorf("image Entrypoint = %v, want %v (ports.NodeBinaryPath, not ports.BunBinaryPath %q)",
			cfg.Config.Entrypoint, wantEntrypoint, ports.BunBinaryPath)
	}

	if got := cfg.Config.Labels[ports.LabelRuntime]; got != string(ports.RuntimeNode) {
		t.Errorf("image label %s = %q, want %q — this label is stamped only for a non-default runtime "+
			"(internal/core/pipeline.go's imageLabels), so its presence with the right value is itself part "+
			"of the runtime-identity signal, not an incidental label", ports.LabelRuntime, got, ports.RuntimeNode)
	}

	// No History entry names the Bun binary: internal/adapters/packager's
	// layered branch appends the Bun-runtime layer addendum (CreatedBy
	// "pokkum: add "+ports.BunBinaryPath) only when the runtime is bun.
	for i, h := range cfg.History {
		if strings.Contains(h.CreatedBy, ports.BunBinaryPath) {
			t.Errorf("History[%d].CreatedBy = %q: a --runtime=node image must carry no Bun-runtime layer", i, h.CreatedBy)
		}
	}

	// Belt-and-suspenders per mem:self_review_checklist row 12/17's own
	// precedent (TestFixtureDrivenE2E_Static's identical double check):
	// confirm no layer's REAL tar members carry the Bun binary either, not
	// just the History string — a History entry could in principle be wrong
	// or absent while the byte actually shipped.
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("img.Layers: %v", err)
	}
	bunMember := strings.TrimPrefix(ports.BunBinaryPath, "/")
	for _, layer := range layers {
		for name := range tarEntries(t, layer) {
			if name == bunMember {
				t.Errorf("found Bun binary %q in a --runtime=node image layer; a --runtime=node image must embed no Bun runtime at all", name)
			}
		}
	}
}
