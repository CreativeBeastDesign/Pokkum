package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestConfigViewCommand(t *testing.T) {
	tmpDir := t.TempDir()

	cfgContent := `version: 1
docker:
  repo: ghcr.io/example/app
base: distroless
strategy: layered
profiles:
  local:
    output: local
    platforms: [local]
    base: chainguard
`
	if err := os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), []byte(cfgContent), 0644); err != nil {
		t.Fatalf("failed to write .pokkum.yaml: %v", err)
	}

	// Test default view (JSON)
	opts := &configViewOptions{
		dir:    tmpDir,
		output: "json",
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runConfigView(nil, opts)

	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("runConfigView failed: %v", err)
	}

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)

	var env ports.JSONEnvelope
	if err := json.Unmarshal(outBuf.Bytes(), &env); err != nil {
		t.Fatalf("config view emitted invalid JSON: %v", err)
	}

	// Test profile view (YAML output)
	optsProfile := &configViewOptions{
		dir:     tmpDir,
		profile: "local",
		output:  "text",
	}

	r2, w2, _ := os.Pipe()
	os.Stdout = w2

	err = runConfigView(nil, optsProfile)

	w2.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("runConfigView with profile failed: %v", err)
	}

	var outBuf2 bytes.Buffer
	_, _ = io.Copy(&outBuf2, r2)
	outStr := outBuf2.String()

	if !strings.Contains(outStr, "base: chainguard") {
		t.Errorf("expected resolved profile output to contain 'base: chainguard', got:\n%s", outStr)
	}
}

func TestConfigValidateCommand(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Valid config
	validCfg := `version: 1
docker:
  repo: ghcr.io/example/app
base: distroless
strategy: layered
platforms:
  - linux/amd64
  - linux/arm64
security:
  fail_on_cve: high
`
	cfgPath := filepath.Join(tmpDir, ports.ConfigFilename)
	_ = os.WriteFile(cfgPath, []byte(validCfg), 0644)

	opts := &configValidateOptions{
		dir:    tmpDir,
		output: "json",
	}

	err := runConfigValidate(nil, opts)
	if err != nil {
		t.Fatalf("expected valid config to pass validation, got: %v", err)
	}

	// 2. Invalid config (bad strategy and bad preset)
	invalidCfg := `version: 1
strategy: unknown-strategy
base: invalid-base-preset
`
	_ = os.WriteFile(cfgPath, []byte(invalidCfg), 0644)

	err = runConfigValidate(nil, opts)
	if err == nil {
		t.Fatalf("expected invalid config to fail validation, got nil")
	}

	// 3. Invalid schema version
	badVersionCfg := `version: 99
strategy: layered
`
	_ = os.WriteFile(cfgPath, []byte(badVersionCfg), 0644)
	err = runConfigValidate(nil, opts)
	if err == nil {
		t.Fatal("expected error on unsupported schema version, got nil")
	}

	// 4. Invalid fail_on_cve severity
	badSeverityCfg := `version: 1
security:
  fail_on_cve: mega-critical
`
	_ = os.WriteFile(cfgPath, []byte(badSeverityCfg), 0644)
	err = runConfigValidate(nil, opts)
	if err == nil {
		t.Fatal("expected error on invalid fail_on_cve severity, got nil")
	}

	// 5. Invalid platforms
	badPlatformCfg := `version: 1
platforms:
  - windows/x86
`
	_ = os.WriteFile(cfgPath, []byte(badPlatformCfg), 0644)
	err = runConfigValidate(nil, opts)
	if err == nil {
		t.Fatal("expected error on invalid platforms, got nil")
	}

	// 6. Invalid SBOM format
	badSBOMCfg := `version: 1
sbom:
  format: invalid-format
`
	_ = os.WriteFile(cfgPath, []byte(badSBOMCfg), 0644)
	err = runConfigValidate(nil, opts)
	if err == nil {
		t.Fatal("expected error on invalid sbom format, got nil")
	}

	// 7. Invalid docker.repo (top-level)
	badRepoCfg := `version: 1
docker:
  repo: "not a valid repo ref!!"
`
	_ = os.WriteFile(cfgPath, []byte(badRepoCfg), 0644)
	err = runConfigValidate(nil, opts)
	if err == nil {
		t.Fatal("expected error on invalid top-level docker.repo, got nil")
	}

	// 8. Invalid docker.tags (top-level): an empty-after-trim tag.
	badTagsCfg := `version: 1
docker:
  repo: ghcr.io/example/app
  tags:
    - "bad tag"
`
	_ = os.WriteFile(cfgPath, []byte(badTagsCfg), 0644)
	err = runConfigValidate(nil, opts)
	if err == nil {
		t.Fatal("expected error on invalid top-level docker.tags, got nil")
	}

	// 9. Duplicate docker.tags (top-level).
	dupTagsCfg := `version: 1
docker:
  repo: ghcr.io/example/app
  tags:
    - v1
    - v1
`
	_ = os.WriteFile(cfgPath, []byte(dupTagsCfg), 0644)
	err = runConfigValidate(nil, opts)
	if err == nil {
		t.Fatal("expected error on duplicate top-level docker.tags, got nil")
	}

	// 10. Valid docker.tags (top-level) must pass.
	goodTagsCfg := `version: 1
docker:
  repo: ghcr.io/example/app
  tags:
    - latest
    - v1.2.3
`
	_ = os.WriteFile(cfgPath, []byte(goodTagsCfg), 0644)
	if err := runConfigValidate(nil, opts); err != nil {
		t.Errorf("expected valid docker.tags to pass validation, got: %v", err)
	}
}

// TestConfigValidateCommand_ProfileValidation exercises the profile
// validation path of runConfigValidate: a valid top-level config with an
// invalid field buried in a named profile must fail validation, name the
// offending profile in the error, and a config with multiple profiles must
// still catch an error on a non-first profile (regression coverage for the
// map-iteration-order / multi-item pitfalls called out in the self-review
// checklist).
func TestConfigValidateCommand_ProfileValidation(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, ports.ConfigFilename)
	opts := &configValidateOptions{dir: tmpDir, output: "json"}

	// 1. A single bad profile: invalid strategy.
	badProfileCfg := `version: 1
docker:
  repo: ghcr.io/example/app
strategy: layered
profiles:
  local:
    strategy: not-a-real-strategy
`
	_ = os.WriteFile(cfgPath, []byte(badProfileCfg), 0644)
	err := runConfigValidate(nil, opts)
	if err == nil {
		t.Fatal("expected error when a profile has an invalid strategy, got nil")
	}

	// 2. Multiple profiles, only the second (non-first, alphabetically last)
	// one is broken — must still be caught, and the error must name it.
	multiProfileCfg := `version: 1
docker:
  repo: ghcr.io/example/app
strategy: layered
profiles:
  local:
    base: chainguard
  zzz-production:
    base: not-a-real-base-preset
`
	_ = os.WriteFile(cfgPath, []byte(multiProfileCfg), 0644)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err = runConfigValidate(nil, opts)

	w.Close()
	os.Stdout = oldStdout

	if err == nil {
		t.Fatal("expected error when a non-first profile has an invalid base preset, got nil")
	}

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)
	var env ports.JSONEnvelope
	if jsonErr := json.Unmarshal(outBuf.Bytes(), &env); jsonErr != nil {
		t.Fatalf("expected valid JSON error envelope, got: %v", jsonErr)
	}
	if env.Error == nil || !strings.Contains(env.Error.Message, `profile "zzz-production"`) {
		t.Errorf(`expected error to name profile "zzz-production", got: %v`, env.Error)
	}

	// 3. A profile with an invalid docker.repo override.
	badProfileRepoCfg := `version: 1
docker:
  repo: ghcr.io/example/app
profiles:
  production:
    docker:
      repo: "not a valid repo ref!!"
`
	_ = os.WriteFile(cfgPath, []byte(badProfileRepoCfg), 0644)
	err = runConfigValidate(nil, opts)
	if err == nil {
		t.Fatal("expected error when a profile has an invalid docker.repo, got nil")
	}

	// 3b. A profile with an invalid docker.tags override (row 10: a profile
	// field must be validated the same as the base config's, not just
	// merged).
	badProfileTagsCfg := `version: 1
docker:
  repo: ghcr.io/example/app
profiles:
  production:
    docker:
      tags:
        - "not a valid tag!"
`
	_ = os.WriteFile(cfgPath, []byte(badProfileTagsCfg), 0644)
	err = runConfigValidate(nil, opts)
	if err == nil {
		t.Fatal("expected error when a profile has an invalid docker.tags entry, got nil")
	}

	// 3c. A profile with duplicate docker.tags.
	dupProfileTagsCfg := `version: 1
docker:
  repo: ghcr.io/example/app
profiles:
  production:
    docker:
      tags:
        - stable
        - stable
`
	_ = os.WriteFile(cfgPath, []byte(dupProfileTagsCfg), 0644)
	err = runConfigValidate(nil, opts)
	if err == nil {
		t.Fatal("expected error when a profile has duplicate docker.tags, got nil")
	}

	// 4. All profiles valid: must pass.
	validProfilesCfg := `version: 1
docker:
  repo: ghcr.io/example/app
strategy: layered
profiles:
  local:
    base: chainguard
    strategy: static
  production:
    docker:
      repo: ghcr.io/example/app-prod
      tags:
        - stable
    security:
      fail_on_cve: critical
`
	_ = os.WriteFile(cfgPath, []byte(validProfilesCfg), 0644)
	if err := runConfigValidate(nil, opts); err != nil {
		t.Fatalf("expected all-valid profiles to pass validation, got: %v", err)
	}
}

// TestConfigValidateCommand_NewFieldValidation covers the fields
// validateConfigFields gained to close the gap where they were merged by
// ApplyProfile but only ever checked deep in the build pipeline
// (sbom.attach, cache.verify_mode, image.port/probe_port,
// image.shutdown_timeout, security.allow_secret_patterns, and profile-only
// output) — see cmd/pokkum/config.go's configFieldsToValidate doc comment.
func TestConfigValidateCommand_NewFieldValidation(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, ports.ConfigFilename)
	opts := &configValidateOptions{dir: tmpDir, output: "json"}

	// 1. Invalid sbom.attach (top-level).
	badSBOMAttachCfg := `version: 1
sbom:
  attach: carrier-pigeon
`
	_ = os.WriteFile(cfgPath, []byte(badSBOMAttachCfg), 0644)
	if err := runConfigValidate(nil, opts); err == nil {
		t.Fatal("expected error on invalid top-level sbom.attach, got nil")
	}

	// 2. Invalid cache.verify_mode (top-level).
	badCacheVerifyModeCfg := `version: 1
cache:
  verify_mode: trust-me-bro
`
	_ = os.WriteFile(cfgPath, []byte(badCacheVerifyModeCfg), 0644)
	if err := runConfigValidate(nil, opts); err == nil {
		t.Fatal("expected error on invalid top-level cache.verify_mode, got nil")
	}

	// 3. Invalid image.port (out of range).
	badPortCfg := `version: 1
image:
  port: 99999
`
	_ = os.WriteFile(cfgPath, []byte(badPortCfg), 0644)
	if err := runConfigValidate(nil, opts); err == nil {
		t.Fatal("expected error on out-of-range top-level image.port, got nil")
	}

	// 3b. Invalid image.probe_port (out of range, negative).
	badProbePortCfg := `version: 1
image:
  probe_port: -1
`
	_ = os.WriteFile(cfgPath, []byte(badProbePortCfg), 0644)
	if err := runConfigValidate(nil, opts); err == nil {
		t.Fatal("expected error on out-of-range top-level image.probe_port, got nil")
	}

	// 4. Invalid image.shutdown_timeout (unparseable duration).
	badShutdownTimeoutCfg := `version: 1
image:
  shutdown_timeout: "not-a-duration"
`
	_ = os.WriteFile(cfgPath, []byte(badShutdownTimeoutCfg), 0644)
	if err := runConfigValidate(nil, opts); err == nil {
		t.Fatal("expected error on unparseable top-level image.shutdown_timeout, got nil")
	}

	// 4b. Invalid image.shutdown_timeout (negative duration — parses fine
	// but is semantically invalid, same rule as internal/core/model.go's
	// validateRuntime).
	negativeShutdownTimeoutCfg := `version: 1
image:
  shutdown_timeout: "-5s"
`
	_ = os.WriteFile(cfgPath, []byte(negativeShutdownTimeoutCfg), 0644)
	if err := runConfigValidate(nil, opts); err == nil {
		t.Fatal("expected error on negative top-level image.shutdown_timeout, got nil")
	}

	// 5. Invalid security.allow_secret_patterns: a bad regex as the
	// non-first entry must still be caught (row 4 of the self-review
	// checklist: non-first-item failure injection).
	badSecretPatternCfg := `version: 1
security:
  allow_secret_patterns:
    - "vendor/.*\\.js"
    - "["
`
	_ = os.WriteFile(cfgPath, []byte(badSecretPatternCfg), 0644)
	if err := runConfigValidate(nil, opts); err == nil {
		t.Fatal("expected error on invalid (non-first) top-level security.allow_secret_patterns entry, got nil")
	}

	// 5b. Valid security.allow_secret_patterns must pass.
	goodSecretPatternCfg := `version: 1
security:
  allow_secret_patterns:
    - "vendor/.*\\.js"
    - "^AKIA[0-9A-Z]{16}$"
`
	_ = os.WriteFile(cfgPath, []byte(goodSecretPatternCfg), 0644)
	if err := runConfigValidate(nil, opts); err != nil {
		t.Errorf("expected valid security.allow_secret_patterns to pass validation, got: %v", err)
	}

	// 6. Valid values for all of the above must pass together.
	goodCfg := `version: 1
sbom:
  attach: tag
cache:
  verify_mode: static-key
image:
  port: 8080
  probe_port: 8081
  shutdown_timeout: 30s
`
	_ = os.WriteFile(cfgPath, []byte(goodCfg), 0644)
	if err := runConfigValidate(nil, opts); err != nil {
		t.Errorf("expected valid new-field values to pass validation, got: %v", err)
	}
}

// TestConfigValidateCommand_NewFieldValidation_Profiles is the per-profile
// counterpart of TestConfigValidateCommand_NewFieldValidation: it proves a
// bad value hidden inside a named profile is caught the same way a bad
// top-level value is (self-review checklist row 10), that the error names
// the offending profile, and that BuildProfile's profile-only `output`
// field (which has no ProjectConfig equivalent and is therefore never
// covered by the base-config validation call) is validated too.
func TestConfigValidateCommand_NewFieldValidation_Profiles(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, ports.ConfigFilename)
	opts := &configValidateOptions{dir: tmpDir, output: "json"}

	// A bad sbom.attach buried in a named profile: must fail and name the
	// profile.
	badProfileCfg := `version: 1
docker:
  repo: ghcr.io/example/app
profiles:
  staging:
    sbom:
      attach: carrier-pigeon
`
	_ = os.WriteFile(cfgPath, []byte(badProfileCfg), 0644)

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runConfigValidate(nil, opts)

	w.Close()
	os.Stdout = oldStdout

	if err == nil {
		t.Fatal("expected error when a profile has an invalid sbom.attach, got nil")
	}

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)
	var env ports.JSONEnvelope
	if jsonErr := json.Unmarshal(outBuf.Bytes(), &env); jsonErr != nil {
		t.Fatalf("expected valid JSON error envelope, got: %v", jsonErr)
	}
	if env.Error == nil || !strings.Contains(env.Error.Message, `profile "staging"`) {
		t.Errorf(`expected error to name profile "staging", got: %v`, env.Error)
	}

	// A profile-only `output` value that is invalid must be caught, even
	// though there is no top-level ProjectConfig.Output field for it to
	// mirror.
	badProfileOutputCfg := `version: 1
docker:
  repo: ghcr.io/example/app
profiles:
  local:
    output: teleport
`
	_ = os.WriteFile(cfgPath, []byte(badProfileOutputCfg), 0644)
	err = runConfigValidate(nil, opts)
	if err == nil {
		t.Fatal("expected error when a profile has an invalid output mode, got nil")
	}

	// A valid profile-only `output` value must pass.
	goodProfileOutputCfg := `version: 1
docker:
  repo: ghcr.io/example/app
profiles:
  local:
    output: local
`
	_ = os.WriteFile(cfgPath, []byte(goodProfileOutputCfg), 0644)
	if err := runConfigValidate(nil, opts); err != nil {
		t.Errorf("expected valid profile output mode to pass validation, got: %v", err)
	}

	// Multiple profiles: only the second (non-first, alphabetically last)
	// one has a bad image.port — must still be caught and named, mirroring
	// TestConfigValidateCommand_ProfileValidation's existing coverage of
	// this same multi-item pitfall for the newly-added fields.
	multiProfileBadPortCfg := `version: 1
docker:
  repo: ghcr.io/example/app
profiles:
  local:
    base: chainguard
  zzz-production:
    image:
      port: 0
      probe_port: 100000
`
	_ = os.WriteFile(cfgPath, []byte(multiProfileBadPortCfg), 0644)

	r2, w2, _ := os.Pipe()
	os.Stdout = w2
	err = runConfigValidate(nil, opts)
	w2.Close()
	os.Stdout = oldStdout

	if err == nil {
		t.Fatal("expected error when a non-first profile has an out-of-range image.probe_port, got nil")
	}
	var outBuf2 bytes.Buffer
	_, _ = io.Copy(&outBuf2, r2)
	var env2 ports.JSONEnvelope
	if jsonErr := json.Unmarshal(outBuf2.Bytes(), &env2); jsonErr != nil {
		t.Fatalf("expected valid JSON error envelope, got: %v", jsonErr)
	}
	if env2.Error == nil || !strings.Contains(env2.Error.Message, `profile "zzz-production"`) {
		t.Errorf(`expected error to name profile "zzz-production", got: %v`, env2.Error)
	}
}

// TestConfigValidateCommand_ExampleGoldenFixture proves `pokkum config
// validate` actually accepts the documented canonical example
// (testdata/config/pokkum.yaml.golden), including its "local" and
// "production" profiles, end-to-end through the real CLI command path.
func TestConfigValidateCommand_ExampleGoldenFixture(t *testing.T) {
	goldenPath := filepath.Join("..", "..", "testdata", "config", "pokkum.yaml.golden")
	data, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("failed to read golden fixture %s: %v", goldenPath, err)
	}

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, ports.ConfigFilename), data, 0644); err != nil {
		t.Fatalf("failed to stage golden fixture: %v", err)
	}

	opts := &configValidateOptions{dir: tmpDir, output: "json"}
	if err := runConfigValidate(nil, opts); err != nil {
		t.Fatalf("expected golden fixture to pass validation, got: %v", err)
	}
}

func TestConfigView_AdversarialErrors(t *testing.T) {
	// 1. View non-existent directory (text mode returns error)
	opts := &configViewOptions{
		dir:    filepath.Join(t.TempDir(), "non-existent"),
		output: "text",
	}
	err := runConfigView(nil, opts)
	if err == nil {
		t.Fatal("expected error when viewing non-existent directory config in text mode, got nil")
	}

	// 2. View non-existent directory (json mode emits error envelope)
	optsJSON := &configViewOptions{
		dir:    filepath.Join(t.TempDir(), "non-existent"),
		output: "json",
	}
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_ = runConfigView(nil, optsJSON)

	w.Close()
	os.Stdout = oldStdout

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)

	var env ports.JSONEnvelope
	if err := json.Unmarshal(outBuf.Bytes(), &env); err != nil {
		t.Fatalf("expected valid JSON error envelope, got error: %v", err)
	}
	if env.Status != "error" || env.Error == nil || env.Error.Code != "ERR_CONFIG_NOT_FOUND" {
		t.Errorf("expected ERR_CONFIG_NOT_FOUND error envelope, got: %v", env)
	}

	// 3. View non-existent profile (text mode returns error)
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, ports.ConfigFilename)
	_ = os.WriteFile(cfgPath, []byte("version: 1\n"), 0644)

	optsProfile := &configViewOptions{
		dir:     tmpDir,
		profile: "missing-profile",
		output:  "text",
	}
	err = runConfigView(nil, optsProfile)
	if err == nil {
		t.Fatal("expected error when viewing non-existent profile, got nil")
	}
}

// TestConfigValidateCommand_RuntimeField is checklist row 10's validation
// half for the new runtime field, at BOTH call sites: a bad runtime in the
// base config and a bad runtime in a named profile must each fail
// `pokkum config validate`, and valid values (bun/node, top-level and
// per-profile) must pass.
func TestConfigValidateCommand_RuntimeField(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, ports.ConfigFilename)
	opts := &configValidateOptions{dir: tmpDir, output: "json"}

	validCfg := `version: 1
runtime: node
profiles:
  bun-dev:
    runtime: bun
`
	_ = os.WriteFile(cfgPath, []byte(validCfg), 0644)
	if err := runConfigValidate(nil, opts); err != nil {
		t.Fatalf("expected valid runtime values to pass validation, got: %v", err)
	}

	badBaseCfg := `version: 1
runtime: deno
`
	_ = os.WriteFile(cfgPath, []byte(badBaseCfg), 0644)
	if err := runConfigValidate(nil, opts); err == nil {
		t.Fatal("expected invalid top-level runtime to fail validation, got nil")
	}

	badProfileCfg := `version: 1
runtime: bun
profiles:
  prod:
    runtime: nodejs
`
	_ = os.WriteFile(cfgPath, []byte(badProfileCfg), 0644)
	if err := runConfigValidate(nil, opts); err == nil {
		t.Fatal("expected invalid profile runtime to fail validation, got nil")
	}
}
