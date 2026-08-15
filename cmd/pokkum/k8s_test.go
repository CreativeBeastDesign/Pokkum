package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// discardLogger returns a logger that writes nowhere, for tests that only
// care about return values.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func TestCollectManifestFiles_DirectoryNonRecursive(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.yaml"), "a: 1")
	writeFile(t, filepath.Join(dir, "b.yml"), "b: 1")
	writeFile(t, filepath.Join(dir, "notes.txt"), "ignore me")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	writeFile(t, filepath.Join(dir, "sub", "c.yaml"), "c: 1")

	files, err := collectManifestFiles(dir, false)
	if err != nil {
		t.Fatalf("collectManifestFiles: %v", err)
	}

	want := []string{filepath.Join(dir, "a.yaml"), filepath.Join(dir, "b.yml")}
	assertStringSliceEqual(t, files, want)
}

func TestCollectManifestFiles_DirectoryRecursive(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.yaml"), "a: 1")
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	writeFile(t, filepath.Join(dir, "sub", "c.yaml"), "c: 1")
	writeFile(t, filepath.Join(dir, "sub", "readme.md"), "ignore me")

	files, err := collectManifestFiles(dir, true)
	if err != nil {
		t.Fatalf("collectManifestFiles: %v", err)
	}

	want := []string{filepath.Join(dir, "a.yaml"), filepath.Join(dir, "sub", "c.yaml")}
	assertStringSliceEqual(t, files, want)
}

func TestCollectManifestFiles_SingleFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "only.yaml")
	writeFile(t, f, "a: 1")

	files, err := collectManifestFiles(f, false)
	if err != nil {
		t.Fatalf("collectManifestFiles: %v", err)
	}
	assertStringSliceEqual(t, files, []string{f})
}

func TestLoadDocuments_MissingPathFails(t *testing.T) {
	_, err := loadDocuments(filepath.Join(t.TempDir(), "does-not-exist.yaml"), false)
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest for missing path, got %v", err)
	}
}

func TestLoadDocuments_EmptyDirectoryFails(t *testing.T) {
	_, err := loadDocuments(t.TempDir(), false)
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest for a directory with no manifests, got %v", err)
	}
}

func TestLoadDocuments_Stdin(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	origStdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = origStdin }()

	const content = "apiVersion: v1\nkind: Pod\n"
	go func() {
		_, _ = w.WriteString(content)
		_ = w.Close()
	}()

	docs, err := loadDocuments("-", false)
	if err != nil {
		t.Fatalf("loadDocuments: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 document from stdin, got %d", len(docs))
	}
	if docs[0].Name != "-" {
		t.Errorf("expected document name %q, got %q", "-", docs[0].Name)
	}
	if string(docs[0].Content) != content {
		t.Errorf("expected content %q, got %q", content, string(docs[0].Content))
	}
}

func TestIsYAMLFile(t *testing.T) {
	cases := map[string]bool{
		"a.yaml":  true,
		"a.yml":   true,
		"A.YAML":  true,
		"a.txt":   false,
		"a.yaml~": false,
		"yaml":    false,
	}
	for name, want := range cases {
		if got := isYAMLFile(name); got != want {
			t.Errorf("isYAMLFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestJoinDocuments(t *testing.T) {
	docs := []documentFixture{
		{name: "a.yaml", content: "a: 1\n"},
		{name: "b.yaml", content: "b: 1\n"},
	}
	got := string(joinDocuments(toPortsDocuments(docs)))
	want := "a: 1\n---\nb: 1\n"
	if got != want {
		t.Errorf("joinDocuments: got %q, want %q", got, want)
	}

	single := toPortsDocuments(docs[:1])
	if got := string(joinDocuments(single)); got != "a: 1\n" {
		t.Errorf("joinDocuments single doc: got %q, want %q", got, "a: 1\n")
	}
}

func TestDeriveRepo(t *testing.T) {
	const repo = "ghcr.io/acme/app"
	cases := []struct {
		path string
		want string
	}{
		{".", repo},
		{"./", repo},
		{"./src/app", repo + "/src-app"},
		{"src/app", repo + "/src-app"},
		{"./Frontend", repo + "/frontend"},
	}
	for _, tc := range cases {
		if got := deriveRepo(repo, tc.path); got != tc.want {
			t.Errorf("deriveRepo(%q, %q) = %q, want %q", repo, tc.path, got, tc.want)
		}
	}
}

func TestManifestBaseDir_File(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "deploy.yaml")
	writeFile(t, f, "a: 1")

	got, err := manifestBaseDir(f)
	if err != nil {
		t.Fatalf("manifestBaseDir: %v", err)
	}
	if got != dir {
		t.Errorf("manifestBaseDir(%q) = %q, want %q", f, got, dir)
	}
}

func TestManifestBaseDir_Directory(t *testing.T) {
	dir := t.TempDir()
	got, err := manifestBaseDir(dir)
	if err != nil {
		t.Fatalf("manifestBaseDir: %v", err)
	}
	if got != dir {
		t.Errorf("manifestBaseDir(%q) = %q, want %q", dir, got, dir)
	}
}

func TestResolveManifests_MissingDockerRepoFailsFast(t *testing.T) {
	t.Setenv("POKKUM_DOCKER_REPO", "")

	// A path that does not exist: if resolveManifests reads it before
	// checking POKKUM_DOCKER_REPO, this test would instead observe a
	// file-not-found error, which is the behaviour being guarded against.
	missing := filepath.Join(t.TempDir(), "does-not-exist.yaml")

	_, err := resolveManifests(context.Background(), discardLogger(), resolveManifestsOptions{
		File:            missing,
		Recursive:       false,
		SecurityContext: true,
	})
	if !errors.Is(err, core.ErrNoDockerRepo) {
		t.Fatalf("expected ErrNoDockerRepo before any file is touched, got %v", err)
	}
}

func TestResolveManifests_WithMockedBuild(t *testing.T) {
	t.Setenv("POKKUM_DOCKER_REPO", "ghcr.io/test/repo")

	manifestDir := t.TempDir()
	manifestPath := filepath.Join(manifestDir, "test.yaml")
	writeFile(t, manifestPath, `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test
spec:
  template:
    spec:
      containers:
      - name: app
        image: pokkum://./src
`)

	mockBuilder := func(ctx context.Context, path string) (string, error) {
		if path != "./src" {
			t.Errorf("unexpected path passed to builder: got %q, want %q", path, "./src")
		}
		return "ghcr.io/test/repo/src@sha256:1111111111111111111111111111111111111111111111111111111111111111", nil
	}

	out, err := resolveManifests(context.Background(), discardLogger(), resolveManifestsOptions{
		File:             manifestPath,
		SecurityContext:  false,
		NetworkPolicy:    false,
		ResourceDefaults: false,
		ImageBuilder:     mockBuilder,
	})
	if err != nil {
		t.Fatalf("resolveManifests failed: %v", err)
	}

	outStr := string(out)
	wantImage := "image: ghcr.io/test/repo/src@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	if !strings.Contains(outStr, wantImage) {
		t.Errorf("expected resolved manifest to contain %q, got:\n%s", wantImage, outStr)
	}
}

// TestResolveManifests_ClusterInspectionWarningIsLogged confirms the visible
// half of the fix: when the resolver reports a ClusterInspectionWarning
// (i.e. ports.ClusterInspector returned a real error, not a graceful
// not-found), resolveManifests logs it at Warn level — visible at the
// default --log-level=INFO — instead of the previous behaviour where the
// only trace of the failure was a Debug line nobody sees by default.
func TestResolveManifests_ClusterInspectionWarningIsLogged(t *testing.T) {
	t.Setenv("POKKUM_DOCKER_REPO", "ghcr.io/test/repo")

	manifestDir := t.TempDir()
	manifestPath := filepath.Join(manifestDir, "test.yaml")
	writeFile(t, manifestPath, `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: web-app
  namespace: prod
spec:
  template:
    spec:
      containers:
      - name: app
        image: pokkum://./src
`)

	mockBuilder := func(_ context.Context, _ string) (string, error) {
		return "ghcr.io/test/repo/src@sha256:1111111111111111111111111111111111111111111111111111111111111111", nil
	}
	failingInspector := func(_ context.Context, kind, name, ns string) (ports.ClusterWorkloadState, error) {
		return ports.ClusterWorkloadState{}, fmt.Errorf("inspect cluster workload %s/%s in %s: Unable to connect to the server: dial tcp: i/o timeout", kind, name, ns)
	}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	out, err := resolveManifests(context.Background(), logger, resolveManifestsOptions{
		File:             manifestPath,
		ImageBuilder:     mockBuilder,
		ClusterInspector: failingInspector,
	})
	if err != nil {
		t.Fatalf("resolveManifests: expected success despite the cluster inspection failure (best-effort), got: %v", err)
	}
	if !strings.Contains(string(out), "sha256:1111111111111111111111111111111111111111111111111111111111111111") {
		t.Fatalf("expected the manifest to still be resolved, got:\n%s", out)
	}

	logged := logBuf.String()
	if !strings.Contains(logged, "level=WARN") {
		t.Errorf("expected a Warn-level log line (visible at the default INFO log level) for the cluster inspection failure, got log output:\n%s", logged)
	}
	if !strings.Contains(logged, "cluster inspection failed") {
		t.Errorf("expected the warning to explain that cluster inspection failed, got log output:\n%s", logged)
	}
	if !strings.Contains(logged, "web-app") || !strings.Contains(logged, "prod") {
		t.Errorf("expected the warning to identify the affected workload, got log output:\n%s", logged)
	}
}

// --- test helpers ---

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertStringSliceEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %q, want %q (full: got=%v want=%v)", i, got[i], want[i], got, want)
		}
	}
}

type documentFixture struct {
	name    string
	content string
}

func toPortsDocuments(fixtures []documentFixture) []ports.Document {
	docs := make([]ports.Document, len(fixtures))
	for i, f := range fixtures {
		docs[i] = ports.Document{Name: f.name, Content: []byte(f.content)}
	}
	return docs
}

// TestResolveManifests_Since_SkipsUnaffectedEndToEnd wires the real git
// detector through resolveManifests: with --since set, only the app whose tree
// actually changed is built; the unchanged app reuses its seeded prior digest
// and is not compiled.
func TestResolveManifests_Since_SkipsUnaffectedEndToEnd(t *testing.T) {
	t.Setenv("POKKUM_DOCKER_REPO", "ghcr.io/test/repo")

	repoDir := t.TempDir()
	gitExec(t, repoDir, "init")
	gitExec(t, repoDir, "config", "user.name", "Pokkum Test")
	gitExec(t, repoDir, "config", "user.email", "test@pokkum.dev")
	gitExec(t, repoDir, "config", "commit.gpgsign", "false")

	webDir := filepath.Join(repoDir, "apps", "web")
	apiDir := filepath.Join(repoDir, "apps", "api")
	if err := os.MkdirAll(webDir, 0o755); err != nil {
		t.Fatalf("mkdir web: %v", err)
	}
	if err := os.MkdirAll(apiDir, 0o755); err != nil {
		t.Fatalf("mkdir api: %v", err)
	}
	writeFile(t, filepath.Join(webDir, "package.json"), `{"name":"web"}`)
	writeFile(t, filepath.Join(apiDir, "package.json"), `{"name":"api"}`)
	gitExec(t, repoDir, "add", ".")
	gitExec(t, repoDir, "commit", "-m", "initial")
	baseSHA := gitExec(t, repoDir, "rev-parse", "HEAD")

	// Change only apps/api in a new commit.
	writeFile(t, filepath.Join(apiDir, "src.ts"), `export const x = 1;`)
	gitExec(t, repoDir, "add", ".")
	gitExec(t, repoDir, "commit", "-m", "touch api")

	// The manifest is relative to the manifest directory (repoDir), which
	// matches the detector's baseDir, so pokkum:// paths resolve correctly.
	manifest := filepath.Join(repoDir, "deploy.yaml")
	writeFile(t, manifest, `apiVersion: apps/v1
kind: Deployment
metadata:
  name: web-app
  annotations:
    pokkum.dev/current-image: ghcr.io/test/repo/apps-web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
spec:
  template:
    metadata:
      annotations:
        pokkum.dev/current-image: ghcr.io/test/repo/apps-web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    spec:
      containers:
      - name: web
        image: pokkum://./apps/web
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api-app
spec:
  template:
    spec:
      containers:
      - name: api
        image: pokkum://./apps/api
`)

	var webBuilds, apiBuilds atomic.Int32
	mockBuilder := func(_ context.Context, path string) (string, error) {
		if path == "./apps/api" {
			apiBuilds.Add(1)
			return "ghcr.io/test/repo/apps-api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", nil
		}
		webBuilds.Add(1)
		return "ghcr.io/test/repo/apps-web@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", nil
	}

	out, err := resolveManifests(context.Background(), discardLogger(), resolveManifestsOptions{
		File:             manifest,
		SecurityContext:  false,
		NetworkPolicy:    false,
		ResourceDefaults: false,
		ImageBuilder:     mockBuilder,
		Since:            baseSHA,
	})
	if err != nil {
		t.Fatalf("resolveManifests failed: %v", err)
	}

	if apiBuilds.Load() != 1 {
		t.Errorf("expected affected app apps/api to be built exactly once, got %d builds", apiBuilds.Load())
	}
	if webBuilds.Load() != 0 {
		t.Errorf("expected unaffected app apps/web to be skipped (reused), got %d builds", webBuilds.Load())
	}

	outStr := string(out)
	if !strings.Contains(outStr, "apps-api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb") {
		t.Errorf("expected apps/api resolved to its built digest, got:\n%s", outStr)
	}
	// apps/web must retain its seeded prior digest (reused, not rebuilt).
	if !strings.Contains(outStr, "apps-web@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") {
		t.Errorf("expected apps/web to reuse its prior seeded digest, got:\n%s", outStr)
	}
}

func gitExec(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\nOutput: %s", args, dir, err, string(out))
	}
	return strings.TrimSpace(string(out))
}
