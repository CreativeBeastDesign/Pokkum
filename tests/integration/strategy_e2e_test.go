package integration

import (
	"context"
	"path/filepath"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunruntime"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/nativeinspect"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registry"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sbom"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// layerMemberNames returns the set of tar entry names inside layer, via
// RegistryHarness.FetchLayerMembers, as a map for quick membership checks.
func layerMemberNames(t *testing.T, harness *RegistryHarness, layer v1.Layer) map[string]bool {
	t.Helper()
	members := harness.FetchLayerMembers(t, layer)
	names := make(map[string]bool, len(members))
	for _, m := range members {
		names[m.Name] = true
	}
	return names
}

// anyLayerContains reports whether any layer in layers has a tar member with
// exactly the given name. Used for negative assertions (e.g. "no bun binary
// anywhere in a static image") where the exact layer index isn't the point.
func anyLayerContains(t *testing.T, harness *RegistryHarness, layers []v1.Layer, name string) bool {
	t.Helper()
	for _, l := range layers {
		if layerMemberNames(t, harness, l)[name] {
			return true
		}
	}
	return false
}

// fakeBunFixtureContent is a local fixture for --bun-binary (BunRuntimeOptions
// .CustomBinaryPath). StrategyLayered is the only strategy whose pipeline path
// calls the REAL bunruntime.Resolver (BunRuntime in core.Deps is not mocked
// like Compiler/Supervisor/StaticServer are, since e2e_test.go's
// TestFixtureDrivenE2E never exercised StrategyLayered before this file
// existed). Pointing CustomBinaryPath at a local file makes
// bunruntime.Resolver.Resolve take its short-circuit local-file path
// (internal/adapters/bunruntime/resolver.go) instead of attempting a real
// network download+checksum-verify against bun's release CDN, which is both
// unavailable and unnecessary in this test environment.
var fakeBunFixtureContent = []byte("fake-bun-runtime-binary\n")

// TestFixtureDrivenE2E_AllStrategies is the golden-master counterpart to
// TestFixtureDrivenE2E (e2e_test.go), which only ever drove StrategyExe. It
// runs the exact same core.Build orchestration — mockCompiler, real
// packager.NewPackager, real registry.NewAdapter against the httptest-backed
// RegistryHarness — once per packaging strategy (StrategyLayered,
// StrategyExe, StrategyStatic), varying only req.Compile.Strategy.
//
// This test exists to characterize CURRENT shipped behavior of pipeline.go's
// per-strategy dispatch (see internal/core/pipeline.go's runPlatformBuilds and
// internal/adapters/packager/packager.go's Build) so that:
//   - a future 4th strategy silently missing a branch in the dispatch switch
//     is caught by an obviously-incomplete table, not silently shipped;
//   - an accidental regression to which layers/entrypoint a given EXISTING
//     strategy produces is caught here rather than downstream.
//
// It is deliberately NOT testing the "Strategy-Dispatch Consolidation"
// refactor described in concepts/strategy-dispatch-refactor-concept.md — that
// refactor is out of scope; this test only pins down behavior as it exists
// today so that refactor (whenever it happens) has a safety net.
func TestFixtureDrivenE2E_AllStrategies(t *testing.T) {
	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "sveltekit-basic"))
	if err != nil {
		t.Fatalf("Abs fixture path: %v", err)
	}
	// mockCompiler.Prepare (e2e_test.go) writes real files into
	// <ProjectDir>/build (StrategyLayered) and <ProjectDir>/.svelte-kit/output
	// (StrategyStatic) for every subtest below — an isolated scratch copy per
	// mem:self_review_checklist and Lessons.md's shared-fixture-mutation entry,
	// not the checked-out fixture directly, since three subtests share this
	// one ProjectDir sequentially and other tests/packages read the same
	// fixture. One copy for the whole table is enough: the three subtests
	// write to disjoint subpaths (build/, .svelte-kit/jesterkit-sveltekit/,
	// .svelte-kit/output) and run in fixed, non-parallel table order.
	fixtureDir = copyFixtureProject(t, fixtureDir)

	type wantLayer struct {
		// index is the layer's position in img.Layers(), where index 0 is
		// always the (single-layer) synthetic base image appended by
		// mockBaseResolver / SyntheticBaseImage — see harness_test.go.
		index int
		// member is a tar entry name (per RegistryHarness.FetchLayerMembers)
		// that must be present in the layer at index, proving the layer's
		// identity (not just its position).
		member string
	}

	tests := []struct {
		name string
		// strategy is the core.BuildStrategy under test.
		strategy core.BuildStrategy
		// wantEntrypoint is the exact Config.Entrypoint value packager.Build
		// produces for this strategy — see internal/ports.DefaultLayeredEntrypoint,
		// DefaultEntrypoint (used for StrategyExe) and DefaultStaticEntrypoint,
		// and internal/adapters/packager/packager.go's Build (lines ~150-160)
		// which selects between them.
		wantEntrypoint []string
		// wantLayerCount is the total layer count of the pushed image,
		// INCLUDING the base image's own single ca-certificates layer.
		wantLayerCount int
		// wantLayers pins down layer identity at specific indices, derived
		// from the exact addenda-append order in packager.Build for this
		// strategy (bun, supervisor, server, client, vendor, native,
		// prerendered for layered; supervisor, app for exe; static server,
		// client, prerendered for static).
		wantLayers []wantLayer
		// forbiddenMembers must not appear in ANY layer of the pushed image —
		// a regression check that e.g. a static image never grows a Bun
		// runtime layer or a supervisor binary, and an exe image never grows
		// a client/vendor/native tree.
		forbiddenMembers []string
	}{
		{
			name:     "layered",
			strategy: core.StrategyLayered,
			// Was a KNOWN BUG (see Lessons.md's 2026-08-16 entry): core.Build's
			// Normalize() runs RuntimeConfig.WithDefaults() before the strategy
			// is known, which used to claim a nil Entrypoint with the
			// StrategyExe-shaped default before packager.Build's
			// StrategyLayered branch ever saw the request, defeating its
			// nil-guard. Fixed by making that branch unconditionally set
			// ports.DefaultLayeredEntrypoint() (internal/adapters/packager/packager.go),
			// mirroring the static branch's own unconditional overwrite. This
			// now asserts the CORRECT entrypoint: {SupervisorPath, "--",
			// BunBinaryPath, AppServerIndexPath} (exec "bun /app/server/index.js").
			wantEntrypoint: []string{ports.SupervisorPath, "--", ports.BunBinaryPath, ports.AppServerIndexPath},
			wantLayerCount: 8, // base + bun + supervisor + server + client + vendor + native + prerendered
			wantLayers: []wantLayer{
				{index: 1, member: "usr/local/bin/bun"},
				{index: 2, member: "pokkum/init"},
				{index: 3, member: "app/server/index.js"},
				{index: 4, member: "app/client/app.js"},
				{index: 5, member: "app/vendor/package.json"},
				{index: 6, member: "app/native/fixture.node"},
				{index: 7, member: "app/prerendered/index.html"},
			},
			forbiddenMembers: []string{"pokkum/static"},
		},
		{
			name:           "exe",
			strategy:       core.StrategyExe,
			wantEntrypoint: []string{ports.SupervisorPath, "--", ports.AppBinaryPath},
			wantLayerCount: 3, // base + supervisor + single app binary
			wantLayers: []wantLayer{
				{index: 1, member: "pokkum/init"},
				{index: 2, member: "app/server"},
			},
			forbiddenMembers: []string{
				"usr/local/bin/bun",
				"pokkum/static",
				"app/client/app.js",
				"app/vendor/package.json",
				"app/native/fixture.node",
				"app/prerendered/index.html",
			},
		},
		{
			name:           "static",
			strategy:       core.StrategyStatic,
			wantEntrypoint: []string{ports.StaticServerPath},
			wantLayerCount: 4, // base + static server + client + prerendered
			wantLayers: []wantLayer{
				{index: 1, member: "pokkum/static"},
				{index: 2, member: "app/client/app.js"},
				{index: 3, member: "app/prerendered/index.html"},
			},
			forbiddenMembers: []string{
				"usr/local/bin/bun",
				"pokkum/init",
				"app/server/index.js",
				"app/vendor/package.json",
				"app/native/fixture.node",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			harness := NewRegistryHarness(t)
			repoName := harness.Repo("strategy-" + tc.name + "-app")

			deps := core.Deps{
				Compiler:        &mockCompiler{},
				BaseImages:      &mockBaseResolver{t: t},
				Supervisor:      &mockSupervisorProvider{},
				StaticServer:    &mockStaticServerProvider{},
				Packager:        packager.NewPackager(testLogger()),
				BunRuntime:      bunruntime.NewResolver("", nil),
				Registry:        registry.NewAdapter(testLogger()),
				SBOM:            sbom.NewGenerator(testLogger()),
				NativeInspector: nativeinspect.NewClosuredAdapter(),
				Logger:          testLogger(),
				Version:         "0.1.0-integration-test",
			}

			req := core.BuildRequest{
				ProjectDir: fixtureDir,
				Repo:       repoName,
				Tags:       []string{"latest"},
				Platforms:  []ports.Platform{ports.LinuxAMD64},
				Compile:    core.CompileOptions{Strategy: tc.strategy},
				// Only StrategyLayered's pipeline path ever calls
				// deps.BunRuntime.Resolve (internal/core/pipeline.go), but
				// setting CustomBinaryPath unconditionally is harmless for
				// the other two strategies and keeps this request block
				// uniform across the table.
				BunRuntime:      core.BunRuntimeOptions{CustomBinaryPath: writeTempBinary(t, "bun-fixture-"+tc.name, fakeBunFixtureContent)},
				Output:          core.OutputOptions{Mode: core.OutputPush},
				Insecure:        true,
				SBOM:            core.SBOMOptions{Format: ports.SBOMFormatSPDXJSON, AttachMode: ports.SBOMAttachTag, NoAttach: false},
				SourceDateEpoch: testEpoch,
			}

			res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
			if err != nil {
				t.Fatalf("core.Build (strategy=%s) failed: %v", tc.strategy, err)
			}

			// 1. Basic result sanity, mirroring TestFixtureDrivenE2E.
			if res.Image.Digest.Hex == "" {
				t.Errorf("BuildResult image digest is empty")
			}
			if res.Image.IsIndex {
				t.Errorf("single-platform build should not be an index")
			}
			if res.SBOM == nil {
				t.Fatalf("BuildResult SBOM is nil")
			}

			// 2. Fetch the pushed single-platform image.
			imgRef := repoName + ":latest"
			img := harness.FetchImage(t, imgRef)

			cfg, _ := harness.FetchConfigFile(t, img)

			// 3. Entrypoint/Cmd shape. Per internal/adapters/packager/config.go,
			// Config.Cmd = slices.Clone(req.Runtime.Cmd); none of the three
			// strategies set Runtime.Cmd in internal/core/pipeline.go, so Cmd
			// is nil/empty for all of them today — this is NOT a strategy-
			// differentiated field in the current implementation, so we assert
			// that explicitly (empty) rather than skip it silently.
			if len(cfg.Config.Cmd) != 0 {
				t.Errorf("Config.Cmd = %v, want empty (no strategy sets Runtime.Cmd today)", cfg.Config.Cmd)
			}
			if len(cfg.Config.Entrypoint) != len(tc.wantEntrypoint) {
				t.Fatalf("Config.Entrypoint = %v, want %v", cfg.Config.Entrypoint, tc.wantEntrypoint)
			}
			for i, want := range tc.wantEntrypoint {
				if cfg.Config.Entrypoint[i] != want {
					t.Errorf("Config.Entrypoint[%d] = %q, want %q (full: %v)", i, cfg.Config.Entrypoint[i], want, cfg.Config.Entrypoint)
				}
			}

			// 4. Base image reference. mockBaseResolver.Resolve (e2e_test.go)
			// returns a fixed synthetic Ref/PinnedRef/Digest regardless of
			// req.Compile.Strategy — the mock does not vary base image
			// selection by strategy, so this assertion is identical across
			// all three subtests. It is included only to prove the mocked
			// base image round-trips into the config's base-image label, not
			// to demonstrate per-strategy base selection: that logic (the
			// CLI defaulting a different base preset for --strategy=static)
			// lives in cmd/pokkum and is already covered by
			// cmd/pokkum/build_test.go, not exercised meaningfully here.
			if got := cfg.Config.Labels[ports.LabelBaseName]; got != "gcr.io/distroless/cc-debian12:nonroot" {
				t.Errorf("Config.Labels[%s] = %q, want %q", ports.LabelBaseName, got, "gcr.io/distroless/cc-debian12:nonroot")
			}

			// 5. Layer count and identity.
			layers, err := img.Layers()
			if err != nil {
				t.Fatalf("img.Layers: %v", err)
			}
			if len(layers) != tc.wantLayerCount {
				t.Fatalf("img.Layers count = %d, want %d", len(layers), tc.wantLayerCount)
			}
			for _, wl := range tc.wantLayers {
				if wl.index >= len(layers) {
					t.Fatalf("wantLayers references index %d but image only has %d layers", wl.index, len(layers))
				}
				names := layerMemberNames(t, harness, layers[wl.index])
				if !names[wl.member] {
					t.Errorf("layer[%d] missing expected member %q; got members: %v", wl.index, wl.member, names)
				}
			}

			// 6. Regression guard: forbidden members must not appear in ANY
			// layer, not just the ones we pinned an index to above — this is
			// what actually catches e.g. a static image accidentally growing
			// a Bun runtime or supervisor layer.
			for _, forbidden := range tc.forbiddenMembers {
				if anyLayerContains(t, harness, layers, forbidden) {
					t.Errorf("strategy=%s: forbidden member %q found in image, want absent", tc.strategy, forbidden)
				}
			}
		})
	}
}
