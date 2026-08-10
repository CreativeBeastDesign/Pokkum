package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// fakeKubectl writes a tiny shell script that behaves like `kubectl apply -f
// -` for test purposes: it echoes its stdin to stdout and exits with the
// given code. This lets exit-code propagation and stdin plumbing be tested
// without a real kubectl binary or cluster on PATH.
func fakeKubectl(t *testing.T, exitCode int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake kubectl script is a POSIX shell script; skipping on windows")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "kubectl")
	script := "#!/bin/sh\ncat\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}
	return path
}

func TestRunKubectlApply_Success(t *testing.T) {
	kubectl := fakeKubectl(t, 0)

	var stdout, stderr bytes.Buffer
	err := runKubectlApply(context.Background(), kubectl, []byte("apiVersion: v1\nkind: Pod\n"), &stdout, &stderr)
	if err != nil {
		t.Fatalf("runKubectlApply: %v", err)
	}
	if !bytes.Contains(stdout.Bytes(), []byte("kind: Pod")) {
		t.Errorf("expected manifest content echoed to stdout, got %q", stdout.String())
	}
}

func TestRunKubectlApply_PropagatesExitCode(t *testing.T) {
	kubectl := fakeKubectl(t, 7)

	var stdout, stderr bytes.Buffer
	err := runKubectlApply(context.Background(), kubectl, []byte("kind: Pod\n"), &stdout, &stderr)
	if err == nil {
		t.Fatal("expected an error for non-zero kubectl exit code")
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *exec.ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode() != 7 {
		t.Errorf("expected exit code 7, got %d", exitErr.ExitCode())
	}
}

func TestRunApply_KubectlNotOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // a PATH with no kubectl on it
	t.Setenv("POKKUM_DOCKER_REPO", "ghcr.io/acme/app")

	flags := &applyFlags{
		file:            filepath.Join(t.TempDir(), "missing.yaml"),
		securityContext: true,
	}
	err := runApply(context.Background(), discardLogger(), flags)
	if err == nil {
		t.Fatal("expected an error when kubectl is not on PATH")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("kubectl")) {
		t.Errorf("expected error to mention kubectl, got %v", err)
	}
}

func TestRunApply_RequiresFile(t *testing.T) {
	err := runApply(context.Background(), discardLogger(), &applyFlags{})
	if err == nil {
		t.Fatal("expected an error when --file is empty")
	}
}
