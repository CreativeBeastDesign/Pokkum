package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

// Output-destination mutual exclusion.
//
// --local, --tarball and --to-oci-layout each name a different place the
// finished image goes. Two of them at once has no defensible resolution:
// silently preferring one builds something the operator did not ask for, and
// doing both would double a multi-minute build's publish stage behind a flag
// combination nobody deliberately types. The pre-existing --local/--tarball
// check established the pattern and the error wording; --to-oci-layout joins
// it rather than inventing a third convention.
//
// Driven through the real command tree (newBuildCommand + Execute) rather
// than a mirrored copy of the reconciliation block, because this error path
// returns before runBuild reaches any adapter — the same reason
// TestBuildCommandStaticStrategyLayeredConflictRejected gets away with it.
func TestBuildCommand_OutputModesAreMutuallyExclusive(t *testing.T) {
	layoutDir := filepath.Join(t.TempDir(), "oci-out")
	tarPath := filepath.Join(t.TempDir(), "out.tar")

	tests := []struct {
		name            string
		args            []string
		wantErrContains string
	}{
		{
			name:            "local and tarball",
			args:            []string{"--local", "--tarball=" + tarPath},
			wantErrContains: "cannot specify both --local and --tarball",
		},
		{
			name:            "local and to-oci-layout",
			args:            []string{"--local", "--to-oci-layout=" + layoutDir},
			wantErrContains: "cannot specify both --local and --to-oci-layout",
		},
		{
			name:            "tarball and to-oci-layout",
			args:            []string{"--tarball=" + tarPath, "--to-oci-layout=" + layoutDir},
			wantErrContains: "cannot specify both --tarball and --to-oci-layout",
		},
		{
			name:            "all three at once",
			args:            []string{"--local", "--tarball=" + tarPath, "--to-oci-layout=" + layoutDir},
			wantErrContains: "cannot specify both --local and --tarball",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			cmd := newBuildCommand(context.Background(), logger)
			cmd.SetArgs(append(append([]string(nil), tt.args...), "--dry-run"))
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true

			err := cmd.Execute()
			if err == nil {
				t.Fatalf("expected %v to be rejected as conflicting output modes, got nil", tt.args)
			}
			if !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErrContains)
			}
		})
	}
}

// TestBuildCommand_ToOCILayoutFlagIsRegistered guards the flag's own shape:
// it takes a path (mirroring --tarball) rather than being a bare boolean, so
// the destination is explicit and the mode is off unless a path is given.
func TestBuildCommand_ToOCILayoutFlagIsRegistered(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cmd := newBuildCommand(context.Background(), logger)

	f := cmd.Flags().Lookup("to-oci-layout")
	if f == nil {
		t.Fatal("--to-oci-layout is not registered on pokkum build")
	}
	if f.Value.Type() != "string" {
		t.Errorf("--to-oci-layout type = %q, want string (it takes a directory path, like --tarball takes a file path)", f.Value.Type())
	}
	if f.DefValue != "" {
		t.Errorf("--to-oci-layout default = %q, want empty (an unset flag must not select the mode)", f.DefValue)
	}
}
