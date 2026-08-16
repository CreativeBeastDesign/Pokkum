package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestBuild_ProfileResolution(t *testing.T) {
	tmpDir := t.TempDir()

	cfgContent := `version: 1
docker:
  repo: ghcr.io/example/app
base: distroless
strategy: layered
profiles:
  custom-dev:
    output: local
    platforms: [local]
    base: chainguard
    sourcemap: true
    security:
      fail_on_cve: critical
`
	_ = os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), []byte(cfgContent), 0644)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	flags := &buildFlags{
		profile:         "custom-dev",
		dryRun:          true,
		platforms:       []string{"linux/amd64", "linux/arm64"},
		base:            "",
		strategy:        "layered",
		sbom:            "spdx-json",
		sbomAttach:      "referrer",
		compression:     "gzip",
		bunVariant:      "standard",
		traceSampleRate: 1.0,
		sign:            false,
		inject:          true,
	}

	req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, tmpDir)
	if err != nil {
		t.Fatalf("buildRequestFromConfigAndFlags with profile failed: %v", err)
	}

	// Verify profile overrides applied correctly
	if req.BaseImage.Preset != ports.BaseImageChainguard {
		t.Errorf("expected BaseImage.Preset chainguard, got: %s", req.BaseImage.Preset)
	}
	if req.Output.Mode != core.OutputLocal {
		t.Errorf("expected Output.Mode local, got: %s", req.Output.Mode)
	}
	if !req.Compile.Sourcemap {
		t.Errorf("expected Compile.Sourcemap true, got false")
	}
	if req.FailOnCVE != core.SeverityCritical {
		t.Errorf("expected FailOnCVE critical, got: %s", req.FailOnCVE)
	}
	if len(req.Platforms) != 1 || req.Platforms[0] != ports.LocalPlatform() {
		t.Errorf("expected Platforms [local (%s)], got: %v", ports.LocalPlatform(), req.Platforms)
	}
}

func TestBuild_CliFlagsOverrideProfile(t *testing.T) {
	tmpDir := t.TempDir()

	cfgContent := `version: 1
docker:
  repo: ghcr.io/example/app
base: distroless
strategy: layered
profiles:
  prod:
    base: distroless
`
	_ = os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), []byte(cfgContent), 0644)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	flags := &buildFlags{
		profile:          "prod",
		base:             "chainguard",
		baseExplicit:     true, // CLI explicitly provided --base
		dryRun:           true,
		platforms:        []string{"linux/amd64"},
		platformExplicit: true,
		strategy:         "layered",
		sbom:             "spdx-json",
		sbomAttach:       "referrer",
		compression:      "gzip",
		bunVariant:       "standard",
		traceSampleRate:  1.0,
		sign:             false,
		inject:           true,
	}

	req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, tmpDir)
	if err != nil {
		t.Fatalf("buildRequestFromConfigAndFlags with explicit flag override failed: %v", err)
	}

	// Verify explicit CLI flag won over profile
	if req.BaseImage.Preset != ports.BaseImageChainguard {
		t.Errorf("expected BaseImage.Preset chainguard (from CLI override), got: %s", req.BaseImage.Preset)
	}
	if len(req.Platforms) != 1 || req.Platforms[0] != ports.LinuxAMD64 {
		t.Errorf("expected Platforms [linux/amd64], got: %v", req.Platforms)
	}
}

func TestBuild_AdversarialProfileEdgeCases(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// 1. Profile requested but no .pokkum.yaml exists in directory
	emptyDir := t.TempDir()
	flagsMissingConfig := &buildFlags{
		profile:   "production",
		platforms: []string{"linux/amd64"},
	}
	_, err := buildRequestFromConfigAndFlags(context.Background(), logger, flagsMissingConfig, emptyDir)
	if err == nil {
		t.Fatal("expected error when requesting profile without .pokkum.yaml, got nil")
	}

	// 2. Profile requested that is not defined in existing .pokkum.yaml
	tmpDir := t.TempDir()
	cfgContent := `version: 1
docker:
  repo: ghcr.io/example/app
profiles:
  staging:
    base: chainguard
`
	_ = os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), []byte(cfgContent), 0644)

	flagsNonExistent := &buildFlags{
		profile:   "unknown-profile",
		platforms: []string{"linux/amd64"},
	}
	_, err = buildRequestFromConfigAndFlags(context.Background(), logger, flagsNonExistent, tmpDir)
	if err == nil {
		t.Fatal("expected error for non-existent profile in .pokkum.yaml, got nil")
	}

	// 3. Deeply nested profile overrides: shutdown_timeout, cache, otel, env, require_env, ports
	deepCfgContent := `version: 1
docker:
  repo: ghcr.io/example/app
strategy: layered
base: distroless
image:
  user: "1000:1000"
  working_dir: "/workspace"
  port: 3000
  probe_port: 8081
  shutdown_timeout: "30s"
  labels:
    base.label: "preserved"
    override.me: "original"
  annotations:
    base.ann: "preserved"
  env:
    BASE_ENV: "val1"
    OVERRIDE_ENV: "orig"
  require_env: ["BASE_REQ"]
  ports: [3000, 8081]
cache:
  enabled: true
otel:
  tracing: true
  metrics: true
profiles:
  complex:
    image:
      shutdown_timeout: "15s"
      user: "2000:2000"
      labels:
        override.me: "new-value"
        profile.label: "added"
      annotations:
        profile.ann: "added"
      env:
        OVERRIDE_ENV: "new"
        PROFILE_ENV: "val2"
      require_env: ["PROFILE_REQ"]
      ports: [9000]
    cache:
      enabled: false
    otel:
      sidecar: true
      tracing: false
      metrics: true
`
	_ = os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), []byte(deepCfgContent), 0644)

	flagsComplex := &buildFlags{
		profile:   "complex",
		platforms: []string{"linux/amd64"},
	}
	req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flagsComplex, tmpDir)
	if err != nil {
		t.Fatalf("buildRequestFromConfigAndFlags for complex profile failed: %v", err)
	}

	// Verify deep overrides
	if req.Runtime.ShutdownTimeout.String() != "15s" {
		t.Errorf("expected ShutdownTimeout 15s, got: %v", req.Runtime.ShutdownTimeout)
	}
	if req.Runtime.User != "2000:2000" {
		t.Errorf("expected User 2000:2000, got: %s", req.Runtime.User)
	}
	if req.Labels["override.me"] != "new-value" || req.Labels["base.label"] != "preserved" || req.Labels["profile.label"] != "added" {
		t.Errorf("labels not merged properly: %v", req.Labels)
	}
	if req.Annotations["base.ann"] != "preserved" || req.Annotations["profile.ann"] != "added" {
		t.Errorf("annotations not merged properly: %v", req.Annotations)
	}
	if req.Runtime.Env["OVERRIDE_ENV"] != "new" || req.Runtime.Env["BASE_ENV"] != "val1" || req.Runtime.Env["PROFILE_ENV"] != "val2" {
		t.Errorf("env not merged properly: %v", req.Runtime.Env)
	}
	if !req.Compile.NoCache {
		t.Errorf("expected NoCache to be true because cache.enabled was overridden to false in profile")
	}
	if !req.Telemetry.WithSidecar {
		t.Errorf("expected Telemetry.WithSidecar true from profile")
	}
	if !req.Telemetry.MetricsOnly {
		t.Errorf("expected Telemetry.MetricsOnly true because tracing=false & metrics=true in profile")
	}

	// 4. Malformed shutdown_timeout in profile
	invalidTimeoutCfg := `version: 1
docker:
  repo: ghcr.io/example/app
profiles:
  bad-timeout:
    image:
      shutdown_timeout: "invalid-duration"
`
	_ = os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), []byte(invalidTimeoutCfg), 0644)

	flagsBadTimeout := &buildFlags{
		profile:   "bad-timeout",
		platforms: []string{"linux/amd64"},
	}
	_, err = buildRequestFromConfigAndFlags(context.Background(), logger, flagsBadTimeout, tmpDir)
	if err == nil {
		t.Fatal("expected error on malformed shutdown_timeout in profile, got nil")
	}

	// 5. Invalid output mode in profile
	invalidOutputCfg := `version: 1
docker:
  repo: ghcr.io/example/app
profiles:
  bad-output:
    output: "non-existent-mode"
`
	_ = os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), []byte(invalidOutputCfg), 0644)

	flagsBadOutput := &buildFlags{
		profile:   "bad-output",
		platforms: []string{"linux/amd64"},
	}
	_, err = buildRequestFromConfigAndFlags(context.Background(), logger, flagsBadOutput, tmpDir)
	if err == nil {
		t.Fatal("expected error on invalid output mode in profile, got nil")
	}
}
