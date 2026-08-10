package core_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

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
	resolveFn func(ctx context.Context, req ports.BaseImageRequest) (*ports.BaseImage, error)
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

func newFullDeps(stdout io.Writer) core.Deps {
	return core.Deps{
		Compiler:   &mockCompiler{},
		BaseImages: &mockBaseImageResolver{},
		Supervisor: &mockSupervisorProvider{},
		Packager:   &mockPackager{},
		Registry:   &mockRegistry{},
		Daemon:     &mockLocalLoader{},
		Tarballs:   &mockTarballWriter{},
		SBOM:       &mockSBOMGenerator{},
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Stdout:     stdout,
		Version:    "v0.1.0-test",
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
		_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
		if !errors.Is(err, core.ErrCompileFailed) {
			t.Errorf("expected ErrCompileFailed, got %v", err)
		}
	})
}
