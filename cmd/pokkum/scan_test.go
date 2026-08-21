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
	// 2.2.0 is genuinely older than the embedded @sveltejs/kit advisory's
	// FixedVersion (2.3.0), so the advisory this test needs comes from the real
	// resolve-and-compare path.
	//
	// This fixture previously declared 2.15.0 — numerically NEWER than 2.3.0,
	// and therefore not vulnerable at all. The scan failed anyway, because the
	// scanner used to fabricate an advisory whenever a real check produced
	// none. So the test asserted the right outcome for entirely the wrong
	// reason, and would have kept passing if the threshold logic broke.
	writeFile(t, filepath.Join(dir, "package.json"), `{"name": "test-app", "dependencies": {"@sveltejs/kit": "2.2.0"}}`)

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
