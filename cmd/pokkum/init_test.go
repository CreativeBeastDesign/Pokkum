package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestInitCommand_CreatesIgnore(t *testing.T) {
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
}
