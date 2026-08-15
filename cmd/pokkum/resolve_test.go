package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

func TestRunResolve_MissingDockerRepoFailsFast(t *testing.T) {
	t.Setenv("POKKUM_DOCKER_REPO", "")

	flags := &resolveFlags{
		file:            filepath.Join(t.TempDir(), "does-not-exist.yaml"),
		securityContext: true,
	}
	err := runResolve(context.Background(), discardLogger(), flags)
	if !errors.Is(err, core.ErrNoDockerRepo) {
		t.Fatalf("expected ErrNoDockerRepo, got %v", err)
	}
}

func TestRunResolve_RequiresFile(t *testing.T) {
	err := runResolve(context.Background(), discardLogger(), &resolveFlags{})
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Fatalf("expected ErrInvalidRequest when --file is empty, got %v", err)
	}
}

func TestSecurityContextEnabled(t *testing.T) {
	cases := []struct {
		securityContext   bool
		noSecurityContext bool
		want              bool
	}{
		{securityContext: true, noSecurityContext: false, want: true},   // default
		{securityContext: true, noSecurityContext: true, want: false},   // --no-security-context wins
		{securityContext: false, noSecurityContext: false, want: false}, // --security-context=false
		{securityContext: false, noSecurityContext: true, want: false},
	}
	for _, tc := range cases {
		if got := securityContextEnabled(tc.securityContext, tc.noSecurityContext); got != tc.want {
			t.Errorf("securityContextEnabled(%v, %v) = %v, want %v", tc.securityContext, tc.noSecurityContext, got, tc.want)
		}
	}
}

func TestResolveCommandSinceFlag(t *testing.T) {
	cmd := newResolveCommand(context.Background(), discardLogger())
	if flag := cmd.Flags().Lookup("since"); flag == nil {
		t.Fatalf("expected --since flag to be registered")
	}
}
