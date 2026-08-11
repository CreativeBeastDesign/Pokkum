package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestVerifyCommand_NoRebuildJSON(t *testing.T) {
	ctx := context.Background()
	opts := &verifyOptions{
		noRebuild: true,
		output:    "json",
	}

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runVerify(ctx, nil, opts, "ghcr.io/example/my-app:latest")

	w.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("unexpected error running verify --no-rebuild: %v", err)
	}

	var outBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, r)

	var env ports.JSONEnvelope
	if err := json.Unmarshal(outBuf.Bytes(), &env); err != nil {
		t.Fatalf("verify --output=json emitted invalid JSON: %v", err)
	}

	if env.Command != "verify" {
		t.Errorf("expected command verify, got %s", env.Command)
	}
}
