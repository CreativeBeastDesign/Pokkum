package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/config"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestInitCommand_CreatesIgnoreAndConfig(t *testing.T) {
	tmpDir := t.TempDir()

	opts := &initOptions{
		dir:    tmpDir,
		output: "json",
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runInit(nil, opts)

	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("unexpected error running init: %v", err)
	}

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)

	var env ports.JSONEnvelope
	if err := json.Unmarshal(outBuf.Bytes(), &env); err != nil {
		t.Fatalf("init --output=json emitted invalid JSON: %v", err)
	}

	ignorePath := filepath.Join(tmpDir, ".pokkumignore")
	if _, err := os.Stat(ignorePath); os.IsNotExist(err) {
		t.Errorf("expected .pokkumignore to be created at %s", ignorePath)
	}

	configPath := filepath.Join(tmpDir, ports.ConfigFilename)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Errorf("expected %s to be created at %s", ports.ConfigFilename, configPath)
	}
}

func TestInitCommand_InteractivePrompts(t *testing.T) {
	tmpDir := t.TempDir()

	// Simulate user input for all 5 prompts
	input := "ghcr.io/acme/my-app\nchainguard\nstatic\ny\nhigh\n"
	opts := &initOptions{
		dir:      tmpDir,
		defaults: false,
		inReader: strings.NewReader(input),
	}

	err := runInit(nil, opts)
	if err != nil {
		t.Fatalf("unexpected error running interactive init: %v", err)
	}

	cfgMgr, err := config.New(tmpDir, nil)
	if err != nil {
		t.Fatalf("config.New failed: %v", err)
	}

	loaded, err := cfgMgr.Load(tmpDir)
	if err != nil {
		t.Fatalf("cfgMgr.Load failed: %v", err)
	}

	if loaded.Docker.Repo != "ghcr.io/acme/my-app" {
		t.Errorf("expected repo ghcr.io/acme/my-app, got %q", loaded.Docker.Repo)
	}
	if loaded.Base != "chainguard" {
		t.Errorf("expected base chainguard, got %q", loaded.Base)
	}
	if loaded.Strategy != "static" {
		t.Errorf("expected strategy static, got %q", loaded.Strategy)
	}
	if loaded.Security.FailOnCVE != "high" {
		t.Errorf("expected FailOnCVE high, got %q", loaded.Security.FailOnCVE)
	}
	if _, ok := loaded.Profiles["local"]; !ok {
		t.Errorf("expected 'local' profile to be configured")
	}
}

func TestInitCommand_PreservesExistingFiles(t *testing.T) {
	tmpDir := t.TempDir()

	ignorePath := filepath.Join(tmpDir, ".pokkumignore")
	_ = os.WriteFile(ignorePath, []byte("# custom ignore\nsecret\n"), 0644)

	configPath := filepath.Join(tmpDir, ports.ConfigFilename)
	_ = os.WriteFile(configPath, []byte("version: 1\nstrategy: static\n"), 0644)

	opts := &initOptions{
		dir:      tmpDir,
		defaults: true,
	}

	err := runInit(nil, opts)
	if err != nil {
		t.Fatalf("unexpected error running init: %v", err)
	}

	ignoreContent, _ := os.ReadFile(ignorePath)
	if !strings.Contains(string(ignoreContent), "secret") {
		t.Errorf("expected existing .pokkumignore to be preserved intact")
	}

	configContent, _ := os.ReadFile(configPath)
	if !strings.Contains(string(configContent), "strategy: static") {
		t.Errorf("expected existing %s to be preserved intact", ports.ConfigFilename)
	}
}
