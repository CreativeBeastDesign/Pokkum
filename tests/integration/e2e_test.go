package integration

import (
	"context"
	"path/filepath"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/packager"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registry"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sbom"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
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
	entrypoint := filepath.Join(req.ProjectDir, ".svelte-kit", "jesterkit-sveltekit", "temp-server", "index.ts")
	outputDir := filepath.Join(req.ProjectDir, ".svelte-kit", "jesterkit-sveltekit")
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
		Ref:       "gcr.io/distroless/cc-debian12:nonroot",
		PinnedRef: "gcr.io/distroless/cc-debian12:nonroot@sha256:11223344556677889900aabbccddeeff11223344556677889900aabbccddeeff",
		Digest:    v1.Hash{Algorithm: "sha256", Hex: "11223344556677889900aabbccddeeff11223344556677889900aabbccddeeff"},
		Images:    images,
		IsIndex:   len(req.Platforms) > 1,
	}, nil
}

// mockSupervisorProvider provides a fixed supervisor binary for testing.
type mockSupervisorProvider struct{}

func (m *mockSupervisorProvider) Binary(_ context.Context, _ ports.Platform) ([]byte, error) {
	return append([]byte(nil), fakeSupervisorContent...), nil
}

func (m *mockSupervisorProvider) Version(_ context.Context) (string, error) {
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
		Compiler:   &mockCompiler{},
		BaseImages: &mockBaseResolver{t: t},
		Supervisor: &mockSupervisorProvider{},
		Packager:   packager.NewPackager(testLogger()),
		Registry:   registry.NewAdapter(testLogger()),
		SBOM:       sbom.NewGenerator(testLogger()),
		Logger:     testLogger(),
		Version:    "0.1.0-integration-test",
	}

	req := core.BuildRequest{
		ProjectDir:      fixtureDir,
		Repo:            repoName,
		Tags:            []string{"latest", "1.0.0"},
		Platforms:       []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64},
		Output:          core.OutputOptions{Mode: core.OutputPush},
		Insecure:        true,
		SBOM:            core.SBOMOptions{Format: ports.SBOMFormatSPDXJSON, NoAttach: false},
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
	if !supBin.ModTime.Equal(testEpoch) {
		t.Errorf("Supervisor layer timestamp = %v, want %v", supBin.ModTime, testEpoch)
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
		Compiler:   &mockCompiler{},
		BaseImages: &mockBaseResolver{t: t},
		Supervisor: &mockSupervisorProvider{},
		Packager:   packager.NewPackager(testLogger()),
		Registry:   registry.NewAdapter(testLogger()),
		SBOM:       sbom.NewGenerator(testLogger()),
		Logger:     testLogger(),
		Version:    "0.1.0-integration-test",
	}

	req := core.BuildRequest{
		ProjectDir:      fixtureDir,
		Repo:            repoName,
		Tags:            []string{"v1"},
		Platforms:       []ports.Platform{ports.LinuxAMD64},
		Output:          core.OutputOptions{Mode: core.OutputPush},
		Insecure:        true,
		SBOM:            core.SBOMOptions{Format: ports.SBOMFormatSPDXJSON},
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
