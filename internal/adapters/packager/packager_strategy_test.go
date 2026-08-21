package packager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// This file pins the current strategy-dispatch behaviour of Packager.Build
// (see the switch on req.Strategy / req.Strategy.ApplyStatic() in
// packager.go's Build). It is a behaviour-preservation / golden-master test
// for already-shipped code: it exists so that a future strategy silently
// missing a branch, or an accidental change to which layers get built for an
// existing strategy, fails a test instead of shipping unnoticed. It is not
// exercising any refactor — none exists yet, and the consolidation sketched
// in concepts/strategy-dispatch-refactor-concept.md is explicitly deferred.

// writeStrategyDir creates a small real directory fixture — enough for
// BuildDirectoryTreeLayer/striputils/precompressutils to walk — following the
// same t.TempDir()-per-call pattern as writeBinary in helper_test.go, just
// generalized to more than one file.
func writeStrategyDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// historyCreatedBy returns the CreatedBy field of every history entry the
// packager itself appended to img's config, in append order, skipping index 0
// (the synthetic base's own "synthetic base" entry — see syntheticBase in
// helper_test.go). This is what TestImageConfigHistory and
// TestPokkumLayersOrderAndContents already assert on for the exe strategy;
// here it is generalized across all three strategies to pin exactly which
// layers each one builds and in what order.
func historyCreatedBy(t *testing.T, img v1.Image) []string {
	t.Helper()
	cfg := configOf(t, img)
	if len(cfg.History) == 0 {
		t.Fatalf("history is empty, want at least the synthetic base entry")
	}
	out := make([]string, 0, len(cfg.History)-1)
	for _, h := range cfg.History[1:] {
		out = append(out, h.CreatedBy)
	}
	return out
}

// envValue looks up KEY=VALUE in an image config's Env slice.
func envValue(env []string, key string) (string, bool) {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix), true
		}
	}
	return "", false
}

// TestBuild_StrategyDispatch pins, for each of the three ports.BuildStrategy
// values, exactly which layers Build assembles (by their history CreatedBy
// string, which also fixes their order and count), and which runtime env var
// Build wires in (ports.EnvStaticRoots for static, ports.EnvPrerenderedDir for
// layered-with-prerendered, neither for exe).
func TestBuild_StrategyDispatch(t *testing.T) {
	tests := []struct {
		name          string
		mutate        func(t *testing.T, r *ports.PackageRequest)
		wantCreatedBy []string
		wantEnv       map[string]string
		wantEnvAbsent []string
	}{
		{
			// Layered with every optional directory present: bun + supervisor +
			// server (unconditional) plus client/vendor/native/prerendered
			// (each guarded by its own os.Stat check in Build).
			name: "layered",
			mutate: func(t *testing.T, r *ports.PackageRequest) {
				r.Strategy = ports.StrategyLayered
				r.BunRuntime = ports.BunResolverResult{
					BinaryPath: writeBinary(t, "bun", []byte("#!/bin/sh\necho bun")),
				}
				r.AppServerDir = writeStrategyDir(t, map[string]string{"index.js": "server entry"})
				r.AppClientDir = writeStrategyDir(t, map[string]string{"app.js": "client asset"})
				r.AppVendorDir = writeStrategyDir(t, map[string]string{"lib.js": "vendor module"})
				r.AppNativeDir = writeStrategyDir(t, map[string]string{"addon.node": "native addon"})
				r.AppPrerenderedDir = writeStrategyDir(t, map[string]string{"index.html": "prerendered page"})
			},
			wantCreatedBy: []string{
				"pokkum: add " + ports.BunBinaryPath,
				historySupervisorCreatedBy,
				"pokkum: add /app/server",
				"pokkum: add " + ports.AppClientDirPrefix,
				"pokkum: add " + ports.AppVendorDirPrefix,
				"pokkum: add " + ports.AppNativeDirPrefix,
				"pokkum: add " + ports.AppPrerenderedDirPrefix,
			},
			wantEnv: map[string]string{
				ports.EnvPrerenderedDir: ports.AppPrerenderedDirPrefix,
				ports.EnvClientDir:      ports.AppClientDirPrefix,
			},
			wantEnvAbsent: []string{ports.EnvStaticRoots},
		},
		{
			// Static: no Bun runtime, no server JS, no vendor/native, no
			// supervisor — pokkum-static is PID 1 — plus optional client and
			// required prerendered.
			name: "static",
			mutate: func(t *testing.T, r *ports.PackageRequest) {
				r.Strategy = ports.StrategyStatic
				r.StaticServer = []byte("fake-pokkum-static-binary")
				r.AppClientDir = writeStrategyDir(t, map[string]string{"app.js": "client asset"})
				r.AppPrerenderedDir = writeStrategyDir(t, map[string]string{"index.html": "prerendered page"})
			},
			wantCreatedBy: []string{
				"pokkum: add " + ports.StaticServerPath,
				"pokkum: add " + ports.AppClientDirPrefix,
				"pokkum: add " + ports.AppPrerenderedDirPrefix,
			},
			wantEnv: map[string]string{
				ports.EnvStaticRoots: ports.AppClientDirPrefix + ":" + ports.AppPrerenderedDirPrefix,
			},
			wantEnvAbsent: []string{ports.EnvPrerenderedDir},
		},
		{
			// Exe: single compiled App binary artifact plus supervisor, no
			// directory-tree layers at all. newRequest already wires App.Path
			// and Supervisor for this strategy.
			name: "exe",
			mutate: func(t *testing.T, r *ports.PackageRequest) {
				r.Strategy = ports.StrategyExe
			},
			wantCreatedBy: []string{historySupervisorCreatedBy, historyAppCreatedBy},
			wantEnvAbsent: []string{ports.EnvStaticRoots, ports.EnvPrerenderedDir},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := newRequest(t, ports.LinuxAMD64)
			tc.mutate(t, &req)

			img, err := NewPackager(testLogger()).Build(context.Background(), req)
			if err != nil {
				t.Fatalf("build: %v", err)
			}

			gotCreatedBy := historyCreatedBy(t, img)
			if !slices.Equal(gotCreatedBy, tc.wantCreatedBy) {
				t.Errorf("history CreatedBy = %v, want %v", gotCreatedBy, tc.wantCreatedBy)
			}

			layers, err := img.Layers()
			if err != nil {
				t.Fatalf("layers: %v", err)
			}
			// +1 for the synthetic base's own single layer (see syntheticBase).
			if wantLayers := len(tc.wantCreatedBy) + 1; len(layers) != wantLayers {
				t.Errorf("got %d layers, want %d (base layer plus %v)", len(layers), wantLayers, tc.wantCreatedBy)
			}

			cfg := configOf(t, img)
			for k, want := range tc.wantEnv {
				got, ok := envValue(cfg.Config.Env, k)
				if !ok {
					t.Errorf("env %s missing, want %q", k, want)
				} else if got != want {
					t.Errorf("env %s = %q, want %q", k, got, want)
				}
			}
			for _, k := range tc.wantEnvAbsent {
				if got, ok := envValue(cfg.Config.Env, k); ok {
					t.Errorf("env %s = %q, want absent for strategy %s", k, got, req.Strategy)
				}
			}
		})
	}
}

// TestBuild_LayeredServerDirIsUnconditional pins the asymmetry called out in
// packager.go's layered branch: AppServerDir is packaged via
// BuildDirectoryTreeLayer with no os.Stat guard beforehand, unlike
// AppClientDir/AppVendorDir/AppNativeDir which are each wrapped in
// `if info, err := os.Stat(...); err == nil && info.IsDir()`. A server
// directory that does not exist on disk must therefore fail the build
// (BuildDirectoryTreeLayer's own os.Stat call inside layer.go errors out),
// rather than being silently skipped the way the optional directories are.
func TestBuild_LayeredServerDirIsUnconditional(t *testing.T) {
	req := newRequest(t, ports.LinuxAMD64)
	req.Strategy = ports.StrategyLayered
	req.BunRuntime = ports.BunResolverResult{BinaryPath: writeBinary(t, "bun", []byte("bun"))}
	req.AppServerDir = filepath.Join(t.TempDir(), "does-not-exist")

	_, err := NewPackager(testLogger()).Build(context.Background(), req)
	if !errors.Is(err, core.ErrPackageFailed) {
		t.Fatalf("err = %v, want core.ErrPackageFailed: AppServerDir has no existence guard in Build, unlike the optional directories", err)
	}
}

// TestBuild_LayeredEntrypoint_DefaultWhenNoTelemetry pins that a layered
// build with no telemetry bootstrap uses ports.DefaultLayeredEntrypoint()
// unchanged — the pre-2026-08-18 behavior, which must not regress now that
// the same branch has a second, telemetry-aware path.
func TestBuild_LayeredEntrypoint_DefaultWhenNoTelemetry(t *testing.T) {
	req := newRequest(t, ports.LinuxAMD64)
	req.Strategy = ports.StrategyLayered
	req.BunRuntime = ports.BunResolverResult{BinaryPath: writeBinary(t, "bun", []byte("bun"))}
	req.AppServerDir = writeStrategyDir(t, map[string]string{"index.js": "server entry"})

	img, err := NewPackager(testLogger()).Build(context.Background(), req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	cfg := configOf(t, img)
	want := ports.DefaultLayeredEntrypoint()
	if !slices.Equal(cfg.Config.Entrypoint, want) {
		t.Errorf("entrypoint = %q, want %q (DefaultLayeredEntrypoint unchanged)", cfg.Config.Entrypoint, want)
	}
}

// TestBuild_LayeredEntrypoint_TelemetryInsertsPreload proves the
// TelemetryPreloadRelPath -> Entrypoint wiring PR-5's layered-strategy
// extension added: a non-empty path must produce
// [supervisor, --, bun, --preload, <AppServerDirPrefix>/<rel>, index] rather
// than the unconditional DefaultLayeredEntrypoint().
func TestBuild_LayeredEntrypoint_TelemetryInsertsPreload(t *testing.T) {
	req := newRequest(t, ports.LinuxAMD64)
	req.Strategy = ports.StrategyLayered
	req.BunRuntime = ports.BunResolverResult{BinaryPath: writeBinary(t, "bun", []byte("bun"))}
	req.AppServerDir = writeStrategyDir(t, map[string]string{
		"index.js":          "server entry",
		"otel-bootstrap.ts": "otel bootstrap",
	})
	req.TelemetryPreloadRelPath = "otel-bootstrap.ts"

	img, err := NewPackager(testLogger()).Build(context.Background(), req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	cfg := configOf(t, img)
	want := []string{
		ports.SupervisorPath, "--", ports.BunBinaryPath, ports.BunNoInstallFlag,
		"--preload", ports.AppServerDirPrefix + "/otel-bootstrap.ts",
		ports.AppServerIndexPath,
	}
	if !slices.Equal(cfg.Config.Entrypoint, want) {
		t.Errorf("entrypoint = %q, want %q", cfg.Config.Entrypoint, want)
	}
}

// TestBuild_ExeStrategyIgnoresTelemetryPreloadRelPath guards row 11
// (mem:self_review_checklist): the new branch inside packager.go's
// `if req.Strategy == ports.StrategyLayered` block must never leak into
// StrategyExe — setting TelemetryPreloadRelPath on a StrategyExe request
// (which nothing in this codebase actually does; PrepareResult only ever
// sets it for StrategyLayered) must have zero effect on the entrypoint,
// since StrategyExe's own telemetry mechanism (the compile-entrypoint
// wrapper) works entirely through EntrypointPath, not this field.
func TestBuild_ExeStrategyIgnoresTelemetryPreloadRelPath(t *testing.T) {
	req := newRequest(t, ports.LinuxAMD64)
	req.TelemetryPreloadRelPath = "otel-bootstrap.ts"

	img, err := NewPackager(testLogger()).Build(context.Background(), req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	cfg := configOf(t, img)
	want := []string{ports.SupervisorPath, "--", ports.AppBinaryPath}
	if !slices.Equal(cfg.Config.Entrypoint, want) {
		t.Errorf("entrypoint = %q, want %q (StrategyExe must ignore TelemetryPreloadRelPath)", cfg.Config.Entrypoint, want)
	}
}

// TestBuild_LayeredOptionalDirsSkippedWhenAbsent is the mirror of the test
// above: AppClientDir/AppVendorDir/AppNativeDir pointed at paths that do not
// exist must be silently skipped (no layer, no error), because each is
// guarded by its own os.Stat check before Build calls into
// BuildDirectoryTreeLayer.
func TestBuild_LayeredOptionalDirsSkippedWhenAbsent(t *testing.T) {
	req := newRequest(t, ports.LinuxAMD64)
	req.Strategy = ports.StrategyLayered
	req.BunRuntime = ports.BunResolverResult{BinaryPath: writeBinary(t, "bun", []byte("bun"))}
	req.AppServerDir = writeStrategyDir(t, map[string]string{"index.js": "server entry"})
	req.AppClientDir = filepath.Join(t.TempDir(), "missing-client")
	req.AppVendorDir = filepath.Join(t.TempDir(), "missing-vendor")
	req.AppNativeDir = filepath.Join(t.TempDir(), "missing-native")

	img, err := NewPackager(testLogger()).Build(context.Background(), req)
	if err != nil {
		t.Fatalf("build: %v (client/vendor/native dirs are os.Stat-guarded and should be silently skipped when absent)", err)
	}

	want := []string{
		"pokkum: add " + ports.BunBinaryPath,
		historySupervisorCreatedBy,
		"pokkum: add /app/server",
	}
	if got := historyCreatedBy(t, img); !slices.Equal(got, want) {
		t.Errorf("history CreatedBy = %v, want %v (absent optional dirs should have been skipped)", got, want)
	}
}

// TestBuild_LayeredPrerenderedEnvSetEvenWhenDirAbsent pins another asymmetry:
// Build decides whether to set ports.EnvPrerenderedDir purely from
// req.AppPrerenderedDir != "" (packager.go, ahead of any layer assembly),
// while the prerendered layer itself is only appended by
// appendPrerenderedLayer if the directory actually exists on disk. A caller
// that sets AppPrerenderedDir to a path that turns out to be missing gets the
// env var wired in with no matching layer — this test exists so that
// tightening the two checks to agree (or not) is a deliberate, visible
// decision rather than an accidental side effect of some other change.
func TestBuild_LayeredPrerenderedEnvSetEvenWhenDirAbsent(t *testing.T) {
	req := newRequest(t, ports.LinuxAMD64)
	req.Strategy = ports.StrategyLayered
	req.BunRuntime = ports.BunResolverResult{BinaryPath: writeBinary(t, "bun", []byte("bun"))}
	req.AppServerDir = writeStrategyDir(t, map[string]string{"index.js": "server entry"})
	req.AppPrerenderedDir = filepath.Join(t.TempDir(), "missing-prerendered")

	img, err := NewPackager(testLogger()).Build(context.Background(), req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	cfg := configOf(t, img)
	if got, ok := envValue(cfg.Config.Env, ports.EnvPrerenderedDir); !ok || got != ports.AppPrerenderedDirPrefix {
		t.Errorf("env %s = %q (ok=%v), want %q", ports.EnvPrerenderedDir, got, ok, ports.AppPrerenderedDirPrefix)
	}

	want := []string{
		"pokkum: add " + ports.BunBinaryPath,
		historySupervisorCreatedBy,
		"pokkum: add /app/server",
	}
	if got := historyCreatedBy(t, img); !slices.Equal(got, want) {
		t.Errorf("history CreatedBy = %v, want %v (no prerendered layer should have been added for a missing directory)", got, want)
	}
}

// TestBuild_StrategyStatic_FallbackEnv covers the opt-in SPA-fallback staging
// contract in the static branch: with req.StaticFallback set to an in-image
// path whose file is staged under the client root, Build stamps
// ports.EnvStaticFallback to that exact path; with the file absent it fails
// (never silently drops the SPA shell); layered/exe never stamp it.
func TestBuild_StrategyStatic_FallbackEnv(t *testing.T) {
	t.Run("stamps env when fallback staged", func(t *testing.T) {
		clientDir := writeStrategyDir(t, map[string]string{
			"app.js":   "client asset",
			"200.html": "<h1>spa shell</h1>",
		})
		req := newRequest(t, ports.LinuxAMD64)
		req.Strategy = ports.StrategyStatic
		req.StaticServer = []byte("fake-pokkum-static-binary")
		req.AppClientDir = clientDir
		req.AppPrerenderedDir = writeStrategyDir(t, map[string]string{"index.html": "prerendered page"})
		req.StaticFallback = ports.AppClientDirPrefix + "/" + "200.html"

		img, err := NewPackager(testLogger()).Build(context.Background(), req)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		cfg := configOf(t, img)
		got, ok := envValue(cfg.Config.Env, ports.EnvStaticFallback)
		if !ok || got != ports.AppClientDirPrefix+"/200.html" {
			t.Errorf("env %s = %q (ok=%v), want %q", ports.EnvStaticFallback, got, ok, ports.AppClientDirPrefix+"/200.html")
		}
	})

	t.Run("fails when fallback configured but not staged", func(t *testing.T) {
		req := newRequest(t, ports.LinuxAMD64)
		req.Strategy = ports.StrategyStatic
		req.StaticServer = []byte("fake-pokkum-static-binary")
		req.AppClientDir = writeStrategyDir(t, map[string]string{"app.js": "client asset"})
		req.AppPrerenderedDir = writeStrategyDir(t, map[string]string{"index.html": "prerendered page"})
		// Configured fallback path whose file was never emitted in the client
		// staging: the packager must fail, not silently drop it.
		req.StaticFallback = ports.AppClientDirPrefix + "/" + "200.html"

		_, err := NewPackager(testLogger()).Build(context.Background(), req)
		if err == nil {
			t.Fatal("build succeeded, want error for a fallback configured but not staged")
		}
		if !strings.Contains(err.Error(), "not staged") {
			t.Errorf("error = %v, want to name the unstaged fallback", err)
		}
	})

	t.Run("rejects fallback outside client root", func(t *testing.T) {
		req := newRequest(t, ports.LinuxAMD64)
		req.Strategy = ports.StrategyStatic
		req.StaticServer = []byte("fake-pokkum-static-binary")
		req.AppClientDir = writeStrategyDir(t, map[string]string{"app.js": "client asset"})
		req.AppPrerenderedDir = writeStrategyDir(t, map[string]string{"index.html": "prerendered page"})
		// An in-image path not under the client root is a config error.
		req.StaticFallback = "/etc/passwd"

		_, err := NewPackager(testLogger()).Build(context.Background(), req)
		if err == nil {
			t.Fatal("build succeeded, want error for a fallback outside the client root")
		}
	})

	t.Run("layered and exe never stamp fallback env", func(t *testing.T) {
		for _, strategy := range []ports.BuildStrategy{ports.StrategyLayered, ports.StrategyExe} {
			req := newRequest(t, ports.LinuxAMD64)
			req.Strategy = strategy
			if strategy == ports.StrategyLayered {
				req.BunRuntime = ports.BunResolverResult{BinaryPath: writeBinary(t, "bun", []byte("bun"))}
				req.AppServerDir = writeStrategyDir(t, map[string]string{"index.js": "server entry"})
			}
			req.StaticFallback = ports.AppClientDirPrefix + "/200.html"
			img, err := NewPackager(testLogger()).Build(context.Background(), req)
			if err != nil {
				t.Fatalf("build %s: %v", strategy, err)
			}
			if _, ok := envValue(configOf(t, img).Config.Env, ports.EnvStaticFallback); ok {
				t.Errorf("%s image must not carry %s env", strategy, ports.EnvStaticFallback)
			}
		}
	})
}

// precompressibleAsset is large and repetitive enough to clear
// precompressutils.PrecompressFile's two skip gates: the 64-byte minimum-size
// floor and the "only keep a sidecar that's actually smaller than the
// source" check. A short fixture like "client asset" (used elsewhere in this
// file for layer-presence tests, where sidecar generation is irrelevant)
// would silently produce zero sidecars of any format, making a `.zst`
// absence assertion trivially true rather than a real regression guard.
var precompressibleAsset = strings.Repeat("console.log('pokkum precompression fixture');\n", 50)

// allTarMemberNames flattens every layer of img into one list of tar entry
// names, so a sidecar-format assertion doesn't need to know which layer
// index the client/prerendered content landed at for a given strategy.
func allTarMemberNames(t *testing.T, img v1.Image) []string {
	t.Helper()
	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("layers: %v", err)
	}
	var names []string
	for _, l := range layers {
		for _, m := range readLayer(t, l) {
			names = append(names, m.Name)
		}
	}
	return names
}

// hasSuffixAmong reports whether any name in names ends with suffix.
func hasSuffixAmong(names []string, suffix string) bool {
	for _, n := range names {
		if strings.HasSuffix(n, suffix) {
			return true
		}
	}
	return false
}

// TestBuild_PrecompressionFormatsPerStrategy is PR-3's regression guard:
// the layered strategy's runtime (adapter-node's bundled sirv server) only
// ever negotiates gzip/brotli, never zstd, so generating `.zst` sidecars for
// it is wasted build time and wasted layer bytes; only pokkum-static
// (--strategy=static) actually serves them. Nothing prior to this test
// asserted either half of that contract.
func TestBuild_PrecompressionFormatsPerStrategy(t *testing.T) {
	t.Run("layered: gzip/brotli present, zstd absent", func(t *testing.T) {
		req := newRequest(t, ports.LinuxAMD64)
		req.Strategy = ports.StrategyLayered
		req.BunRuntime = ports.BunResolverResult{BinaryPath: writeBinary(t, "bun", []byte("bun"))}
		req.AppServerDir = writeStrategyDir(t, map[string]string{"index.js": "server entry"})
		req.AppClientDir = writeStrategyDir(t, map[string]string{"app.js": precompressibleAsset})
		req.AppPrerenderedDir = writeStrategyDir(t, map[string]string{"index.html": precompressibleAsset})

		img, err := NewPackager(testLogger()).Build(context.Background(), req)
		if err != nil {
			t.Fatalf("build: %v", err)
		}

		names := allTarMemberNames(t, img)
		if !hasSuffixAmong(names, ".gz") {
			t.Error("no .gz sidecar found in a layered-strategy build; want gzip still generated")
		}
		if !hasSuffixAmong(names, ".br") {
			t.Error("no .br sidecar found in a layered-strategy build; want brotli still generated")
		}
		if hasSuffixAmong(names, ".zst") {
			t.Error("found a .zst sidecar in a layered-strategy build; adapter-node's sirv server never negotiates zstd, so it should not be generated")
		}
	})

	t.Run("static: gzip/brotli/zstd all present", func(t *testing.T) {
		req := newRequest(t, ports.LinuxAMD64)
		req.Strategy = ports.StrategyStatic
		req.StaticServer = []byte("fake-pokkum-static-binary")
		req.AppClientDir = writeStrategyDir(t, map[string]string{"app.js": precompressibleAsset})
		req.AppPrerenderedDir = writeStrategyDir(t, map[string]string{"index.html": precompressibleAsset})

		img, err := NewPackager(testLogger()).Build(context.Background(), req)
		if err != nil {
			t.Fatalf("build: %v", err)
		}

		names := allTarMemberNames(t, img)
		for _, suffix := range []string{".gz", ".br", ".zst"} {
			if !hasSuffixAmong(names, suffix) {
				t.Errorf("no %s sidecar found in a static-strategy build; pokkum-static negotiates all three formats", suffix)
			}
		}
	})
}

// TestBuild_LayeredClientEnvSetWithoutPrerendered is the regression guard for the
// silent-404 defect.
//
// EnvClientDir used to be set nowhere at all, and the branch that set
// EnvPrerenderedDir was reached only when AppPrerenderedDir was non-empty. An
// app with client assets and no prerendered pages — entirely ordinary — got
// neither, so adapter-node looked for its assets under /app/server/client,
// found nothing, and dropped its asset middleware silently via .filter(Boolean).
// Every stylesheet and script 404'd while the image booted, both probes passed
// and / returned 200.
//
// The condition here is deliberately "no prerendered dir": that is the case the
// old code could not reach.
func TestBuild_LayeredClientEnvSetWithoutPrerendered(t *testing.T) {
	req := newRequest(t, ports.LinuxAMD64)
	req.Strategy = ports.StrategyLayered
	req.BunRuntime = ports.BunResolverResult{
		BinaryPath: writeBinary(t, "bun", []byte("#!/bin/sh\necho bun")),
	}
	req.AppServerDir = writeStrategyDir(t, map[string]string{"index.js": "server entry"})
	req.AppClientDir = writeStrategyDir(t, map[string]string{"app.js": "client asset"})
	req.AppPrerenderedDir = ""

	img, err := NewPackager(testLogger()).Build(context.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("ConfigFile: %v", err)
	}

	env := map[string]string{}
	for _, kv := range cfg.Config.Env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}

	if got := env[ports.EnvClientDir]; got != ports.AppClientDirPrefix {
		t.Errorf("%s = %q, want %q — without it adapter-node serves assets from /app/server/client, which does not exist, and drops the handler silently",
			ports.EnvClientDir, got, ports.AppClientDirPrefix)
	}
	if _, ok := env[ports.EnvPrerenderedDir]; ok {
		t.Errorf("%s must not be set when there is no prerendered tree", ports.EnvPrerenderedDir)
	}
}

// TestBuild_LayeredPackagesNodeModulesWhereResolutionLooks is the regression
// guard for images that were not self-contained.
//
// Two things must hold, and the second is the one that was subtly wrong before:
// the dependency tree has to be IN the image, and it has to be at a path module
// resolution actually consults. Node and Bun walk upward from the importing
// file, so /app/server/index.js finds /app/server/node_modules, then
// /app/node_modules, then /node_modules — and never /app/vendor, which is where
// the (never-populated) vendor layer pointed.
func TestBuild_LayeredPackagesNodeModulesWhereResolutionLooks(t *testing.T) {
	req := newRequest(t, ports.LinuxAMD64)
	req.Strategy = ports.StrategyLayered
	req.BunRuntime = ports.BunResolverResult{
		BinaryPath: writeBinary(t, "bun", []byte("#!/bin/sh\necho bun")),
	}
	req.AppServerDir = writeStrategyDir(t, map[string]string{"index.js": "import 'valibot'"})
	req.AppClientDir = writeStrategyDir(t, map[string]string{"app.js": "client asset"})
	req.AppNodeModulesDir = writeStrategyDir(t, map[string]string{
		"valibot-package.json": `{"name":"valibot","version":"1.4.2"}`,
		"valibot-index.js":     "export const x = 1",
	})

	img, err := NewPackager(testLogger()).Build(context.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("ConfigFile: %v", err)
	}

	var found bool
	for _, h := range cfg.History {
		if h.CreatedBy == "pokkum: add "+ports.AppNodeModulesDirPrefix {
			found = true
		}
	}
	if !found {
		var got []string
		for _, h := range cfg.History {
			got = append(got, h.CreatedBy)
		}
		t.Errorf("no %s layer in the image; the server bundle's bare imports resolve to nothing and Bun would fetch them from npm at runtime.\nlayers: %v",
			ports.AppNodeModulesDirPrefix, got)
	}

	// The mount point is the load-bearing part: /app/vendor would package the
	// same bytes somewhere resolution never looks.
	if ports.AppNodeModulesDirPrefix != "/app/node_modules" {
		t.Errorf("dependencies must mount at /app/node_modules for upward resolution to find them, got %q", ports.AppNodeModulesDirPrefix)
	}
}
