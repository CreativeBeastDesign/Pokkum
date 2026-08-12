package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestScanCommand_JSONOutput(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"name": "test-app", "dependencies": {"@sveltejs/kit": "2.15.0"}}`)

	flags := &scanFlags{
		failOn:    "critical",
		toolchain: true,
		output:    "json",
		offline:   true,
	}

	err := runScan(context.Background(), discardLogger(), flags, []string{dir})
	if err != nil {
		t.Fatalf("runScan failed: %v", err)
	}
}

func TestScanCommand_FailsOnExceededThreshold(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package.json"), `{"name": "test-app", "dependencies": {"@sveltejs/kit": "2.15.0"}}`)

	flags := &scanFlags{
		failOn:    "low",
		toolchain: true,
		output:    "text",
		offline:   true,
	}

	origStdout := os.Stdout
	os.Stdout, _ = os.Open(os.DevNull)
	defer func() { os.Stdout = origStdout }()

	err := runScan(context.Background(), discardLogger(), flags, []string{dir})
	if err == nil {
		t.Fatalf("expected runScan to fail when advisories exceed low severity threshold")
	}
}
