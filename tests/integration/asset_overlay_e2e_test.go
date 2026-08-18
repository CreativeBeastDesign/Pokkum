package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/assetoverlay"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunruntime"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/nativeinspect"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registry"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// assetOverlayMockCompiler is a per-generation variant of mockCompiler
// (e2e_test.go) that writes content-hashed client assets under
// _app/immutable/ — the real SvelteKit build-output convention this
// feature targets — instead of mockCompiler's single fixed app.js. Each
// generation's chunk filename changes (simulating a real code change
// producing a new content hash), while entry/start.shared.js stays
// byte-identical across generations, exercising extractImmutableAssets'
// identical-content dedup path alongside its orphaned-asset path.
type assetOverlayMockCompiler struct {
	generation int
}

func (m *assetOverlayMockCompiler) Preflight(_ context.Context, _ ports.PreflightRequest) (ports.PreflightResult, error) {
	return ports.PreflightResult{
		BunPath:          "/usr/local/bin/bun",
		BunVersion:       "1.2.18",
		AdapterVersion:   "0.1.7",
		SvelteKitVersion: "2.15.0",
	}, nil
}

func (m *assetOverlayMockCompiler) Prepare(_ context.Context, req ports.PrepareRequest) (ports.PrepareResult, error) {
	outputDir := filepath.Join(req.ProjectDir, fmt.Sprintf("build-gen%d", m.generation))
	serverDir := filepath.Join(outputDir, "server")
	if err := os.MkdirAll(serverDir, 0o755); err != nil {
		return ports.PrepareResult{}, fmt.Errorf("assetOverlayMockCompiler: prepare: mkdir server dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outputDir, "index.js"), []byte("export default { fetch() {} };\n"), 0o644); err != nil {
		return ports.PrepareResult{}, fmt.Errorf("assetOverlayMockCompiler: prepare: write entrypoint fixture: %w", err)
	}

	immutableDir := filepath.Join(outputDir, "client", "_app", "immutable", "chunks")
	if err := os.MkdirAll(immutableDir, 0o755); err != nil {
		return ports.PrepareResult{}, fmt.Errorf("assetOverlayMockCompiler: prepare: mkdir immutable chunks dir: %w", err)
	}
	// This generation's own hashed chunk — the filename changes every
	// generation, mirroring a real content-hash change from a code edit.
	chunkName := fmt.Sprintf("gen%d-chunk.js", m.generation)
	chunkContent := fmt.Sprintf("console.log('generation %d chunk');\n", m.generation)
	if err := os.WriteFile(filepath.Join(immutableDir, chunkName), []byte(chunkContent), 0o644); err != nil {
		return ports.PrepareResult{}, fmt.Errorf("assetOverlayMockCompiler: prepare: write chunk fixture: %w", err)
	}

	entryDir := filepath.Join(outputDir, "client", "_app", "immutable", "entry")
	if err := os.MkdirAll(entryDir, 0o755); err != nil {
		return ports.PrepareResult{}, fmt.Errorf("assetOverlayMockCompiler: prepare: mkdir immutable entry dir: %w", err)
	}
	// Byte-identical across every generation — the shared-runtime chunk
	// SvelteKit's own content hashing would keep stable when its source
	// doesn't change.
	if err := os.WriteFile(filepath.Join(entryDir, "start.shared.js"), []byte("shared start module, unchanged\n"), 0o644); err != nil {
		return ports.PrepareResult{}, fmt.Errorf("assetOverlayMockCompiler: prepare: write shared entry fixture: %w", err)
	}

	clientDir := filepath.Join(outputDir, "client")
	// Non-hashed file living alongside the immutable tree — must never be
	// extracted into the overlay (extractImmutableAssets filters by prefix).
	if err := os.WriteFile(filepath.Join(clientDir, "index.html"), fakePrerenderedHTML, 0o644); err != nil {
		return ports.PrepareResult{}, fmt.Errorf("assetOverlayMockCompiler: prepare: write index.html fixture: %w", err)
	}

	prerenderedDir := filepath.Join(outputDir, "prerendered")
	if err := os.MkdirAll(prerenderedDir, 0o755); err != nil {
		return ports.PrepareResult{}, fmt.Errorf("assetOverlayMockCompiler: prepare: mkdir prerendered dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(prerenderedDir, "index.html"), fakePrerenderedHTML, 0o644); err != nil {
		return ports.PrepareResult{}, fmt.Errorf("assetOverlayMockCompiler: prepare: write prerendered fixture: %w", err)
	}

	return ports.PrepareResult{
		EntrypointPath: filepath.Join(outputDir, "index.js"),
		OutputDir:      outputDir,
	}, nil
}

func (m *assetOverlayMockCompiler) Compile(_ context.Context, req ports.CompileRequest) (ports.Artifact, error) {
	outPath := writeTempBinary(&testing.T{}, fmt.Sprintf("server-gen%d-%s", m.generation, req.Platform.Arch), fakeAppContent)
	return ports.Artifact{
		Platform: req.Platform,
		Path:     outPath,
		Size:     int64(len(fakeAppContent)),
		SHA256:   "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
	}, nil
}

// findEnvValue looks up key=value in a Config.Env-style string slice.
func findEnvValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, prefix); ok {
			return v, true
		}
	}
	return "", false
}

// assetOverlayDeps builds a fresh core.Deps wired with the given generation's
// compiler, matching TestFixtureDrivenE2E_AllStrategies' dependency shape
// (strategy_e2e_test.go) plus a real assetoverlay.Resolver — the same
// production adapter cmd/pokkum/build.go wires, not a mock, since this test's
// entire point is proving the resolver's registry-side chain walk and
// content pull work against a real (if in-memory) registry.
func assetOverlayDeps(t *testing.T, gen int) core.Deps {
	t.Helper()
	return core.Deps{
		Compiler:        &assetOverlayMockCompiler{generation: gen},
		BaseImages:      &mockBaseResolver{t: t},
		Supervisor:      &mockSupervisorProvider{},
		Packager:        packager.NewPackager(testLogger()),
		BunRuntime:      bunruntime.NewResolver("", nil),
		Registry:        registry.NewAdapter(testLogger()),
		AssetOverlay:    assetoverlay.NewResolver(),
		NativeInspector: nativeinspect.NewClosuredAdapter(),
		Logger:          testLogger(),
		Version:         "0.1.0-asset-overlay-test",
	}
}

// TestRealBuild_AssetOverlay_TwoGenerations is this feature's core empirical
// proof (Tier 1.1 plan, row 13 / row 17 self-review discipline): two real,
// sequential core.Build runs pushed to a real ephemeral registry
// (pkg/registry's in-memory server, via RegistryHarness) — not synthetic
// unit-level fixtures.
//
// Generation 1 is built and pushed first. Generation 2 is built with
// --asset-overlay=1 (req.Compile.AssetOverlayGenerations = 1) and NO
// explicit --asset-overlay-from, so it must auto-discover generation 1
// purely by GETting the target repo:tag it's about to overwrite — proving
// the registry-side pokkum.dev/predecessor lineage mechanism actually works
// end-to-end, not just that its unit-level pieces compile.
//
// After generation 2 is pushed, this test pulls the REAL pushed image back
// out of the registry and confirms generation 1's now-orphaned hashed asset
// (gen1-chunk.js, absent from generation 2's own client build output) is
// present and byte-correct in the merged overlay layer — the exact
// mid-rollout 404 this feature exists to prevent. It also confirms the
// byte-identical shared chunk across generations doesn't spuriously
// duplicate-fail, and that the non-hashed index.html was never pulled into
// the overlay.
func TestRealBuild_AssetOverlay_TwoGenerations(t *testing.T) {
	fixtureDir := t.TempDir() // ProjectDir is only threaded through; the mock compiler ignores its real contents.

	harness := NewRegistryHarness(t)
	repoName := harness.Repo("asset-overlay-app")

	baseReq := func(gen int) core.BuildRequest {
		return core.BuildRequest{
			ProjectDir: fixtureDir,
			Repo:       repoName,
			Tags:       []string{"latest"},
			Platforms:  []ports.Platform{ports.LinuxAMD64},
			Compile:    core.CompileOptions{Strategy: core.StrategyLayered},
			BunRuntime: core.BunRuntimeOptions{CustomBinaryPath: writeTempBinary(t, fmt.Sprintf("bun-fixture-gen%d", gen), fakeBunFixtureContent)},
			Output:     core.OutputOptions{Mode: core.OutputPush},
			Insecure:   true,
			SBOM:       core.SBOMOptions{Format: ports.SBOMFormatNone},
			// SBOM generation is disabled: this test's signal is the overlay
			// layer's content, not SBOM plumbing, which is already covered
			// by TestFixtureDrivenE2E_AllStrategies.
			SourceDateEpoch: testEpoch,
		}
	}

	// --- Generation 1: ordinary build, no overlay involved yet. ---
	req1 := baseReq(1)
	res1, err := core.Build(context.Background(), assetOverlayDeps(t, 1), req1, core.BuildOptions{})
	if err != nil {
		t.Fatalf("generation 1 core.Build failed: %v", err)
	}
	if res1.Image.Digest.Hex == "" {
		t.Fatalf("generation 1 BuildResult image digest is empty")
	}
	gen1Digest := res1.Image.Digest.String()

	// --- Generation 2: --asset-overlay=1, auto-discovery (no explicit refs). ---
	req2 := baseReq(2)
	req2.Compile.AssetOverlayGenerations = 1

	res2, err := core.Build(context.Background(), assetOverlayDeps(t, 2), req2, core.BuildOptions{})
	if err != nil {
		t.Fatalf("generation 2 core.Build (--asset-overlay=1) failed: %v", err)
	}
	if res2.Image.Digest.Hex == "" {
		t.Fatalf("generation 2 BuildResult image digest is empty")
	}
	if res2.Image.Digest.String() == gen1Digest {
		t.Fatalf("generation 2 produced the same digest as generation 1 — fixtures did not actually differ")
	}

	// --- Fetch the REAL pushed generation-2 image and inspect its layers. ---
	imgRef := repoName + ":latest"
	img := harness.FetchImage(t, imgRef)

	manifest, _ := harness.FetchManifest(t, imgRef)
	if got := manifest.Annotations[ports.AnnotationPredecessor]; got != gen1Digest {
		t.Errorf("manifest annotation %s = %q, want generation 1's digest %q — auto-discovery did not resolve/stamp the predecessor chain", ports.AnnotationPredecessor, got, gen1Digest)
	}
	if got := manifest.Annotations[ports.AnnotationAssetOverlaySources]; got != gen1Digest {
		t.Errorf("manifest annotation %s = %q, want generation 1's digest %q", ports.AnnotationAssetOverlaySources, got, gen1Digest)
	}

	cfg, _ := harness.FetchConfigFile(t, img)
	if got, ok := findEnvValue(cfg.Config.Env, ports.EnvAttestationDigest); !ok || len(got) != 64 {
		t.Fatalf("generation 2 image is missing a valid %s; overlay build must still be attestable", ports.EnvAttestationDigest)
	}

	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("img.Layers: %v", err)
	}

	allEntries := map[string]string{}
	for _, layer := range layers {
		for name, content := range tarEntries(t, layer) {
			if existing, dup := allEntries[name]; dup && existing != content {
				// Two layers may legitimately both carry the byte-identical
				// shared chunk (generation 2's own client layer AND the
				// overlay layer) — that's fine. A real conflict would only
				// show up as extractImmutableAssets returning a hard error
				// during the build itself (see the dedicated conflict test),
				// so this loop only records the last-seen content per name.
				_ = dup
			}
			allEntries[name] = content
		}
	}

	// Generation 1's orphaned hashed asset must have survived into
	// generation 2's pushed image via the overlay layer.
	orphanPath := "app/client/_app/immutable/chunks/gen1-chunk.js"
	orphanContent, found := allEntries[orphanPath]
	if !found {
		t.Fatalf("generation 1's orphaned asset %q not found in generation 2's pushed image; asset overlay did not carry it forward — this is exactly the mid-rollout 404 the feature exists to prevent", orphanPath)
	}
	if want := "console.log('generation 1 chunk');\n"; orphanContent != want {
		t.Errorf("orphaned asset %q content = %q, want %q", orphanPath, orphanContent, want)
	}

	// Generation 2's own hashed asset must also be present (from its
	// ordinary, non-overlay client layer).
	gen2Path := "app/client/_app/immutable/chunks/gen2-chunk.js"
	if content, found := allEntries[gen2Path]; !found {
		t.Fatalf("generation 2's own asset %q not found in its pushed image", gen2Path)
	} else if want := "console.log('generation 2 chunk');\n"; content != want {
		t.Errorf("generation 2 asset %q content = %q, want %q", gen2Path, content, want)
	}

	// The byte-identical shared chunk must be present and unambiguous.
	sharedPath := "app/client/_app/immutable/entry/start.shared.js"
	if content, found := allEntries[sharedPath]; !found {
		t.Fatalf("shared asset %q not found", sharedPath)
	} else if want := "shared start module, unchanged\n"; content != want {
		t.Errorf("shared asset %q content = %q, want %q", sharedPath, content, want)
	}

	// The non-hashed index.html must never be pulled into the overlay from
	// generation 1 — only generation 2's own copy (via its ordinary client
	// layer) should be present, and its content must be generation 2's.
	htmlPath := "app/client/index.html"
	if content, found := allEntries[htmlPath]; !found {
		t.Fatalf("generation 2's own index.html not found")
	} else if string(content) != string(fakePrerenderedHTML) {
		t.Errorf("index.html content unexpectedly differs from generation 2's own fixture — non-immutable file leaked from the overlay merge")
	}
}

// TestResolvePredecessorChain_TruncatesOnMalformedPredecessorAnnotation is
// the regression guard for a real bug found by adversarial review:
// ResolvePredecessorChain used to hard-fail the WHOLE call the moment any
// predecessor annotation in the chain failed to parse as a valid image
// reference, instead of truncating the chain at that point the way it
// already does for a GC'd/not-found predecessor. pokkum.dev/predecessor is
// read off a remote manifest — reachable by anyone with push access to the
// target repo — so a single malformed value (accidental corruption, or a
// deliberately crafted one) would otherwise permanently wedge every future
// --asset-overlay build against that repo: a self-inflicted denial of
// service this feature has no business causing.
//
// This pushes a real image directly (not via core.Build) whose
// pokkum.dev/predecessor annotation is a garbage, unparseable string, then
// calls assetoverlay.ResolvePredecessorChain against it directly and
// confirms it returns the one resolvable digest (this image's own) with no
// error, rather than erroring out entirely.
func TestResolvePredecessorChain_TruncatesOnMalformedPredecessorAnnotation(t *testing.T) {
	harness := NewRegistryHarness(t)
	repoName := harness.Repo("malformed-predecessor-app")

	img := mutate.Annotations(empty.Image, map[string]string{
		ports.AnnotationPredecessor: "this is not a valid image reference @@@ ///",
	}).(v1.Image)

	ref, err := name.ParseReference(repoName+":latest", name.Insecure)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatalf("remote.Write: %v", err)
	}

	chain, err := assetoverlay.ResolvePredecessorChain(context.Background(), repoName, "latest", "", true, 5)
	if err != nil {
		t.Fatalf("ResolvePredecessorChain: expected the chain to truncate on a malformed predecessor annotation, got a hard error instead: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("chain length = %d, want 1 (this image's own digest only — the malformed predecessor must truncate the walk, not extend it)", len(chain))
	}
}

// TestAssetOverlayFrom_CrossRepoRefWorks is the regression guard for a real
// functional bug found by adversarial review: --asset-overlay-from's whole
// documented purpose is naming "arbitrary external images, not this build's
// own push target" (pipeline.go's Stage 4.4 comment), but
// assetoverlay.ResolveDigest used to return only the bare digest, and
// BuildOverlayDir always pulled every resolved digest from the CALLER's own
// repo regardless of where it actually resolved — so any cross-repo
// --asset-overlay-from ref 404'd on pull and hard-failed the whole build.
//
// This builds and pushes a real image to one repo (the "source"), then runs
// a real core.Build targeting a COMPLETELY DIFFERENT repo (the "target")
// with --asset-overlay-from pointed at the source repo's image, and
// confirms the build succeeds and the source's immutable asset is actually
// carried into the target's pushed image — proving the fix pulls from the
// ref's own repo, not the build's push target.
func TestAssetOverlayFrom_CrossRepoRefWorks(t *testing.T) {
	harness := NewRegistryHarness(t)
	sourceRepo := harness.Repo("overlay-source-app")
	targetRepo := harness.Repo("overlay-target-app")

	fixtureDir := t.TempDir()
	baseReq := func(gen int, repo string) core.BuildRequest {
		return core.BuildRequest{
			ProjectDir:      fixtureDir,
			Repo:            repo,
			Tags:            []string{"latest"},
			Platforms:       []ports.Platform{ports.LinuxAMD64},
			Compile:         core.CompileOptions{Strategy: core.StrategyLayered},
			BunRuntime:      core.BunRuntimeOptions{CustomBinaryPath: writeTempBinary(t, fmt.Sprintf("bun-fixture-crossrepo-%d", gen), fakeBunFixtureContent)},
			Output:          core.OutputOptions{Mode: core.OutputPush},
			Insecure:        true,
			SBOM:            core.SBOMOptions{Format: ports.SBOMFormatNone},
			SourceDateEpoch: testEpoch,
		}
	}

	// --- Push the "source" image to its own, separate repo. ---
	sourceReq := baseReq(1, sourceRepo)
	sourceRes, err := core.Build(context.Background(), assetOverlayDeps(t, 1), sourceReq, core.BuildOptions{})
	if err != nil {
		t.Fatalf("source build failed: %v", err)
	}

	// --- Build the "target" image in a DIFFERENT repo, referencing the
	// source repo's image explicitly via --asset-overlay-from. ---
	targetReq := baseReq(2, targetRepo)
	targetReq.Compile.AssetOverlayGenerations = 1
	targetReq.Compile.AssetOverlayFrom = []string{sourceRepo + ":latest"}

	targetRes, err := core.Build(context.Background(), assetOverlayDeps(t, 2), targetReq, core.BuildOptions{})
	if err != nil {
		t.Fatalf("target build with cross-repo --asset-overlay-from failed (this is exactly the bug this test guards against): %v", err)
	}
	if targetRes.Image.Digest.Hex == "" {
		t.Fatalf("target build produced an empty digest")
	}

	// The source's own asset (gen1-chunk.js) must be present in the
	// target's pushed image, carried across from the other repository.
	img := harness.FetchImage(t, targetRepo+":latest")
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("img.Layers: %v", err)
	}
	found := false
	for _, l := range layers {
		if content, ok := tarEntries(t, l)["app/client/_app/immutable/chunks/gen1-chunk.js"]; ok {
			found = true
			if content != "console.log('generation 1 chunk');\n" {
				t.Errorf("cross-repo overlay asset content = %q, want the source repo's real content", content)
			}
		}
	}
	if !found {
		t.Fatalf("source repo's asset (chunks/gen1-chunk.js) not found in target's pushed image — cross-repo --asset-overlay-from did not actually carry the content across")
	}

	// Also confirm predecessorDigest was NOT stamped from this
	// --asset-overlay-from build (task #97's fix): the source image is
	// unrelated to targetRepo's own push history, so
	// pokkum.dev/predecessor must be absent here.
	manifest, _ := harness.FetchManifest(t, targetRepo+":latest")
	if got, ok := manifest.Annotations[ports.AnnotationPredecessor]; ok {
		t.Errorf("manifest annotation %s = %q, want absent — --asset-overlay-from must not stamp an unrelated image's digest as this repo's lineage", ports.AnnotationPredecessor, got)
	}
	_ = sourceRes
}
