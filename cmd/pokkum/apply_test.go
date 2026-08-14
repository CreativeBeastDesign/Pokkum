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

func TestApplyCommandClusterInspectFlags(t *testing.T) {
	cmd := newApplyCommand(context.Background(), discardLogger())
	cmd.SetArgs([]string{"--cluster-inspect", "--no-cluster-inspect", "-f", "deploy.yaml"})

	if flag := cmd.Flags().Lookup("cluster-inspect"); flag == nil {
		t.Fatalf("expected --cluster-inspect flag to be registered")
	}
	if flag := cmd.Flags().Lookup("no-cluster-inspect"); flag == nil {
		t.Fatalf("expected --no-cluster-inspect flag to be registered")
	}
}

func TestKubectlClusterInspector(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell script test on windows")
	}

	dir := t.TempDir()
	kubectlPath := filepath.Join(dir, "kubectl")
	script := `#!/bin/sh
if [ "$1" = "get" ]; then
  cat << 'EOF'
{
  "metadata": {
    "annotations": {
      "pokkum.dev/current-image": "ghcr.io/acme/app@sha256:1111111111111111111111111111111111111111111111111111111111111111",
      "pokkum.dev/image-history": "ghcr.io/acme/app@sha256:0000000000000000000000000000000000000000000000000000000000000000"
    }
  },
  "spec": {
    "template": {
      "spec": {
        "containers": [
          {
            "name": "web",
            "image": "ghcr.io/acme/app@sha256:1111111111111111111111111111111111111111111111111111111111111111"
          }
        ]
      }
    }
  }
}
EOF
  exit 0
fi
exit 1
`
	if err := os.WriteFile(kubectlPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}

	inspector := newKubectlClusterInspector(discardLogger(), kubectlPath)
	state, err := inspector(context.Background(), "Deployment", "web-app", "prod")
	if err != nil {
		t.Fatalf("unexpected inspector error: %v", err)
	}

	if state.Annotations["pokkum.dev/current-image"] != "ghcr.io/acme/app@sha256:1111111111111111111111111111111111111111111111111111111111111111" {
		t.Errorf("unexpected current-image annotation: %v", state.Annotations["pokkum.dev/current-image"])
	}
	if state.Containers["web"] != "ghcr.io/acme/app@sha256:1111111111111111111111111111111111111111111111111111111111111111" {
		t.Errorf("unexpected container image: %v", state.Containers["web"])
	}
}

func TestKubectlClusterInspector_NotFoundGraceful(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping shell script test on windows")
	}

	dir := t.TempDir()
	kubectlPath := filepath.Join(dir, "kubectl")
	script := `#!/bin/sh
echo "Error from server (NotFound): deployments.apps \"missing\" not found" >&2
exit 1
`
	if err := os.WriteFile(kubectlPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake kubectl: %v", err)
	}

	inspector := newKubectlClusterInspector(discardLogger(), kubectlPath)
	state, err := inspector(context.Background(), "Deployment", "missing", "default")
	if err != nil {
		t.Fatalf("expected no error on not found, got %v", err)
	}
	if len(state.Annotations) != 0 || len(state.Containers) != 0 {
		t.Errorf("expected empty state on not found, got: %v", state)
	}
}
