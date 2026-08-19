package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
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

// TestPromptInitOptions_RejectsUnknownChoiceAndReAsks covers the prompt path
// directly, because it cannot be reached through runInit from a test: init only
// prompts when stdin is a TTY, so piping answers to the built binary silently
// skips prompting entirely and every value stays at its default. A "test" that
// piped an invalid answer and observed a valid config would therefore prove
// nothing at all — it would be measuring the default, not the validation.
//
// The bug this guards: the prompts used to accept whatever was typed, verbatim.
// Combined with the prompt offering "chainguard-static" — an unimplemented
// roadmap item, not a preset — anyone picking option 3 got a .pokkum.yaml that
// `pokkum build` refused to start with, a long way from the cause.
func TestPromptInitOptions_RejectsUnknownChoiceAndReAsks(t *testing.T) {
	t.Run("invalid base is re-asked, and the retry is honoured", func(t *testing.T) {
		// repo, invalid base, valid base, strategy, local, cve
		got := promptInitOptions(strings.NewReader("\nchainguard-static\nchainguard\n\n\n\n"))
		// chainguard proves the retry was consumed; distroless would mean the
		// bad answer merely fell through to the default, which is a different
		// (and untested) behaviour.
		if got.BasePreset != "chainguard" {
			t.Errorf("BasePreset = %q, want %q — the re-asked answer must be honoured", got.BasePreset, "chainguard")
		}
	})

	t.Run("no longer offers a preset that does not exist", func(t *testing.T) {
		// Feeding the removed option as the only answer must not set it.
		got := promptInitOptions(strings.NewReader("\nchainguard-static\n\n\n\n\n"))
		if got.BasePreset == "chainguard-static" {
			t.Error("BasePreset = \"chainguard-static\", which is not a real preset — pokkum build refuses it")
		}
	})

	t.Run("every default is a value the config validator accepts", func(t *testing.T) {
		// All answers empty: the defaults this prompt hands to GenerateDefault
		// must themselves be valid, or an operator who just presses Enter five
		// times gets a broken project.
		got := promptInitOptions(strings.NewReader("\n\n\n\n\n"))
		cfg := configManagerForTest(t).GenerateDefault(got)
		if problems := validateGeneratedConfig(cfg); len(problems) > 0 {
			t.Errorf("pressing Enter through every prompt produced an invalid config: %v", problems)
		}
	})

	t.Run("valid answers are accepted as given", func(t *testing.T) {
		got := promptInitOptions(strings.NewReader("ghcr.io/example/app\ndistroless-node\nstatic\nn\nhigh\n"))
		if got.BasePreset != "distroless-node" || got.Strategy != "static" || got.FailOnCVE != "high" {
			t.Errorf("valid answers not honoured: base=%q strategy=%q cve=%q", got.BasePreset, got.Strategy, got.FailOnCVE)
		}
		if got.EnableLocalProfile {
			t.Error("answering n to the local-profile prompt must disable it")
		}
		cfg := configManagerForTest(t).GenerateDefault(got)
		if problems := validateGeneratedConfig(cfg); len(problems) > 0 {
			t.Errorf("a config built from valid prompt answers must validate: %v", problems)
		}
	})
}

func configManagerForTest(t *testing.T) *config.Manager {
	t.Helper()
	m, err := config.New(t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("config.New: %v", err)
	}
	return m
}
