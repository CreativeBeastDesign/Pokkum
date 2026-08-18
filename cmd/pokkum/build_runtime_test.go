package main

// Tests for the --runtime flag's CLI wiring: registration and default,
// flag > config > default precedence (including profile merge), the
// node-defaults-the-base behavior, and the explicit-bun-flag conflicts that
// only the flag layer can detect (see buildFlags.explicitBunFlags).

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestBuildCommandRuntimeFlagRegistered(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cmd := newBuildCommand(context.Background(), logger)

	flag := cmd.Flags().Lookup("runtime")
	if flag == nil {
		t.Fatal("expected --runtime flag to be registered")
	}
	if flag.DefValue != "bun" {
		t.Errorf("--runtime default = %q, want %q", flag.DefValue, "bun")
	}

	if err := cmd.Flags().Parse([]string{"--runtime=node"}); err != nil {
		t.Fatalf("parse --runtime: %v", err)
	}
	got, err := cmd.Flags().GetString("runtime")
	if err != nil {
		t.Fatalf("GetString(runtime): %v", err)
	}
	if got != "node" {
		t.Errorf("--runtime parsed value = %q, want %q", got, "node")
	}
}

// baseRuntimeTestFlags returns a buildFlags with the same defaults the cobra
// flag definitions would produce, so buildRequestFromConfigAndFlags can be
// exercised directly (matching build_profile_test.go's pattern).
func baseRuntimeTestFlags() *buildFlags {
	return &buildFlags{
		platforms:       []string{"linux/amd64"},
		strategy:        "layered",
		runtime:         "bun",
		sbom:            "spdx-json",
		sbomAttach:      "auto",
		compression:     "gzip",
		bunVariant:      "standard",
		traceSampleRate: 1.0,
		sign:            false,
		inject:          true,
	}
}

func TestBuildRuntimePrecedence(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("explicit flag wins over config", func(t *testing.T) {
		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), []byte("version: 1\nruntime: node\n"), 0o644)

		flags := baseRuntimeTestFlags()
		flags.runtime = "bun"
		flags.runtimeExplicit = true

		req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, tmpDir)
		if err != nil {
			t.Fatalf("buildRequestFromConfigAndFlags: %v", err)
		}
		if req.AppRuntime != core.RuntimeBun {
			t.Errorf("AppRuntime = %q, want bun (explicit flag beats config)", req.AppRuntime)
		}
	})

	t.Run("config wins over untouched flag default", func(t *testing.T) {
		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), []byte("version: 1\nruntime: node\n"), 0o644)

		flags := baseRuntimeTestFlags()

		req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, tmpDir)
		if err != nil {
			t.Fatalf("buildRequestFromConfigAndFlags: %v", err)
		}
		if req.AppRuntime != core.RuntimeNode {
			t.Errorf("AppRuntime = %q, want node (config beats untouched flag default)", req.AppRuntime)
		}

		// And the runtime dimension must flow into the normalized base
		// preset — the pokkum.lock key — not just sit on the request.
		req.Normalize()
		if req.BaseImage.Preset != core.BaseImageDistrolessNode {
			t.Errorf("BaseImage.Preset = %q, want %q", req.BaseImage.Preset, core.BaseImageDistrolessNode)
		}
	})

	t.Run("profile runtime overrides top-level runtime", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfg := "version: 1\nruntime: bun\nprofiles:\n  node-prod:\n    runtime: node\n"
		_ = os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), []byte(cfg), 0o644)

		flags := baseRuntimeTestFlags()
		flags.profile = "node-prod"

		req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, tmpDir)
		if err != nil {
			t.Fatalf("buildRequestFromConfigAndFlags: %v", err)
		}
		if req.AppRuntime != core.RuntimeNode {
			t.Errorf("AppRuntime = %q, want node (profile beats top-level config)", req.AppRuntime)
		}
	})

	t.Run("invalid config runtime rejected", func(t *testing.T) {
		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), []byte("version: 1\nruntime: deno\n"), 0o644)

		flags := baseRuntimeTestFlags()
		if _, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, tmpDir); err == nil {
			t.Fatal("expected error for runtime: deno, got nil")
		}
	})
}

// TestBuildRuntimeNodeRejectsExplicitBunFlags covers the flag-layer half of
// the bun-flag conflict: --bun-version/--bun-variant have non-empty
// normalized defaults core's Validate cannot tell apart from an explicit
// choice, so silently ignoring an explicitly-passed one under an effective
// --runtime=node (from flag OR config) is only preventable here.
func TestBuildRuntimeNodeRejectsExplicitBunFlags(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("runtime from flag", func(t *testing.T) {
		flags := baseRuntimeTestFlags()
		flags.runtime = "node"
		flags.runtimeExplicit = true
		flags.explicitBunFlags = []string{"bun-version"}

		_, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "bun-version") {
			t.Fatalf("err = %v, want an error naming --bun-version", err)
		}
	})

	t.Run("runtime from config", func(t *testing.T) {
		tmpDir := t.TempDir()
		_ = os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), []byte("version: 1\nruntime: node\n"), 0o644)

		flags := baseRuntimeTestFlags()
		flags.explicitBunFlags = []string{"bun-variant"}

		_, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, tmpDir)
		if err == nil || !strings.Contains(err.Error(), "bun-variant") {
			t.Fatalf("err = %v, want an error naming --bun-variant (config-sourced runtime must conflict too)", err)
		}
	})
}
