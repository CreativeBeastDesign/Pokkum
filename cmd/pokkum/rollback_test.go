package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRollbackCommand_ExplicitTo(t *testing.T) {
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

	content := string(updated)
	if !strings.Contains(content, "image: ghcr.io/test/app:v1.0.0") {
		t.Errorf("expected rolled back image ref in manifest, got:\n%s", content)
	}
	if !strings.Contains(content, `pokkum.dev/previous-image: "ghcr.io/test/app:v2.0.0"`) {
		t.Errorf("expected previous image annotation injected, got:\n%s", content)
	}
}

func TestRollbackCommand_AnnotationImplicit(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "deploy.yaml")
	manifestContent := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
  annotations:
    pokkum.dev/previous-image: "ghcr.io/test/app:v1.0.0"
spec:
  template:
    spec:
      containers:
      - name: web
        image: ghcr.io/test/app:v2.0.0
`
	writeFile(t, manifestPath, manifestContent)

	// Step 1: Run rollback without --to flag
	flags := &rollbackFlags{
		file:   manifestPath,
		toRef:  "",
		output: "text",
	}

	err := runRollback(context.Background(), discardLogger(), flags)
	if err != nil {
		t.Fatalf("runRollback failed: %v", err)
	}

	updated1, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	content1 := string(updated1)
	if !strings.Contains(content1, "image: ghcr.io/test/app:v1.0.0") {
		t.Errorf("expected image ref rolled back to v1.0.0, got:\n%s", content1)
	}
	if !strings.Contains(content1, `pokkum.dev/previous-image: "ghcr.io/test/app:v2.0.0"`) {
		t.Errorf("expected previous image annotation updated to v2.0.0, got:\n%s", content1)
	}

	// Step 2: Toggle back (run rollback again without --to)
	err = runRollback(context.Background(), discardLogger(), flags)
	if err != nil {
		t.Fatalf("second runRollback failed: %v", err)
	}

	updated2, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}

	content2 := string(updated2)
	if !strings.Contains(content2, "image: ghcr.io/test/app:v2.0.0") {
		t.Errorf("expected image ref toggled back to v2.0.0, got:\n%s", content2)
	}
	if !strings.Contains(content2, `pokkum.dev/previous-image: "ghcr.io/test/app:v1.0.0"`) {
		t.Errorf("expected previous image annotation updated to v1.0.0, got:\n%s", content2)
	}
}

func TestRollbackCommand_MissingAnnotationError(t *testing.T) {
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
		toRef:  "",
		output: "text",
	}

	err := runRollback(context.Background(), discardLogger(), flags)
	if err == nil {
		t.Fatal("expected error when no --to flag and no annotation present, got nil")
	}
	if !strings.Contains(err.Error(), "no previous image annotation") {
		t.Errorf("expected missing annotation error message, got: %v", err)
	}
}
