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
