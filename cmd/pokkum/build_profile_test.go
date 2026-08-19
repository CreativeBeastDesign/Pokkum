package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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

// TestBuild_CustomBaseReferenceReachesRequest is the CLI-reachability check
// for the --base preset-vs-reference gap (mem:self_review_checklist row 16):
// before this fix, buildRequestFromConfigAndFlags only ever called
// core.ParseBaseImagePreset on --base, which rejects any string that is not
// one of the four fixed preset names — so a full image reference could never
// reach core.BuildRequest.BaseImage.Ref via the CLI at all, even though
// ports.BaseImageCustom and BaseImageRequest.Ref already existed and the
// resolver already consumed them. This drives the real production function
// build.go's runBuild calls (buildRequestFromConfigAndFlags), not a
// mirrored/duplicated copy, so it is direct evidence the flag reaches the
// request — not just that a struct field can be set in isolation.
func TestBuild_CustomBaseReferenceReachesRequest(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("CLI flag", func(t *testing.T) {
		tmpDir := t.TempDir()
		flags := &buildFlags{
			base:         "gcr.io/my-org/my-base:v1.2.3",
			baseExplicit: true,
			platforms:    []string{"linux/amd64"},
			strategy:     "layered",
			bunVariant:   "standard",
			inject:       true,
		}
		req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, tmpDir)
		if err != nil {
			t.Fatalf("buildRequestFromConfigAndFlags with custom --base failed: %v", err)
		}
		if req.BaseImage.Preset != ports.BaseImageCustom {
			t.Errorf("expected BaseImage.Preset custom, got: %s", req.BaseImage.Preset)
		}
		if req.BaseImage.Ref != "gcr.io/my-org/my-base:v1.2.3" {
			t.Errorf("expected BaseImage.Ref %q, got: %q", "gcr.io/my-org/my-base:v1.2.3", req.BaseImage.Ref)
		}
	})

	t.Run("project config base field", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgContent := `version: 1
docker:
  repo: ghcr.io/example/app
base: gcr.io/my-org/my-base:v1.2.3
`
		_ = os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), []byte(cfgContent), 0644)

		flags := &buildFlags{
			platforms:  []string{"linux/amd64"},
			strategy:   "layered",
			bunVariant: "standard",
			inject:     true,
		}
		req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, tmpDir)
		if err != nil {
			t.Fatalf("buildRequestFromConfigAndFlags with custom config base failed: %v", err)
		}
		if req.BaseImage.Preset != ports.BaseImageCustom {
			t.Errorf("expected BaseImage.Preset custom, got: %s", req.BaseImage.Preset)
		}
		if req.BaseImage.Ref != "gcr.io/my-org/my-base:v1.2.3" {
			t.Errorf("expected BaseImage.Ref %q, got: %q", "gcr.io/my-org/my-base:v1.2.3", req.BaseImage.Ref)
		}
	})

	t.Run("profile base override", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgContent := `version: 1
docker:
  repo: ghcr.io/example/app
base: distroless
profiles:
  custom-dev:
    base: gcr.io/my-org/my-base:v1.2.3
`
		_ = os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), []byte(cfgContent), 0644)

		flags := &buildFlags{
			profile:    "custom-dev",
			platforms:  []string{"linux/amd64"},
			strategy:   "layered",
			bunVariant: "standard",
			inject:     true,
		}
		req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, tmpDir)
		if err != nil {
			t.Fatalf("buildRequestFromConfigAndFlags with custom profile base failed: %v", err)
		}
		if req.BaseImage.Preset != ports.BaseImageCustom {
			t.Errorf("expected BaseImage.Preset custom, got: %s", req.BaseImage.Preset)
		}
		if req.BaseImage.Ref != "gcr.io/my-org/my-base:v1.2.3" {
			t.Errorf("expected BaseImage.Ref %q, got: %q", "gcr.io/my-org/my-base:v1.2.3", req.BaseImage.Ref)
		}
	})

	t.Run("typo'd preset is rejected, not silently treated as a reference", func(t *testing.T) {
		tmpDir := t.TempDir()
		flags := &buildFlags{
			base:         "distrolss",
			baseExplicit: true,
			platforms:    []string{"linux/amd64"},
			strategy:     "layered",
			bunVariant:   "standard",
			inject:       true,
		}
		_, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, tmpDir)
		if err == nil {
			t.Fatal("expected a typo'd base preset to be rejected, got nil")
		}
		if !strings.Contains(err.Error(), "not a recognized preset") {
			t.Errorf("expected error to explain the preset is unrecognized, got: %v", err)
		}
	})
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

// TestBuild_VEXExemptionsFromConfig is PR-6's CLI-wiring regression guard:
// self-review (checklist row 13, mem:self_review_checklist) found that
// core.ParseVEXExemptions and the scanner adapter were both unit-tested in
// isolation, but nothing proved a real .pokkum.yaml security.vex_exemptions
// block actually reaches BuildRequest.VEXExemptions through this file's real
// parsing call chain (build.go's buildRequestFromConfigAndFlags), following
// the same pattern already established above for --profile/--base wiring.
func TestBuild_VEXExemptionsFromConfig(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("valid vex_exemptions block reaches BuildRequest.VEXExemptions", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgContent := `version: 1
docker:
  repo: ghcr.io/example/app
base: distroless
strategy: layered
security:
  vex_exemptions:
    - cve: CVE-2026-1234
      package: libssl3
      justification: component_not_present
      status_notes: not compiled into this image
      expires: "2099-01-01"
      owner: security-team
`
		_ = os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), []byte(cfgContent), 0644)

		flags := &buildFlags{
			dryRun:      true,
			platforms:   []string{"linux/amd64"},
			strategy:    "layered",
			sbom:        "spdx-json",
			sbomAttach:  "referrer",
			compression: "gzip",
			bunVariant:  "standard",
			inject:      true,
		}

		req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, tmpDir)
		if err != nil {
			t.Fatalf("buildRequestFromConfigAndFlags with vex_exemptions failed: %v", err)
		}
		if len(req.VEXExemptions) != 1 {
			t.Fatalf("expected 1 parsed VEXExemption, got %d: %+v", len(req.VEXExemptions), req.VEXExemptions)
		}
		ex := req.VEXExemptions[0]
		if ex.CVE != "CVE-2026-1234" || ex.Package != "libssl3" || ex.Owner != "security-team" {
			t.Errorf("unexpected parsed exemption: %+v", ex)
		}
		if string(ex.Justification) != "component_not_present" {
			t.Errorf("expected justification component_not_present, got %q", ex.Justification)
		}
	})

	t.Run("invalid vex_exemptions block fails the build request with a clear error", func(t *testing.T) {
		tmpDir := t.TempDir()
		cfgContent := `version: 1
docker:
  repo: ghcr.io/example/app
base: distroless
strategy: layered
security:
  vex_exemptions:
    - cve: CVE-2026-9999
      justification: not_a_real_justification_code
      expires: "2099-01-01"
      owner: security-team
`
		_ = os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), []byte(cfgContent), 0644)

		flags := &buildFlags{
			dryRun:      true,
			platforms:   []string{"linux/amd64"},
			strategy:    "layered",
			sbom:        "spdx-json",
			sbomAttach:  "referrer",
			compression: "gzip",
			bunVariant:  "standard",
			inject:      true,
		}

		_, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, tmpDir)
		if err == nil {
			t.Fatal("expected an error for an invalid vex_exemptions justification, got nil")
		}
		if !strings.Contains(err.Error(), "vex_exemptions") {
			t.Errorf("expected error to mention vex_exemptions, got: %v", err)
		}
	})
}

// TestWriteVEXDocument covers PR-6's --vex-output flag: the OpenVEX document
// writer that runs after a successful build (cmd/pokkum/build.go's
// writeVEXDocument), which self-review found had no direct test — only
// vexutils.BuildDocument (the pure document-construction half) was tested.
func TestWriteVEXDocument(t *testing.T) {
	t.Run("writes a real OpenVEX JSON document when exemptions are present", func(t *testing.T) {
		tmpDir := t.TempDir()
		outPath := filepath.Join(tmpDir, "vex.json")

		exemptions := []core.VEXExemption{{
			CVE:           "CVE-2026-1234",
			Justification: core.VEXJustification(ports.VEXComponentNotPresent),
			Expires:       time.Now().AddDate(1, 0, 0),
			Owner:         "security-team",
		}}

		if err := writeVEXDocument(outPath, exemptions, "ghcr.io/example/app@sha256:abc123"); err != nil {
			t.Fatalf("writeVEXDocument failed: %v", err)
		}

		data, err := os.ReadFile(outPath)
		if err != nil {
			t.Fatalf("expected %s to be written, got: %v", outPath, err)
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("written file is not valid JSON: %v", err)
		}
		statements, ok := doc["statements"].([]any)
		if !ok || len(statements) != 1 {
			t.Fatalf("expected 1 statement in the written document, got: %+v", doc["statements"])
		}
	})

	t.Run("writes nothing when there are no exemptions", func(t *testing.T) {
		tmpDir := t.TempDir()
		outPath := filepath.Join(tmpDir, "vex.json")

		if err := writeVEXDocument(outPath, nil, "ghcr.io/example/app"); err != nil {
			t.Fatalf("writeVEXDocument failed: %v", err)
		}
		if _, err := os.Stat(outPath); !os.IsNotExist(err) {
			t.Errorf("expected no file to be written when there are no exemptions, got err=%v", err)
		}
	})
}

// TestBuild_TagsPrecedence proves --tag / POKKUM_DOCKER_TAGS / docker.tags
// (including its per-profile override) reach BuildRequest.Tags through the
// real buildRequestFromConfigAndFlags call chain, in the documented
// precedence order: explicit --tag flag > POKKUM_DOCKER_TAGS env >
// project config / profile > default ("latest", applied by
// BuildRequest.Normalize, not by this function).
func TestBuild_TagsPrecedence(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cfgContent := `version: 1
docker:
  repo: ghcr.io/example/app
  tags:
    - from-config
profiles:
  prod:
    docker:
      tags:
        - from-profile
`

	newTmpDirWithConfig := func(t *testing.T) string {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), []byte(cfgContent), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", ports.ConfigFilename, err)
		}
		return tmpDir
	}

	t.Run("explicit --tag flag wins over env and config", func(t *testing.T) {
		tmpDir := newTmpDirWithConfig(t)
		t.Setenv("POKKUM_DOCKER_TAGS", "from-env")

		flags := &buildFlags{
			platforms:    []string{"linux/amd64"},
			tags:         []string{"from-flag-1", "from-flag-2"},
			tagsExplicit: true,
		}
		req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, tmpDir)
		if err != nil {
			t.Fatalf("buildRequestFromConfigAndFlags failed: %v", err)
		}
		if want := []string{"from-flag-1", "from-flag-2"}; !slices.Equal(req.Tags, want) {
			t.Errorf("expected Tags %v (flag wins), got %v", want, req.Tags)
		}
	})

	t.Run("POKKUM_DOCKER_TAGS env wins over config when flag is not explicit", func(t *testing.T) {
		tmpDir := newTmpDirWithConfig(t)
		t.Setenv("POKKUM_DOCKER_TAGS", "from-env-1, from-env-2")

		flags := &buildFlags{
			platforms: []string{"linux/amd64"},
		}
		req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, tmpDir)
		if err != nil {
			t.Fatalf("buildRequestFromConfigAndFlags failed: %v", err)
		}
		if want := []string{"from-env-1", "from-env-2"}; !slices.Equal(req.Tags, want) {
			t.Errorf("expected Tags %v (env wins over config), got %v", want, req.Tags)
		}
	})

	t.Run("docker.tags from project config is used when neither flag nor env is set", func(t *testing.T) {
		tmpDir := newTmpDirWithConfig(t)
		t.Setenv("POKKUM_DOCKER_TAGS", "")

		flags := &buildFlags{
			platforms: []string{"linux/amd64"},
		}
		req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, tmpDir)
		if err != nil {
			t.Fatalf("buildRequestFromConfigAndFlags failed: %v", err)
		}
		if want := []string{"from-config"}; !slices.Equal(req.Tags, want) {
			t.Errorf("expected Tags %v (from base config), got %v", want, req.Tags)
		}
	})

	t.Run("a named profile's docker.tags overrides the base config's", func(t *testing.T) {
		tmpDir := newTmpDirWithConfig(t)
		t.Setenv("POKKUM_DOCKER_TAGS", "")

		flags := &buildFlags{
			platforms: []string{"linux/amd64"},
			profile:   "prod",
		}
		req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, tmpDir)
		if err != nil {
			t.Fatalf("buildRequestFromConfigAndFlags failed: %v", err)
		}
		if want := []string{"from-profile"}; !slices.Equal(req.Tags, want) {
			t.Errorf("expected Tags %v (profile override), got %v", want, req.Tags)
		}
	})

	t.Run("defaults to latest via Normalize when nothing sets a tag", func(t *testing.T) {
		tmpDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), []byte("version: 1\ndocker:\n  repo: ghcr.io/example/app\n"), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", ports.ConfigFilename, err)
		}
		t.Setenv("POKKUM_DOCKER_TAGS", "")

		flags := &buildFlags{
			platforms: []string{"linux/amd64"},
		}
		req, err := buildRequestFromConfigAndFlags(context.Background(), logger, flags, tmpDir)
		if err != nil {
			t.Fatalf("buildRequestFromConfigAndFlags failed: %v", err)
		}
		if len(req.Tags) != 0 {
			t.Errorf("expected buildRequestFromConfigAndFlags to leave Tags unset before Normalize, got %v", req.Tags)
		}
		req.Normalize()
		if want := []string{core.DefaultTag}; !slices.Equal(req.Tags, want) {
			t.Errorf("expected Normalize to default Tags to %v, got %v", want, req.Tags)
		}
	})
}
