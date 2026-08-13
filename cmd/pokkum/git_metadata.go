package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// discoverGitMetadata attempts to auto-populate standard OCI image labels
// (revision, source, version) from CI environment variables (e.g. GitHub Actions)
// or local git repository metadata. Keys already specified explicitly by the caller
// are preserved and take precedence.
func discoverGitMetadata(ctx context.Context, dir string, labels map[string]string) map[string]string {
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
