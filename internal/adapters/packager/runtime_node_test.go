package packager

// Tests for the --runtime=node packaging half: a layered image whose runtime
// is the base image's own Node.js must get the node entrypoint, no Bun
// runtime layer, and must not require a resolved Bun binary — while the
// zero-value and explicit-bun paths keep their pre-existing contract intact.

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// newLayeredNodeRequest is newRequest reshaped for a --runtime=node layered
// build: no BunRuntime (nothing is embedded) and a real server directory.
func newLayeredNodeRequest(t *testing.T) ports.PackageRequest {
	t.Helper()
	req := newRequest(t, ports.LinuxAMD64)
	req.Strategy = ports.StrategyLayered
	req.AppRuntime = ports.RuntimeNode
	req.App = ports.Artifact{} // layered has no compiled exe artifact
	req.AppServerDir = writeStrategyDir(t, map[string]string{"index.js": "server entry"})
	return req
}

func TestBuild_LayeredNode_EntrypointAndNoBunLayer(t *testing.T) {
	req := newLayeredNodeRequest(t)

	img, err := NewPackager(testLogger()).Build(context.Background(), req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	cfg := configOf(t, img)
	want := ports.DefaultLayeredNodeEntrypoint()
	if !slices.Equal(cfg.Config.Entrypoint, want) {
		t.Errorf("entrypoint = %q, want %q", cfg.Config.Entrypoint, want)
	}

	// No Bun runtime layer: neither a history entry naming BunBinaryPath nor
	// an extra layer may exist. Expected layers: the synthetic base's own
	// single layer, the supervisor, and /app/server.
	created := historyCreatedBy(t, img)
	for _, cb := range created {
		if cb == "pokkum: add "+ports.BunBinaryPath {
			t.Errorf("node image carries a Bun runtime layer history entry: %v", created)
		}
	}
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("layers: %v", err)
	}
	if len(layers) != 3 {
		t.Errorf("got %d layers, want 3 (base + supervisor + server; NO bun layer), history: %v", len(layers), created)
	}

	// Same request, second build: the node path must stay bit-for-bit
	// deterministic like every other strategy/runtime combination.
	img2, err := NewPackager(testLogger()).Build(context.Background(), req)
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	d1, err := img.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	d2, err := img2.Digest()
	if err != nil {
		t.Fatalf("digest 2: %v", err)
	}
	if d1 != d2 {
		t.Errorf("node image digest not deterministic: %s vs %s", d1, d2)
	}
}

// TestBuild_LayeredNode_DoesNotRequireBunBinary pins the validation split:
// the layered strategy's "bun runtime binary path is required" rule applies
// to the bun runtime only. The inverse — bun (explicit AND zero-value
// AppRuntime) still requiring it — is the pre-existing contract every old
// caller relies on, asserted alongside so the split can't silently widen.
func TestBuild_LayeredNode_DoesNotRequireBunBinary(t *testing.T) {
	for _, runtime := range []ports.AppRuntime{"", ports.RuntimeBun} {
		req := newLayeredNodeRequest(t)
		req.AppRuntime = runtime // BunRuntime.BinaryPath is empty
		if _, err := NewPackager(testLogger()).Build(context.Background(), req); !errors.Is(err, core.ErrPackageFailed) {
			t.Errorf("AppRuntime %q with no BunRuntime.BinaryPath: err = %v, want core.ErrPackageFailed (bun runtime binary required)", runtime, err)
		}
	}
	// The node arm of the same switch must accept the identical request.
	req := newLayeredNodeRequest(t)
	if _, err := NewPackager(testLogger()).Build(context.Background(), req); err != nil {
		t.Errorf("AppRuntime node with no BunRuntime.BinaryPath: err = %v, want nil (base image provides the runtime)", err)
	}
}

// TestBuild_LayeredNode_RejectsTelemetryPreload is the packager-side
// belt-and-suspenders check behind core's validation: the telemetry preload
// mechanism is `bun --preload` of a TypeScript file, executable by neither
// half under Node — packaging it would produce an image that crashes at
// startup, so the packager refuses outright.
func TestBuild_LayeredNode_RejectsTelemetryPreload(t *testing.T) {
	req := newLayeredNodeRequest(t)
	req.TelemetryPreloadRelPath = "otel-bootstrap.ts"

	if _, err := NewPackager(testLogger()).Build(context.Background(), req); !errors.Is(err, core.ErrPackageFailed) {
		t.Fatalf("err = %v, want core.ErrPackageFailed (telemetry preload is Bun-specific)", err)
	}
}

// TestBuild_LayeredNode_UnknownRuntimeRejected pins the positive-switch
// discipline (mem:self_review_checklist row 11): an unrecognized AppRuntime
// value must fail the build, never silently fall through to the Bun shape —
// an entrypoint naming a runtime binary the image doesn't contain is the
// worst place to discover an unhandled enum value.
func TestBuild_LayeredNode_UnknownRuntimeRejected(t *testing.T) {
	req := newLayeredNodeRequest(t)
	req.AppRuntime = ports.AppRuntime("deno")

	if _, err := NewPackager(testLogger()).Build(context.Background(), req); !errors.Is(err, core.ErrPackageFailed) {
		t.Fatalf("err = %v, want core.ErrPackageFailed for unknown runtime", err)
	}
}
