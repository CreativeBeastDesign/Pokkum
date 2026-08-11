package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestExplainCommand_JSONOutput(t *testing.T) {
	opts := &explainOptions{
		output: "json",
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runExplain(nil, opts, "test-image:latest")

	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("unexpected error running explain: %v", err)
	}

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)

	var env ports.JSONEnvelope
	if err := json.Unmarshal(outBuf.Bytes(), &env); err != nil {
		t.Fatalf("explain --output=json emitted invalid JSON: %v", err)
	}

	if env.Command != "explain" {
		t.Errorf("expected command explain, got %s", env.Command)
	}
}
