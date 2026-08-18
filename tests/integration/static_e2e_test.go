package integration

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/nativeinspect"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registry"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sbom"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Port interface conformance assertions for mock implementations.
var (
	_ ports.Compiler          = (*staticFixtureCompiler)(nil)
	_ ports.BaseImageResolver = (*recordingBaseResolver)(nil)
)

// Fixture content for the static-strategy client tree. These live in this
// file (rather than harness_test.go/e2e_test.go) so a concurrent task
// editing the shared mocks doesn't collide with this one. Both must be >=64
// bytes with genuine repetition so internal/adapters/precompressutils's
// 64-byte-minimum + positive-savings rule actually produces .gz/.br/.zst
// sidecars for them (see precompressutils.PrecompressFile).
var (
	// fakeImmutableChunkJS simulates a content-hashed SvelteKit client chunk,
	// e.g. the kind emitted under client/immutable/chunks/*.js.
	fakeImmutableChunkJS = bytes.Repeat([]byte("console.log('pokkum immutable chunk asset');\n"), 20)

	// fakeFaviconSVG simulates a plain, non-hashed static asset served
	// straight from the client root.
	fakeFaviconSVG = bytes.Repeat([]byte("<svg xmlns=\"http://www.w3.org/2000/svg\"><circle r=\"1\"/></svg>\n"), 20)

	// fakeSPAFallbackHTML simulates the SPA fallback shell @sveltejs/
	// adapter-static emits (e.g. client/200.html). It is large and repetitive
	// so precompressutils genuinely produces .gz/.br/.zst sidecars for it.
	fakeSPAFallbackHTML = bytes.Repeat([]byte("<!doctype html><html><body><div id=\"app\">SPA shell</div></body></html>\n"), 20)
)

// staticFixtureCompiler is a StrategyStatic-only ports.Compiler double, local
// to this test file. It exists instead of extending the shared mockCompiler
// in e2e_test.go so this test can add extra fixture files (an /immutable/
// hashed chunk and a plain asset) without touching a file a concurrent task
// may also be editing. Its Prepare mirrors mockCompiler's StrategyStatic
// branch (outputDir = <ProjectDir>/.svelte-kit/output, no entrypoint) but
// additionally populates client/immutable/chunks/ and a root-level asset.
type staticFixtureCompiler struct {
	// fallback, when non-empty, is the leaf filename of an opt-in SPA fallback
	// page to stage in the client output (e.g. "200.html") and report via
	// PrepareResult.StaticFallbackRelPath, exercising the packager's
	// POKKUM_STATIC_FALLBACK stamping. Empty (the zero value) means no fallback
	// — the default static build fixture's behavior is unchanged.
	fallback string
}

func (m *staticFixtureCompiler) Preflight(_ context.Context, _ ports.PreflightRequest) (ports.PreflightResult, error) {
	return ports.PreflightResult{
		BunPath:          "/usr/local/bin/bun",
		BunVersion:       "1.2.18",
		AdapterVersion:   "0.1.7",
		SvelteKitVersion: "2.15.0",
	}, nil
}

func (m *staticFixtureCompiler) Prepare(_ context.Context, req ports.PrepareRequest) (ports.PrepareResult, error) {
	// Mirrors bunexec.Compiler.Prepare's static path / mockCompiler's
	// StrategyStatic branch: no server entrypoint, outputDir is the
	// SvelteKit static staging tree.
	outputDir := filepath.Join(req.ProjectDir, ".svelte-kit", "output")

	clientDir := filepath.Join(outputDir, "client")
	if err := os.MkdirAll(clientDir, 0o755); err != nil {
		return ports.PrepareResult{}, fmt.Errorf("staticFixtureCompiler: prepare: mkdir client dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(clientDir, "app.js"), []byte("console.log('fixture client bundle');\n"), 0o644); err != nil {
		return ports.PrepareResult{}, fmt.Errorf("staticFixtureCompiler: prepare: write client fixture: %w", err)
	}

	// One /immutable/-style hashed asset.
	immutableDir := filepath.Join(clientDir, "immutable", "chunks")
	if err := os.MkdirAll(immutableDir, 0o755); err != nil {
		return ports.PrepareResult{}, fmt.Errorf("staticFixtureCompiler: prepare: mkdir immutable dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(immutableDir, "app-a1b2c3d4.js"), fakeImmutableChunkJS, 0o644); err != nil {
		return ports.PrepareResult{}, fmt.Errorf("staticFixtureCompiler: prepare: write immutable chunk fixture: %w", err)
	}

	// One plain, non-hashed asset.
	if err := os.WriteFile(filepath.Join(clientDir, "favicon.svg"), fakeFaviconSVG, 0o644); err != nil {
		return ports.PrepareResult{}, fmt.Errorf("staticFixtureCompiler: prepare: write favicon fixture: %w", err)
	}

	prerenderedDir := filepath.Join(outputDir, "prerendered")
	if err := os.MkdirAll(prerenderedDir, 0o755); err != nil {
		return ports.PrepareResult{}, fmt.Errorf("staticFixtureCompiler: prepare: mkdir prerendered dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(prerenderedDir, "index.html"), fakePrerenderedHTML, 0o644); err != nil {
		return ports.PrepareResult{}, fmt.Errorf("staticFixtureCompiler: prepare: write prerendered fixture: %w", err)
	}

	// Optional SPA fallback: stage it in the client output root and report the
	// leaf name so the packager stamps POKKUM_STATIC_FALLBACK.
	staticFallbackRel := ""
	if m.fallback != "" {
		if err := os.WriteFile(filepath.Join(clientDir, m.fallback), fakeSPAFallbackHTML, 0o644); err != nil {
			return ports.PrepareResult{}, fmt.Errorf("staticFixtureCompiler: prepare: write SPA fallback fixture: %w", err)
		}
		staticFallbackRel = m.fallback
	}

	return ports.PrepareResult{
		EntrypointPath:        "",
		OutputDir:             outputDir,
		StaticFallbackRelPath: staticFallbackRel,
	}, nil
}

func (m *staticFixtureCompiler) Compile(_ context.Context, req ports.CompileRequest) (ports.Artifact, error) {
	// core.Build's fan-out never calls Compile for StrategyStatic — the
	// SvelteKit build output (.svelte-kit/output) is the entire artifact, see
	// internal/core/pipeline.go's per-platform fan-out (`else if
	// !req.Compile.Strategy.ApplyStatic()`). If this is ever invoked for a
	// static build, that invariant broke; fail loudly instead of silently
	// returning a fake binary that would mask the regression.
	return ports.Artifact{}, fmt.Errorf("staticFixtureCompiler: Compile unexpectedly invoked for static strategy, platform %s", req.Platform)
}

// recordingBaseResolver is a ports.BaseImageResolver double, local to this
// test file, that behaves like mockBaseResolver (synthetic, network-free
// resolution) but additionally records the exact ports.BaseImageRequest it
// was called with. This is what lets the test assert on how core.Build
// threaded base-image selection through to the resolver — in particular
// AllowStatic, which internal/core/pipeline.go derives internally from
// req.Compile.Strategy.ApplyStatic() rather than from any field on
// core.BuildRequest, so it can only be observed by capturing the call.
type recordingBaseResolver struct {
	t *testing.T

	mu      sync.Mutex
	lastReq ports.BaseImageRequest
	calls   int
}

func (m *recordingBaseResolver) Resolve(_ context.Context, req ports.BaseImageRequest) (*ports.BaseImage, error) {
	m.mu.Lock()
	m.lastReq = req
	m.calls++
	m.mu.Unlock()

	images := make(map[ports.Platform]v1.Image, len(req.Platforms))
	for _, p := range req.Platforms {
		images[p] = SyntheticBaseImage(m.t, p)
	}
	return &ports.BaseImage{
		Ref:       "cgr.dev/chainguard/static:latest",
		PinnedRef: "cgr.dev/chainguard/static:latest@sha256:99887766554433221100ffeeddccbbaa99887766554433221100ffeeddccbb",
		Digest:    v1.Hash{Algorithm: "sha256", Hex: "99887766554433221100ffeeddccbbaa99887766554433221100ffeeddccbb"},
		Images:    images,
		IsIndex:   len(req.Platforms) > 1,
	}, nil
}

func (m *recordingBaseResolver) RecordScanResult(_ context.Context, _ string, _ ports.BaseImagePreset, _ ports.ScanResult) error {
	return nil
}

func (m *recordingBaseResolver) VerifyBaseImage(_ context.Context, _ *ports.BaseImage, _ ports.BaseImageRequest) error {
	return nil
}

func (m *recordingBaseResolver) recorded() (ports.BaseImageRequest, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lastReq, m.calls
}

// TestFixtureDrivenE2E_Static is the static-strategy counterpart to
// TestFixtureDrivenE2E: it drives the full core.Build orchestration for
// --strategy=static through the real packaging/registry/SBOM pipeline (using
// staticFixtureCompiler and mockStaticServerProvider in place of a real Bun
// toolchain and embedded pokkum-static binary) and pushes the result to the
// in-memory registry harness, then asserts on the pushed image that:
//
//  1. No Bun-runtime layer (/usr/local/bin/bun) and no supervisor layer
//     (/pokkum/init) are present — a static image is its own PID 1.
//  2. The static-server binary layer (/pokkum/static, from
//     mockStaticServerProvider) IS present.
//  3. core.Build threads base-image selection through to the resolver
//     correctly for a static build. This test does NOT re-assert "does
//     --static request the chainguard/static preset" — that CLI-level
//     defaulting (the --static flag reconciling to
//     {Preset: core.BaseImageChainguard, Ref: core.StaticBaseRef}) is
//     already fully covered by TestBuildStaticStrategyReconciliation in
//     cmd/pokkum/build_test.go, which calls core.Build with a mocked
//     resolver and therefore can't observe the resolver-side of the
//     contract either. What's asserted here, that isn't asserted anywhere
//     else, is the pipeline-level behavior in
//     internal/core/pipeline.go (the `baseReq := ports.BaseImageRequest{...
//     AllowStatic: req.Compile.Strategy.ApplyStatic()}` block): given a
//     request already carrying the static-appropriate preset/ref (as the
//     CLI would have set for --static), core.Build (a) passes Preset/Ref
//     through unchanged, and (b) always sets AllowStatic: true for a
//     static-strategy build regardless of what the caller set — this is
//     pipeline-derived, not caller-supplied, so it can only be verified by
//     inspecting the actual BaseImageRequest the resolver received.
//  4. The built image pushes successfully and can be fetched back via
//     FetchIndex/FetchImage, proving a follow-up test can
//     ExtractLayerFiles() the client/prerendered layers out of it (e.g. to
//     point supervisor/cmd/pokkum-static's own test server at real files).
func TestFixtureDrivenE2E_Static(t *testing.T) {
	harness := NewRegistryHarness(t)
	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "sveltekit-basic"))
	if err != nil {
		t.Fatalf("Abs fixture path: %v", err)
	}

	repoName := harness.Repo("sveltekit-static-app")

	baseResolver := &recordingBaseResolver{t: t}

	deps := core.Deps{
		Compiler:   &staticFixtureCompiler{},
		BaseImages: baseResolver,
		// Deps.validate requires Supervisor to be non-nil unconditionally
		// (internal/core/pipeline.go's Deps.validate), even though a static
		// build never calls Supervisor.Binary/Version — see the fan-out's
		// `if req.Compile.Strategy.ApplyStatic() { ... } else { sup, _ =
		// deps.Supervisor.Binary(...) }` branch, which a static build never
		// takes.
		Supervisor: &mockSupervisorProvider{},
		// Required because req.Compile.Strategy.ApplyStatic() is true.
		StaticServer: &mockStaticServerProvider{},
		Packager:     packager.NewPackager(testLogger()),
		// BunRuntime is intentionally left nil: it is only dereferenced for
		// StrategyLayered (internal/core/pipeline.go fan-out), never for
		// StrategyStatic.
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
		Platforms:  []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64},
		Compile:    core.CompileOptions{Strategy: core.StrategyStatic},
		Output:     core.OutputOptions{Mode: core.OutputPush},
		Insecure:   true,
		BaseImage: core.BaseImageOptions{
			// Mirrors what cmd/pokkum's --static flag reconciliation computes
			// (see TestBuildStaticStrategyReconciliation in
			// cmd/pokkum/build_test.go): the static-appropriate preset/ref.
			// core.Build itself does no strategy-specific preset defaulting
			// (that's a CLI-only concern), so the caller must supply this.
			Preset: core.BaseImageChainguard,
			Ref:    core.StaticBaseRef,
		},
		SBOM:            core.SBOMOptions{Format: ports.SBOMFormatSPDXJSON, AttachMode: ports.SBOMAttachTag, NoAttach: false},
		SourceDateEpoch: testEpoch,
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		t.Fatalf("core.Build failed: %v", err)
	}

	// 1. Assert result metadata.
	if res.Image.Digest.Hex == "" {
		t.Errorf("BuildResult image digest is empty")
	}
	if !res.Image.IsIndex {
		t.Errorf("BuildResult isIndex should be true for multi-platform build")
	}
	if res.SBOM == nil {
		t.Fatalf("BuildResult SBOM is nil")
	}
	if res.Toolchain.StaticServerVersion == "" {
		t.Errorf("BuildResult Toolchain.StaticServerVersion is empty, want mockStaticServerProvider's version")
	}
	if res.Toolchain.SupervisorVersion != "" {
		t.Errorf("BuildResult Toolchain.SupervisorVersion = %q, want empty for a static build (a static build must not report pokkum-init's version)", res.Toolchain.SupervisorVersion)
	}

	// 2. Base-image threading: assert what core.Build actually sent to the
	// resolver (see judgment-call note in the test doc comment above).
	recordedReq, calls := baseResolver.recorded()
	if calls == 0 {
		t.Fatalf("recordingBaseResolver.Resolve was never called")
	}
	if recordedReq.Preset != core.BaseImageChainguard {
		t.Errorf("BaseImageRequest.Preset = %q, want %q (threaded from BuildRequest.BaseImage.Preset)", recordedReq.Preset, core.BaseImageChainguard)
	}
	if recordedReq.Ref != core.StaticBaseRef {
		t.Errorf("BaseImageRequest.Ref = %q, want %q (threaded from BuildRequest.BaseImage.Ref)", recordedReq.Ref, core.StaticBaseRef)
	}
	if !recordedReq.AllowStatic {
		t.Errorf("BaseImageRequest.AllowStatic = false, want true — core.Build must derive AllowStatic from req.Compile.Strategy.ApplyStatic() for a static build, not leave it at the caller's (unset) default")
	}

	// 3. Assert multi-arch image index pushed to registry.
	indexRef := repoName + ":latest"
	idx := harness.FetchIndex(t, indexRef)
	idxManifest, err := idx.IndexManifest()
	if err != nil {
		t.Fatalf("FetchIndex IndexManifest: %v", err)
	}
	if len(idxManifest.Manifests) != 2 {
		t.Errorf("Index child manifests count = %d, want 2", len(idxManifest.Manifests))
	}

	// 4. Inspect the amd64 child image.
	amd64Digest := idxManifest.Manifests[0].Digest.String()
	childImageRef := repoName + "@" + amd64Digest
	img := harness.FetchImage(t, childImageRef)

	cfg, _ := harness.FetchConfigFile(t, img)
	if cfg.Architecture != "amd64" {
		t.Errorf("Child image architecture = %s, want amd64", cfg.Architecture)
	}
	if len(cfg.Config.Entrypoint) != 1 || cfg.Config.Entrypoint[0] != ports.StaticServerPath {
		t.Errorf("Child image Entrypoint = %v, want [%s] (a static image is its own PID 1, no supervisor)", cfg.Config.Entrypoint, ports.StaticServerPath)
	}
	if got := cfg.Config.Env; !envContains(got, ports.EnvStaticRoots, ports.AppClientDirPrefix+":"+ports.AppPrerenderedDirPrefix) {
		t.Errorf("Child image Env = %v, want an entry %s=%s", got, ports.EnvStaticRoots, ports.AppClientDirPrefix+":"+ports.AppPrerenderedDirPrefix)
	}

	// 5. Layer identity: no Bun layer, no supervisor layer; static-server
	// layer present.
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("img.Layers: %v", err)
	}
	// base (1) + static-server (1) + client (1) + prerendered (1) = 4. See
	// packager.go's `else if req.Strategy.ApplyStatic()` branch: exactly
	// these addenda are appended, in this order, for a static build.
	if len(layers) != 4 {
		t.Fatalf("img.Layers count = %d, want 4 (base, static-server, client, prerendered)", len(layers))
	}

	for i, h := range cfg.History {
		if strings.Contains(h.CreatedBy, ports.BunBinaryPath) {
			t.Errorf("History[%d].CreatedBy = %q: a static image must carry no Bun-runtime layer", i, h.CreatedBy)
		}
		if strings.Contains(h.CreatedBy, ports.SupervisorPath) {
			t.Errorf("History[%d].CreatedBy = %q: a static image must carry no supervisor layer", i, h.CreatedBy)
		}
	}
	var sawStaticServerHistory, sawClientHistory, sawPrerenderedHistory bool
	for _, h := range cfg.History {
		switch {
		case strings.Contains(h.CreatedBy, ports.StaticServerPath):
			sawStaticServerHistory = true
		case strings.Contains(h.CreatedBy, ports.AppClientDirPrefix):
			sawClientHistory = true
		case strings.Contains(h.CreatedBy, ports.AppPrerenderedDirPrefix):
			sawPrerenderedHistory = true
		}
	}
	if !sawStaticServerHistory {
		t.Errorf("no History entry with CreatedBy containing %q found; static-server binary layer is missing", ports.StaticServerPath)
	}
	if !sawClientHistory {
		t.Errorf("no History entry with CreatedBy containing %q found; client layer is missing", ports.AppClientDirPrefix)
	}
	if !sawPrerenderedHistory {
		t.Errorf("no History entry with CreatedBy containing %q found; prerendered layer is missing", ports.AppPrerenderedDirPrefix)
	}

	// Layer order is fixed by packager.go's static-strategy branch (`else if
	// req.Strategy.ApplyStatic()`): base image layer(s) first (1, from
	// SyntheticBaseImage), then addenda in the order appended — static
	// server, client, prerendered. This mirrors how TestFixtureDrivenE2E
	// (the Exe-strategy test) identifies layers[1]/layers[2] positionally
	// rather than via History, since go-containerregistry's ConfigFile.History
	// alignment to Layers() is an implementation detail this test doesn't
	// need to depend on for layer *selection* (History is still used above
	// for the no-Bun/no-supervisor content check, which only needs the
	// strings, not index correspondence).
	staticServerLayer := layers[1]
	clientLayer := layers[2]
	prerenderedLayer := layers[3]

	staticServerMembers := harness.FetchLayerMembers(t, staticServerLayer)
	assertMemberContains(t, staticServerMembers, strings.TrimPrefix(ports.StaticServerPath, "/"))

	// Also confirm no layer's actual file members carry a Bun binary or
	// pokkum-init path, not just the History strings.
	for _, l := range layers {
		members := harness.FetchLayerMembers(t, l)
		for _, mem := range members {
			if mem.Name == strings.TrimPrefix(ports.SupervisorPath, "/") {
				t.Errorf("found supervisor binary %q in a static image layer", mem.Name)
			}
			if mem.Name == strings.TrimPrefix(ports.BunBinaryPath, "/") {
				t.Errorf("found Bun binary %q in a static image layer", mem.Name)
			}
		}
	}

	// 6. Client layer contains the hashed immutable chunk + plain asset +
	// their precompression sidecars (proving the fixture content was large
	// and repetitive enough to trigger precompressutils).
	clientMembers := harness.FetchLayerMembers(t, clientLayer)
	wantClientFiles := []string{
		"app/client/app.js",
		"app/client/immutable/chunks/app-a1b2c3d4.js",
		"app/client/immutable/chunks/app-a1b2c3d4.js.gz",
		"app/client/immutable/chunks/app-a1b2c3d4.js.br",
		"app/client/immutable/chunks/app-a1b2c3d4.js.zst",
		"app/client/favicon.svg",
		"app/client/favicon.svg.gz",
		"app/client/favicon.svg.br",
		"app/client/favicon.svg.zst",
	}
	for _, want := range wantClientFiles {
		assertMemberContains(t, clientMembers, want)
	}

	// 7. Prerendered layer contains index.html + sidecars.
	prerenderedMembers := harness.FetchLayerMembers(t, prerenderedLayer)
	wantPrerenderedFiles := []string{
		"app/prerendered/index.html",
		"app/prerendered/index.html.gz",
		"app/prerendered/index.html.br",
		"app/prerendered/index.html.zst",
	}
	for _, want := range wantPrerenderedFiles {
		assertMemberContains(t, prerenderedMembers, want)
	}

	// 8. Verify attached SBOM document in test registry.
	indexDigest, err := idx.Digest()
	if err != nil {
		t.Fatalf("idx.Digest: %v", err)
	}
	_, sbomContent := harness.FetchAttachedSBOM(t, repoName, indexDigest)
	if len(sbomContent) == 0 {
		t.Errorf("Attached SBOM content is empty")
	}

	// 9. Verify request traffic on harness, and that the pushed image really
	// is fetchable back out (what a follow-up ExtractLayerFiles-based test
	// needs).
	if harness.RequestCount() == 0 {
		t.Errorf("Expected HTTP requests to be recorded by registry harness, got 0")
	}
	reFetchedIdx := harness.FetchIndex(t, indexRef)
	if _, err := reFetchedIdx.Digest(); err != nil {
		t.Errorf("re-fetching pushed index by ref failed: %v", err)
	}
}

func assertMemberContains(t *testing.T, members []TarMember, wantName string) {
	t.Helper()
	for _, m := range members {
		if m.Name == wantName {
			return
		}
	}
	names := make([]string, len(members))
	for i, m := range members {
		names[i] = m.Name
	}
	t.Errorf("layer members do not contain %q; got %v", wantName, names)
}

func envContains(env []string, key, val string) bool {
	want := key + "=" + val
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

// TestFixtureDrivenE2E_Static_SPAFallback is the SPA-fallback counterpart to
// TestFixtureDrivenE2E_Static: it drives the same full core.Build static
// pipeline, but the fixture compiles a project that configures an
// adapter-static SPA fallback (client/200.html), and it asserts that the SPA
// shell is staged into the client layer (with real .gz/.br/.zst sidecars) and
// surfaced to pokkum-static via the POKKUM_STATIC_FALLBACK image env — never
// silently dropped.
func TestFixtureDrivenE2E_Static_SPAFallback(t *testing.T) {
	harness := NewRegistryHarness(t)
	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "sveltekit-basic"))
	if err != nil {
		t.Fatalf("Abs fixture path: %v", err)
	}

	repoName := harness.Repo("sveltekit-static-spa-app")

	deps := core.Deps{
		Compiler:   &staticFixtureCompiler{fallback: "200.html"},
		BaseImages: &recordingBaseResolver{t: t},
		// Deps.validate requires Supervisor to be non-nil even though a static
		// build never calls it — see TestFixtureDrivenE2E_Static's notes.
		Supervisor:      &mockSupervisorProvider{},
		StaticServer:    &mockStaticServerProvider{},
		Packager:        packager.NewPackager(testLogger()),
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
		// Two platforms so the push produces a multi-platform image index
		// (FetchIndex below expects an index, matching the original static e2e).
		Platforms: []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64},
		Compile:   core.CompileOptions{Strategy: core.StrategyStatic},
		Output:    core.OutputOptions{Mode: core.OutputPush},
		Insecure:  true,
		BaseImage: core.BaseImageOptions{
			Preset: core.BaseImageChainguard,
			Ref:    core.StaticBaseRef,
		},
		SBOM:            core.SBOMOptions{Format: ports.SBOMFormatSPDXJSON, AttachMode: ports.SBOMAttachTag, NoAttach: false},
		SourceDateEpoch: testEpoch,
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		t.Fatalf("core.Build failed: %v", err)
	}
	if res.Image.Digest.Hex == "" {
		t.Errorf("BuildResult image digest is empty")
	}

	// Fetch the pushed amd64 child image.
	imageRef := repoName + ":latest"
	idx := harness.FetchIndex(t, imageRef)
	idxManifest, err := idx.IndexManifest()
	if err != nil {
		t.Fatalf("FetchIndex IndexManifest: %v", err)
	}
	childRef := repoName + "@" + idxManifest.Manifests[0].Digest.String()
	img := harness.FetchImage(t, childRef)
	cfg, _ := harness.FetchConfigFile(t, img)

	// 1. The SPA fallback is surfaced to pokkum-static via image env.
	if !envContains(cfg.Config.Env, ports.EnvStaticFallback, ports.AppClientDirPrefix+"/200.html") {
		t.Errorf("Child image Env = %v, want an entry %s=%s", cfg.Config.Env, ports.EnvStaticFallback, ports.AppClientDirPrefix+"/200.html")
	}
	// Static roots are still stamped alongside the fallback.
	if !envContains(cfg.Config.Env, ports.EnvStaticRoots, ports.AppClientDirPrefix+":"+ports.AppPrerenderedDirPrefix) {
		t.Errorf("Child image Env = %v, want the static roots entry unchanged", cfg.Config.Env)
	}

	// 2. The SPA shell is staged in the client layer (with real sidecars) — not
	// silently dropped. Layer order for static: base, static-server, client,
	// prerendered (see packager.go's static branch).
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("img.Layers: %v", err)
	}
	if len(layers) != 4 {
		t.Fatalf("img.Layers count = %d, want 4 (base, static-server, client, prerendered)", len(layers))
	}
	clientMembers := harness.FetchLayerMembers(t, layers[2])
	for _, want := range []string{
		"app/client/200.html",
		"app/client/200.html.gz",
		"app/client/200.html.br",
		"app/client/200.html.zst",
	} {
		assertMemberContains(t, clientMembers, want)
	}
}
