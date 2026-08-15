// Package gitutils provides git-based helper utilities, including monorepo
// affected-project detection for `pokkum resolve --since` / `apply --since`.
package gitutils

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// IsUtilityPackage marks this package as a non-adapter utility module.
const IsUtilityPackage = true

// NewAffectedDetector constructs a ports.AffectedDetector anchored at baseDir.
//
// The detector reports whether the project at projectPath has changed since
// sinceRef. It is fail-open only when sinceRef is empty (returns true — treat
// as affected); on any real git error or an unknown ref it returns an error so
// the caller decides whether that resolve fails (it does) rather than silently
// skipping or silently building on uncertainty.
func NewAffectedDetector(baseDir string) ports.AffectedDetector {
	return func(ctx context.Context, projectPath, sinceRef string) (bool, error) {
		if strings.TrimSpace(sinceRef) == "" {
			return true, nil
		}

		targetDir := filepath.Join(baseDir, projectPath)
		if _, err := os.Stat(targetDir); err != nil {
			return false, fmt.Errorf("gitutils: stat target directory %q: %w: %w", targetDir, err, core.ErrInvalidRequest)
		}

		// 1. Verify sinceRef is a valid commit object in the repository.
		if err := verifyGitRef(ctx, targetDir, sinceRef); err != nil {
			return false, err
		}

		// 2. Check tracked diffs against sinceRef, scoped to this project dir.
		diffOut, err := runGit(ctx, targetDir, "diff", "--name-only", sinceRef, "--", ".")
		if err != nil {
			return false, fmt.Errorf("gitutils: diff against %q in %q: %w", sinceRef, targetDir, err)
		}
		if len(strings.TrimSpace(diffOut)) > 0 {
			return true, nil
		}

		// 3. Check for untracked or staged working-tree changes in targetDir so
		// a dirty local tree is never treated as an unaffected, skippable app.
		statusOut, err := runGit(ctx, targetDir, "status", "--porcelain", "--", ".")
		if err != nil {
			return false, fmt.Errorf("gitutils: git status in %q: %w", targetDir, err)
		}
		if len(strings.TrimSpace(statusOut)) > 0 {
			return true, nil
		}

		return false, nil
	}
}

// verifyGitRef confirms ref resolves to a commit in the repository anchored at
// dir. A ref that does not resolve is an operator error, surfaced as
// ErrInvalidRequest so it fails fast rather than silently changing behavior.
func verifyGitRef(ctx context.Context, dir, ref string) error {
	out, err := runGit(ctx, dir, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return fmt.Errorf("gitutils: unknown or invalid git ref %q: %w: %w", ref, err, core.ErrInvalidRequest)
	}
	if strings.TrimSpace(out) == "" {
		return fmt.Errorf("gitutils: invalid git ref %q: %w", ref, core.ErrInvalidRequest)
	}
	return nil
}

// runGit runs git with args in dir, returning stdout. Stderr is folded into
// the error so a failing command carries the reason, not just an exit status.
func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return "", fmt.Errorf("%s (exit %w)", errMsg, err)
		}
		return "", err
	}
	return stdout.String(), nil
}
