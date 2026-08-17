package core_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Mock implementation of ports.Compiler
type mockCompiler struct {
	preflightFn func(ctx context.Context, req ports.PreflightRequest) (ports.PreflightResult, error)
	prepareFn   func(ctx context.Context, req ports.PrepareRequest) (ports.PrepareResult, error)
	compileFn   func(ctx context.Context, req ports.CompileRequest) (ports.Artifact, error)
}

func (m *mockCompiler) Preflight(ctx context.Context, req ports.PreflightRequest) (ports.PreflightResult, error) {
	if m.preflightFn != nil {
		return m.preflightFn(ctx, req)
	}
	return ports.PreflightResult{
		BunVersion:       "1.2.18",
		BunPath:          "/usr/local/bin/bun",
		AdapterVersion:   "0.1.0",
		SvelteKitVersion: "2.0.0",
	}, nil
}

func (m *mockCompiler) Prepare(ctx context.Context, req ports.PrepareRequest) (ports.PrepareResult, error) {
	if m.prepareFn != nil {
		return m.prepareFn(ctx, req)
	}
	return ports.PrepareResult{
		EntrypointPath: "/tmp/project/build/index.js",
	}, nil
}

func (m *mockCompiler) Compile(ctx context.Context, req ports.CompileRequest) (ports.Artifact, error) {
	if m.compileFn != nil {
		return m.compileFn(ctx, req)
	}
	return ports.Artifact{
		Platform: req.Platform,
		Path:     req.OutputPath,
		Size:     1024 * 1024,
		SHA256:   "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
	}, nil
}

// Mock implementation of ports.BaseImageResolver
type mockBaseImageResolver struct {
	resolveFn          func(ctx context.Context, req ports.BaseImageRequest) (*ports.BaseImage, error)
	recordScanResultFn func(ctx context.Context, lockfilePath string, preset ports.BaseImagePreset, scan ports.ScanResult) error
	verifyBaseImageFn  func(ctx context.Context, resolved *ports.BaseImage, req ports.BaseImageRequest) error
}

func (m *mockBaseImageResolver) Resolve(ctx context.Context, req ports.BaseImageRequest) (*ports.BaseImage, error) {
	if m.resolveFn != nil {
		return m.resolveFn(ctx, req)
	}
	img, _ := mutate.ConfigFile(empty.Image, &v1.ConfigFile{
		Architecture: "amd64",
		OS:           "linux",
	})
	images := make(map[core.Platform]v1.Image, len(req.Platforms))
	for _, p := range req.Platforms {
		images[p] = img
	}
	return &ports.BaseImage{
		Ref:       req.Ref,
		PinnedRef: req.Ref + "@sha256:1111222233334444555566667777888811112222333344445555666677778888",
		Digest:    v1.Hash{Algorithm: "sha256", Hex: "1111222233334444555566667777888811112222333344445555666677778888"},
		Images:    images,
		IsIndex:   true,
	}, nil
}

func (m *mockBaseImageResolver) RecordScanResult(ctx context.Context, lockfilePath string, preset ports.BaseImagePreset, scan ports.ScanResult) error {
	if m.recordScanResultFn != nil {
		return m.recordScanResultFn(ctx, lockfilePath, preset, scan)
	}
	return nil
}

func (m *mockBaseImageResolver) VerifyBaseImage(ctx context.Context, resolved *ports.BaseImage, req ports.BaseImageRequest) error {
	if m.verifyBaseImageFn != nil {
		return m.verifyBaseImageFn(ctx, resolved, req)
	}
	return nil
}

// Mock implementation of ports.Scanner
type mockScanner struct {
	scanFn func(ctx context.Context, req ports.ScanRequest) (ports.ScanResult, error)
}

func (m *mockScanner) Scan(ctx context.Context, req ports.ScanRequest) (ports.ScanResult, error) {
	if m.scanFn != nil {
		return m.scanFn(ctx, req)
	}
	return ports.ScanResult{
		Target:           req.Target,
		Passed:           true,
		MaxSeverityFound: ports.SeverityLow,
	}, nil
}

// Mock implementation of ports.SupervisorProvider
type mockSupervisorProvider struct {
	versionFn func(ctx context.Context) (string, error)
	binaryFn  func(ctx context.Context, p core.Platform) ([]byte, error)
}

func (m *mockSupervisorProvider) Version(ctx context.Context) (string, error) {
	if m.versionFn != nil {
		return m.versionFn(ctx)
	}
	return "v0.1.0", nil
}

func (m *mockSupervisorProvider) Binary(ctx context.Context, p core.Platform) ([]byte, error) {
	if m.binaryFn != nil {
		return m.binaryFn(ctx, p)
	}
	return []byte("mock-supervisor-binary"), nil
}

// Mock implementation of ports.Packager
type mockPackager struct {
	buildFn func(ctx context.Context, req ports.PackageRequest) (v1.Image, error)
	indexFn func(ctx context.Context, req ports.IndexRequest) (v1.ImageIndex, error)
}

func (m *mockPackager) Build(ctx context.Context, req ports.PackageRequest) (v1.Image, error) {
	if m.buildFn != nil {
		return m.buildFn(ctx, req)
	}
	return mutate.ConfigFile(empty.Image, &v1.ConfigFile{
		Architecture: req.Platform.Arch,
		OS:           req.Platform.OS,
	})
}

func (m *mockPackager) Index(ctx context.Context, req ports.IndexRequest) (v1.ImageIndex, error) {
	if m.indexFn != nil {
		return m.indexFn(ctx, req)
	}
	return empty.Index, nil
}

// Mock implementation of ports.Registry
type mockRegistry struct {
	pushFn       func(ctx context.Context, req ports.PushRequest) (ports.PublishResult, error)
	attachSBOMFn func(ctx context.Context, req ports.AttachSBOMRequest) (ports.PublishResult, error)
}

func (m *mockRegistry) Push(ctx context.Context, req ports.PushRequest) (ports.PublishResult, error) {
	if m.pushFn != nil {
		return m.pushFn(ctx, req)
	}
	hash := v1.Hash{Algorithm: "sha256", Hex: "9999888877776666555544443333222299998888777766665555444433332222"}
	return ports.PublishResult{
		Ref:    req.Repo + "@" + hash.String(),
		Digest: hash,
		Tags:   req.Tags,
		Size:   2048,
	}, nil
}

func (m *mockRegistry) AttachSBOM(ctx context.Context, req ports.AttachSBOMRequest) (ports.PublishResult, error) {
	if m.attachSBOMFn != nil {
		return m.attachSBOMFn(ctx, req)
	}
	return ports.PublishResult{
		Ref:    req.Repo + ":" + strings.Replace(req.Subject.String(), ":", "-", 1) + ".sbom",
		Digest: v1.Hash{Algorithm: "sha256", Hex: "aaaa2222aaaa2222aaaa2222aaaa2222aaaa2222aaaa2222aaaa2222aaaa2222"},
	}, nil
}

// Mock implementation of ports.LocalLoader
type mockLocalLoader struct {
	loadFn func(ctx context.Context, req ports.LoadRequest) (ports.PublishResult, error)
}

func (m *mockLocalLoader) Load(ctx context.Context, req ports.LoadRequest) (ports.PublishResult, error) {
	if m.loadFn != nil {
		return m.loadFn(ctx, req)
	}
	hash := v1.Hash{Algorithm: "sha256", Hex: "8888777766665555444433332222111188887777666655554444333322221111"}
	return ports.PublishResult{
		Ref:    req.Repo + ":" + req.Tags[0],
		Digest: hash,
		Tags:   req.Tags,
		Size:   2048,
	}, nil
}

// Mock implementation of ports.TarballWriter
type mockTarballWriter struct {
	writeFn func(ctx context.Context, req ports.TarballRequest) (ports.PublishResult, error)
}

func (m *mockTarballWriter) Write(ctx context.Context, req ports.TarballRequest) (ports.PublishResult, error) {
	if m.writeFn != nil {
		return m.writeFn(ctx, req)
	}
	hash := v1.Hash{Algorithm: "sha256", Hex: "7777666655554444333322221111000077776666555544443333222211110000"}
	return ports.PublishResult{
		Ref:    req.Repo + ":" + req.Tags[0],
		Digest: hash,
		Tags:   req.Tags,
		Path:   req.Path,
		Size:   4096,
	}, nil
}

// Mock implementation of ports.SBOMGenerator
type mockSBOMGenerator struct {
	generateFn func(ctx context.Context, req ports.SBOMRequest) (*ports.SBOMDocument, error)
}

func (m *mockSBOMGenerator) Generate(ctx context.Context, req ports.SBOMRequest) (*ports.SBOMDocument, error) {
	if m.generateFn != nil {
		return m.generateFn(ctx, req)
	}
	return &ports.SBOMDocument{
		Format:       req.Format,
		Content:      []byte(`{"spdxVersion":"SPDX-2.3"}`),
		SHA256:       "3333444455556666333344445555666633334444555566663333444455556666",
		PackageCount: 15,
	}, nil
}

// Mock implementation of ports.StaticServerProvider
type mockStaticServerProvider struct{}

func (m *mockStaticServerProvider) Binary(ctx context.Context, p core.Platform) ([]byte, error) {
	return []byte("fake-pokkum-static-binary"), nil
}

func (m *mockStaticServerProvider) Version(ctx context.Context) (string, error) {
	return "v-test", nil
}

// Mock implementation of ports.NativeInspector
type mockNativeInspector struct {
	inspectFn func(ctx context.Context, projectDir string, platform core.Platform) (ports.NativeInspectionResult, error)
}

func (m *mockNativeInspector) Inspect(ctx context.Context, projectDir string, platform core.Platform) (ports.NativeInspectionResult, error) {
	if m.inspectFn != nil {
		return m.inspectFn(ctx, projectDir, platform)
	}
	return ports.NativeInspectionResult{}, nil
}

// Mock implementation of signing ports
type mockSLSAGenerator struct {
	generateFn func(ctx context.Context, req ports.SLSAGeneratorRequest) (ports.SLSAStatement, error)
}

func (m *mockSLSAGenerator) Generate(ctx context.Context, req ports.SLSAGeneratorRequest) (ports.SLSAStatement, error) {
	if m.generateFn != nil {
		return m.generateFn(ctx, req)
	}
	return ports.SLSAStatement{}, nil
}

type mockCosignSigner struct{}

func (m *mockCosignSigner) CreatePayload(req ports.CosignSignRequest) ([]byte, error) {
	return []byte{}, nil
}

func (m *mockCosignSigner) Sign(ctx context.Context, req ports.CosignSignRequest) (ports.CosignSignatureBundle, error) {
	return ports.CosignSignatureBundle{}, nil
}

func (m *mockCosignSigner) Verify(ctx context.Context, bundle ports.CosignSignatureBundle, pubKeyPEM []byte, expectedRepo string, expectedDigest v1.Hash) error {
	return nil
}

type mockDSSESigner struct{}

func (m *mockDSSESigner) CreatePAE(payloadType string, payload []byte) []byte {
	return []byte{}
}

func (m *mockDSSESigner) Sign(ctx context.Context, req ports.DSSESignRequest) (ports.DSSEEnvelope, error) {
	return ports.DSSEEnvelope{}, nil
}

func (m *mockDSSESigner) Verify(ctx context.Context, envelope ports.DSSEEnvelope, pubKeyPEM []byte) ([]byte, error) {
	return []byte{}, nil
}

type mockBunRuntimeResolver struct {
	resolveFn func(ctx context.Context, req ports.BunResolverRequest) (ports.BunResolverResult, error)
}

func (m *mockBunRuntimeResolver) Resolve(ctx context.Context, req ports.BunResolverRequest) (ports.BunResolverResult, error) {
	if m.resolveFn != nil {
		return m.resolveFn(ctx, req)
	}
	return ports.BunResolverResult{
		BinaryPath: "/mock/bun",
		Version:    "1.2.2",
		Variant:    ports.BunVariantStandard,
		Platform:   req.Platform,
		SHA256:     "mockbunsha256",
		Size:       1000,
	}, nil
}

// mockRemoteCacher records whether ComputeInputHash/Check were invoked, so
// tests can prove the remote build-skip cache is (or isn't) consulted for a
// given build, without needing a real registry.
type mockRemoteCacher struct {
	computeInputHashCalled bool
	checkCalled            bool
	hit                    bool
	verified               bool
	signerIdentity         string
}

func (m *mockRemoteCacher) ComputeInputHash(context.Context, ports.RemoteCacheInputRequest) (string, error) {
	m.computeInputHashCalled = true
	return "deadbeef", nil
}

func (m *mockRemoteCacher) Check(context.Context, ports.RemoteCacheRequest) (ports.RemoteCacheResult, error) {
	m.checkCalled = true
	if m.hit {
		return ports.RemoteCacheResult{
			Hit:            true,
			Ref:            "ghcr.io/example/app@sha256:cachedcachedcachedcachedcachedcachedcachedcachedcachedcachedcach",
			Verified:       m.verified,
			SignerIdentity: m.signerIdentity,
		}, nil
	}
	return ports.RemoteCacheResult{Hit: false}, nil
}

func newFullDeps(stdout io.Writer) core.Deps {
	return core.Deps{
		Compiler:        &mockCompiler{},
		BaseImages:      &mockBaseImageResolver{},
		Supervisor:      &mockSupervisorProvider{},
		Packager:        &mockPackager{},
		BunRuntime:      &mockBunRuntimeResolver{},
		Registry:        &mockRegistry{},
		Daemon:          &mockLocalLoader{},
		Tarballs:        &mockTarballWriter{},
		SBOM:            &mockSBOMGenerator{},
		NativeInspector: &mockNativeInspector{},
		SLSAGenerator:   &mockSLSAGenerator{},
		CosignSigner:    &mockCosignSigner{},
		DSSESigner:      &mockDSSESigner{},
		Scanner:         &mockScanner{},
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		Stdout:          stdout,
		Version:         "v0.1.0-test",
	}
}

func TestDepsValidate(t *testing.T) {
	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
	}
	req.Normalize()

	t.Run("missing compiler", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.Compiler = nil
		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
		if err == nil || !errors.Is(err, core.ErrInvalidRequest) {
			t.Errorf("expected ErrInvalidRequest, got %v", err)
		}
	})

	t.Run("missing base image resolver", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.BaseImages = nil
		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
		if err == nil || !errors.Is(err, core.ErrInvalidRequest) {
			t.Errorf("expected ErrInvalidRequest, got %v", err)
		}
	})

	t.Run("missing supervisor provider", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.Supervisor = nil
		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
		if err == nil || !errors.Is(err, core.ErrInvalidRequest) {
			t.Errorf("expected ErrInvalidRequest, got %v", err)
		}
	})

	t.Run("dry run bypasses packager requirement", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.Packager = nil
		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{DryRun: true})
		if err != nil {
			t.Errorf("dry run should pass deps validation without packager: %v", err)
		}
	})

	t.Run("missing registry for push mode", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.Registry = nil
		reqPush := req
		reqPush.Output.Mode = core.OutputPush
		_, err := core.Build(context.Background(), deps, reqPush, core.BuildOptions{})
		if err == nil || !errors.Is(err, core.ErrInvalidRequest) {
			t.Errorf("expected ErrInvalidRequest for missing registry port, got %v", err)
		}
	})

	t.Run("missing local loader for local mode", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.Daemon = nil
		reqLocal := req
		reqLocal.Output.Mode = core.OutputLocal
		reqLocal.Normalize()
		_, err := core.Build(context.Background(), deps, reqLocal, core.BuildOptions{})
		if err == nil || !errors.Is(err, core.ErrInvalidRequest) {
			t.Errorf("expected ErrInvalidRequest for missing daemon port, got %v", err)
		}
	})

	t.Run("missing tarball writer for tarball mode", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.Tarballs = nil
		reqTar := req
		reqTar.Output.Mode = core.OutputTarball
		reqTar.Output.TarballPath = "/tmp/out.tar"
		reqTar.Normalize()
		_, err := core.Build(context.Background(), deps, reqTar, core.BuildOptions{})
		if err == nil || !errors.Is(err, core.ErrInvalidRequest) {
			t.Errorf("expected ErrInvalidRequest for missing tarballs port, got %v", err)
		}
	})
}

func TestBuildOptionsValidate(t *testing.T) {
	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
	}
	req.Normalize()
	deps := newFullDeps(io.Discard)

	_, err := core.Build(context.Background(), deps, req, core.BuildOptions{
		DryRun:        true,
		PrintManifest: true,
	})
	if err == nil || !errors.Is(err, core.ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest for mutually exclusive dry-run and print-manifest, got %v", err)
	}
}

func TestBuildDryRun(t *testing.T) {
	var buf bytes.Buffer
	deps := newFullDeps(&buf)

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Build dry run failed: %v", err)
	}

	if res.Toolchain.PokkumVersion != "v0.1.0-test" {
		t.Errorf("Toolchain.PokkumVersion = %q, want v0.1.0-test", res.Toolchain.PokkumVersion)
	}

	outStr := buf.String()
	if !strings.Contains(outStr, "DRY RUN") {
		t.Errorf("expected stdout to contain DRY RUN report, got: %s", outStr)
	}
	if !strings.Contains(outStr, "ghcr.io/example/app") {
		t.Errorf("expected stdout to contain repo name, got: %s", outStr)
	}
}

func TestBuildPrintManifest(t *testing.T) {
	var buf bytes.Buffer
	deps := newFullDeps(&buf)

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64, core.LinuxARM64},
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{PrintManifest: true})
	if err != nil {
		t.Fatalf("Build print manifest failed: %v", err)
	}

	if res.Image.Mode != core.OutputPush {
		t.Errorf("Image.Mode = %v, want OutputPush", res.Image.Mode)
	}

	outStr := buf.String()
	if !strings.Contains(outStr, `"repo": "ghcr.io/example/app"`) {
		t.Errorf("expected stdout to contain manifest JSON, got: %s", outStr)
	}
	if !strings.Contains(outStr, `"index"`) {
		t.Errorf("expected stdout to contain index field for multi-arch, got: %s", outStr)
	}
}

func TestBuildPushSuccess(t *testing.T) {
	var buf bytes.Buffer
	deps := newFullDeps(&buf)

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64, core.LinuxARM64},
		Tags:       []string{"v1.0.0"},
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		t.Fatalf("Build push failed: %v", err)
	}

	expectedRef := "ghcr.io/example/app@sha256:9999888877776666555544443333222299998888777766665555444433332222"
	if res.Image.Ref != expectedRef {
		t.Errorf("Image.Ref = %q, want %q", res.Image.Ref, expectedRef)
	}
	if res.SBOM == nil || res.SBOM.Ref == "" {
		t.Errorf("expected attached SBOM result, got %v", res.SBOM)
	}
	if !res.Image.IsIndex {
		t.Errorf("IsIndex = false, want true for multi-platform")
	}

	outStr := buf.String()
	if !strings.Contains(outStr, expectedRef) {
		t.Errorf("expected stdout to contain ref line %q, got %q", expectedRef, outStr)
	}
}

// TestBuildPushSuccess_SLSAAndSBOMCarryResolvedBunRuntime is PR-7's
// regression guard, covering both halves the roadmap flagged as
// inconsistent: SLSA claimed to correctly record Bun's SHA-256 but never
// actually populated BunBinaryHash in a real build, and the SBOM never
// carried Bun at all. It also pins a real (if subtle) correctness fix: the
// SLSA "bun" dependency descriptor must name the resolved EMBEDDED runtime
// artifact (ports.BunResolverResult, "1.2.2"/"mockbunsha256" per
// mockBunRuntimeResolver's default), not the HOST's compiler bun
// (ports.PreflightResult.BunVersion, "1.2.18" per mockCompiler's default) —
// the two fixtures are deliberately different values here specifically so a
// regression back to the wrong source is provable, not just plausible.
func TestBuildPushSuccess_SLSAAndSBOMCarryResolvedBunRuntime(t *testing.T) {
	deps := newFullDeps(io.Discard)

	var sbomReq ports.SBOMRequest
	deps.SBOM = &mockSBOMGenerator{
		generateFn: func(ctx context.Context, req ports.SBOMRequest) (*ports.SBOMDocument, error) {
			sbomReq = req
			return &ports.SBOMDocument{
				Format:       req.Format,
				Content:      []byte(`{"spdxVersion":"SPDX-2.3"}`),
				SHA256:       "3333444455556666333344445555666633334444555566663333444455556666",
				PackageCount: 1,
			}, nil
		},
	}

	var slsaReq ports.SLSAGeneratorRequest
	deps.SLSAGenerator = &mockSLSAGenerator{
		generateFn: func(ctx context.Context, req ports.SLSAGeneratorRequest) (ports.SLSAStatement, error) {
			slsaReq = req
			return ports.SLSAStatement{}, nil
		},
	}

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
		Sign:       true,
	}

	if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if slsaReq.Toolchain.BunVersion != "1.2.2" {
		t.Errorf("SLSA Toolchain.BunVersion = %q, want %q (the resolved embedded runtime, not the host compiler bun %q)",
			slsaReq.Toolchain.BunVersion, "1.2.2", "1.2.18")
	}
	if slsaReq.Toolchain.BunBinaryHash != "mockbunsha256" {
		t.Errorf("SLSA Toolchain.BunBinaryHash = %q, want %q — this field was never populated in a real build before this fix", slsaReq.Toolchain.BunBinaryHash, "mockbunsha256")
	}

	if sbomReq.BunVersion != "1.2.2" {
		t.Errorf("SBOM request BunVersion = %q, want %q", sbomReq.BunVersion, "1.2.2")
	}
	if sbomReq.BunSHA256 != "mockbunsha256" {
		t.Errorf("SBOM request BunSHA256 = %q, want %q", sbomReq.BunSHA256, "mockbunsha256")
	}
}

// TestBuildLocalSuccess_ExeStrategyKeepsHostBunVersionForSLSA guards the
// fallback half of the same fix: for a strategy with no resolved embedded
// runtime (exe: Bun compiled the binary but there is no separate downloaded
// runtime artifact), the SLSA "bun" descriptor must keep using the host
// compiler's bun version rather than silently going empty.
func TestBuildLocalSuccess_ExeStrategyKeepsHostBunVersionForSLSA(t *testing.T) {
	deps := newFullDeps(io.Discard)

	var slsaReq ports.SLSAGeneratorRequest
	deps.SLSAGenerator = &mockSLSAGenerator{
		generateFn: func(ctx context.Context, req ports.SLSAGeneratorRequest) (ports.SLSAStatement, error) {
			slsaReq = req
			return ports.SLSAStatement{}, nil
		},
	}

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
		Sign:       true,
	}
	req.Compile.Strategy = core.StrategyExe

	if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if slsaReq.Toolchain.BunVersion != "1.2.18" {
		t.Errorf("SLSA Toolchain.BunVersion = %q, want %q (host compiler bun, since exe has no resolved embedded runtime artifact)", slsaReq.Toolchain.BunVersion, "1.2.18")
	}
	if slsaReq.Toolchain.BunBinaryHash != "" {
		t.Errorf("SLSA Toolchain.BunBinaryHash = %q, want empty (no embedded runtime resolve happens for exe strategy)", slsaReq.Toolchain.BunBinaryHash)
	}
}

// TestBuildPushSuccess_SignedBuildNeverConsultsRemoteCache proves the F1/F4
// mitigation for the remote build-skip cache: a cache hit reconciles release
// tags to a previously-pushed digest without this build ever running its own
// TestBuildPushSuccess_SignedBuildWithVerificationDisabledBypassesCache proves
// that when a signed build runs with cache verification explicitly disabled,
// it never consults the remote cache to avoid adopting an unverified digest.
func TestBuildPushSuccess_SignedBuildWithVerificationDisabledBypassesCache(t *testing.T) {
	deps := newFullDeps(io.Discard)
	cacher := &mockRemoteCacher{hit: true}
	deps.RemoteCache = cacher

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
		Sign:       true,
		CacheVerify: core.RemoteCacheVerifyOptions{
			VerifyMode: core.CacheVerifyNone,
		},
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if cacher.computeInputHashCalled || cacher.checkCalled {
		t.Fatalf("BUG: remote cache was consulted for a signed build with verification disabled (ComputeInputHash called=%v, Check called=%v)", cacher.computeInputHashCalled, cacher.checkCalled)
	}
	if res.Cached {
		t.Errorf("expected a real, non-cached build result for a signed build")
	}
}

// TestBuildPushSuccess_SignedBuildWithVerifiedCacheHitCanSkipBuild proves that
// when cache verification is active, a signed build can safely skip compilation on a verified cache hit.
func TestBuildPushSuccess_SignedBuildWithVerifiedCacheHitCanSkipBuild(t *testing.T) {
	deps := newFullDeps(io.Discard)
	cacher := &mockRemoteCacher{hit: true, verified: true, signerIdentity: "static-key"}
	deps.RemoteCache = cacher

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
		Sign:       true,
		CacheVerify: core.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      core.CacheVerifyStaticKey,
		},
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if !cacher.computeInputHashCalled || !cacher.checkCalled {
		t.Fatalf("expected remote cache to be consulted for signed build with verification enabled")
	}
	if !res.Cached {
		t.Errorf("expected a cached build result for a signed build on verified cache hit")
	}
}

// TestBuildPushSuccess_UnsignedBuildCanHitRemoteCache confirms that an unsigned build
// still gets the cache's benefit and short-circuits the build on a cache hit.
func TestBuildPushSuccess_UnsignedBuildCanHitRemoteCache(t *testing.T) {
	deps := newFullDeps(io.Discard)
	cacher := &mockRemoteCacher{hit: true, verified: true, signerIdentity: "static-key"}
	deps.RemoteCache = cacher

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if !cacher.computeInputHashCalled || !cacher.checkCalled {
		t.Fatalf("expected the remote cache to be consulted for an unsigned build (ComputeInputHash called=%v, Check called=%v)", cacher.computeInputHashCalled, cacher.checkCalled)
	}
	if !res.Cached {
		t.Errorf("expected a cached build result on a genuine cache hit")
	}
}

// TestBuildPushSuccess_SLSAStatementWithEmptySubjectDoesNotPanic pins a bug
// found while adding the remote-cache Sign-gating tests above: this was the
// first test in this file to ever exercise Sign: true against a real
// OutputPush build, and doing so panicked with "index out of range [0] with
// length 0" — internal/core/pipeline.go's post-sign log line indexed
// slsaStmt.Subject[0] unconditionally. mockSLSAGenerator legitimately
// returns an empty-Subject ports.SLSAStatement with a nil error (the same
// shape a real generator could return for an as-yet-unencountered edge
// case), so this is a genuine crash-on-success bug, not a test-harness
// artifact — a build's own logging must never be able to take the whole
// process down after publishing has already succeeded.
func TestBuildPushSuccess_SLSAStatementWithEmptySubjectDoesNotPanic(t *testing.T) {
	deps := newFullDeps(io.Discard)

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
		Sign:       true,
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if res.Image.Ref == "" {
		t.Errorf("expected a published image ref despite the empty-subject SLSA statement")
	}
}

func TestBuildLocalSuccess(t *testing.T) {
	var buf bytes.Buffer
	deps := newFullDeps(&buf)

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Output:     core.OutputOptions{Mode: core.OutputLocal},
		Platforms:  []core.Platform{core.LinuxAMD64},
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		t.Fatalf("Build local failed: %v", err)
	}

	expectedRef := "pokkum.local/project:latest"
	if res.Image.Ref != expectedRef {
		t.Errorf("Image.Ref = %q, want %q", res.Image.Ref, expectedRef)
	}
}

func TestBuildTarballSuccess(t *testing.T) {
	var buf bytes.Buffer
	deps := newFullDeps(&buf)

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Output: core.OutputOptions{
			Mode:        core.OutputTarball,
			TarballPath: "/tmp/app.tar",
		},
		Platforms: []core.Platform{core.LinuxAMD64},
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		t.Fatalf("Build tarball failed: %v", err)
	}

	if res.Image.TarballPath != "/tmp/app.tar" {
		t.Errorf("TarballPath = %q, want /tmp/app.tar", res.Image.TarballPath)
	}
}

func TestBuildCancelledContext(t *testing.T) {
	deps := newFullDeps(io.Discard)
	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := core.Build(ctx, deps, req, core.BuildOptions{})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled error, got %v", err)
	}
}

func TestBuildErrorPropagation(t *testing.T) {
	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
	}

	t.Run("compiler preflight error", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.Compiler = &mockCompiler{
			preflightFn: func(_ context.Context, _ ports.PreflightRequest) (ports.PreflightResult, error) {
				return ports.PreflightResult{}, core.ErrBunNotFound
			},
		}
		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
		if !errors.Is(err, core.ErrBunNotFound) {
			t.Errorf("expected ErrBunNotFound, got %v", err)
		}
	})

	t.Run("base image resolution error", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.BaseImages = &mockBaseImageResolver{
			resolveFn: func(_ context.Context, _ ports.BaseImageRequest) (*ports.BaseImage, error) {
				return nil, core.ErrBaseImageIncompatible
			},
		}
		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
		if !errors.Is(err, core.ErrBaseImageIncompatible) {
			t.Errorf("expected ErrBaseImageIncompatible, got %v", err)
		}
	})

	t.Run("prepare sveltekit error", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.Compiler = &mockCompiler{
			prepareFn: func(_ context.Context, _ ports.PrepareRequest) (ports.PrepareResult, error) {
				return ports.PrepareResult{}, core.ErrPrepareFailed
			},
		}
		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
		if !errors.Is(err, core.ErrPrepareFailed) {
			t.Errorf("expected ErrPrepareFailed, got %v", err)
		}
	})

	t.Run("compile error", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.Compiler = &mockCompiler{
			compileFn: func(_ context.Context, _ ports.CompileRequest) (ports.Artifact, error) {
				return ports.Artifact{}, core.ErrCompileFailed
			},
		}
		reqExe := req
		reqExe.Compile.Strategy = core.StrategyExe
		_, err := core.Build(context.Background(), deps, reqExe, core.BuildOptions{})
		if !errors.Is(err, core.ErrCompileFailed) {
			t.Errorf("expected ErrCompileFailed, got %v", err)
		}
	})

	t.Run("bun runtime resolution error", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.BunRuntime = &mockBunRuntimeResolver{
			resolveFn: func(_ context.Context, _ ports.BunResolverRequest) (ports.BunResolverResult, error) {
				return ports.BunResolverResult{}, core.ErrBunResolutionFailed
			},
		}
		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
		if !errors.Is(err, core.ErrBunResolutionFailed) {
			t.Errorf("expected ErrBunResolutionFailed, got %v", err)
		}
	})
}

func TestPipeline_BaseImageCVE_Gating(t *testing.T) {
	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
	}
	req.Normalize()

	t.Run("warn only when fail gate is inactive", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.Scanner = &mockScanner{
			scanFn: func(_ context.Context, _ ports.ScanRequest) (ports.ScanResult, error) {
				return ports.ScanResult{
					Passed:           false,
					MaxSeverityFound: ports.SeverityCritical,
					Vulnerabilities: []ports.Vulnerability{
						{ID: "CVE-2026-1234", Severity: ports.SeverityCritical, Package: "libssl3"},
					},
				}, core.ErrVulnerabilityThresholdExceeded
			},
		}

		res, err := core.Build(context.Background(), deps, req, core.BuildOptions{DryRun: true})
		if err != nil {
			t.Fatalf("expected build to succeed in warn-only mode, got: %v", err)
		}
		if res.BaseImage.Ref == "" {
			t.Errorf("expected resolved base image in result")
		}
	})

	t.Run("fail build when FailOnCVE flag threshold is exceeded", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.Scanner = &mockScanner{
			scanFn: func(_ context.Context, _ ports.ScanRequest) (ports.ScanResult, error) {
				return ports.ScanResult{
					Passed:           false,
					MaxSeverityFound: ports.SeverityCritical,
					Vulnerabilities: []ports.Vulnerability{
						{ID: "CVE-2026-1234", Severity: ports.SeverityCritical, Package: "libssl3"},
					},
				}, core.ErrVulnerabilityThresholdExceeded
			},
		}

		reqFlag := req
		reqFlag.FailOnCVE = ports.SeverityCritical

		_, err := core.Build(context.Background(), deps, reqFlag, core.BuildOptions{DryRun: true})
		if err == nil || !errors.Is(err, core.ErrVulnerabilityThresholdExceeded) {
			t.Fatalf("expected ErrVulnerabilityThresholdExceeded, got: %v", err)
		}
	})

	t.Run("fail build when POKKUM_FAIL_ON_CVE env is set", func(t *testing.T) {
		t.Setenv("POKKUM_FAIL_ON_CVE", "critical")
		deps := newFullDeps(io.Discard)
		deps.Scanner = &mockScanner{
			scanFn: func(_ context.Context, _ ports.ScanRequest) (ports.ScanResult, error) {
				return ports.ScanResult{
					Passed:           false,
					MaxSeverityFound: ports.SeverityCritical,
					Vulnerabilities: []ports.Vulnerability{
						{ID: "CVE-2026-1234", Severity: ports.SeverityCritical, Package: "libssl3"},
					},
				}, core.ErrVulnerabilityThresholdExceeded
			},
		}

		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{DryRun: true})
		if err == nil || !errors.Is(err, core.ErrVulnerabilityThresholdExceeded) {
			t.Fatalf("expected ErrVulnerabilityThresholdExceeded from env gate, got: %v", err)
		}
	})

	t.Run("clean base image passes scan", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.Scanner = &mockScanner{
			scanFn: func(_ context.Context, _ ports.ScanRequest) (ports.ScanResult, error) {
				return ports.ScanResult{
					Passed:           true,
					MaxSeverityFound: ports.SeverityLow,
					Vulnerabilities:  nil,
				}, nil
			},
		}

		reqFlag := req
		reqFlag.FailOnCVE = ports.SeverityHigh

		res, err := core.Build(context.Background(), deps, reqFlag, core.BuildOptions{DryRun: true})
		if err != nil {
			t.Fatalf("expected clean scan to pass build, got: %v", err)
		}
		if res.BaseImage.Ref == "" {
			t.Errorf("expected resolved base image")
		}
	})

	t.Run("incomplete scan fails closed when gate is active", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.Scanner = &mockScanner{
			scanFn: func(_ context.Context, _ ports.ScanRequest) (ports.ScanResult, error) {
				return ports.ScanResult{
					Passed:     false,
					Incomplete: true,
					Warnings:   []string{"vulnerability database lookup failed"},
				}, core.ErrScanIncomplete
			},
		}

		reqFlag := req
		reqFlag.FailOnCVE = ports.SeverityCritical

		_, err := core.Build(context.Background(), deps, reqFlag, core.BuildOptions{DryRun: true})
		if err == nil || !errors.Is(err, core.ErrScanIncomplete) {
			t.Fatalf("expected ErrScanIncomplete failure, got: %v", err)
		}
	})

	t.Run("incomplete scan succeeds when AllowIncompleteScan is true", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.Scanner = &mockScanner{
			scanFn: func(_ context.Context, req ports.ScanRequest) (ports.ScanResult, error) {
				if req.AllowIncomplete {
					return ports.ScanResult{
						Passed:     true,
						Incomplete: true,
					}, nil
				}
				return ports.ScanResult{}, core.ErrScanIncomplete
			},
		}

		reqFlag := req
		reqFlag.FailOnCVE = ports.SeverityCritical
		reqFlag.AllowIncompleteScan = true

		res, err := core.Build(context.Background(), deps, reqFlag, core.BuildOptions{DryRun: true})
		if err != nil {
			t.Fatalf("expected build to succeed with AllowIncompleteScan=true, got: %v", err)
		}
		if res.BaseImage.Ref == "" {
			t.Errorf("expected resolved base image")
		}
	})

	t.Run("offline build fails closed instead of silently skipping an active CVE gate", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		scanCalled := false
		deps.Scanner = &mockScanner{
			scanFn: func(_ context.Context, _ ports.ScanRequest) (ports.ScanResult, error) {
				scanCalled = true
				return ports.ScanResult{
					Passed:           false,
					MaxSeverityFound: ports.SeverityCritical,
					Vulnerabilities: []ports.Vulnerability{
						{ID: "CVE-2026-1234", Severity: ports.SeverityCritical, Package: "libssl3"},
					},
				}, core.ErrVulnerabilityThresholdExceeded
			},
		}

		reqFlag := req
		reqFlag.FailOnCVE = ports.SeverityCritical
		reqFlag.BaseImage.Offline = true
		reqFlag.Normalize()

		_, err := core.Build(context.Background(), deps, reqFlag, core.BuildOptions{DryRun: true})
		if err == nil || !errors.Is(err, core.ErrScanIncomplete) {
			t.Fatalf("expected ErrScanIncomplete when offline build skips an active CVE gate, got: %v", err)
		}
		if scanCalled {
			t.Errorf("scanner must not be invoked for an offline build")
		}
	})

	t.Run("hermetic build fails closed instead of silently skipping an active CVE gate", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		scanCalled := false
		deps.Scanner = &mockScanner{
			scanFn: func(_ context.Context, _ ports.ScanRequest) (ports.ScanResult, error) {
				scanCalled = true
				return ports.ScanResult{
					Passed:           false,
					MaxSeverityFound: ports.SeverityCritical,
					Vulnerabilities: []ports.Vulnerability{
						{ID: "CVE-2026-1234", Severity: ports.SeverityCritical, Package: "libssl3"},
					},
				}, core.ErrVulnerabilityThresholdExceeded
			},
		}

		reqFlag := req
		reqFlag.FailOnCVE = ports.SeverityCritical
		reqFlag.Hermetic = true
		reqFlag.Normalize()
		if !reqFlag.BaseImage.Offline {
			t.Fatalf("test premise broken: Normalize() no longer sets BaseImage.Offline from Hermetic")
		}

		_, err := core.Build(context.Background(), deps, reqFlag, core.BuildOptions{DryRun: true})
		if err == nil || !errors.Is(err, core.ErrScanIncomplete) {
			t.Fatalf("expected ErrScanIncomplete when hermetic build skips an active CVE gate, got: %v", err)
		}
		if scanCalled {
			t.Errorf("scanner must not be invoked for a hermetic build")
		}
	})

	t.Run("offline build with AllowIncompleteScan proceeds without calling the scanner", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		scanCalled := false
		deps.Scanner = &mockScanner{
			scanFn: func(_ context.Context, _ ports.ScanRequest) (ports.ScanResult, error) {
				scanCalled = true
				return ports.ScanResult{}, core.ErrVulnerabilityThresholdExceeded
			},
		}

		reqFlag := req
		reqFlag.FailOnCVE = ports.SeverityCritical
		reqFlag.BaseImage.Offline = true
		reqFlag.AllowIncompleteScan = true
		reqFlag.Normalize()

		res, err := core.Build(context.Background(), deps, reqFlag, core.BuildOptions{DryRun: true})
		if err != nil {
			t.Fatalf("expected build to succeed offline with AllowIncompleteScan=true, got: %v", err)
		}
		if scanCalled {
			t.Errorf("scanner must not be invoked for an offline build even with AllowIncompleteScan")
		}
		if res.BaseImage.Ref == "" {
			t.Errorf("expected resolved base image")
		}
	})

	t.Run("offline build without an active CVE gate stays silent-skip (no error)", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		scanCalled := false
		deps.Scanner = &mockScanner{
			scanFn: func(_ context.Context, _ ports.ScanRequest) (ports.ScanResult, error) {
				scanCalled = true
				return ports.ScanResult{}, nil
			},
		}

		reqFlag := req
		reqFlag.BaseImage.Offline = true
		reqFlag.Normalize()

		res, err := core.Build(context.Background(), deps, reqFlag, core.BuildOptions{DryRun: true})
		if err != nil {
			t.Fatalf("expected offline build with no CVE gate to succeed, got: %v", err)
		}
		if scanCalled {
			t.Errorf("scanner must not be invoked for an offline build")
		}
		if res.BaseImage.Ref == "" {
			t.Errorf("expected resolved base image")
		}
	})

	t.Run("records scan results into base image resolver / lockfile", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.Scanner = &mockScanner{
			scanFn: func(_ context.Context, _ ports.ScanRequest) (ports.ScanResult, error) {
				return ports.ScanResult{
					Passed:           true,
					MaxSeverityFound: ports.SeverityMedium,
					Vulnerabilities: []ports.Vulnerability{
						{ID: "CVE-2026-9999", Severity: ports.SeverityMedium, Package: "libxyz"},
					},
				}, nil
			},
		}

		recorded := false
		var recordedScan ports.ScanResult
		deps.BaseImages = &mockBaseImageResolver{
			recordScanResultFn: func(_ context.Context, _ string, _ ports.BaseImagePreset, scan ports.ScanResult) error {
				recorded = true
				recordedScan = scan
				return nil
			},
		}

		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{DryRun: true})
		if err != nil {
			t.Fatalf("unexpected build error: %v", err)
		}
		if !recorded {
			t.Errorf("expected RecordScanResult to be invoked on BaseImages resolver")
		}
		if recordedScan.MaxSeverityFound != ports.SeverityMedium {
			t.Errorf("recorded max severity = %v, want medium", recordedScan.MaxSeverityFound)
		}
		if len(recordedScan.Vulnerabilities) != 1 {
			t.Errorf("recorded vulns = %d, want 1", len(recordedScan.Vulnerabilities))
		}
	})

	t.Run("Offline build reads cached lockfile scan audit and fails on threshold violation", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		req := core.BuildRequest{
			ProjectDir: "/abs/project",
			Repo:       "ghcr.io/example/app",
			Platforms:  []core.Platform{core.LinuxAMD64},
			BaseImage: core.BaseImageOptions{
				Preset:  core.BaseImageDistroless,
				Offline: true,
			},
			FailOnCVE: core.SeverityCritical,
		}

		// Mock resolver returning base image with cached critical audit
		deps.BaseImages = &mockBaseImageResolver{
			resolveFn: func(_ context.Context, _ ports.BaseImageRequest) (*ports.BaseImage, error) {
				return &ports.BaseImage{
					Ref:                  "gcr.io/distroless/cc-debian12:nonroot",
					PinnedRef:            "gcr.io/distroless/cc-debian12@sha256:1111111111111111111111111111111111111111111111111111111111111111",
					Digest:               v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("1", 64)},
					Images:               map[ports.Platform]v1.Image{ports.LinuxAMD64: empty.Image},
					LastScannedAt:        "2026-08-15T00:00:00Z",
					VulnerabilitiesCount: 2,
					MaxSeverity:          "critical",
				}, nil
			},
		}

		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{DryRun: true})
		if err == nil {
			t.Fatalf("expected build to fail when cached audit contains critical vulnerability")
		}
		if !errors.Is(err, core.ErrVulnerabilityThresholdExceeded) {
			t.Errorf("expected ErrVulnerabilityThresholdExceeded, got: %v", err)
		}
	})

	t.Run("Sourcemap flag propagates to packager in layered strategy", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		pkgReqSourcemap := false
		deps.Packager = &mockPackager{
			buildFn: func(_ context.Context, req ports.PackageRequest) (v1.Image, error) {
				pkgReqSourcemap = req.Sourcemap
				return empty.Image, nil
			},
		}

		req := core.BuildRequest{
			ProjectDir: "/abs/project",
			Repo:       "ghcr.io/example/app",
			Platforms:  []core.Platform{core.LinuxAMD64},
			Compile: core.CompileOptions{
				Strategy:  core.StrategyLayered,
				Sourcemap: true,
			},
		}

		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
		if err != nil {
			t.Fatalf("Build failed: %v", err)
		}
		if !pkgReqSourcemap {
			t.Errorf("expected Packager to receive Sourcemap = true in layered strategy")
		}
	})

	t.Run("Sourcemap flag propagates to compiler and packager in exe strategy", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		compileReqSourcemap := false
		deps.Compiler = &mockCompiler{
			compileFn: func(_ context.Context, req ports.CompileRequest) (ports.Artifact, error) {
				compileReqSourcemap = req.Sourcemap
				return ports.Artifact{
					Path: "/tmp/out/bin",
				}, nil
			},
		}
		pkgReqSourcemap := false
		deps.Packager = &mockPackager{
			buildFn: func(_ context.Context, req ports.PackageRequest) (v1.Image, error) {
				pkgReqSourcemap = req.Sourcemap
				return empty.Image, nil
			},
		}

		req := core.BuildRequest{
			ProjectDir: "/abs/project",
			Repo:       "ghcr.io/example/app",
			Platforms:  []core.Platform{core.LinuxAMD64},
			Compile: core.CompileOptions{
				Strategy:  core.StrategyExe,
				Sourcemap: true,
			},
		}

		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
		if err != nil {
			t.Fatalf("Build failed: %v", err)
		}
		if !compileReqSourcemap {
			t.Errorf("expected Compiler to receive Sourcemap = true in exe strategy")
		}
		if !pkgReqSourcemap {
			t.Errorf("expected Packager to receive Sourcemap = true in exe strategy")
		}
	})

	t.Run("Offline build with cached clean audit (0 vulns) passes threshold check", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		req := core.BuildRequest{
			ProjectDir: "/abs/project",
			Repo:       "ghcr.io/example/app",
			Platforms:  []core.Platform{core.LinuxAMD64},
			BaseImage: core.BaseImageOptions{
				Preset:  core.BaseImageDistroless,
				Offline: true,
			},
			FailOnCVE: core.SeverityCritical,
		}

		deps.BaseImages = &mockBaseImageResolver{
			resolveFn: func(_ context.Context, _ ports.BaseImageRequest) (*ports.BaseImage, error) {
				return &ports.BaseImage{
					Ref:                  "gcr.io/distroless/cc-debian12:nonroot",
					PinnedRef:            "gcr.io/distroless/cc-debian12@sha256:1111111111111111111111111111111111111111111111111111111111111111",
					Digest:               v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("1", 64)},
					Images:               map[ports.Platform]v1.Image{ports.LinuxAMD64: empty.Image},
					LastScannedAt:        "2026-08-15T00:00:00Z",
					VulnerabilitiesCount: 0,
					MaxSeverity:          "",
				}, nil
			},
		}

		res, err := core.Build(context.Background(), deps, req, core.BuildOptions{DryRun: true})
		if err != nil {
			t.Fatalf("expected build to succeed for clean cached audit, got: %v", err)
		}
		if res.Image.Digest.String() == "" {
			t.Errorf("expected non-empty digest on build result")
		}
	})

	t.Run("Offline build with cached audit below threshold passes", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		req := core.BuildRequest{
			ProjectDir: "/abs/project",
			Repo:       "ghcr.io/example/app",
			Platforms:  []core.Platform{core.LinuxAMD64},
			BaseImage: core.BaseImageOptions{
				Preset:  core.BaseImageDistroless,
				Offline: true,
			},
			FailOnCVE: core.SeverityCritical,
		}

		deps.BaseImages = &mockBaseImageResolver{
			resolveFn: func(_ context.Context, _ ports.BaseImageRequest) (*ports.BaseImage, error) {
				return &ports.BaseImage{
					Ref:                  "gcr.io/distroless/cc-debian12:nonroot",
					PinnedRef:            "gcr.io/distroless/cc-debian12@sha256:1111111111111111111111111111111111111111111111111111111111111111",
					Digest:               v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("1", 64)},
					Images:               map[ports.Platform]v1.Image{ports.LinuxAMD64: empty.Image},
					LastScannedAt:        "2026-08-15T00:00:00Z",
					VulnerabilitiesCount: 2,
					MaxSeverity:          "medium",
				}, nil
			},
		}

		res, err := core.Build(context.Background(), deps, req, core.BuildOptions{DryRun: true})
		if err != nil {
			t.Fatalf("expected build to succeed when cached audit is below threshold, got: %v", err)
		}
		if res.Image.Digest.String() == "" {
			t.Errorf("expected non-empty digest on build result")
		}
	})

	t.Run("Offline build with malformed MaxSeverity in cached audit fails safe", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		req := core.BuildRequest{
			ProjectDir: "/abs/project",
			Repo:       "ghcr.io/example/app",
			Platforms:  []core.Platform{core.LinuxAMD64},
			BaseImage: core.BaseImageOptions{
				Preset:  core.BaseImageDistroless,
				Offline: true,
			},
			FailOnCVE: core.SeverityCritical,
		}

		deps.BaseImages = &mockBaseImageResolver{
			resolveFn: func(_ context.Context, _ ports.BaseImageRequest) (*ports.BaseImage, error) {
				return &ports.BaseImage{
					Ref:                  "gcr.io/distroless/cc-debian12:nonroot",
					PinnedRef:            "gcr.io/distroless/cc-debian12@sha256:1111111111111111111111111111111111111111111111111111111111111111",
					Digest:               v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("1", 64)},
					Images:               map[ports.Platform]v1.Image{ports.LinuxAMD64: empty.Image},
					LastScannedAt:        "2026-08-15T00:00:00Z",
					VulnerabilitiesCount: 3,
					MaxSeverity:          "unrecognized_bogus_severity",
				}, nil
			},
		}

		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{DryRun: true})
		if err == nil {
			t.Fatalf("expected build to fail on malformed cached severity string")
		}
		if !errors.Is(err, core.ErrScanIncomplete) {
			t.Errorf("expected ErrScanIncomplete, got: %v", err)
		}
	})

	t.Run("Offline build with malformed MaxSeverity and allow-incomplete proceeds", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		req := core.BuildRequest{
			ProjectDir: "/abs/project",
			Repo:       "ghcr.io/example/app",
			Platforms:  []core.Platform{core.LinuxAMD64},
			BaseImage: core.BaseImageOptions{
				Preset:  core.BaseImageDistroless,
				Offline: true,
			},
			FailOnCVE:           core.SeverityCritical,
			AllowIncompleteScan: true,
		}

		deps.BaseImages = &mockBaseImageResolver{
			resolveFn: func(_ context.Context, _ ports.BaseImageRequest) (*ports.BaseImage, error) {
				return &ports.BaseImage{
					Ref:                  "gcr.io/distroless/cc-debian12:nonroot",
					PinnedRef:            "gcr.io/distroless/cc-debian12@sha256:1111111111111111111111111111111111111111111111111111111111111111",
					Digest:               v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("1", 64)},
					Images:               map[ports.Platform]v1.Image{ports.LinuxAMD64: empty.Image},
					LastScannedAt:        "2026-08-15T00:00:00Z",
					VulnerabilitiesCount: 3,
					MaxSeverity:          "unrecognized_bogus_severity",
				}, nil
			},
		}

		res, err := core.Build(context.Background(), deps, req, core.BuildOptions{DryRun: true})
		if err != nil {
			t.Fatalf("expected build to proceed when AllowIncompleteScan is set, got: %v", err)
		}
		if res.Image.Digest.String() == "" {
			t.Errorf("expected non-empty digest on build result")
		}
	})
}

// --- Overlapped pipeline stage tests ---------------------------------------
//
// These cover the errgroup-based concurrency introduced in Build's Stage 5:
// Compiler.Prepare now runs alongside BaseImageResolver.VerifyBaseImage and
// NativeInspector.Inspect (internal/core/pipeline.go), gated on a confirmed
// remote-cache miss, with signature verification still running synchronously
// on --dry-run.

// TestBuild_VerifyBaseImage_CancelsInFlightPrepare proves two things at once:
// a VerifyBaseImage failure cancels an in-flight Prepare (rather than letting
// it run to completion after the build has already failed), and that the two
// calls genuinely overlap in wall-clock time rather than merely both being
// invoked. The mock VerifyBaseImage blocks until Prepare has observably
// started before it fails, so a pass here is only possible if both were
// in-flight simultaneously.
func TestBuild_VerifyBaseImage_CancelsInFlightPrepare(t *testing.T) {
	prepareStarted := make(chan struct{})
	var prepareSawCancel bool

	deps := newFullDeps(io.Discard)
	deps.Compiler = &mockCompiler{
		prepareFn: func(ctx context.Context, _ ports.PrepareRequest) (ports.PrepareResult, error) {
			close(prepareStarted)
			<-ctx.Done()
			prepareSawCancel = ctx.Err() != nil
			return ports.PrepareResult{}, ctx.Err()
		},
	}
	deps.BaseImages = &mockBaseImageResolver{
		verifyBaseImageFn: func(_ context.Context, _ *ports.BaseImage, _ ports.BaseImageRequest) error {
			// Block until Prepare is demonstrably in flight before failing, so
			// this test cannot pass on a regression to sequential-but-still-
			// correct calls (Prepare would never start, and this line would
			// hang forever instead of silently passing).
			<-prepareStarted
			return fmt.Errorf("bad signature: %w", core.ErrBaseSignatureInvalid)
		},
	}

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
	}

	_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if !errors.Is(err, core.ErrBaseSignatureInvalid) {
		t.Fatalf("err = %v, want core.ErrBaseSignatureInvalid", err)
	}
	if !prepareSawCancel {
		t.Errorf("Compiler.Prepare's context was not observably cancelled when VerifyBaseImage failed")
	}
}

// TestBuild_NativeInspection_CancelsInFlightPrepare is the same shape as
// TestBuild_VerifyBaseImage_CancelsInFlightPrepare, via NativeInspector.Inspect
// failing instead of VerifyBaseImage.
func TestBuild_NativeInspection_CancelsInFlightPrepare(t *testing.T) {
	prepareStarted := make(chan struct{})
	var prepareSawCancel bool

	deps := newFullDeps(io.Discard)
	deps.Compiler = &mockCompiler{
		prepareFn: func(ctx context.Context, _ ports.PrepareRequest) (ports.PrepareResult, error) {
			close(prepareStarted)
			<-ctx.Done()
			prepareSawCancel = ctx.Err() != nil
			return ports.PrepareResult{}, ctx.Err()
		},
	}
	wantErr := errors.New("native inspection failed")
	deps.NativeInspector = &mockNativeInspector{
		inspectFn: func(_ context.Context, _ string, _ core.Platform) (ports.NativeInspectionResult, error) {
			<-prepareStarted
			return ports.NativeInspectionResult{}, wantErr
		},
	}

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
	}

	_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if !prepareSawCancel {
		t.Errorf("Compiler.Prepare's context was not observably cancelled when Inspect failed")
	}
}

// TestBuild_CacheHit_SkipsVerifyBaseImageAndNativeInspection is the explicit
// regression guard for the disclosed behavior change: on a confirmed
// remote-cache hit, neither base-image signature verification nor native
// module inspection has anything left to gate (nothing is built from source
// or from the base image), so neither must run. Compiler.Prepare is
// instrumented to fail the test outright if it is ever called, since a cache
// hit must short-circuit before Stage 5 entirely.
func TestBuild_CacheHit_SkipsVerifyBaseImageAndNativeInspection(t *testing.T) {
	deps := newFullDeps(io.Discard)
	cacher := &mockRemoteCacher{hit: true, verified: true, signerIdentity: "static-key"}
	deps.RemoteCache = cacher

	verifyCalled := false
	deps.BaseImages = &mockBaseImageResolver{
		verifyBaseImageFn: func(_ context.Context, _ *ports.BaseImage, _ ports.BaseImageRequest) error {
			verifyCalled = true
			return nil
		},
	}
	inspectCalled := false
	deps.NativeInspector = &mockNativeInspector{
		inspectFn: func(_ context.Context, _ string, _ core.Platform) (ports.NativeInspectionResult, error) {
			inspectCalled = true
			return ports.NativeInspectionResult{}, nil
		},
	}
	deps.Compiler = &mockCompiler{
		prepareFn: func(_ context.Context, _ ports.PrepareRequest) (ports.PrepareResult, error) {
			t.Error("Compiler.Prepare must not run on a confirmed remote-cache hit")
			return ports.PrepareResult{}, nil
		},
	}

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if !res.Cached {
		t.Fatalf("expected a cached build result on a confirmed cache hit")
	}
	if verifyCalled {
		t.Errorf("VerifyBaseImage must not be called on a confirmed remote-cache hit")
	}
	if inspectCalled {
		t.Errorf("NativeInspector.Inspect must not be called on a confirmed remote-cache hit")
	}
}

// TestBuild_OriginWarning is PB-4's regression guard: adapter-node's
// ORIGIN contract previously had no proactive signal at all, so a user
// deploying behind a reverse proxy/ingress would only discover the gap when
// a real user's form submission 403'd in production. Warn, don't fail —
// plenty of real deployments never hit this — but the warning must actually
// fire for the strategies where it matters (layered/exe, which embed
// adapter-node) and stay silent everywhere it doesn't apply.
func TestBuild_OriginWarning(t *testing.T) {
	const warningSubstring = "ORIGIN not set"

	runBuild := func(t *testing.T, mutate func(*core.BuildRequest)) string {
		t.Helper()
		var buf bytes.Buffer
		deps := newFullDeps(io.Discard)
		deps.Logger = slog.New(slog.NewTextHandler(&buf, nil))

		req := core.BuildRequest{
			ProjectDir: "/abs/project",
			Repo:       "ghcr.io/example/app",
			Platforms:  []core.Platform{core.LinuxAMD64},
			Tags:       []string{"v1.0.0"},
		}
		if mutate != nil {
			mutate(&req)
		}
		if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
			t.Fatalf("Build failed: %v", err)
		}
		return buf.String()
	}

	t.Run("warns for layered strategy with no origin", func(t *testing.T) {
		got := runBuild(t, nil)
		if !strings.Contains(got, warningSubstring) {
			t.Errorf("expected ORIGIN warning for a layered build with no origin, got:\n%s", got)
		}
	})

	t.Run("does not warn when origin is set", func(t *testing.T) {
		got := runBuild(t, func(req *core.BuildRequest) {
			req.Runtime.Origin = "https://example.com"
		})
		if strings.Contains(got, warningSubstring) {
			t.Errorf("unexpected ORIGIN warning with Runtime.Origin set, got:\n%s", got)
		}
	})

	t.Run("does not warn for static strategy (no adapter-node runtime)", func(t *testing.T) {
		var buf bytes.Buffer
		deps := newFullDeps(io.Discard)
		deps.Logger = slog.New(slog.NewTextHandler(&buf, nil))
		deps.StaticServer = &mockStaticServerProvider{}

		req := core.BuildRequest{
			ProjectDir: "/abs/project",
			Repo:       "ghcr.io/example/app",
			Platforms:  []core.Platform{core.LinuxAMD64},
			Tags:       []string{"v1.0.0"},
		}
		req.Compile.Strategy = core.StrategyStatic

		// A full static-strategy build needs more mock wiring than this test
		// cares about (the packager/compiler mocks here are shaped for
		// layered/exe output) — the ORIGIN warning is emitted before any of
		// that runs, so what matters is that it doesn't fire, regardless of
		// whether the build goes on to fail for an unrelated reason.
		_, _ = core.Build(context.Background(), deps, req, core.BuildOptions{})

		if got := buf.String(); strings.Contains(got, warningSubstring) {
			t.Errorf("unexpected ORIGIN warning for a static build (no Bun/adapter-node runtime at all), got:\n%s", got)
		}
	})
}

// mockEnvBakeDetector is a test double for ports.EnvBakeDetector — real
// detection is exercised by sveltekitutils.DetectStaticEnvBindings's own
// tests; here only the pipeline wiring (warn + RuntimeConfig.EnvBaked
// population) is under test.
type mockEnvBakeDetector struct {
	bindings []string
	err      error
}

func (m *mockEnvBakeDetector) DetectStaticEnv(_ context.Context, _ ports.EnvBakeRequest) (ports.EnvBakeResult, error) {
	if m.err != nil {
		return ports.EnvBakeResult{}, m.err
	}
	return ports.EnvBakeResult{Bindings: m.bindings}, nil
}

// TestBuild_EnvBakeWarning is PB-3's regression guard: a project importing
// $env/static/* must warn and stamp the detected bindings into
// req.Runtime.EnvBaked (which flows into the image's annotation — see
// packager's config_unit_test.go for that half), while an ordinary project
// with no such import gets neither.
func TestBuild_EnvBakeWarning(t *testing.T) {
	const warningSubstring = "imports from $env/static/*"

	newReq := func() core.BuildRequest {
		return core.BuildRequest{
			ProjectDir: "/abs/project",
			Repo:       "ghcr.io/example/app",
			Platforms:  []core.Platform{core.LinuxAMD64},
			Tags:       []string{"v1.0.0"},
		}
	}

	t.Run("warns and records bindings when detected", func(t *testing.T) {
		var buf bytes.Buffer
		deps := newFullDeps(io.Discard)
		deps.Logger = slog.New(slog.NewTextHandler(&buf, nil))
		deps.EnvBakeDetector = &mockEnvBakeDetector{bindings: []string{"PUBLIC_API_URL"}}

		req := newReq()
		if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
			t.Fatalf("Build failed: %v", err)
		}
		if got := buf.String(); !strings.Contains(got, warningSubstring) {
			t.Errorf("expected env-baked warning, got:\n%s", got)
		}
	})

	t.Run("does not warn when nothing detected", func(t *testing.T) {
		var buf bytes.Buffer
		deps := newFullDeps(io.Discard)
		deps.Logger = slog.New(slog.NewTextHandler(&buf, nil))
		deps.EnvBakeDetector = &mockEnvBakeDetector{bindings: nil}

		req := newReq()
		if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
			t.Fatalf("Build failed: %v", err)
		}
		if got := buf.String(); strings.Contains(got, warningSubstring) {
			t.Errorf("unexpected env-baked warning with nothing detected, got:\n%s", got)
		}
	})

	t.Run("does not warn or fail when no detector is wired", func(t *testing.T) {
		var buf bytes.Buffer
		deps := newFullDeps(io.Discard)
		deps.Logger = slog.New(slog.NewTextHandler(&buf, nil))
		deps.EnvBakeDetector = nil

		req := newReq()
		if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
			t.Fatalf("Build failed: %v", err)
		}
		if got := buf.String(); strings.Contains(got, warningSubstring) {
			t.Errorf("unexpected env-baked warning with no detector wired, got:\n%s", got)
		}
	})

	t.Run("a detection error is tolerated, not fatal", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.EnvBakeDetector = &mockEnvBakeDetector{err: fmt.Errorf("boom")}

		req := newReq()
		if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
			t.Fatalf("expected a detector error to be swallowed (best-effort scan), got: %v", err)
		}
	})
}

// TestBuild_VEXExemptionWarning is PR-6's pipeline-level regression guard:
// self-review (checklist row 13, mem:self_review_checklist) found that
// TestScanner_VEXExemptionExcludesFromThreshold and TestActiveVEXExemption
// only exercised the scanner adapter directly — nothing proved
// BuildRequest.VEXExemptions actually reaches ports.ScanRequest.VEXExemptions
// through core.Build's real wiring (internal/core/pipeline.go), nor that a
// scan's real ScanResult.ExemptedVulnerabilities gets surfaced as a build
// warning. This closes that gap; the parallel half (that the warning's CVE
// list also lands on the image's label/annotation) is covered by
// packager/config_unit_test.go's TestMergeLabelsAndAnnotationsWithVEXExemptions,
// matching the same split already used by TestBuild_EnvBakeWarning above.
func TestBuild_VEXExemptionWarning(t *testing.T) {
	const warningSubstring = "VEX exemption(s) applied"

	newReq := func(exemptions []core.VEXExemption) core.BuildRequest {
		return core.BuildRequest{
			ProjectDir:    "/abs/project",
			Repo:          "ghcr.io/example/app",
			Platforms:     []core.Platform{core.LinuxAMD64},
			Tags:          []string{"v1.0.0"},
			VEXExemptions: exemptions,
		}
	}

	t.Run("warns and passes through the exempted CVE when the scanner reports one", func(t *testing.T) {
		var buf bytes.Buffer
		deps := newFullDeps(io.Discard)
		deps.Logger = slog.New(slog.NewTextHandler(&buf, nil))

		var gotExemptions []ports.VEXExemption
		deps.Scanner = &mockScanner{
			scanFn: func(_ context.Context, req ports.ScanRequest) (ports.ScanResult, error) {
				gotExemptions = req.VEXExemptions
				return ports.ScanResult{
					Passed:           true,
					MaxSeverityFound: ports.SeverityCritical,
					ExemptedVulnerabilities: []ports.Vulnerability{
						{ID: "CVE-2026-1234", Severity: ports.SeverityCritical, Package: "libssl3"},
					},
				}, nil
			},
		}

		exemption := core.VEXExemption{
			CVE:           "CVE-2026-1234",
			Justification: core.VEXJustification(ports.VEXComponentNotPresent),
			Expires:       time.Now().AddDate(1, 0, 0),
			Owner:         "test-owner",
		}
		req := newReq([]core.VEXExemption{exemption})
		if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
			t.Fatalf("Build failed: %v", err)
		}

		if len(gotExemptions) != 1 || gotExemptions[0].CVE != "CVE-2026-1234" {
			t.Fatalf("expected BuildRequest.VEXExemptions to reach ports.ScanRequest.VEXExemptions, got: %+v", gotExemptions)
		}
		if got := buf.String(); !strings.Contains(got, warningSubstring) {
			t.Errorf("expected VEX exemption warning, got:\n%s", got)
		}
		if got := buf.String(); !strings.Contains(got, "CVE-2026-1234") {
			t.Errorf("expected warning to name the exempted CVE, got:\n%s", got)
		}
	})

	t.Run("does not warn when the scanner reports no exemptions applied", func(t *testing.T) {
		var buf bytes.Buffer
		deps := newFullDeps(io.Discard)
		deps.Logger = slog.New(slog.NewTextHandler(&buf, nil))
		deps.Scanner = &mockScanner{
			scanFn: func(_ context.Context, _ ports.ScanRequest) (ports.ScanResult, error) {
				return ports.ScanResult{Passed: true, MaxSeverityFound: ports.SeverityLow}, nil
			},
		}

		req := newReq(nil)
		if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
			t.Fatalf("Build failed: %v", err)
		}
		if got := buf.String(); strings.Contains(got, warningSubstring) {
			t.Errorf("unexpected VEX exemption warning with nothing exempted, got:\n%s", got)
		}
	})
}

// TestBuild_CacheHit_DisclosesBaseImageVerificationSkip proves the auditable
// disclosure of the accepted security tradeoff: on a confirmed remote-cache
// hit, base-image signature verification is deliberately skipped, and the
// pipeline must emit an explicit log line naming the base ref+digest so the
// skip is visible in CI/operator logs rather than silently invisible.
func TestBuild_CacheHit_DisclosesBaseImageVerificationSkip(t *testing.T) {
	var buf bytes.Buffer
	slogLogger := slog.New(slog.NewTextHandler(&buf, nil))

	deps := newFullDeps(io.Discard)
	deps.Logger = slogLogger
	cacher := &mockRemoteCacher{hit: true, verified: true, signerIdentity: "static-key"}
	deps.RemoteCache = cacher
	deps.Compiler = &mockCompiler{
		prepareFn: func(_ context.Context, _ ports.PrepareRequest) (ports.PrepareResult, error) {
			t.Error("Compiler.Prepare must not run on a confirmed remote-cache hit")
			return ports.PrepareResult{}, nil
		},
	}

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
	}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if !res.Cached {
		t.Fatalf("expected a cached build result on a confirmed cache hit")
	}
	got := buf.String()
	if !strings.Contains(got, "base image signature verification skipped") {
		t.Errorf("expected an explicit auditable log line disclosing the base-image verification skip on a cache hit, got:\n%s", got)
	}
}

// TestBuild_DryRun_StillVerifiesBaseImageSignature is the regression guard
// for the dry-run carve-out: Resolve itself no longer verifies signatures
// (VerifySignature is always false in the request Build builds), so dry-run
// must call VerifyBaseImage synchronously before writing its plan in order to
// keep failing fast on a bad base image signature, exactly as it did when
// Resolve verified inline.
func TestBuild_DryRun_StillVerifiesBaseImageSignature(t *testing.T) {
	deps := newFullDeps(io.Discard)
	deps.BaseImages = &mockBaseImageResolver{
		verifyBaseImageFn: func(_ context.Context, _ *ports.BaseImage, _ ports.BaseImageRequest) error {
			return fmt.Errorf("bad signature: %w", core.ErrBaseSignatureInvalid)
		},
	}

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
	}

	_, err := core.Build(context.Background(), deps, req, core.BuildOptions{DryRun: true})
	if !errors.Is(err, core.ErrBaseSignatureInvalid) {
		t.Fatalf("err = %v, want core.ErrBaseSignatureInvalid (dry run must still fail fast on a bad base image signature)", err)
	}
}

// TestBuild_DryRun_StillInspectsNativeModules is the regression guard for
// the same carve-out on the other check: NativeInspector.Inspect used to
// run unconditionally before the dry-run stop, before the concurrent block
// existed. It moved into that concurrent block, which sits past the
// dry-run return, so dry-run needs its own explicit call to keep failing
// fast on an unsupported native module exactly as it did before.
func TestBuild_DryRun_StillInspectsNativeModules(t *testing.T) {
	deps := newFullDeps(io.Discard)
	deps.NativeInspector = &mockNativeInspector{
		inspectFn: func(_ context.Context, _ string, _ core.Platform) (ports.NativeInspectionResult, error) {
			return ports.NativeInspectionResult{}, fmt.Errorf("native module: %w", core.ErrNativeModulesUnsupported)
		},
	}

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
	}

	_, err := core.Build(context.Background(), deps, req, core.BuildOptions{DryRun: true})
	if !errors.Is(err, core.ErrNativeModulesUnsupported) {
		t.Fatalf("err = %v, want core.ErrNativeModulesUnsupported (dry run must still fail fast on an unsupported native module)", err)
	}
}
