package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/slsa"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// discoverGitMetadata attempts to auto-populate standard OCI image labels
// (revision, source, version, created) from CI environment variables (e.g.
// GitHub Actions) or local git repository metadata. Keys already specified
// explicitly by the caller are preserved and take precedence.
//
// created is deliberately NOT resolved independently here — it is set to
// buildTimestamp, the exact same SOURCE_DATE_EPOCH-derived value the rest of
// the build uses for layer mtimes, the image config, and history entries
// (resolved once by the caller via config.Loader.ResolveBuildTimestamp,
// before this function runs). A second, independent resolution here would
// risk disagreeing with it — e.g. this function's own git lookup runs
// against dir (the project directory) while ResolveBuildTimestamp's runs
// against the CLI process's own working directory, which are not always the
// same repository. A single resolution point removes that risk entirely.
func discoverGitMetadata(ctx context.Context, dir string, labels map[string]string, buildTimestamp time.Time) map[string]string {
	if labels == nil {
		labels = make(map[string]string)
	}

	// 1. Revision (org.opencontainers.image.revision)
	if !hasLabelKey(labels, ports.LabelRevision, "revision") {
		if rev := getGitRevision(ctx, dir); rev != "" {
			labels[ports.LabelRevision] = rev
		}
	}

	// 2. Source (org.opencontainers.image.source)
	if !hasLabelKey(labels, ports.LabelSource, "source") {
		if src := getGitSource(ctx, dir); src != "" {
			labels[ports.LabelSource] = src
		}
	}

	// 3. Version (org.opencontainers.image.version)
	if !hasLabelKey(labels, ports.LabelVersion, "version") {
		if ver := getGitVersion(ctx, dir); ver != "" {
			labels[ports.LabelVersion] = ver
		}
	}

	// 4. Created Timestamp (org.opencontainers.image.created) — see doc
	// comment above for why this reuses buildTimestamp instead of resolving
	// its own. Not set at all if the build timestamp is unresolved (the Go
	// zero value); Normalize's own Unix-epoch fallback happens later, in
	// core, and this function has no business anticipating it.
	if !hasLabelKey(labels, ports.LabelCreated, "created") && !buildTimestamp.IsZero() {
		labels[ports.LabelCreated] = buildTimestamp.UTC().Format(time.RFC3339)
	}

	return labels
}

func hasLabelKey(labels map[string]string, fullKey, shortKey string) bool {
	if _, ok := labels[fullKey]; ok {
		return true
	}
	if _, ok := labels[shortKey]; ok {
		return true
	}
	return false
}

func getGitRevision(ctx context.Context, dir string) string {
	if sha := os.Getenv("GITHUB_SHA"); sha != "" {
		return sha
	}
	out, err := execGit(ctx, dir, "rev-parse", "HEAD")
	if err == nil {
		return strings.TrimSpace(out)
	}
	return ""
}

func getGitSource(ctx context.Context, dir string) string {
	server := os.Getenv("GITHUB_SERVER_URL")
	repo := os.Getenv("GITHUB_REPOSITORY")
	if server != "" && repo != "" {
		return strings.TrimRight(server, "/") + "/" + strings.TrimLeft(repo, "/")
	}
	out, err := execGit(ctx, dir, "config", "--get", "remote.origin.url")
	if err == nil {
		return strings.TrimSpace(out)
	}
	return ""
}

func getGitVersion(ctx context.Context, dir string) string {
	base := gitVersionBase(ctx, dir)
	if base == "" {
		return ""
	}

	// `git describe --dirty` only considers TRACKED modifications: an untracked
	// source file leaves it reporting a clean version. The SLSA provenance uses
	// slsa.WorkingTreeDirty, which does count untracked files (excluding
	// Pokkum's own .pokkum/ and pokkum.lock), so for an untracked-file build the
	// two disagreed about the same image — `pokkum history` reported a clean
	// revision while the attestation on that very image said "-dirty".
	//
	// Resolve the dirty marker from WorkingTreeDirty on both paths so the label
	// and the attestation can only ever reach one verdict. This is the same
	// single-source-of-truth argument gitdiscovery.go already makes for
	// `repro doctor`; the OCI label was simply a third implementation nobody
	// had pointed at it.
	if !strings.HasSuffix(base, "-dirty") && slsa.WorkingTreeDirty(ctx, dir) {
		return base + "-dirty"
	}
	return base
}

// gitVersionBase resolves the version string before any dirty marker: a CI tag
// ref when one is present, otherwise `git describe`.
func gitVersionBase(ctx context.Context, dir string) string {
	if os.Getenv("GITHUB_REF_TYPE") == "tag" {
		if ref := os.Getenv("GITHUB_REF_NAME"); ref != "" {
			return ref
		}
	}
	out, err := execGit(ctx, dir, "describe", "--tags", "--always", "--dirty")
	if err == nil {
		return strings.TrimSpace(out)
	}
	return ""
}

func execGit(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}
