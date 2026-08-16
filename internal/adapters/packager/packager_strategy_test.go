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
			wantEnv:       map[string]string{ports.EnvPrerenderedDir: ports.AppPrerenderedDirPrefix},
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
