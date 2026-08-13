package main

import (
	"context"
	"testing"
)

func TestDiscoverGitMetadata_EnvVars(t *testing.T) {
	t.Setenv("GITHUB_SHA", "1234567890abcdef1234567890abcdef12345678")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GITHUB_REPOSITORY", "acme/pokkum-app")
	t.Setenv("GITHUB_REF_TYPE", "tag")
	t.Setenv("GITHUB_REF_NAME", "v1.2.3")

	ctx := context.Background()
	labels := discoverGitMetadata(ctx, t.TempDir(), nil)

	if got := labels["org.opencontainers.image.revision"]; got != "1234567890abcdef1234567890abcdef12345678" {
		t.Errorf("expected revision from GITHUB_SHA, got %q", got)
	}
	if got := labels["org.opencontainers.image.source"]; got != "https://github.com/acme/pokkum-app" {
		t.Errorf("expected source from GITHUB_SERVER_URL/REPOSITORY, got %q", got)
	}
	if got := labels["org.opencontainers.image.version"]; got != "v1.2.3" {
		t.Errorf("expected version from GITHUB_REF_NAME, got %q", got)
	}
}

func TestDiscoverGitMetadata_ExplicitLabelPrecedence(t *testing.T) {
	t.Setenv("GITHUB_SHA", "env_sha")

	ctx := context.Background()
	initialLabels := map[string]string{
		"org.opencontainers.image.revision": "explicit_sha",
	}

	labels := discoverGitMetadata(ctx, t.TempDir(), initialLabels)

	if got := labels["org.opencontainers.image.revision"]; got != "explicit_sha" {
		t.Errorf("explicit label should take precedence over env var, got %q", got)
	}
}
