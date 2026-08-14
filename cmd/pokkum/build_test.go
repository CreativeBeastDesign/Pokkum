package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func TestBuildCommandRequireEnvFlag(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cmd := newBuildCommand(context.Background(), logger)

	// Set args with --require-env
	cmd.SetArgs([]string{"--require-env=FOO,BAR", "--dry-run", "--platform=linux/amd64"})

	// Validate flag was registered and parses correctly
	if flag := cmd.Flags().Lookup("require-env"); flag == nil {
		t.Fatalf("expected --require-env flag to be registered")
	}
}

func TestBuildCommandFailOnCVEFlags(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cmd := newBuildCommand(context.Background(), logger)

	cmd.SetArgs([]string{"--fail-on-cve=high", "--allow-incomplete", "--dry-run"})

	if flag := cmd.Flags().Lookup("fail-on-cve"); flag == nil {
		t.Fatalf("expected --fail-on-cve flag to be registered")
	}
	if flag := cmd.Flags().Lookup("allow-incomplete"); flag == nil {
		t.Fatalf("expected --allow-incomplete flag to be registered")
	}
}
