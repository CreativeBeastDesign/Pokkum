package main

import (
	"context"
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
