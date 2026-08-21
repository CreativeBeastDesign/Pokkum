package core_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/secretguard"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// Port interface conformance assertions for mock implementations.
var (
	_ ports.Compiler             = (*mockCompiler)(nil)
	_ ports.BaseImageResolver    = (*mockBaseImageResolver)(nil)
	_ ports.Scanner              = (*mockScanner)(nil)
	_ ports.SupervisorProvider   = (*mockSupervisorProvider)(nil)
	_ ports.Packager             = (*mockPackager)(nil)
	_ ports.Registry             = (*mockRegistry)(nil)
	_ ports.LocalLoader          = (*mockLocalLoader)(nil)
	_ ports.TarballWriter        = (*mockTarballWriter)(nil)
	_ ports.SBOMGenerator        = (*mockSBOMGenerator)(nil)
	_ ports.StaticServerProvider = (*mockStaticServerProvider)(nil)
	_ ports.NativeInspector      = (*mockNativeInspector)(nil)
	_ ports.SLSAGenerator        = (*mockSLSAGenerator)(nil)
	_ ports.CosignSigner         = (*mockCosignSigner)(nil)
	_ ports.DSSESigner           = (*mockDSSESigner)(nil)
	_ ports.BunRuntimeResolver   = (*mockBunRuntimeResolver)(nil)
	_ ports.RemoteCacher         = (*mockRemoteCacher)(nil)
	_ ports.EnvBakeDetector      = (*mockEnvBakeDetector)(nil)
	_ ports.AssetOverlayResolver = (*mockAssetOverlayForSecretScan)(nil)
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

func (m *mockBaseImageResolver) RecordScanResult(ctx context.Context, lockfilePath string, preset ports.BaseImagePreset, _ string, scan ports.ScanResult) error {
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

// Mock implementation of ports.Registry. The signature/attestation methods
// record their calls and, by default, round-trip what was attached: a fetch
// returns exactly the bundle/envelope the matching attach stored, keyed by
// subject digest — so the pipeline's post-push self-verification stage
// exercises its real fetch-back flow against the mock without a registry.
type mockRegistry struct {
	pushFn               func(ctx context.Context, req ports.PushRequest) (ports.PublishResult, error)
	attachSBOMFn         func(ctx context.Context, req ports.AttachSBOMRequest) (ports.PublishResult, error)
	attachSignatureFn    func(ctx context.Context, req ports.AttachSignatureRequest) (ports.PublishResult, error)
	attachAttestationFn  func(ctx context.Context, req ports.AttachAttestationRequest) (ports.PublishResult, error)
	fetchSignatureFn     func(ctx context.Context, req ports.FetchAttachmentRequest) (ports.CosignSignatureBundle, error)
	fetchAttestationFn   func(ctx context.Context, req ports.FetchAttachmentRequest) (ports.DSSEEnvelope, error)
	attachedSignatures   map[string]ports.CosignSignatureBundle
	attachedAttestations map[string]ports.DSSEEnvelope
	fetchSignatureCalls  int
	fetchAttestCalls     int
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

func (m *mockRegistry) AttachSignature(ctx context.Context, req ports.AttachSignatureRequest) (ports.PublishResult, error) {
	if m.attachSignatureFn != nil {
		return m.attachSignatureFn(ctx, req)
	}
	if m.attachedSignatures == nil {
		m.attachedSignatures = make(map[string]ports.CosignSignatureBundle)
	}
	m.attachedSignatures[req.Subject.String()] = req.Bundle
	return ports.PublishResult{Ref: req.Repo + ":" + ports.SigTag(req.Subject), Tags: []string{ports.SigTag(req.Subject)}}, nil
}

func (m *mockRegistry) AttachAttestation(ctx context.Context, req ports.AttachAttestationRequest) (ports.PublishResult, error) {
	if m.attachAttestationFn != nil {
		return m.attachAttestationFn(ctx, req)
	}
	if m.attachedAttestations == nil {
		m.attachedAttestations = make(map[string]ports.DSSEEnvelope)
	}
	m.attachedAttestations[req.Subject.String()] = req.Envelope
	return ports.PublishResult{Ref: req.Repo + ":" + ports.AttTag(req.Subject), Tags: []string{ports.AttTag(req.Subject)}}, nil
}

func (m *mockRegistry) FetchSignature(ctx context.Context, req ports.FetchAttachmentRequest) (ports.CosignSignatureBundle, error) {
	m.fetchSignatureCalls++
	if m.fetchSignatureFn != nil {
		return m.fetchSignatureFn(ctx, req)
	}
	bundle, ok := m.attachedSignatures[req.Subject.String()]
	if !ok {
		return ports.CosignSignatureBundle{}, fmt.Errorf("mock registry: no signature attached for %s: %w", req.Subject, core.ErrSignatureMissing)
	}
	return bundle, nil
}

func (m *mockRegistry) FetchAttestation(ctx context.Context, req ports.FetchAttachmentRequest) (ports.DSSEEnvelope, error) {
	m.fetchAttestCalls++
	if m.fetchAttestationFn != nil {
		return m.fetchAttestationFn(ctx, req)
	}
	env, ok := m.attachedAttestations[req.Subject.String()]
	if !ok {
		return ports.DSSEEnvelope{}, fmt.Errorf("mock registry: no attestation attached for %s: %w", req.Subject, core.ErrSignatureMissing)
	}
	return env, nil
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
	// The default statement names the requested output digest as its
	// subject, matching the real slsa generator's contract — the signing
	// stage's self-verification cross-checks the fetched statement's
	// subject against the digest it was attached under, so a subject-less
	// default would fail every signing-enabled pipeline test for the wrong
	// reason.
	return ports.SLSAStatement{
		Type:          ports.InTotoStatementType,
		PredicateType: ports.SLSAProvenancePredicateType,
		Subject: []ports.ResourceDescriptor{{
			Name:   req.Repo,
			Digest: map[string]string{req.OutputDigest.Algorithm: req.OutputDigest.Hex},
		}},
	}, nil
}

type mockCosignSigner struct {
	signFn    func(ctx context.Context, req ports.CosignSignRequest) (ports.CosignSignatureBundle, error)
	verifyFn  func(ctx context.Context, bundle ports.CosignSignatureBundle, pubKeyPEM []byte, expectedRepo string, expectedDigest v1.Hash) error
	signCalls []ports.CosignSignRequest
}

func (m *mockCosignSigner) CreatePayload(req ports.CosignSignRequest) ([]byte, error) {
	return []byte(`{"critical":{}}`), nil
}

func (m *mockCosignSigner) Sign(ctx context.Context, req ports.CosignSignRequest) (ports.CosignSignatureBundle, error) {
	m.signCalls = append(m.signCalls, req)
	if m.signFn != nil {
		return m.signFn(ctx, req)
	}
	return ports.CosignSignatureBundle{
		PayloadBytes:    []byte(`{"critical":{"image":{"docker-manifest-digest":"` + req.Digest.String() + `"}}}`),
		SignatureBytes:  []byte("mock-signature"),
		Base64Signature: base64.StdEncoding.EncodeToString([]byte("mock-signature")),
		Repo:            req.Repo,
		Digest:          req.Digest,
	}, nil
}

func (m *mockCosignSigner) Verify(ctx context.Context, bundle ports.CosignSignatureBundle, pubKeyPEM []byte, expectedRepo string, expectedDigest v1.Hash) error {
	if m.verifyFn != nil {
		return m.verifyFn(ctx, bundle, pubKeyPEM, expectedRepo, expectedDigest)
	}
	return nil
}

type mockDSSESigner struct {
	signFn    func(ctx context.Context, req ports.DSSESignRequest) (ports.DSSEEnvelope, error)
	verifyFn  func(ctx context.Context, envelope ports.DSSEEnvelope, pubKeyPEM []byte) ([]byte, error)
	signCalls []ports.DSSESignRequest
}

func (m *mockDSSESigner) CreatePAE(payloadType string, payload []byte) []byte {
	return []byte{}
}

func (m *mockDSSESigner) Sign(ctx context.Context, req ports.DSSESignRequest) (ports.DSSEEnvelope, error) {
	m.signCalls = append(m.signCalls, req)
	if m.signFn != nil {
		return m.signFn(ctx, req)
	}
	// Round-trippable default: the payload is genuinely base64-encoded so
	// the default Verify below can decode it back for the pipeline's
	// self-verification subject cross-check.
	return ports.DSSEEnvelope{
		Payload:     base64.StdEncoding.EncodeToString(req.PayloadBytes),
		PayloadType: req.PayloadType,
		Signatures:  []ports.DSSESignature{{Sig: base64.StdEncoding.EncodeToString([]byte("mock-dsse-sig"))}},
	}, nil
}

func (m *mockDSSESigner) Verify(ctx context.Context, envelope ports.DSSEEnvelope, pubKeyPEM []byte) ([]byte, error) {
	if m.verifyFn != nil {
		return m.verifyFn(ctx, envelope, pubKeyPEM)
	}
	return base64.StdEncoding.DecodeString(envelope.Payload)
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

	// lastCheckReq captures the request the pipeline actually handed to
	// Check, so tests can assert on the verification options the pipeline
	// derived (rather than only on whether Check was reached at all).
	lastCheckReq ports.RemoteCacheRequest
}

func (m *mockRemoteCacher) ComputeInputHash(context.Context, ports.RemoteCacheInputRequest) (string, error) {
	m.computeInputHashCalled = true
	return "deadbeef", nil
}

func (m *mockRemoteCacher) Check(_ context.Context, req ports.RemoteCacheRequest) (ports.RemoteCacheResult, error) {
	m.checkCalled = true
	m.lastCheckReq = req
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
			return ports.SLSAStatement{
				Type:          ports.InTotoStatementType,
				PredicateType: ports.SLSAProvenancePredicateType,
				Subject: []ports.ResourceDescriptor{{
					Name:   req.Repo,
					Digest: map[string]string{req.OutputDigest.Algorithm: req.OutputDigest.Hex},
				}},
			}, nil
		},
	}

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
		Sign:       true,
		Signing:    core.SigningOptions{KeyPEM: []byte("test-key"), PublicKeyPEM: []byte("test-pub")},
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

// TestBuildPushSuccess_ImageLabelUsesResolvedBunRuntimeNotHostToolchain is
// F5's regression guard: the dev.pokkum.bun.version IMAGE LABEL must report
// the Bun runtime actually embedded in the image — the same resolved
// ports.BunResolverResult that SBOM, SLSA provenance and `pokkum verify`
// already agree on ("1.2.2", per mockBunRuntimeResolver's default) — never
// the HOST's build-tool bun that compiled the app (mockCompiler's Preflight,
// "1.2.18"). The two fixtures are deliberately distinct so a regression
// back to the host value is provable, not just plausible.
func TestBuildPushSuccess_ImageLabelUsesResolvedBunRuntimeNotHostToolchain(t *testing.T) {
	deps := newFullDeps(io.Discard)

	var pkgReqs []ports.PackageRequest
	basePackager := &mockPackager{}
	deps.Packager = &mockPackager{
		buildFn: func(ctx context.Context, req ports.PackageRequest) (v1.Image, error) {
			pkgReqs = append(pkgReqs, req)
			return basePackager.Build(ctx, req)
		},
	}

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
	}

	if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if len(pkgReqs) == 0 {
		t.Fatalf("packager was never invoked")
	}

	got, ok := pkgReqs[0].Labels[ports.LabelBunVersion]
	if !ok {
		t.Fatalf("image labels missing %s entirely, want %q", ports.LabelBunVersion, "1.2.2")
	}
	if got != "1.2.2" {
		t.Errorf("image label %s = %q, want %q (the resolved embedded runtime, not the host compiler bun %q)",
			ports.LabelBunVersion, got, "1.2.2", "1.2.18")
	}
}

// TestBuildPushSuccess_StaticStrategyOmitsBunVersionLabel is a second half
// of F5 found during self-review, not in the original bug report: a static
// -strategy image ships "no Bun runtime and no supervisor layer" at all
// (see ports.BuildStrategy.ApplyStatic's doc comment — pokkum-static serves
// prerendered files directly), so dev.pokkum.bun.version must be absent
// there too, exactly like the --runtime=node case, rather than falling back
// to the host compiler bun the way the exe strategy legitimately does
// (exe's `bun build --compile` actually bakes that bun into the artifact;
// static never touches Bun at runtime at all).
func TestBuildPushSuccess_StaticStrategyOmitsBunVersionLabel(t *testing.T) {
	projectDir := t.TempDir()
	outputDir := t.TempDir()

	deps := newFullDeps(io.Discard)
	deps.StaticServer = &mockStaticServerProvider{}
	deps.Compiler = &mockCompiler{
		prepareFn: func(context.Context, ports.PrepareRequest) (ports.PrepareResult, error) {
			return ports.PrepareResult{EntrypointPath: filepath.Join(outputDir, "index.js"), OutputDir: outputDir}, nil
		},
	}

	var pkgReqs []ports.PackageRequest
	basePackager := &mockPackager{}
	deps.Packager = &mockPackager{
		buildFn: func(ctx context.Context, req ports.PackageRequest) (v1.Image, error) {
			pkgReqs = append(pkgReqs, req)
			return basePackager.Build(ctx, req)
		},
	}

	req := postBuildScanTestRequest(t, projectDir)
	req.Compile.Strategy = core.StrategyStatic

	if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
		t.Fatalf("static build failed: %v", err)
	}
	if len(pkgReqs) == 0 {
		t.Fatalf("packager was never invoked")
	}

	if v, ok := pkgReqs[0].Labels[ports.LabelBunVersion]; ok {
		t.Errorf("image label %s = %q, want ABSENT for a static-strategy image (no Bun is embedded in it)", ports.LabelBunVersion, v)
	}
}

// TestBuild_HermeticThreadsIntoBunResolverAndSLSAProvenance is PR-2's
// pipeline-wiring regression guard (security review findings F5/F6): a
// --hermetic build must (a) tell the Bun runtime resolver not to reach the
// network on a cache miss (BunResolverRequest.Offline), and (b) record an
// honest, platform-accurate enforcement mode in SLSA provenance
// (SLSAGeneratorRequest.HermeticEnforcement), not just echo the Hermetic
// bool back unexamined. Both are asserted from one real core.Build call so
// this proves the actual wiring, not just that the two structs' fields
// exist.
func TestBuild_HermeticThreadsIntoBunResolverAndSLSAProvenance(t *testing.T) {
	deps := newFullDeps(io.Discard)

	var bunReq ports.BunResolverRequest
	deps.BunRuntime = &mockBunRuntimeResolver{
		resolveFn: func(_ context.Context, req ports.BunResolverRequest) (ports.BunResolverResult, error) {
			bunReq = req
			return ports.BunResolverResult{
				BinaryPath: "/mock/bun", Version: "1.2.2", Variant: ports.BunVariantStandard,
				Platform: req.Platform, SHA256: "mockbunsha256", Size: 1000,
			}, nil
		},
	}

	var slsaReq ports.SLSAGeneratorRequest
	deps.SLSAGenerator = &mockSLSAGenerator{
		generateFn: func(_ context.Context, req ports.SLSAGeneratorRequest) (ports.SLSAStatement, error) {
			slsaReq = req
			return ports.SLSAStatement{
				Type:          ports.InTotoStatementType,
				PredicateType: ports.SLSAProvenancePredicateType,
				Subject: []ports.ResourceDescriptor{{
					Name:   req.Repo,
					Digest: map[string]string{req.OutputDigest.Algorithm: req.OutputDigest.Hex},
				}},
			}, nil
		},
	}

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
		Sign:       true,
		Signing:    core.SigningOptions{KeyPEM: []byte("test-key"), PublicKeyPEM: []byte("test-pub")},
		Hermetic:   true,
	}

	if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if !bunReq.Offline {
		t.Errorf("expected BunResolverRequest.Offline = true for a --hermetic build, got false")
	}
	if !slsaReq.Hermetic {
		t.Errorf("expected SLSAGeneratorRequest.Hermetic = true, got false")
	}
	wantEnforcement := "advisory-env-only"
	if runtime.GOOS == "linux" {
		wantEnforcement = "kernel-enforced-netns"
	}
	if slsaReq.HermeticEnforcement != wantEnforcement {
		t.Errorf("SLSAGeneratorRequest.HermeticEnforcement = %q, want %q (GOOS=%s)", slsaReq.HermeticEnforcement, wantEnforcement, runtime.GOOS)
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
			return ports.SLSAStatement{
				Type:          ports.InTotoStatementType,
				PredicateType: ports.SLSAProvenancePredicateType,
				Subject: []ports.ResourceDescriptor{{
					Name:   req.Repo,
					Digest: map[string]string{req.OutputDigest.Algorithm: req.OutputDigest.Hex},
				}},
			}, nil
		},
	}

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
		Sign:       true,
		Signing:    core.SigningOptions{KeyPEM: []byte("test-key"), PublicKeyPEM: []byte("test-pub")},
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

// TestBuild_CacheVerifyKeyInheritsSigningPublicKey pins down the pipeline's
// half of the cache-verify key chain: the composition root fills
// req.CacheVerify.PublicKeyPEM from --cache-verify-key/POKKUM_CACHE_PUBKEY/
// .pokkum.yaml, the cacher then walks POKKUM_*_PUBKEY, and this build's own
// signing public key is offered as the LAST-RESORT entry via a dedicated
// field. The whole point of the dedicated field (rather than pipeline.go
// writing PublicKeyPEM directly) is that precedence stays in exactly one
// place — so what this test guards is that the pipeline OFFERS the derived
// key without ever overwriting an explicit one.
func TestBuild_CacheVerifyKeyInheritsSigningPublicKey(t *testing.T) {
	signingPub := []byte("-----BEGIN PUBLIC KEY-----\nSIGNING-KEY-PUBLIC-HALF\n-----END PUBLIC KEY-----\n")
	explicitPub := []byte("-----BEGIN PUBLIC KEY-----\nEXPLICIT-CACHE-VERIFY-KEY\n-----END PUBLIC KEY-----\n")

	buildWith := func(t *testing.T, signing core.SigningOptions, verify core.RemoteCacheVerifyOptions) *mockRemoteCacher {
		t.Helper()
		deps := newFullDeps(io.Discard)
		cacher := &mockRemoteCacher{hit: true, verified: true, signerIdentity: "static-key"}
		deps.RemoteCache = cacher

		req := core.BuildRequest{
			ProjectDir:  "/abs/project",
			Repo:        "ghcr.io/example/app",
			Platforms:   []core.Platform{core.LinuxAMD64},
			Tags:        []string{"v1.0.0"},
			Sign:        true,
			Signing:     signing,
			CacheVerify: verify,
		}
		if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
			t.Fatalf("Build failed: %v", err)
		}
		if !cacher.checkCalled {
			t.Fatalf("expected RemoteCache.Check to be consulted")
		}
		return cacher
	}

	activeVerify := core.RemoteCacheVerifyOptions{
		VerifySignature: true,
		VerifyMode:      core.CacheVerifyStaticKey,
	}

	t.Run("signing public key is offered when nothing else in the chain is set", func(t *testing.T) {
		cacher := buildWith(t,
			core.SigningOptions{KeyPEM: []byte("PRIVATE"), PublicKeyPEM: signingPub},
			activeVerify,
		)
		got := cacher.lastCheckReq.Verify
		if !bytes.Equal(got.SigningPublicKeyPEM, signingPub) {
			t.Errorf("Verify.SigningPublicKeyPEM = %q, want the signing key's public half %q — a build signed with --signing-key alone must be able to verify its own cache entries without a second key being configured", got.SigningPublicKeyPEM, signingPub)
		}
		if len(got.PublicKeyPEM) != 0 {
			t.Errorf("Verify.PublicKeyPEM = %q, want it left empty — the derived key must arrive as the last-resort field, not by pre-empting the POKKUM_*_PUBKEY entries the cacher resolves after PublicKeyPEM", got.PublicKeyPEM)
		}
	})

	t.Run("an explicitly configured cache-verify key is never overridden", func(t *testing.T) {
		verify := activeVerify
		verify.PublicKeyPEM = explicitPub
		cacher := buildWith(t,
			core.SigningOptions{KeyPEM: []byte("PRIVATE"), PublicKeyPEM: signingPub},
			verify,
		)
		got := cacher.lastCheckReq.Verify
		if !bytes.Equal(got.PublicKeyPEM, explicitPub) {
			t.Errorf("Verify.PublicKeyPEM = %q, want the explicitly configured key %q preserved verbatim", got.PublicKeyPEM, explicitPub)
		}
		if bytes.Equal(got.PublicKeyPEM, signingPub) {
			t.Errorf("BUG: an explicit --cache-verify-key was replaced by the signing key's public half; an explicit trust choice must always win")
		}
	})

	t.Run("no signing key leaves the chain exactly as it was", func(t *testing.T) {
		cacher := buildWith(t, core.SigningOptions{}, activeVerify)
		got := cacher.lastCheckReq.Verify
		if len(got.SigningPublicKeyPEM) != 0 {
			t.Errorf("Verify.SigningPublicKeyPEM = %q, want empty when no signing key is configured (that case must not change behaviour)", got.SigningPublicKeyPEM)
		}
		if len(got.PublicKeyPEM) != 0 {
			t.Errorf("Verify.PublicKeyPEM = %q, want empty", got.PublicKeyPEM)
		}
	})
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
		Signing:    core.SigningOptions{KeyPEM: []byte("test-key"), PublicKeyPEM: []byte("test-pub")},
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
		// A dry run must NOT persist the scan result. This assertion used to
		// require the opposite — it pinned the behaviour that made
		// `pokkum build --dry-run`, documented as "perform no writes", create
		// pokkum.lock in a project that had none. An assertion that pins
		// current behaviour is not a test of intended behaviour.
		if recorded {
			t.Errorf("RecordScanResult was invoked during a --dry-run build, which is documented to perform no writes "+
				"(it persists scan findings and timestamps into pokkum.lock, creating the file if absent); recorded scan = %+v", recordedScan)
		}
	})

	t.Run("Non-dry-run build records the scan result in the lockfile", func(t *testing.T) {
		// The positive half of the pair above: the recording still has to
		// happen on a real build, or "dry run does not write" would be
		// satisfied by a resolver that never writes at all.
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

		if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{PrintManifest: true}); err != nil {
			t.Fatalf("unexpected build error: %v", err)
		}
		if !recorded {
			t.Fatal("expected RecordScanResult to be invoked on a non-dry-run build")
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

// signedTestRequest returns a push-mode single-platform request with signing
// enabled and a (fake, mock-consumed) key pair present — the state the real
// signing stage requires to run.
func signedTestRequest() core.BuildRequest {
	return core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
		Sign:       true,
		Signing:    core.SigningOptions{KeyPEM: []byte("test-key"), PublicKeyPEM: []byte("test-pub")},
	}
}

// TestBuild_SignReachesSignersAndSelfVerifies is THE regression guard for
// the original signing bug: a signing-enabled push used to generate a SLSA
// statement, log it, and stop — CosignSigner.Sign and DSSESigner.Sign had
// zero production call sites, nothing was ever attached, and `cosign verify`
// failed on every image this tool built. This test proves, from one real
// core.Build call, that the signers are actually invoked with the pushed
// digest and their outputs actually attached AND fetched back for
// self-verification.
func TestBuild_SignReachesSignersAndSelfVerifies(t *testing.T) {
	deps := newFullDeps(io.Discard)
	cosignMock := &mockCosignSigner{}
	dsseMock := &mockDSSESigner{}
	regMock := &mockRegistry{}
	deps.CosignSigner = cosignMock
	deps.DSSESigner = dsseMock
	deps.Registry = regMock

	res, err := core.Build(context.Background(), deps, signedTestRequest(), core.BuildOptions{})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	wantDigest := res.Image.Digest
	if wantDigest.Hex == "" {
		t.Fatalf("published digest is empty")
	}

	if len(cosignMock.signCalls) != 1 {
		t.Fatalf("CosignSigner.Sign calls = %d, want 1 (single-platform push has one subject)", len(cosignMock.signCalls))
	}
	if cosignMock.signCalls[0].Digest != wantDigest {
		t.Errorf("CosignSigner.Sign digest = %s, want the pushed digest %s", cosignMock.signCalls[0].Digest, wantDigest)
	}
	if len(cosignMock.signCalls[0].KeyPEM) == 0 {
		t.Errorf("CosignSigner.Sign received no key material")
	}
	// Two in-toto statements are signed per subject: the SLSA provenance and
	// the SBOM. Asserting WHICH ones, by predicateType, rather than just the
	// count — a bare count passes just as happily if the same statement were
	// signed twice, and it was the SBOM's absence from any signature that let
	// a pushed SBOM be swapped undetected.
	if len(dsseMock.signCalls) != 2 {
		t.Fatalf("DSSESigner.Sign calls = %d, want 2 (SLSA provenance + SBOM attestation)", len(dsseMock.signCalls))
	}
	seenPredicates := map[string]bool{}
	for i, call := range dsseMock.signCalls {
		if call.PayloadType != ports.InTotoPayloadType {
			t.Errorf("DSSE payload type[%d] = %q, want %q", i, call.PayloadType, ports.InTotoPayloadType)
		}
		var stmt struct {
			Type          string `json:"_type"`
			PredicateType string `json:"predicateType"`
			Subject       []struct {
				Digest map[string]string `json:"digest"`
			} `json:"subject"`
		}
		if err := json.Unmarshal(call.PayloadBytes, &stmt); err != nil {
			t.Fatalf("DSSE payload[%d] is not a JSON in-toto statement: %v", i, err)
		}
		seenPredicates[stmt.PredicateType] = true
		// Every signed statement must name the digest actually pushed, or it
		// authenticates a claim about some other image.
		if len(stmt.Subject) == 0 || stmt.Subject[0].Digest["sha256"] != wantDigest.Hex {
			t.Errorf("signed statement[%d] (%s) subject = %+v, want the pushed digest %s",
				i, stmt.PredicateType, stmt.Subject, wantDigest.Hex)
		}
	}
	if !seenPredicates[ports.SLSAProvenancePredicateType] {
		t.Errorf("no SLSA provenance statement was signed; predicates seen: %v", seenPredicates)
	}
	if !seenPredicates[ports.SPDXPredicateType] {
		t.Errorf("no SBOM statement was signed, so the attached SBOM is unauthenticated and can be replaced "+
			"without any verification path noticing; predicates seen: %v", seenPredicates)
	}

	if _, ok := regMock.attachedSignatures[wantDigest.String()]; !ok {
		t.Errorf("no signature attached for pushed digest %s", wantDigest)
	}
	if _, ok := regMock.attachedAttestations[wantDigest.String()]; !ok {
		t.Errorf("no attestation attached for pushed digest %s", wantDigest)
	}
	if regMock.fetchSignatureCalls != 1 || regMock.fetchAttestCalls != 1 {
		t.Errorf("self-verification fetches = %d sig / %d att, want 1/1 — the post-push self-verify stage must actually read the attachments back", regMock.fetchSignatureCalls, regMock.fetchAttestCalls)
	}

	if res.Signing == nil || !res.Signing.Signed {
		t.Fatalf("BuildResult.Signing = %+v, want Signed=true", res.Signing)
	}
	if len(res.Signing.SignatureRefs) != 1 || len(res.Signing.AttestationRefs) != 1 {
		t.Errorf("Signing refs = %v / %v, want one of each", res.Signing.SignatureRefs, res.Signing.AttestationRefs)
	}
}

// TestBuild_SignAttachFailureFailsBuild: a signing-enabled build whose
// signature could not be attached must NOT report success, even though the
// image itself already landed in the registry.
func TestBuild_SignAttachFailureFailsBuild(t *testing.T) {
	deps := newFullDeps(io.Discard)
	deps.Registry = &mockRegistry{
		attachSignatureFn: func(_ context.Context, req ports.AttachSignatureRequest) (ports.PublishResult, error) {
			return ports.PublishResult{}, fmt.Errorf("registry: attach signature %s: boom: %w", req.Repo, core.ErrSigningFailed)
		},
	}

	_, err := core.Build(context.Background(), deps, signedTestRequest(), core.BuildOptions{})
	if err == nil {
		t.Fatalf("Build succeeded despite the signature attach failing")
	}
	if !errors.Is(err, core.ErrSigningFailed) {
		t.Errorf("err = %v, want core.ErrSigningFailed", err)
	}
}

// TestBuild_SignSelfVerifyFailureFailsBuild: an attach that "succeeded" but
// whose artifact cannot be fetched back and verified from the registry must
// fail the build — this is the stage that would have made the original
// log-and-forget signing bug unshippable.
func TestBuild_SignSelfVerifyFailureFailsBuild(t *testing.T) {
	t.Run("fetch fails", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.Registry = &mockRegistry{
			fetchSignatureFn: func(_ context.Context, req ports.FetchAttachmentRequest) (ports.CosignSignatureBundle, error) {
				return ports.CosignSignatureBundle{}, fmt.Errorf("mock: nothing at %s: %w", ports.SigTag(req.Subject), core.ErrSignatureMissing)
			},
		}
		_, err := core.Build(context.Background(), deps, signedTestRequest(), core.BuildOptions{})
		if !errors.Is(err, core.ErrSignatureSelfVerifyFailed) {
			t.Fatalf("err = %v, want core.ErrSignatureSelfVerifyFailed", err)
		}
	})

	t.Run("signature does not verify", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.CosignSigner = &mockCosignSigner{
			verifyFn: func(_ context.Context, _ ports.CosignSignatureBundle, _ []byte, _ string, _ v1.Hash) error {
				return errors.New("cosign: signature verification failed")
			},
		}
		_, err := core.Build(context.Background(), deps, signedTestRequest(), core.BuildOptions{})
		if !errors.Is(err, core.ErrSignatureSelfVerifyFailed) {
			t.Fatalf("err = %v, want core.ErrSignatureSelfVerifyFailed", err)
		}
	})

	t.Run("attestation subject mismatch", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		deps.SLSAGenerator = &mockSLSAGenerator{
			generateFn: func(_ context.Context, req ports.SLSAGeneratorRequest) (ports.SLSAStatement, error) {
				// Subject names a DIFFERENT digest than the one it will be
				// attached under — the self-verify cross-check must catch it.
				return ports.SLSAStatement{
					Type:          ports.InTotoStatementType,
					PredicateType: ports.SLSAProvenancePredicateType,
					Subject: []ports.ResourceDescriptor{{
						Name:   req.Repo,
						Digest: map[string]string{"sha256": "0000000000000000000000000000000000000000000000000000000000000000"},
					}},
				}, nil
			},
		}
		_, err := core.Build(context.Background(), deps, signedTestRequest(), core.BuildOptions{})
		if !errors.Is(err, core.ErrSignatureSelfVerifyFailed) {
			t.Fatalf("err = %v, want core.ErrSignatureSelfVerifyFailed (fetched attestation names a different subject digest)", err)
		}
	})
}

// TestBuild_SignWithoutKeyPushesUnsignedWithHonestResult: with signing
// enabled (the default) but no key available, the build must succeed
// unsigned — but the result must say so, and no signer may have been
// consulted (nothing to sign with).
func TestBuild_SignWithoutKeyPushesUnsignedWithHonestResult(t *testing.T) {
	deps := newFullDeps(io.Discard)
	cosignMock := &mockCosignSigner{}
	deps.CosignSigner = cosignMock

	req := signedTestRequest()
	req.Signing = core.SigningOptions{}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if res.Signing == nil {
		t.Fatalf("BuildResult.Signing is nil — an unsigned signing-enabled push must record its state")
	}
	if res.Signing.Signed {
		t.Errorf("Signing.Signed = true for a build with no key")
	}
	if res.Signing.Reason == "" {
		t.Errorf("Signing.Reason is empty; want an explanation of why the image is unsigned")
	}
	if len(cosignMock.signCalls) != 0 {
		t.Errorf("CosignSigner.Sign was called %d times with no key available", len(cosignMock.signCalls))
	}
}

// TestBuild_RequireSignedGate: --require-signed must fail fast (at
// validation, before any build work) when its preconditions cannot hold.
func TestBuild_RequireSignedGate(t *testing.T) {
	t.Run("no key", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		req := signedTestRequest()
		req.Signing = core.SigningOptions{Require: true}
		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
		if !errors.Is(err, core.ErrSigningKeyMissing) {
			t.Fatalf("err = %v, want core.ErrSigningKeyMissing", err)
		}
	})

	t.Run("no-sign conflict", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		req := signedTestRequest()
		req.Sign = false
		req.Signing.Require = true
		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
		if !errors.Is(err, core.ErrInvalidRequest) {
			t.Fatalf("err = %v, want core.ErrInvalidRequest", err)
		}
	})

	t.Run("non-push output", func(t *testing.T) {
		deps := newFullDeps(io.Discard)
		req := signedTestRequest()
		req.Signing.Require = true
		req.Output = core.OutputOptions{Mode: core.OutputTarball, TarballPath: "/tmp/x.tar"}
		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
		if !errors.Is(err, core.ErrInvalidRequest) {
			t.Fatalf("err = %v, want core.ErrInvalidRequest", err)
		}
	})
}

// TestBuild_SignNonPushOutputRecordsSkip: --sign (default true) with
// --tarball/--local used to silently no-op; now the result must record that
// nothing was signed (the loud warning is asserted by inspection of the log
// path; the machine-readable state is what this test pins).
func TestBuild_SignNonPushOutputRecordsSkip(t *testing.T) {
	deps := newFullDeps(io.Discard)
	cosignMock := &mockCosignSigner{}
	deps.CosignSigner = cosignMock

	req := signedTestRequest()
	req.Output = core.OutputOptions{Mode: core.OutputTarball, TarballPath: "/tmp/x.tar"}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}
	if res.Signing == nil || res.Signing.Signed {
		t.Fatalf("BuildResult.Signing = %+v, want non-nil with Signed=false", res.Signing)
	}
	if !strings.Contains(res.Signing.Reason, "tarball") {
		t.Errorf("Signing.Reason = %q, want it to name the output mode", res.Signing.Reason)
	}
	if len(cosignMock.signCalls) != 0 {
		t.Errorf("CosignSigner.Sign called %d times for a tarball build", len(cosignMock.signCalls))
	}
}

// TestBuild_MultiPlatformSignsIndexAndPerPlatformManifests pins the
// multi-arch attestation-subject answer (Roadmap 2d): .sig/.att attach to
// BOTH the index digest and every per-platform manifest digest, each
// attestation's statement naming exactly the digest it hangs off.
func TestBuild_MultiPlatformSignsIndexAndPerPlatformManifests(t *testing.T) {
	deps := newFullDeps(io.Discard)
	cosignMock := &mockCosignSigner{}
	regMock := &mockRegistry{}
	deps.CosignSigner = cosignMock
	deps.Registry = regMock

	req := signedTestRequest()
	req.Platforms = []core.Platform{core.LinuxAMD64, core.LinuxARM64}

	res, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	// One subject for the index plus one per platform.
	if len(cosignMock.signCalls) != 3 {
		t.Fatalf("CosignSigner.Sign calls = %d, want 3 (index + 2 per-platform manifests)", len(cosignMock.signCalls))
	}
	if cosignMock.signCalls[0].Digest != res.Image.Digest {
		t.Errorf("first signing subject = %s, want the published index digest %s", cosignMock.signCalls[0].Digest, res.Image.Digest)
	}
	seen := make(map[string]bool)
	for _, c := range cosignMock.signCalls {
		if seen[c.Digest.String()] {
			t.Errorf("digest %s signed twice — per-platform subjects must be distinct", c.Digest)
		}
		seen[c.Digest.String()] = true
	}
	if len(regMock.attachedSignatures) != 3 || len(regMock.attachedAttestations) != 3 {
		t.Errorf("attached %d signatures / %d attestations, want 3/3", len(regMock.attachedSignatures), len(regMock.attachedAttestations))
	}
	if regMock.fetchSignatureCalls != 3 || regMock.fetchAttestCalls != 3 {
		t.Errorf("self-verification fetches = %d/%d, want 3/3 — every subject must be verified, not just the index", regMock.fetchSignatureCalls, regMock.fetchAttestCalls)
	}
	if res.Signing == nil || !res.Signing.Signed || len(res.Signing.SignatureRefs) != 3 {
		t.Errorf("BuildResult.Signing = %+v, want Signed=true with 3 signature refs", res.Signing)
	}
}

// fakeSecretGuard is a controllable ports.SecretGuard test double that
// records every directory it was asked to scan (in call order) — used to
// pin down exactly WHICH trees the pipeline's post-build scan targets per
// strategy, independent of the real secretguard adapter's own detection
// logic (covered separately in internal/adapters/secretguard).
type fakeSecretGuard struct {
	// resultFor maps a scanned ProjectDir to the result to return for it.
	// A directory with no entry gets a clean ports.SecretScanResult{Passed:
	// true}.
	resultFor map[string]ports.SecretScanResult
	err       error

	scannedDirs []string
}

func (f *fakeSecretGuard) ScanDirectory(_ context.Context, req ports.SecretScanRequest) (ports.SecretScanResult, error) {
	f.scannedDirs = append(f.scannedDirs, req.ProjectDir)
	if f.err != nil {
		return ports.SecretScanResult{}, f.err
	}
	if res, ok := f.resultFor[req.ProjectDir]; ok {
		return res, nil
	}
	return ports.SecretScanResult{Passed: true}, nil
}

// postBuildScanTestRequest returns a minimal layered-strategy push request
// with real, empty temp directories so the real filesystem walk in
// internal/adapters/secretguard has somewhere real to look — unlike most of
// this file's requests, which point ProjectDir at a path
// ("/abs/project") that deliberately never exists on disk (fine for tests
// with SecretGuard left nil, but not for these).
func postBuildScanTestRequest(t *testing.T, projectDir string) core.BuildRequest {
	t.Helper()
	return core.BuildRequest{
		ProjectDir: projectDir,
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
	}
}

// TestBuild_PostBuildSecretScan_TargetsExactStrategyDirs pins down exactly
// which directories the pipeline hands to SecretGuard.ScanDirectory for
// each strategy, using fakeSecretGuard's call recording rather than the
// real adapter's own detection — this is specifically testing the WIRING
// (postBuildScanDirs / the fanOut pkgReq switch it mirrors), not secret
// detection itself.
func TestBuild_PostBuildSecretScan_TargetsExactStrategyDirs(t *testing.T) {
	t.Run("layered scans the whole output tree once", func(t *testing.T) {
		projectDir := t.TempDir()
		outputDir := t.TempDir()
		guard := &fakeSecretGuard{}
		deps := newFullDeps(io.Discard)
		deps.SecretGuard = guard
		deps.Compiler = &mockCompiler{
			prepareFn: func(context.Context, ports.PrepareRequest) (ports.PrepareResult, error) {
				return ports.PrepareResult{EntrypointPath: filepath.Join(outputDir, "index.js"), OutputDir: outputDir}, nil
			},
		}

		req := postBuildScanTestRequest(t, projectDir)
		if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
			t.Fatalf("Build failed: %v", err)
		}

		wantSecondCall := outputDir // pre-build source scan is call 1, post-build is call 2
		if len(guard.scannedDirs) < 2 || guard.scannedDirs[1] != wantSecondCall {
			t.Fatalf("scannedDirs = %v, want [%q, %q, ...] (source scan, then the WHOLE layered output tree)", guard.scannedDirs, projectDir, wantSecondCall)
		}
	})

	t.Run("static scans only client and prerendered, not the whole tree", func(t *testing.T) {
		projectDir := t.TempDir()
		outputDir := t.TempDir()
		guard := &fakeSecretGuard{}
		deps := newFullDeps(io.Discard)
		deps.SecretGuard = guard
		deps.StaticServer = &mockStaticServerProvider{}
		deps.Compiler = &mockCompiler{
			prepareFn: func(context.Context, ports.PrepareRequest) (ports.PrepareResult, error) {
				return ports.PrepareResult{EntrypointPath: filepath.Join(outputDir, "index.js"), OutputDir: outputDir}, nil
			},
		}

		req := postBuildScanTestRequest(t, projectDir)
		req.Compile.Strategy = core.StrategyStatic
		if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
			t.Fatalf("Build failed: %v", err)
		}

		wantClient := filepath.Join(outputDir, "client")
		wantPrerendered := filepath.Join(outputDir, "prerendered")
		got := guard.scannedDirs[1:] // drop the pre-build source scan
		if len(got) != 2 || got[0] != wantClient || got[1] != wantPrerendered {
			t.Fatalf("post-build scannedDirs = %v, want exactly [%q, %q] — static must not scan the whole output tree (it never ships %s itself, only client/prerendered)",
				got, wantClient, wantPrerendered, outputDir)
		}
	})

	t.Run("exe scans the output tree as a best-effort proxy for the compiled binary", func(t *testing.T) {
		projectDir := t.TempDir()
		outputDir := t.TempDir()
		guard := &fakeSecretGuard{}
		deps := newFullDeps(io.Discard)
		deps.SecretGuard = guard
		deps.Compiler = &mockCompiler{
			prepareFn: func(context.Context, ports.PrepareRequest) (ports.PrepareResult, error) {
				return ports.PrepareResult{EntrypointPath: filepath.Join(outputDir, "index.ts"), OutputDir: outputDir}, nil
			},
		}

		req := postBuildScanTestRequest(t, projectDir)
		req.Compile.Strategy = core.StrategyExe
		if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
			t.Fatalf("Build failed: %v", err)
		}

		if len(guard.scannedDirs) < 2 || guard.scannedDirs[1] != outputDir {
			t.Fatalf("post-build scannedDirs = %v, want the compile input tree %q scanned as a proxy (see the honest limitation documented at the Stage 5.5 call site: the final compiled binary itself is not scanned)", guard.scannedDirs, outputDir)
		}
	})
}

// TestBuild_PostBuildSecretScan_ExeCoversBundledJSOutsideOutputDir closes
// the --strategy=exe scanning gap with the REAL secretguard adapter through
// the real core.Build chain. exe is the only strategy whose shipped artifact
// is compiled from an entrypoint rather than packaged from a directory:
// `bun build --compile` bundles prep.EntrypointPath and its imports, and
// that entrypoint is not always inside prep.OutputDir — with --telemetry,
// sveltekitutils.PrepareVirtualTelemetryEntry rewrites it to
// <projectDir>/.pokkum/telemetry-entry.ts alongside a generated
// .pokkum/otel-bootstrap.ts. Both are written by Prepare, so they do not
// exist yet at the pre-build source scan, and neither is reachable from
// prep.OutputDir — so scanning only OutputDir left exactly the bundled JS
// that gets compiled into the shipped binary uncovered at BOTH stages.
//
// The mirror cases matter as much as the positive one: --allow-secret-pattern
// must still suppress on this path (a second, differently-behaving scan
// surface would be its own bug), and an entrypoint that IS inside OutputDir
// must not be scanned twice.
func TestBuild_PostBuildSecretScan_ExeCoversBundledJSOutsideOutputDir(t *testing.T) {
	// A real Google API key shape, matching the adapter's own rule set.
	const bakedSecret = `const OTEL_HEADERS={token:"AIzaSyD-9tSrke72PouQMnMX-a7eZSW0jkFMBWY"};`

	// buildExe models the real exe layout: OutputDir is the adapter's
	// staging tree, and the compile entrypoint lives in a sibling .pokkum/
	// directory outside it (the --telemetry wrapper case).
	buildExe := func(t *testing.T, allowPatterns []string) error {
		t.Helper()
		projectDir := t.TempDir()
		outputDir := filepath.Join(projectDir, ".svelte-kit", "jesterkit-sveltekit")
		if err := os.MkdirAll(filepath.Join(outputDir, "temp-server"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		// The staging tree itself is genuinely clean — this test must fail
		// only because of the entrypoint tree, never because the
		// already-covered OutputDir scan happened to catch something.
		if err := os.WriteFile(filepath.Join(outputDir, "temp-server", "assets.generated.ts"), []byte("export const assets=[];"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		pokkumDir := filepath.Join(projectDir, ".pokkum")
		if err := os.MkdirAll(pokkumDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(pokkumDir, "otel-bootstrap.ts"), []byte(bakedSecret), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		entrypoint := filepath.Join(pokkumDir, "telemetry-entry.ts")
		if err := os.WriteFile(entrypoint, []byte(`import "./otel-bootstrap.ts";`), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		deps := newFullDeps(io.Discard)
		deps.SecretGuard = secretguard.NewAdapter()
		deps.Compiler = &mockCompiler{
			prepareFn: func(context.Context, ports.PrepareRequest) (ports.PrepareResult, error) {
				return ports.PrepareResult{EntrypointPath: entrypoint, OutputDir: outputDir}, nil
			},
		}

		req := postBuildScanTestRequest(t, projectDir)
		req.Compile.Strategy = core.StrategyExe
		req.AllowSecretPatterns = allowPatterns
		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
		return err
	}

	t.Run("a secret in the bundled JS outside OutputDir fails the build", func(t *testing.T) {
		err := buildExe(t, nil)
		if err == nil {
			t.Fatalf("expected the build to fail: the secret sits in the JS that `bun build --compile` bundles into the shipped binary")
		}
		if !errors.Is(err, core.ErrSecretInlined) {
			t.Errorf("err = %v, want it to wrap core.ErrSecretInlined — exe must fail exactly the way layered/static do, not through a second, softer path", err)
		}
		if !strings.Contains(err.Error(), "post-build") {
			t.Errorf("err = %v, want it to name the post-build stage", err)
		}
	})

	t.Run("--allow-secret-pattern still suppresses on this path", func(t *testing.T) {
		if err := buildExe(t, []string{`AIzaSyD-9tSrke72PouQMnMX-a7eZSW0jkFMBWY`}); err != nil {
			t.Errorf("expected --allow-secret-pattern to suppress the finding on exe's entrypoint tree exactly as it does on every other scanned tree, got: %v", err)
		}
	})

	// Row 3/4 counterpart: exe now scans TWO trees, so the case above (a
	// finding on the second) must not be the only one covered — a refactor
	// that handled only the newly added directory would pass it while
	// silently dropping the pre-existing OutputDir coverage.
	t.Run("a secret in OutputDir (the first scanned tree) still fails the build", func(t *testing.T) {
		projectDir := t.TempDir()
		outputDir := filepath.Join(projectDir, ".svelte-kit", "jesterkit-sveltekit", "temp-server")
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(outputDir, "index.ts"), []byte(bakedSecret), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		pokkumDir := filepath.Join(projectDir, ".pokkum")
		if err := os.MkdirAll(pokkumDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		entrypoint := filepath.Join(pokkumDir, "telemetry-entry.ts")
		if err := os.WriteFile(entrypoint, []byte(`import "../.svelte-kit/jesterkit-sveltekit/temp-server/index.ts";`), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		deps := newFullDeps(io.Discard)
		deps.SecretGuard = secretguard.NewAdapter()
		deps.Compiler = &mockCompiler{
			prepareFn: func(context.Context, ports.PrepareRequest) (ports.PrepareResult, error) {
				return ports.PrepareResult{EntrypointPath: entrypoint, OutputDir: outputDir}, nil
			},
		}
		req := postBuildScanTestRequest(t, projectDir)
		req.Compile.Strategy = core.StrategyExe
		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
		if err == nil || !errors.Is(err, core.ErrSecretInlined) {
			t.Errorf("expected a secret in exe's output tree to still fail with ErrSecretInlined, got: %v", err)
		}
	})

	// Precision guard: when the entrypoint already lives inside OutputDir
	// (the no-telemetry exe layout, and every layered build), the tree must
	// be handed to the scanner ONCE — a duplicate scan doubles every finding
	// an operator has to read and doubles the walk cost of the largest tree
	// in the build.
	t.Run("an entrypoint inside OutputDir is not scanned twice", func(t *testing.T) {
		projectDir := t.TempDir()
		outputDir := t.TempDir()
		guard := &fakeSecretGuard{}
		deps := newFullDeps(io.Discard)
		deps.SecretGuard = guard
		deps.Compiler = &mockCompiler{
			prepareFn: func(context.Context, ports.PrepareRequest) (ports.PrepareResult, error) {
				return ports.PrepareResult{
					EntrypointPath: filepath.Join(outputDir, "temp-server", "index.ts"),
					OutputDir:      outputDir,
				}, nil
			},
		}

		req := postBuildScanTestRequest(t, projectDir)
		req.Compile.Strategy = core.StrategyExe
		if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
			t.Fatalf("Build failed: %v", err)
		}

		got := guard.scannedDirs[1:] // drop the pre-build source scan
		if len(got) != 1 || got[0] != outputDir {
			t.Fatalf("post-build scannedDirs = %v, want exactly [%q] — the entrypoint's own directory is already inside the output tree", got, outputDir)
		}
	})
}

// TestBuild_PostBuildSecretScan_CatchesSecretAbsentFromSource is this
// feature's row-14 regression fixture (mem:self_review_checklist): a
// secret-scanning feature's test must start in the state the feature exists
// to catch. Source (ProjectDir) is genuinely clean; the secret exists ONLY
// in the compiler's OUTPUT — modeling $env/static/* baking, a Vite `define`
// replacement, or a compromised build-time dependency writing into a
// server chunk, none of which exist yet at the pre-build scan point. This
// exercises the REAL internal/adapters/secretguard adapter (not a fake)
// through the real core.Build call chain, so it proves the wiring AND the
// detection logic together — not just one or the other.
func TestBuild_PostBuildSecretScan_CatchesSecretAbsentFromSource(t *testing.T) {
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, "src_app.ts"), []byte(`console.log("clean source, nothing here");`), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	outputDir := t.TempDir()
	serverDir := filepath.Join(outputDir, "server")
	if err := os.MkdirAll(serverDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A real hardcoded secret pattern, present ONLY in the built chunk —
	// exactly the shape a $env/static/private misuse or a compromised
	// build-time dependency would produce.
	const bakedSecret = `export const cfg={apiKey:"AIzaSyD-9tSrke72PouQMnMX-a7eZSW0jkFMBWY"};`
	if err := os.WriteFile(filepath.Join(serverDir, "chunk-abc123.js"), []byte(bakedSecret), 0o644); err != nil {
		t.Fatalf("write output file: %v", err)
	}

	deps := newFullDeps(io.Discard)
	deps.SecretGuard = secretguard.NewAdapter()
	deps.Compiler = &mockCompiler{
		prepareFn: func(context.Context, ports.PrepareRequest) (ports.PrepareResult, error) {
			return ports.PrepareResult{EntrypointPath: filepath.Join(outputDir, "index.js"), OutputDir: outputDir}, nil
		},
	}

	req := postBuildScanTestRequest(t, projectDir)
	_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err == nil {
		t.Fatalf("expected the build to fail on a secret present only in the compiled output")
	}
	if !errors.Is(err, core.ErrSecretInlined) {
		t.Errorf("err = %v, want it to wrap core.ErrSecretInlined", err)
	}
	if !strings.Contains(err.Error(), "post-build") {
		t.Errorf("err = %v, want it to identify the post-build stage (so an operator isn't left thinking their SOURCE is the problem)", err)
	}
}

// TestBuild_PostBuildSecretScan_CleanOutputStillPublishes is the mirror of
// the above: a genuinely clean compiled output must not be flagged, so this
// feature does not turn every green build red.
func TestBuild_PostBuildSecretScan_CleanOutputStillPublishes(t *testing.T) {
	projectDir := t.TempDir()
	outputDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outputDir, "index.js"), []byte(`console.log("nothing sensitive here");`), 0o644); err != nil {
		t.Fatalf("write output file: %v", err)
	}

	deps := newFullDeps(io.Discard)
	deps.SecretGuard = secretguard.NewAdapter()
	deps.Compiler = &mockCompiler{
		prepareFn: func(context.Context, ports.PrepareRequest) (ports.PrepareResult, error) {
			return ports.PrepareResult{EntrypointPath: filepath.Join(outputDir, "index.js"), OutputDir: outputDir}, nil
		},
	}

	req := postBuildScanTestRequest(t, projectDir)
	if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
		t.Fatalf("expected a clean build to publish successfully, got: %v", err)
	}
}

// TestBuild_PostBuildSecretScan_StaticIgnoresSecretOutsideShippedTrees
// proves postBuildScanDirs' static-strategy precision end-to-end with the
// real adapter: a secret sitting in a part of the static output that is
// NEVER packaged (there is no server component for --strategy=static) must
// not fail the build, while the same secret inside client/ (which IS
// shipped) must.
func TestBuild_PostBuildSecretScan_StaticIgnoresSecretOutsideShippedTrees(t *testing.T) {
	const bakedSecret = `export const cfg={apiKey:"AIzaSyD-9tSrke72PouQMnMX-a7eZSW0jkFMBWY"};`

	buildStatic := func(t *testing.T, secretRelPath string) error {
		t.Helper()
		projectDir := t.TempDir()
		outputDir := t.TempDir()
		for _, d := range []string{"client", "prerendered", "not-shipped"} {
			if err := os.MkdirAll(filepath.Join(outputDir, d), 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
		}
		full := filepath.Join(outputDir, secretRelPath)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(bakedSecret), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		deps := newFullDeps(io.Discard)
		deps.SecretGuard = secretguard.NewAdapter()
		deps.StaticServer = &mockStaticServerProvider{}
		deps.Compiler = &mockCompiler{
			prepareFn: func(context.Context, ports.PrepareRequest) (ports.PrepareResult, error) {
				return ports.PrepareResult{EntrypointPath: filepath.Join(outputDir, "index.js"), OutputDir: outputDir}, nil
			},
		}
		req := postBuildScanTestRequest(t, projectDir)
		req.Compile.Strategy = core.StrategyStatic
		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
		return err
	}

	t.Run("secret outside client/prerendered does not fail the build", func(t *testing.T) {
		if err := buildStatic(t, "not-shipped/leftover.js"); err != nil {
			t.Errorf("expected static build to ignore a secret outside client/prerendered, got: %v", err)
		}
	})

	t.Run("secret inside client fails the build", func(t *testing.T) {
		err := buildStatic(t, "client/_app/immutable/chunks/abc.js")
		if err == nil || !errors.Is(err, core.ErrSecretInlined) {
			t.Errorf("expected a secret inside client/ to fail with ErrSecretInlined, got: %v", err)
		}
	})

	// postBuildScanDirs scans [client, prerendered] in that order — this
	// case exercises the SECOND directory specifically (self_review_checklist
	// row 4: a failure on a non-first item in a loop can pass a test that
	// only ever injects the failure on the first one).
	t.Run("secret inside prerendered (second scanned dir) fails the build", func(t *testing.T) {
		err := buildStatic(t, "prerendered/about.html.js")
		if err == nil || !errors.Is(err, core.ErrSecretInlined) {
			t.Errorf("expected a secret inside prerendered/ to fail with ErrSecretInlined, got: %v", err)
		}
	})
}

// TestBuild_PostBuildSecretScan_SkippedFileFailsClosed proves the pipeline
// turns an unscannable file (ports.SecretSkip — e.g. one that exceeded
// secretguard's size ceiling) into a build failure wrapping
// core.ErrSecretScanIncomplete, distinct from core.ErrSecretInlined: "we
// don't know" must never be silently treated the same as "scanned clean".
func TestBuild_PostBuildSecretScan_SkippedFileFailsClosed(t *testing.T) {
	projectDir := t.TempDir()
	outputDir := t.TempDir()
	guard := &fakeSecretGuard{
		resultFor: map[string]ports.SecretScanResult{
			outputDir: {
				Skipped: []ports.SecretSkip{{FilePath: "huge-bundle.js", Reason: "too large"}},
				Passed:  false,
			},
		},
	}
	deps := newFullDeps(io.Discard)
	deps.SecretGuard = guard
	deps.Compiler = &mockCompiler{
		prepareFn: func(context.Context, ports.PrepareRequest) (ports.PrepareResult, error) {
			return ports.PrepareResult{EntrypointPath: filepath.Join(outputDir, "index.js"), OutputDir: outputDir}, nil
		},
	}

	req := postBuildScanTestRequest(t, projectDir)
	_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err == nil {
		t.Fatalf("expected a skipped/unscannable file to fail the build")
	}
	if !errors.Is(err, core.ErrSecretScanIncomplete) {
		t.Errorf("err = %v, want it to wrap core.ErrSecretScanIncomplete (not ErrSecretInlined — nothing was actually found, coverage was just incomplete)", err)
	}
	if errors.Is(err, core.ErrSecretInlined) {
		t.Errorf("err = %v must NOT also wrap ErrSecretInlined — an unscanned file is not the same claim as a found secret", err)
	}
}

// mockAssetOverlayForSecretScan is a minimal ports.AssetOverlayResolver test
// double: only BuildOverlayDir's return value matters here (it is what
// hands the pipeline a directory to scan); the other two methods just need
// to satisfy the interface for the --asset-overlay-from code path, which
// works regardless of output mode.
type mockAssetOverlayForSecretScan struct {
	overlayDir string
}

func (m *mockAssetOverlayForSecretScan) ResolvePredecessorChain(context.Context, string, string, string, bool, int) ([]string, error) {
	return nil, nil
}

func (m *mockAssetOverlayForSecretScan) BuildOverlayDir(context.Context, string, []string, string, bool) (string, error) {
	return m.overlayDir, nil
}

func (m *mockAssetOverlayForSecretScan) ResolveDigest(_ context.Context, ref, _ string, _ bool) (string, error) {
	return ref, nil
}

// TestBuild_PostBuildSecretScan_CoversAssetOverlayContent proves the
// pipeline also scans --asset-overlay's merged prior-generation content
// before packaging it into this build's own image: that content is pulled
// from a registry (this build's own predecessor chain, or, via
// --asset-overlay-from, an arbitrary caller-named image) and re-shipped —
// skipping it would defeat the point of this gate exactly as much as
// skipping the local build output would.
func TestBuild_PostBuildSecretScan_CoversAssetOverlayContent(t *testing.T) {
	const bakedSecret = `export const cfg={apiKey:"AIzaSyD-9tSrke72PouQMnMX-a7eZSW0jkFMBWY"};`

	projectDir := t.TempDir()
	outputDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outputDir, "index.js"), []byte(`console.log("clean");`), 0o644); err != nil {
		t.Fatalf("write output file: %v", err)
	}

	overlayDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(overlayDir, "chunk-old-gen.js"), []byte(bakedSecret), 0o644); err != nil {
		t.Fatalf("write overlay file: %v", err)
	}

	deps := newFullDeps(io.Discard)
	deps.SecretGuard = secretguard.NewAdapter()
	deps.AssetOverlay = &mockAssetOverlayForSecretScan{overlayDir: overlayDir}
	deps.Compiler = &mockCompiler{
		prepareFn: func(context.Context, ports.PrepareRequest) (ports.PrepareResult, error) {
			return ports.PrepareResult{EntrypointPath: filepath.Join(outputDir, "index.js"), OutputDir: outputDir}, nil
		},
	}

	req := postBuildScanTestRequest(t, projectDir)
	// --asset-overlay-from's explicit-ref path works regardless of output
	// mode (see the Stage 4.4 comment in pipeline.go), which keeps this
	// test from needing to also stand up a real push+registry round trip
	// just to reach BuildOverlayDir.
	req.Compile.AssetOverlayGenerations = 1
	req.Compile.AssetOverlayFrom = []string{"ghcr.io/example/other@sha256:1111222233334444555566667777888811112222333344445555666677778888"}

	_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err == nil {
		t.Fatalf("expected a secret in the resolved asset-overlay content to fail the build")
	}
	if !errors.Is(err, core.ErrSecretInlined) {
		t.Errorf("err = %v, want it to wrap core.ErrSecretInlined", err)
	}
	if !strings.Contains(err.Error(), "asset-overlay") {
		t.Errorf("err = %v, want it to identify the asset-overlay stage", err)
	}
}
