package main

import (
	"bytes"
	"encoding/json"
	"io"

	"os"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestDoctorCommand_JSONOutput(t *testing.T) {
	tmpDir := t.TempDir()

	// Write mock package.json
	pkgJSON := `{"dependencies": {"@sveltejs/kit": "2.31.0"}}`
	if err := os.WriteFile(tmpDir+"/package.json", []byte(pkgJSON), 0644); err != nil {
		t.Fatalf("failed to write mock package.json: %v", err)
	}

	opts := &doctorOptions{
		dir:    tmpDir,
		fix:    true,
		output: "json",
	}

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	_ = runDoctor(nil, opts)

	w.Close()
	os.Stdout = oldStdout

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)

	var env ports.JSONEnvelope
	if err := json.Unmarshal(outBuf.Bytes(), &env); err != nil {
		t.Fatalf("doctor --output=json emitted invalid JSON: %v, raw: %s", err, outBuf.String())
	}

	if env.Command != "doctor" {
		t.Errorf("expected command doctor, got %s", env.Command)
	}
}
