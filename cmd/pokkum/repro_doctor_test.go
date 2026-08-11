package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestReproDoctorCommand_FastJSON(t *testing.T) {
	opts := &reproDoctorOptions{
		fast:   true,
		dir:    t.TempDir(),
		output: "json",
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runReproDoctor(nil, opts)

	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("unexpected error running repro doctor: %v", err)
	}

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)

	var env ports.JSONEnvelope
	if err := json.Unmarshal(outBuf.Bytes(), &env); err != nil {
		t.Fatalf("repro doctor --output=json emitted invalid JSON: %v", err)
	}

	if env.Command != "repro doctor" {
		t.Errorf("expected command repro doctor, got %s", env.Command)
	}
}
