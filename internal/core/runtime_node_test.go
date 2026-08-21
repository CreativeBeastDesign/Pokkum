package core_test

// Tests for the --runtime dimension (ports.AppRuntime): the (runtime ×
// strategy) validation matrix, the node base-preset defaulting, and the
// pipeline wiring that must treat the runtime exactly like a second
// dimension on everything --bun-version already touches — the remote-cache
// input hash, the scan request, the package request, and SLSA provenance.

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestParseAppRuntime(t *testing.T) {
	cases := []struct {
		in      string
		want    core.AppRuntime
		wantErr bool
	}{
		{"bun", core.RuntimeBun, false},
		{"node", core.RuntimeNode, false},
		{" NODE ", core.RuntimeNode, false},
		{"", "", false}, // zero value: Normalize's job, not an error
		{"deno", "", true},
		{"nodejs", "", true},
	}
	for _, c := range cases {
		got, err := core.ParseAppRuntime(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseAppRuntime(%q): expected error, got %q", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseAppRuntime(%q): unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseAppRuntime(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalize_AppRuntimeDefaultsToBun(t *testing.T) {
	req := core.BuildRequest{ProjectDir: "/abs/project", Repo: "ghcr.io/example/app"}
	req.Normalize()
	if req.AppRuntime != core.RuntimeBun {
		t.Errorf("AppRuntime = %q, want %q", req.AppRuntime, core.RuntimeBun)
	}
	if req.BaseImage.Preset != core.BaseImageDistroless {
		t.Errorf("BaseImage.Preset = %q, want %q", req.BaseImage.Preset, core.BaseImageDistroless)
	}
}

func TestNormalize_NodeRuntimeDefaultsBasePresetToDistrolessNode(t *testing.T) {
	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		AppRuntime: core.RuntimeNode,
	}
	req.Normalize()
	if req.BaseImage.Preset != core.BaseImageDistrolessNode {
		t.Errorf("BaseImage.Preset = %q, want %q", req.BaseImage.Preset, core.BaseImageDistrolessNode)
	}
	if req.BaseImage.Ref != ports.DistrolessNodeBaseRef {
		t.Errorf("BaseImage.Ref = %q, want %q", req.BaseImage.Ref, ports.DistrolessNodeBaseRef)
	}
	if err := req.Validate(); err != nil {
		t.Errorf("normalized node request should validate, got %v", err)
	}
}

func TestNormalize_NodeRuntimeKeepsExplicitCustomRef(t *testing.T) {
	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		AppRuntime: core.RuntimeNode,
		BaseImage:  core.BaseImageOptions{Ref: "registry.example.com/mirror/nodejs:pinned"},
	}
	req.Normalize()
	if req.BaseImage.Preset != core.BaseImageCustom {
		t.Errorf("BaseImage.Preset = %q, want %q (explicit ref wins over node default)", req.BaseImage.Preset, core.BaseImageCustom)
	}
	if err := req.Validate(); err != nil {
		t.Errorf("node + custom base ref should validate (operator asserts it provides %s), got %v", ports.NodeBinaryPath, err)
	}
}

// TestValidate_RuntimeStrategyMatrix pins the full documented support matrix:
// bun × {layered, exe, static} supported; node × layered supported; node ×
// {exe, static} rejected; and node's option constraints (stub launcher,
// custom bun binary, telemetry, no-Node base presets) each rejected with a
// wrapped sentinel.
func TestValidate_RuntimeStrategyMatrix(t *testing.T) {
	base := func() core.BuildRequest {
		return core.BuildRequest{ProjectDir: "/abs/project", Repo: "ghcr.io/example/app"}
	}

	cases := []struct {
		name        string
		mutate      func(*core.BuildRequest)
		wantErr     error  // nil means the request must validate
		wantErrText string // substring the error message must carry, "" to skip
	}{
		{
			name:   "bun layered ok",
			mutate: func(r *core.BuildRequest) { r.Compile.Strategy = core.StrategyLayered },
		},
		{
			name:   "bun exe ok",
			mutate: func(r *core.BuildRequest) { r.Compile.Strategy = core.StrategyExe },
		},
		{
			name:   "bun static ok",
			mutate: func(r *core.BuildRequest) { r.Compile.Strategy = core.StrategyStatic },
		},
		{
			name: "node layered ok",
			mutate: func(r *core.BuildRequest) {
				r.AppRuntime = core.RuntimeNode
				r.Compile.Strategy = core.StrategyLayered
			},
		},
		{
			name: "node exe rejected",
			mutate: func(r *core.BuildRequest) {
				r.AppRuntime = core.RuntimeNode
				r.Compile.Strategy = core.StrategyExe
			},
			wantErr:     core.ErrInvalidRequest,
			wantErrText: "no Node equivalent",
		},
		{
			name: "node static rejected",
			mutate: func(r *core.BuildRequest) {
				r.AppRuntime = core.RuntimeNode
				r.Compile.Strategy = core.StrategyStatic
			},
			wantErr: core.ErrInvalidRequest,
		},
		{
			name: "node stub launcher rejected",
			mutate: func(r *core.BuildRequest) {
				r.AppRuntime = core.RuntimeNode
				r.BunRuntime.StubLauncher = true
			},
			wantErr:     core.ErrInvalidRequest,
			wantErrText: "stub-launcher",
		},
		{
			name: "node custom bun binary rejected",
			mutate: func(r *core.BuildRequest) {
				r.AppRuntime = core.RuntimeNode
				r.BunRuntime.CustomBinaryPath = "/usr/local/bin/bun"
			},
			wantErr:     core.ErrInvalidRequest,
			wantErrText: "bun-binary",
		},
		{
			name: "node telemetry rejected",
			mutate: func(r *core.BuildRequest) {
				r.AppRuntime = core.RuntimeNode
				r.Telemetry.Enabled = true
			},
			wantErr:     core.ErrInvalidRequest,
			wantErrText: "telemetry",
		},
		{
			name: "node with distroless cc preset rejected",
			mutate: func(r *core.BuildRequest) {
				r.AppRuntime = core.RuntimeNode
				r.BaseImage.Preset = core.BaseImageDistroless
			},
			wantErr:     core.ErrInvalidBaseImage,
			wantErrText: "ships no Node.js runtime",
		},
		{
			name: "node with chainguard preset rejected",
			mutate: func(r *core.BuildRequest) {
				r.AppRuntime = core.RuntimeNode
				r.BaseImage.Preset = core.BaseImageChainguard
			},
			wantErr: core.ErrInvalidBaseImage,
		},
		{
			name: "node with distroless-node preset ok",
			mutate: func(r *core.BuildRequest) {
				r.AppRuntime = core.RuntimeNode
				r.BaseImage.Preset = core.BaseImageDistrolessNode
			},
		},
		{
			name:    "unknown runtime rejected",
			mutate:  func(r *core.BuildRequest) { r.AppRuntime = core.AppRuntime("deno") },
			wantErr: core.ErrInvalidRequest,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := base()
			c.mutate(&req)
			req.Normalize()
			err := req.Validate()
			if c.wantErr == nil {
				if err != nil {
					t.Fatalf("expected valid request, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error wrapping %v, got nil", c.wantErr)
			}
			if !errors.Is(err, c.wantErr) {
				t.Errorf("error %v does not wrap %v", err, c.wantErr)
			}
			if c.wantErrText != "" && !strings.Contains(err.Error(), c.wantErrText) {
				t.Errorf("error %q does not contain %q", err.Error(), c.wantErrText)
			}
		})
	}
}

// capturingRemoteCacher records the exact RemoteCacheInputRequest the
// pipeline computed the composite input hash from, and always misses.
type capturingRemoteCacher struct {
	inputReq ports.RemoteCacheInputRequest
}

func (c *capturingRemoteCacher) ComputeInputHash(_ context.Context, req ports.RemoteCacheInputRequest) (string, error) {
	c.inputReq = req
	return "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef", nil
}

func (c *capturingRemoteCacher) Check(context.Context, ports.RemoteCacheRequest) (ports.RemoteCacheResult, error) {
	return ports.RemoteCacheResult{Hit: false}, nil
}

// TestBuildPush_NodeRuntime_WiresRuntimeDimensionEverywhere runs one real
// core.Build in --runtime=node mode and asserts every place the runtime
// identity must reach, from the same build:
//
//   - the Bun runtime resolver is NEVER called (nothing to embed; a node
//     build must not be gated on Bun release infrastructure),
//   - the packager receives AppRuntime == node,
//   - the composite remote-cache input hash request carries the runtime
//     (the correctness-critical dimension — a hash that ignored it would
//     let a bun-built image satisfy a node-requested build),
//   - the scanner request carries the runtime (keys which embedded
//     toolchain advisories can apply),
//   - SLSA provenance records the runtime as a replayable parameter, with
//     BunVersion falling back to the HOST bun (the build tool) rather than
//     claiming an embedded runtime that isn't there.
func TestBuildPush_NodeRuntime_WiresRuntimeDimensionEverywhere(t *testing.T) {
	deps := newFullDeps(io.Discard)

	bunResolveCalled := false
	deps.BunRuntime = &mockBunRuntimeResolver{
		resolveFn: func(_ context.Context, req ports.BunResolverRequest) (ports.BunResolverResult, error) {
			bunResolveCalled = true
			return ports.BunResolverResult{BinaryPath: "/mock/bun", Version: "1.2.2", Platform: req.Platform}, nil
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

	cacher := &capturingRemoteCacher{}
	deps.RemoteCache = cacher

	var scanReq ports.ScanRequest
	deps.Scanner = &mockScanner{
		scanFn: func(_ context.Context, req ports.ScanRequest) (ports.ScanResult, error) {
			scanReq = req
			return ports.ScanResult{Target: req.Target, Passed: true}, nil
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
		AppRuntime: core.RuntimeNode,
		Sign:       true,
		Signing:    core.SigningOptions{KeyPEM: []byte("test-key"), PublicKeyPEM: []byte("test-pub")},
	}
	// Signed builds never consult the remote cache; run the cache assertion
	// with signing off in a second build below. First: the signed build,
	// which exercises SLSA.
	if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
		t.Fatalf("node build failed: %v", err)
	}

	if bunResolveCalled {
		t.Errorf("BunRuntimeResolver.Resolve was called for a --runtime=node build; a node image embeds no Bun and must not depend on Bun release infrastructure")
	}
	if len(pkgReqs) == 0 {
		t.Fatalf("packager was never invoked")
	}
	for _, pr := range pkgReqs {
		if pr.AppRuntime != ports.RuntimeNode {
			t.Errorf("PackageRequest.AppRuntime = %q, want %q", pr.AppRuntime, ports.RuntimeNode)
		}
		if pr.BunRuntime.BinaryPath != "" {
			t.Errorf("PackageRequest.BunRuntime.BinaryPath = %q, want empty for node", pr.BunRuntime.BinaryPath)
		}
	}
	if scanReq.AppRuntime != string(ports.RuntimeNode) {
		t.Errorf("ScanRequest.AppRuntime = %q, want %q", scanReq.AppRuntime, ports.RuntimeNode)
	}
	if slsaReq.Toolchain.AppRuntime != string(ports.RuntimeNode) {
		t.Errorf("SLSAToolchain.AppRuntime = %q, want %q", slsaReq.Toolchain.AppRuntime, ports.RuntimeNode)
	}
	// mockCompiler's Preflight reports the HOST bun ("1.2.18"); with no
	// embedded runtime resolved, the SLSA bun dependency must fall back to
	// exactly that — the build tool — not an embedded version that was
	// never resolved.
	if slsaReq.Toolchain.BunVersion != "1.2.18" {
		t.Errorf("SLSAToolchain.BunVersion = %q, want host toolchain %q for a node build", slsaReq.Toolchain.BunVersion, "1.2.18")
	}
	if slsaReq.Toolchain.BunBinaryHash != "" {
		t.Errorf("SLSAToolchain.BunBinaryHash = %q, want empty for a node build (no embedded bun artifact)", slsaReq.Toolchain.BunBinaryHash)
	}

	// Second build, unsigned, so the remote cache is actually consulted:
	// the input-hash request must carry the runtime dimension.
	req2 := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
		AppRuntime: core.RuntimeNode,
	}
	if _, err := core.Build(context.Background(), deps, req2, core.BuildOptions{}); err != nil {
		t.Fatalf("unsigned node build failed: %v", err)
	}
	if cacher.inputReq.AppRuntime != string(ports.RuntimeNode) {
		t.Errorf("RemoteCacheInputRequest.AppRuntime = %q, want %q — the composite input hash MUST key the runtime dimension", cacher.inputReq.AppRuntime, ports.RuntimeNode)
	}
}

// TestBuildPush_NodeRuntime_OmitsBunVersionLabel is F5's node-image half: a
// --runtime=node image embeds no Bun runtime at all (Node comes from the
// base image itself, see ports.RuntimeNode's doc comment), so
// dev.pokkum.bun.version must be ABSENT from the image labels entirely —
// exactly like dev.pokkum.runtime is absent for the default (bun) runtime,
// per imageLabels' "stamped only for the non-default runtime" comment.
// Before the fix, imageLabels stamped this label from the HOST's compiler
// bun (mockCompiler's Preflight, "1.2.18") unconditionally, so a node image
// — with no Bun anywhere in it — still carried a Bun version label.
func TestBuildPush_NodeRuntime_OmitsBunVersionLabel(t *testing.T) {
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
		AppRuntime: core.RuntimeNode,
	}

	if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
		t.Fatalf("node build failed: %v", err)
	}
	if len(pkgReqs) == 0 {
		t.Fatalf("packager was never invoked")
	}

	if v, ok := pkgReqs[0].Labels[ports.LabelBunVersion]; ok {
		t.Errorf("image label %s = %q, want ABSENT for a --runtime=node image (no Bun is embedded in it)", ports.LabelBunVersion, v)
	}
}
