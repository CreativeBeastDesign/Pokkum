package slsa

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// gitCommandTimeout bounds every git subprocess this file invokes. Mirrors
// cmd/pokkum/git_metadata.go's execGit: discovering source provenance is
// best-effort and must never hang (or meaningfully slow) a build waiting on
// a slow, wedged, or network-backed git invocation.
const gitCommandTimeout = 2 * time.Second

// discoverGitCommit resolves the current commit SHA for the project at dir,
// and reports whether the working tree had uncommitted changes at that
// moment.
//
// GITHUB_SHA is checked first, mirroring cmd/pokkum/git_metadata.go's
// getGitRevision — so a CI build's SLSA statement agrees with the same
// build's org.opencontainers.image.revision label, both derived from the
// same environment signal. A GitHub Actions checkout is always clean at that
// SHA, so dirty is unconditionally false on that path; the working-tree
// check below only runs when falling back to `git rev-parse HEAD`.
//
// Returns ("", false) on any failure — outside a git checkout, in a
// repository with no commits yet, if the git binary is missing, or if dir
// is empty — never an error: a build must not fail because this
// best-effort source attribution could not be computed. Callers that
// already have an explicit commit (a caller-supplied
// SLSAGeneratorRequest.GitCommit) must not call this at all; it exists
// only to fill in what the caller didn't supply.
func discoverGitCommit(ctx context.Context, dir string) (commit string, dirty bool) {
	if dir == "" {
		return "", false
	}
	if sha := os.Getenv("GITHUB_SHA"); sha != "" {
		return sha, false
	}
	out, err := runGit(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", false
	}
	commit = strings.TrimSpace(out)
	if commit == "" {
		return "", false
	}

	// A bare commit hash asserts "the source tree matched this commit
	// exactly." That is false when the working tree has uncommitted
	// changes, so a rebuild verifying against the recorded commit could
	// never actually reproduce the artifact that was built — the dirty
	// suffix makes that honest rather than silently omitted.
	// `git status --porcelain` prints one line per modified/staged/untracked
	// path and nothing for a clean tree; it needs only the index and
	// working tree, so it works the same in a shallow clone as a full one.
	statusOut, statusErr := runGit(ctx, dir, "status", "--porcelain")
	dirty = statusErr == nil && strings.TrimSpace(statusOut) != ""
	return commit, dirty
}

// discoverGitSource resolves the source repository URL for the project at
// dir. GITHUB_SERVER_URL/GITHUB_REPOSITORY are checked first, for the same
// consistency reason as discoverGitCommit's GITHUB_SHA check, falling back
// to the "origin" remote's URL. Returns "" on any failure, including
// outside a git checkout or when no "origin" remote is configured — never
// an error, for the same reason as discoverGitCommit.
func discoverGitSource(ctx context.Context, dir string) string {
	if dir == "" {
		return ""
	}
	server := os.Getenv("GITHUB_SERVER_URL")
	repo := os.Getenv("GITHUB_REPOSITORY")
	if server != "" && repo != "" {
		return strings.TrimRight(server, "/") + "/" + strings.TrimLeft(repo, "/")
	}
	out, err := runGit(ctx, dir, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// runGit runs `git <args...>` with its working directory set to dir,
// bounded by gitCommandTimeout.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}
