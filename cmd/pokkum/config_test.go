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
