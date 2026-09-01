package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/bunruntime"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/nativeinspect"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registry"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sbom"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Port interface conformance assertions for mock implementations.
var (
	_ ports.Compiler             = (*mockCompiler)(nil)
	_ ports.BaseImageResolver    = (*mockBaseResolver)(nil)
	_ ports.SupervisorProvider   = (*mockSupervisorProvider)(nil)
	_ ports.StaticServerProvider = (*mockStaticServerProvider)(nil)
)

// mockCompiler simulates the Compiler port for fixture testing.
type mockCompiler struct{}

func (m *mockCompiler) Preflight(_ context.Context, req ports.PreflightRequest) (ports.PreflightResult, error) {
	return ports.PreflightResult{
		BunPath:          "/usr/local/bin/bun",
		BunVersion:       "1.2.18",
		AdapterVersion:   "0.1.7",
		SvelteKitVersion: "2.15.0",
	}, nil
}

func (m *mockCompiler) Prepare(_ context.Context, req ports.PrepareRequest) (ports.PrepareResult, error) {
	var (
		entrypoint string
		outputDir  string
	)

	switch req.Strategy {
	case ports.StrategyLayered:
		// Mirrors bunexec.Compiler.Prepare's non-exe path shape:
		// outputDir = <ProjectDir>/build, no single-file entrypoint.
		outputDir = filepath.Join(req.ProjectDir, "build")
		entrypoint = filepath.Join(outputDir, "index.js")
	case ports.StrategyStatic:
		// Mirrors bunexec.Compiler.Prepare's static path: no server
		// entrypoint, outputDir is the SvelteKit static staging tree.
		outputDir = filepath.Join(req.ProjectDir, ".svelte-kit", "output")
		entrypoint = ""
	default: // ports.StrategyExe
		entrypoint = filepath.Join(req.ProjectDir, ".svelte-kit", "jesterkit-sveltekit", "temp-server", "index.ts")
		outputDir = filepath.Join(req.ProjectDir, ".svelte-kit", "jesterkit-sveltekit")
	}

	// StrategyExe's packaging path never reads OutputDir — it only consumes
	// the single compiled binary returned by Compile — so behavior there must
	// stay exactly as before: no directories are created on disk. Only
	// StrategyLayered and StrategyStatic need real fixture trees, since the
	// packager unconditionally walks AppServerDir (layered) and
	// AppPrerenderedDir (layered + static); see
	// internal/adapters/packager/packager.go's validatePackageRequest and
	// buildPrerenderedAddenda.
	if req.Strategy != ports.StrategyExe {
		if req.Strategy == ports.StrategyLayered {
			// Mirrors a real @sveltejs/adapter-node build's actual shape: the
			// entrypoint (index.js) sits at outputDir's top level, a SIBLING
			// of server/ — not inside it. AppServerDir (internal/core/pipeline.go)
			// packages the whole outputDir now, precisely because a real build's
			// entrypoint lives here and chunk files inside server/ reach back
			// out via relative paths that only resolve correctly when this
			// nesting is preserved exactly. This fixture used to place index.js
			// inside server/ instead, which never modeled the real shape and is
			// part of why the missing-entrypoint bug this mirrors went
			// undetected — see Lessons.md.
			serverDir := filepath.Join(outputDir, "server")
			if err := os.MkdirAll(serverDir, 0o755); err != nil {
				return ports.PrepareResult{}, fmt.Errorf("mockCompiler: prepare: mkdir server dir: %w", err)
			}
			if err := os.WriteFile(filepath.Join(outputDir, "index.js"), []byte("import './server/chunks/fixture.js';\nexport default { fetch() {} };\n"), 0o644); err != nil {
				return ports.PrepareResult{}, fmt.Errorf("mockCompiler: prepare: write entrypoint fixture: %w", err)
			}
			chunksDir := filepath.Join(serverDir, "chunks")
			if err := os.MkdirAll(chunksDir, 0o755); err != nil {
				return ports.PrepareResult{}, fmt.Errorf("mockCompiler: prepare: mkdir chunks dir: %w", err)
			}
			if err := os.WriteFile(filepath.Join(chunksDir, "fixture.js"), []byte("export default {};\n"), 0o644); err != nil {
				return ports.PrepareResult{}, fmt.Errorf("mockCompiler: prepare: write chunk fixture: %w", err)
			}

			vendorDir := filepath.Join(outputDir, "vendor")
			if err := os.MkdirAll(vendorDir, 0o755); err != nil {
				return ports.PrepareResult{}, fmt.Errorf("mockCompiler: prepare: mkdir vendor dir: %w", err)
			}
			if err := os.WriteFile(filepath.Join(vendorDir, "package.json"), []byte(`{"name":"fixture-vendor"}`), 0o644); err != nil {
				return ports.PrepareResult{}, fmt.Errorf("mockCompiler: prepare: write vendor fixture: %w", err)
			}

			nativeDir := filepath.Join(outputDir, "native")
			if err := os.MkdirAll(nativeDir, 0o755); err != nil {
				return ports.PrepareResult{}, fmt.Errorf("mockCompiler: prepare: mkdir native dir: %w", err)
			}
			if err := os.WriteFile(filepath.Join(nativeDir, "fixture.node"), []byte("fake-native-module"), 0o644); err != nil {
				return ports.PrepareResult{}, fmt.Errorf("mockCompiler: prepare: write native fixture: %w", err)
			}
		}

		// Both StrategyLayered and StrategyStatic package a client dir and a
		// prerendered dir.
		clientDir := filepath.Join(outputDir, "client")
		if err := os.MkdirAll(clientDir, 0o755); err != nil {
			return ports.PrepareResult{}, fmt.Errorf("mockCompiler: prepare: mkdir client dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(clientDir, "app.js"), []byte("console.log('fixture client bundle');\n"), 0o644); err != nil {
			return ports.PrepareResult{}, fmt.Errorf("mockCompiler: prepare: write client fixture: %w", err)
		}

		prerenderedDir := filepath.Join(outputDir, "prerendered")
		if err := os.MkdirAll(prerenderedDir, 0o755); err != nil {
			return ports.PrepareResult{}, fmt.Errorf("mockCompiler: prepare: mkdir prerendered dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(prerenderedDir, "index.html"), fakePrerenderedHTML, 0o644); err != nil {
			return ports.PrepareResult{}, fmt.Errorf("mockCompiler: prepare: write prerendered fixture: %w", err)
		}
	}

	return ports.PrepareResult{
		EntrypointPath: entrypoint,
		OutputDir:      outputDir,
	}, nil
}

func (m *mockCompiler) Compile(_ context.Context, req ports.CompileRequest) (ports.Artifact, error) {
	outPath := writeTempBinary(&testing.T{}, "server-"+req.Platform.Arch, fakeAppContent)
	return ports.Artifact{
		Platform: req.Platform,
		Path:     outPath,
		Size:     int64(len(fakeAppContent)),
		SHA256:   "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2",
	}, nil
}

// mockBaseResolver resolves synthetic base images without network dependencies.
type mockBaseResolver struct {
	t *testing.T
}

func (m *mockBaseResolver) Resolve(_ context.Context, req ports.BaseImageRequest) (*ports.BaseImage, error) {
	images := make(map[ports.Platform]v1.Image, len(req.Platforms))
	for _, p := range req.Platforms {
		images[p] = SyntheticBaseImage(m.t, p)
	}
	return &ports.BaseImage{
		// UpstreamRef mirrors what the real resolver does (`upstreamRef := ref`
		// in baseimage/resolver.go): it starts equal to the requested
		// reference and is never rebound to a mirror or a locked digest. It is
		// what org.opencontainers.image.base.name records, so a stub omitting
		// it does not model the contract and sends the label down a fallback
		// path production never takes.
		//
		// PinnedRef is the repository name plus the digest, with the TAG
		// STRIPPED — `pinnedRef()` builds it from `parsedRef.Context().Name()`,
		// which never yields "repo:tag@sha256:…". This stub previously
		// fabricated that impossible shape (self-review checklist row 12).
		Ref:         "gcr.io/distroless/cc-debian12:nonroot",
		UpstreamRef: "gcr.io/distroless/cc-debian12:nonroot",
		PinnedRef:   "gcr.io/distroless/cc-debian12@sha256:11223344556677889900aabbccddeeff11223344556677889900aabbccddeeff",
		Digest:      v1.Hash{Algorithm: "sha256", Hex: "11223344556677889900aabbccddeeff11223344556677889900aabbccddeeff"},
		Images:      images,
		IsIndex:     len(req.Platforms) > 1,
	}, nil
}

func (m *mockBaseResolver) RecordScanResult(_ context.Context, _ string, _ ports.BaseImagePreset, _ string, _ ports.ScanResult) error {
	return nil
}

func (m *mockBaseResolver) VerifyBaseImage(_ context.Context, _ *ports.BaseImage, _ ports.BaseImageRequest) error {
	return nil
}

// mockSupervisorProvider provides a fixed supervisor binary for testing.
type mockSupervisorProvider struct{}

func (m *mockSupervisorProvider) Binary(_ context.Context, _ ports.Platform) ([]byte, error) {
	return append([]byte(nil), fakeSupervisorContent...), nil
}

func (m *mockSupervisorProvider) Version(_ context.Context) (string, error) {
	return "0.1.0-test", nil
}

// mockStaticServerProvider provides a fixed pokkum-static binary for testing,
// so StrategyStatic builds can populate ports.PackageRequest.StaticServer
// without exercising the real internal/adapters/staticserver embedding.
type mockStaticServerProvider struct{}

func (m *mockStaticServerProvider) Binary(_ context.Context, _ ports.Platform) ([]byte, error) {
	return append([]byte(nil), fakeStaticServerContent...), nil
}

func (m *mockStaticServerProvider) Version(_ context.Context) (string, error) {
	return "0.1.0-test", nil
}

// TestFixtureDrivenE2E exercises the full core.Build orchestration —
// resolving the base image, packaging every platform, building the index,
// generating and attaching an SBOM, and publishing to a registry — against
// the on-disk sveltekit-basic fixture. Despite the name, it does NOT invoke
// bun: Compiler is mockCompiler above, which never runs `bun run build` or
// `bun build --compile` and returns a fixed fakeAppContent binary regardless
// of what the fixture actually contains. The fixture directory is used only
// as a plausible ProjectDir value threaded through the request. This test is
// therefore a real end-to-end check of the packaging/registry/SBOM pipeline,
// but not of the SvelteKit/Bun toolchain — see
// TestRealBuildIsReproducibleAcrossRuns in reproducibility_e2e_test.go for
// the test that actually runs bun against this fixture.
func TestFixtureDrivenE2E(t *testing.T) {
	harness := NewRegistryHarness(t)
	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "sveltekit-basic"))
	if err != nil {
		t.Fatalf("Abs fixture path: %v", err)
	}

	repoName := harness.Repo("sveltekit-basic-app")

	deps := core.Deps{
		Compiler:        &mockCompiler{},
		BaseImages:      &mockBaseResolver{t: t},
		Supervisor:      &mockSupervisorProvider{},
		Packager:        packager.NewPackager(testLogger()),
		BunRuntime:      bunruntime.NewResolver("", nil),
		Registry:        registry.NewAdapter(testLogger()),
		SBOM:            sbom.NewGenerator(testLogger()),
		NativeInspector: nativeinspect.NewClosuredAdapter(),
		Logger:          testLogger(),
		Version:         "0.1.0-integration-test",
	}

	req := core.BuildRequest{
		ProjectDir:      fixtureDir,
		Repo:            repoName,
		Tags:            []string{"latest", "1.0.0"},
		Platforms:       []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64},
		Compile:         core.CompileOptions{Strategy: core.StrategyExe},
		Output:          core.OutputOptions{Mode: core.OutputPush},
		Insecure:        true,
		SBOM:            core.SBOMOptions{Format: ports.SBOMFormatSPDXJSON, AttachMode: ports.SBOMAttachTag, NoAttach: false},
		SourceDateEpoch: testEpoch,
	}

	opts := core.BuildOptions{}

	res, err := core.Build(context.Background(), deps, req, opts)
	if err != nil {
		t.Fatalf("core.Build failed: %v", err)
	}

	// 1. Assert result metadata
	if res.Image.Digest.Hex == "" {
		t.Errorf("BuildResult image digest is empty")
	}
	if !res.Image.IsIndex {
		t.Errorf("BuildResult isIndex should be true for multi-platform build")
	}
	if res.SBOM == nil {
		t.Fatalf("BuildResult SBOM is nil")
	}

	// 2. Assert multi-arch image index pushed to registry
	indexRef := repoName + ":latest"
	idx := harness.FetchIndex(t, indexRef)
	idxManifest, err := idx.IndexManifest()
	if err != nil {
		t.Fatalf("FetchIndex IndexManifest: %v", err)
	}
	if len(idxManifest.Manifests) != 2 {
		t.Errorf("Index child manifests count = %d, want 2", len(idxManifest.Manifests))
	}

	// 3. Inspect child image for linux/amd64
	amd64Digest := idxManifest.Manifests[0].Digest.String()
	childImageRef := repoName + "@" + amd64Digest
	img := harness.FetchImage(t, childImageRef)

	cfg, _ := harness.FetchConfigFile(t, img)
	if cfg.Architecture != "amd64" {
		t.Errorf("Child image architecture = %s, want amd64", cfg.Architecture)
	}
	if cfg.Config.User != "65532:65532" {
		t.Errorf("Child image User = %s, want 65532:65532", cfg.Config.User)
	}
	if len(cfg.Config.Entrypoint) != 3 || cfg.Config.Entrypoint[0] != "/pokkum/init" {
		t.Errorf("Child image Entrypoint = %v, want [/pokkum/init -- /app/server]", cfg.Config.Entrypoint)
	}

	// 4. Verify layers inside the pushed image
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("img.Layers: %v", err)
	}
	if len(layers) != 3 {
		t.Fatalf("img.Layers count = %d, want 3 (base, supervisor, app)", len(layers))
	}

	// Supervisor layer check
	supMembers := harness.FetchLayerMembers(t, layers[1])
	var supBin *TarMember
	for i := range supMembers {
		if supMembers[i].Name == "pokkum/init" {
			supBin = &supMembers[i]
			break
		}
	}
	if supBin == nil {
		t.Fatalf("Supervisor binary pokkum/init not found in layer members: %v", supMembers)
	}
	if supBin.Mode != 0o555 && supBin.Mode != 0o755 {
		t.Errorf("Supervisor binary mode = %o, want 0555 or 0755", supBin.Mode)
	}
	// The supervisor is an immutable embedded binary, so its layer is pinned to
	// a fixed epoch rather than SOURCE_DATE_EPOCH (see packager's
	// pinnedImmutableBinaryEpoch and Roadmap item 3f). Deriving it from the
	// build timestamp gave the ~90MB Bun layer and this one a fresh digest on
	// every commit, defeating both the local layer cache and registry-side
	// dedup across a fleet. The app layer below still asserts testEpoch, since
	// that content genuinely reflects the source snapshot.
	if !supBin.ModTime.Equal(time.Unix(0, 0).UTC()) {
		t.Errorf("Supervisor layer timestamp = %v, want the pinned immutable-binary epoch %v", supBin.ModTime, time.Unix(0, 0).UTC())
	}

	// App layer check
	appMembers := harness.FetchLayerMembers(t, layers[2])
	var appBin *TarMember
	for i := range appMembers {
		if appMembers[i].Name == "app/server" {
			appBin = &appMembers[i]
			break
		}
	}
	if appBin == nil {
		t.Fatalf("App binary app/server not found in layer members: %v", appMembers)
	}
	if appBin.Mode != 0o555 && appBin.Mode != 0o755 {
		t.Errorf("App binary mode = %o, want 0555 or 0755", appBin.Mode)
	}

	// 5. Verify attached SBOM document in test registry
	indexDigest, err := idx.Digest()
	if err != nil {
		t.Fatalf("idx.Digest: %v", err)
	}
	_, sbomContent := harness.FetchAttachedSBOM(t, repoName, indexDigest)
	if len(sbomContent) == 0 {
		t.Errorf("Attached SBOM content is empty")
	}

	// 6. Verify request traffic on harness
	if harness.RequestCount() == 0 {
		t.Errorf("Expected HTTP requests to be recorded by registry harness, got 0")
	}
}

func TestE2ESinglePlatformPush(t *testing.T) {
	harness := NewRegistryHarness(t)
	fixtureDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "sveltekit-basic"))
	if err != nil {
		t.Fatalf("Abs fixture path: %v", err)
	}

	repoName := harness.Repo("single-arch-app")

	deps := core.Deps{
		Compiler:        &mockCompiler{},
		BaseImages:      &mockBaseResolver{t: t},
		Supervisor:      &mockSupervisorProvider{},
		Packager:        packager.NewPackager(testLogger()),
		BunRuntime:      bunruntime.NewResolver("", nil),
		Registry:        registry.NewAdapter(testLogger()),
		SBOM:            sbom.NewGenerator(testLogger()),
		NativeInspector: nativeinspect.NewClosuredAdapter(),
		Logger:          testLogger(),
		Version:         "0.1.0-integration-test",
	}

	req := core.BuildRequest{
		ProjectDir:      fixtureDir,
		Repo:            repoName,
		Tags:            []string{"v1"},
		Platforms:       []ports.Platform{ports.LinuxAMD64},
		Compile:         core.CompileOptions{Strategy: core.StrategyExe},
		Output:          core.OutputOptions{Mode: core.OutputPush},
		Insecure:        true,
		SBOM:            core.SBOMOptions{Format: ports.SBOMFormatSPDXJSON, AttachMode: ports.SBOMAttachTag},
		SourceDateEpoch: testEpoch,
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		t.Fatalf("core.Build single-platform failed: %v", err)
	}

	if res.Image.IsIndex {
		t.Errorf("Single-platform build should not be an index")
	}

	// Fetch single image from registry
	imgRef := repoName + ":v1"
	m, _ := harness.FetchManifest(t, imgRef)
	if m.SchemaVersion != 2 {
		t.Errorf("Manifest SchemaVersion = %d, want 2", m.SchemaVersion)
	}
	if len(m.Layers) != 3 {
		t.Errorf("Manifest layers count = %d, want 3", len(m.Layers))
	}
}
