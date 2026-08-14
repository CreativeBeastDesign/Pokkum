package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestAdoptCommand(t *testing.T) {
	tmpDir := t.TempDir()

	pkg := `{"name": "test-app", "devDependencies": {"@sveltejs/adapter-node": "^2.0.0"}}`
	cfg := `import adapter from '@sveltejs/adapter-node'; export default { kit: { adapter: adapter() } };`

	if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "svelte.config.js"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cmd := newAdoptCommand(context.Background(), logger)

	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{tmpDir, "--dry-run"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("adopt execute failed: %v", err)
	}
}

func TestAdoptCommandJSON(t *testing.T) {
	tmpDir := t.TempDir()

	pkg := `{"name": "test-app", "devDependencies": {"@sveltejs/adapter-node": "^2.0.0"}}`
	if err := os.WriteFile(filepath.Join(tmpDir, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	opts := &adoptCmdOptions{
		dir:    tmpDir,
		dryRun: true,
		output: "json",
	}

	err := runAdopt(logger, opts)
	_ = w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("runAdopt failed: %v", err)
	}

	var output bytes.Buffer
	_, _ = io.Copy(&output, r)

	var env ports.JSONEnvelope
	if err := json.Unmarshal(output.Bytes(), &env); err != nil {
		t.Fatalf("failed to parse JSON envelope: %v, raw: %s", err, output.String())
	}

	if env.Command != "adopt" || env.Status != "success" {
		t.Fatalf("unexpected envelope: %+v", env)
	}

	dataBytes, _ := json.Marshal(env.Data)
	var res sveltekitutils.AdoptResult
	if err := json.Unmarshal(dataBytes, &res); err != nil {
		t.Fatalf("failed to parse AdoptResult: %v", err)
	}

	if !res.DryRun {
		t.Errorf("expected dry run in result")
	}
}
