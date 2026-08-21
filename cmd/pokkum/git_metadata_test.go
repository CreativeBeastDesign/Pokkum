package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDiscoverGitMetadata_EnvVars(t *testing.T) {
	t.Setenv("GITHUB_SHA", "1234567890abcdef1234567890abcdef12345678")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REPOSITORY", "acme/pokkum-app")
	t.Setenv("GITHUB_REF_TYPE", "tag")
	t.Setenv("GITHUB_REF_NAME", "v1.2.3")

	ctx := context.Background()
	buildTime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	labels := discoverGitMetadata(ctx, t.TempDir(), nil, buildTime)

	if got := labels["org.opencontainers.image.revision"]; got != "1234567890abcdef1234567890abcdef12345678" {
		t.Errorf("expected revision from GITHUB_SHA, got %q", got)
	}
	if got := labels["org.opencontainers.image.source"]; got != "https://github.com/acme/pokkum-app" {
		t.Errorf("expected source from GITHUB_SERVER_URL/REPOSITORY, got %q", got)
	}
	if got := labels["org.opencontainers.image.version"]; got != "v1.2.3" {
		t.Errorf("expected version from GITHUB_REF_NAME, got %q", got)
	}
	if got, want := labels["org.opencontainers.image.created"], "2026-01-02T03:04:05Z"; got != want {
		t.Errorf("expected created to equal the resolved build timestamp %q, got %q", want, got)
	}
}

func TestDiscoverGitMetadata_ExplicitLabelPrecedence(t *testing.T) {
	t.Setenv("GITHUB_SHA", "env_sha")

	ctx := context.Background()
	initialLabels := map[string]string{
		"org.opencontainers.image.revision": "explicit_sha",
		"org.opencontainers.image.created":  "2020-01-01T00:00:00Z",
	}

	labels := discoverGitMetadata(ctx, t.TempDir(), initialLabels, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	if got := labels["org.opencontainers.image.revision"]; got != "explicit_sha" {
		t.Errorf("explicit label should take precedence over env var, got %q", got)
	}
	if got := labels["org.opencontainers.image.created"]; got != "2020-01-01T00:00:00Z" {
		t.Errorf("explicit created label should take precedence over the resolved build timestamp, got %q", got)
	}
}

// TestDiscoverGitMetadata_ZeroTimestampLeavesCreatedUnset guards the
// documented behavior: an unresolved (zero-value) build timestamp must not
// produce a fabricated "created" label — core.Normalize's own Unix-epoch
// fallback is a separate, later concern this function must not anticipate.
func TestDiscoverGitMetadata_ZeroTimestampLeavesCreatedUnset(t *testing.T) {
	ctx := context.Background()
	labels := discoverGitMetadata(ctx, t.TempDir(), nil, time.Time{})

	if got, ok := labels["org.opencontainers.image.created"]; ok {
		t.Errorf("expected no created label for a zero build timestamp, got %q", got)
	}
}

// initGitRepo creates a real git repository with one commit and returns its
// path. A real repo, not a stub: the behaviour under test is precisely the
// difference between two real git commands' opinions of the same tree, so a
// fake that returned canned strings could not exercise it at all.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v unavailable in this environment: %v (%s)", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-qm", "initial")
	return dir
}

// TestGetGitVersion_UntrackedSourceMarksDirty is the regression guard for the
// OCI label and the SLSA attestation disagreeing about the same build.
//
// `git describe --dirty` only considers TRACKED modifications, so an untracked
// source file left the version label reading clean while the provenance on
// that very image reported "-dirty" (provenance uses slsa.WorkingTreeDirty,
// which counts untracked files). `pokkum history` therefore reported a clean
// revision for a demonstrably dirty build.
//
// Both directions are asserted: a clean tree must NOT be marked, or the check
// would pass by always saying "dirty".
func TestGetGitVersion_UntrackedSourceMarksDirty(t *testing.T) {
	// GITHUB_REF_TYPE would short-circuit version resolution to the CI tag ref.
	t.Setenv("GITHUB_REF_TYPE", "")
	t.Setenv("GITHUB_REF_NAME", "")

	dir := initGitRepo(t)
	ctx := context.Background()

	clean := getGitVersion(ctx, dir)
	if clean == "" {
		t.Fatal("expected a version for a repo with one commit")
	}
	if strings.HasSuffix(clean, "-dirty") {
		t.Fatalf("clean tree reported a dirty version %q; the marker would be meaningless", clean)
	}

	// An untracked SOURCE file: invisible to `git describe --dirty`, counted by
	// slsa.WorkingTreeDirty, and the exact case that shipped the disagreement.
	if err := os.WriteFile(filepath.Join(dir, "untracked.ts"), []byte("export const x = 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := getGitVersion(ctx, dir); !strings.HasSuffix(got, "-dirty") {
		t.Errorf("untracked source file did not mark the version label dirty: got %q\n"+
			"The SLSA provenance for this same build reports -dirty, so the image's own\n"+
			"label and its attestation would disagree about whether it is reproducible.", got)
	}
}

// TestGetGitVersion_PokkumArtifactsDoNotMarkDirty pins the other half of the
// contract, matching F8: Pokkum's own generated artifacts must not make a
// build look dirty, or every build in a project that has not gitignored them
// would be labelled unreproducible.
func TestGetGitVersion_PokkumArtifactsDoNotMarkDirty(t *testing.T) {
	t.Setenv("GITHUB_REF_TYPE", "")
	t.Setenv("GITHUB_REF_NAME", "")

	dir := initGitRepo(t)
	if err := os.MkdirAll(filepath.Join(dir, ".pokkum"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".pokkum", "stage.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pokkum.lock"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := getGitVersion(context.Background(), dir); strings.HasSuffix(got, "-dirty") {
		t.Errorf("untracked .pokkum/ and pokkum.lock marked the build dirty: got %q", got)
	}
}
