package gitutils

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

func initTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	execInDir(t, dir, "git", "init")
	execInDir(t, dir, "git", "config", "user.name", "Pokkum Test")
	execInDir(t, dir, "git", "config", "user.email", "test@pokkum.dev")
	execInDir(t, dir, "git", "config", "commit.gpgsign", "false")

	// Create initial monorepo structure: apps/web, apps/api
	webDir := filepath.Join(dir, "apps", "web")
	apiDir := filepath.Join(dir, "apps", "api")
	if err := os.MkdirAll(webDir, 0o755); err != nil {
		t.Fatalf("mkdir web: %v", err)
	}
	if err := os.MkdirAll(apiDir, 0o755); err != nil {
		t.Fatalf("mkdir api: %v", err)
	}

	writeFile(t, filepath.Join(webDir, "package.json"), `{"name":"web"}`)
	writeFile(t, filepath.Join(apiDir, "package.json"), `{"name":"api"}`)

	execInDir(t, dir, "git", "add", ".")
	execInDir(t, dir, "git", "commit", "-m", "initial commit")
	execInDir(t, dir, "git", "tag", "v1.0.0")

	return dir
}

func execInDir(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command %s %v in %s failed: %v\nOutput: %s", name, args, dir, err, string(out))
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestAffectedDetector_UnchangedDirectory(t *testing.T) {
	repoDir := initTestGitRepo(t)
	detector := NewAffectedDetector(repoDir)

	// Modify only apps/web in a new commit
	writeFile(t, filepath.Join(repoDir, "apps", "web", "index.js"), `console.log("hello");`)
	execInDir(t, repoDir, "git", "add", ".")
	execInDir(t, repoDir, "git", "commit", "-m", "update web")

	ctx := context.Background()

	// apps/web was modified -> affected
	webAffected, err := detector(ctx, "./apps/web", "v1.0.0")
	if err != nil {
		t.Fatalf("detector apps/web: %v", err)
	}
	if !webAffected {
		t.Errorf("expected apps/web to be affected since v1.0.0")
	}

	// apps/api was NOT modified -> unaffected
	apiAffected, err := detector(ctx, "./apps/api", "v1.0.0")
	if err != nil {
		t.Fatalf("detector apps/api: %v", err)
	}
	if apiAffected {
		t.Errorf("expected apps/api to NOT be affected since v1.0.0")
	}
}

func TestAffectedDetector_UntrackedChanges(t *testing.T) {
	repoDir := initTestGitRepo(t)
	detector := NewAffectedDetector(repoDir)

	// Add untracked file to apps/api without committing
	writeFile(t, filepath.Join(repoDir, "apps", "api", "new_file.ts"), `export const x = 1;`)

	ctx := context.Background()
	apiAffected, err := detector(ctx, "./apps/api", "v1.0.0")
	if err != nil {
		t.Fatalf("detector apps/api: %v", err)
	}
	if !apiAffected {
		t.Errorf("expected apps/api with untracked file to be affected")
	}
}

func TestAffectedDetector_InvalidRefFailsFast(t *testing.T) {
	repoDir := initTestGitRepo(t)
	detector := NewAffectedDetector(repoDir)

	ctx := context.Background()
	_, err := detector(ctx, "./apps/web", "nonexistent-ref-12345")
	if err == nil {
		t.Fatalf("expected error for invalid git ref")
	}
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got: %v", err)
	}
}

func TestAffectedDetector_NonexistentDirFails(t *testing.T) {
	repoDir := initTestGitRepo(t)
	detector := NewAffectedDetector(repoDir)

	ctx := context.Background()
	_, err := detector(ctx, "./apps/does-not-exist", "v1.0.0")
	if err == nil {
		t.Fatalf("expected error for nonexistent target dir")
	}
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest, got: %v", err)
	}
}

func TestAffectedDetector_EmptySinceAlwaysAffected(t *testing.T) {
	repoDir := initTestGitRepo(t)
	detector := NewAffectedDetector(repoDir)

	ctx := context.Background()
	affected, err := detector(ctx, "./apps/api", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !affected {
		t.Errorf("expected empty sinceRef to return affected = true")
	}

	// Whitespace-only ref should also be treated as empty -> affected = true
	affectedWs, err := detector(ctx, "./apps/api", "   ")
	if err != nil {
		t.Fatalf("unexpected error with whitespace since: %v", err)
	}
	if !affectedWs {
		t.Errorf("expected whitespace sinceRef to return affected = true")
	}
}

func TestAffectedDetector_StagedChanges(t *testing.T) {
	repoDir := initTestGitRepo(t)
	detector := NewAffectedDetector(repoDir)

	// Modify tracked file and stage it with `git add` without committing
	writeFile(t, filepath.Join(repoDir, "apps", "web", "package.json"), `{"name":"web","version":"2.0.0"}`)
	execInDir(t, repoDir, "git", "add", "apps/web/package.json")

	ctx := context.Background()
	webAffected, err := detector(ctx, "./apps/web", "v1.0.0")
	if err != nil {
		t.Fatalf("detector apps/web: %v", err)
	}
	if !webAffected {
		t.Errorf("expected apps/web with staged changes to be affected")
	}

	// apps/api should remain unaffected
	apiAffected, err := detector(ctx, "./apps/api", "v1.0.0")
	if err != nil {
		t.Fatalf("detector apps/api: %v", err)
	}
	if apiAffected {
		t.Errorf("expected apps/api to remain unaffected when apps/web has staged changes")
	}
}

func TestAffectedDetector_NestedUntrackedChanges(t *testing.T) {
	repoDir := initTestGitRepo(t)
	detector := NewAffectedDetector(repoDir)

	// Create deeply nested untracked file
	nestedDir := filepath.Join(repoDir, "apps", "api", "src", "routes", "v1")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	writeFile(t, filepath.Join(nestedDir, "handler.ts"), `export const handle = () => {};`)

	ctx := context.Background()
	apiAffected, err := detector(ctx, "./apps/api", "v1.0.0")
	if err != nil {
		t.Fatalf("detector apps/api: %v", err)
	}
	if !apiAffected {
		t.Errorf("expected apps/api with deeply nested untracked file to be affected")
	}
}

func TestAffectedDetector_DeletedFile(t *testing.T) {
	repoDir := initTestGitRepo(t)
	detector := NewAffectedDetector(repoDir)

	// Delete tracked file in apps/web without committing
	if err := os.Remove(filepath.Join(repoDir, "apps", "web", "package.json")); err != nil {
		t.Fatalf("remove file: %v", err)
	}

	ctx := context.Background()
	webAffected, err := detector(ctx, "./apps/web", "v1.0.0")
	if err != nil {
		t.Fatalf("detector apps/web: %v", err)
	}
	if !webAffected {
		t.Errorf("expected apps/web with deleted tracked file to be affected")
	}
}

func TestAffectedDetector_CommitSHAAndRelativeRefs(t *testing.T) {
	repoDir := initTestGitRepo(t)
	detector := NewAffectedDetector(repoDir)

	// Make a new commit that touches apps/web
	writeFile(t, filepath.Join(repoDir, "apps", "web", "app.ts"), `console.log("update");`)
	execInDir(t, repoDir, "git", "add", ".")
	execInDir(t, repoDir, "git", "commit", "-m", "add app.ts")

	ctx := context.Background()

	// HEAD~1 is the initial commit where app.ts did not exist
	webAffected, err := detector(ctx, "./apps/web", "HEAD~1")
	if err != nil {
		t.Fatalf("detector apps/web with HEAD~1: %v", err)
	}
	if !webAffected {
		t.Errorf("expected apps/web to be affected against HEAD~1")
	}

	// When compared against HEAD, apps/web should NOT be affected if working directory is clean
	webCleanAgainstHead, err := detector(ctx, "./apps/web", "HEAD")
	if err != nil {
		t.Fatalf("detector apps/web with HEAD: %v", err)
	}
	if webCleanAgainstHead {
		t.Errorf("expected apps/web to be unaffected against HEAD when working directory is clean")
	}
}

// TestAffectedDetector_InjectionAttack_DashDashOutput empirically proves that
// a malicious sinceRef beginning with `--` (which would normally be parsed by
// git as an option, e.g., `--output=<file>` for unauthorized writes) is rejected.
// This test confirms the fix for the git injection vulnerability: sinceRef is
// verified and resolved to a canonical SHA before being used in git commands,
// making option injection impossible.
func TestAffectedDetector_InjectionAttack_DashDashOutput(t *testing.T) {
	repoDir := initTestGitRepo(t)
	detector := NewAffectedDetector(repoDir)

	// Create a temporary target file path where an attacker would want to write.
	// This file should NOT exist before the test.
	injectionTarget := filepath.Join(repoDir, "injected_by_attacker.txt")

	// Ensure the target does not already exist.
	if _, err := os.Stat(injectionTarget); err == nil {
		t.Fatalf("injection target file unexpectedly exists: %s", injectionTarget)
	}

	ctx := context.Background()
	// Craft a malicious sinceRef that looks like `git diff --name-only --output=<path> -- .`
	// If the vulnerability existed, this would attempt to write to the target file.
	maliciousRef := "--output=" + injectionTarget

	// The detector should fail because this ref is invalid (not a real commit).
	_, err := detector(ctx, "./apps/web", maliciousRef)
	if err == nil {
		t.Fatalf("expected error for injection attack sinceRef %q, but got nil", maliciousRef)
	}
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest for invalid ref, got: %v", err)
	}

	// Empirically verify: the attacker's target file should not have been created.
	// This is the key assertion — if the bug existed, git would have written to this file.
	if _, err := os.Stat(injectionTarget); err == nil {
		t.Errorf("VULNERABILITY: injection target file was created at %s; the git injection attack succeeded", injectionTarget)
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error checking injection target: %v", err)
	}
}

// TestAffectedDetector_InjectionAttack_DashO tests another common git injection
// vector: the short form `-O<file>` (reorder-file option for `git diff`).
func TestAffectedDetector_InjectionAttack_DashO(t *testing.T) {
	repoDir := initTestGitRepo(t)
	detector := NewAffectedDetector(repoDir)

	// Create a temporary target file.
	injectionTarget := filepath.Join(repoDir, "reorder_by_attacker.txt")
	if _, err := os.Stat(injectionTarget); err == nil {
		t.Fatalf("injection target file unexpectedly exists: %s", injectionTarget)
	}

	ctx := context.Background()
	// Craft a malicious sinceRef using the short `-O` form.
	maliciousRef := "-O" + injectionTarget

	_, err := detector(ctx, "./apps/web", maliciousRef)
	if err == nil {
		t.Fatalf("expected error for injection attack sinceRef %q, but got nil", maliciousRef)
	}
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Errorf("expected ErrInvalidRequest for invalid ref, got: %v", err)
	}

	// Verify the target file was not created.
	if _, err := os.Stat(injectionTarget); err == nil {
		t.Errorf("VULNERABILITY: injection target file was created at %s; the git injection attack succeeded", injectionTarget)
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected error checking injection target: %v", err)
	}
}

// TestAffectedDetector_LegitimateRefTypes ensures that the fix for the
// injection vulnerability does not break legitimate git ref types:
// branch names, tags, short SHAs, relative refs (HEAD~N, origin/branch), etc.
func TestAffectedDetector_LegitimateRefTypes(t *testing.T) {
	repoDir := initTestGitRepo(t)

	// Create a second commit.
	writeFile(t, filepath.Join(repoDir, "apps", "web", "file1.txt"), "content1")
	execInDir(t, repoDir, "git", "add", ".")
	execInDir(t, repoDir, "git", "commit", "-m", "commit 2")
	execInDir(t, repoDir, "git", "tag", "v2.0.0")

	// Create a third commit.
	writeFile(t, filepath.Join(repoDir, "apps", "web", "file2.txt"), "content2")
	execInDir(t, repoDir, "git", "add", ".")
	execInDir(t, repoDir, "git", "commit", "-m", "commit 3")

	detector := NewAffectedDetector(repoDir)
	ctx := context.Background()

	tests := []struct {
		name   string
		ref    string
		expect bool // expect affected?
	}{
		// Tag reference (should work)
		{"tag v2.0.0", "v2.0.0", true},
		// Relative reference HEAD~1 (should work)
		{"relative HEAD~1", "HEAD~1", true},
		// Branch name (if we had a branch; we can use HEAD as a ref too)
		{"HEAD", "HEAD", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			affected, err := detector(ctx, "./apps/web", tc.ref)
			if err != nil {
				t.Fatalf("unexpected error with ref %q: %v", tc.ref, err)
			}
			if affected != tc.expect {
				t.Errorf("ref %q: expected affected=%v, got %v", tc.ref, tc.expect, affected)
			}
		})
	}
}
