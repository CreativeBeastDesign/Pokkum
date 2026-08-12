package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRollbackCommand_Success(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "deploy.yaml")
	manifestContent := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
spec:
  template:
    spec:
      containers:
      - name: web
        image: ghcr.io/test/app:v2.0.0
`
	writeFile(t, manifestPath, manifestContent)

	flags := &rollbackFlags{
		file:   manifestPath,
		toRef:  "ghcr.io/test/app:v1.0.0",
		output: "text",
	}

	err := runRollback(context.Background(), discardLogger(), flags)
	if err != nil {
		t.Fatalf("runRollback failed: %v", err)
	}

	updated, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	if !strings.Contains(string(updated), "image: ghcr.io/test/app:v1.0.0") {
		t.Errorf("expected rolled back image ref in manifest, got:\n%s", string(updated))
	}
}
