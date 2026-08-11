package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestMetricsCommand_JSONOutput(t *testing.T) {
	opts := &metricsOptions{
		port:   8889,
		output: "json",
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runMetrics(nil, opts)

	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("unexpected error running metrics: %v", err)
	}

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)

	var env ports.JSONEnvelope
	if err := json.Unmarshal(outBuf.Bytes(), &env); err != nil {
		t.Fatalf("metrics --output=json emitted invalid JSON: %v", err)
	}

	if env.Command != "metrics" {
		t.Errorf("expected command metrics, got %s", env.Command)
	}
}
