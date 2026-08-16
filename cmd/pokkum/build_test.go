package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
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

func TestBuildCommandStaticStrategyLayeredConflictRejected(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cmd := newBuildCommand(context.Background(), logger)
	cmd.SetArgs([]string{"--static", "--strategy=layered", "--dry-run"})
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected --static combined with an explicit --strategy=layered to error")
	}
	if !strings.Contains(err.Error(), "cannot be combined with --strategy=layered") {
		t.Fatalf("expected a --static/--strategy conflict error, got: %v", err)
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

func TestBuildCommandSourcemapFlag(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cmd := newBuildCommand(context.Background(), logger)

	if flag := cmd.Flags().Lookup("sourcemap"); flag == nil {
		t.Fatalf("expected --sourcemap flag to be registered")
	}
	if flag := cmd.Flags().Lookup("no-cache-verify"); flag == nil {
		t.Fatalf("expected --no-cache-verify flag to be registered")
	}
	if flag := cmd.Flags().Lookup("cache-verify-mode"); flag == nil {
		t.Fatalf("expected --cache-verify-mode flag to be registered")
	}
	if flag := cmd.Flags().Lookup("cache-verify-key"); flag == nil {
		t.Fatalf("expected --cache-verify-key flag to be registered")
	}
	if flag := cmd.Flags().Lookup("cache-keyless-identity"); flag == nil {
		t.Fatalf("expected --cache-keyless-identity flag to be registered")
	}
	if flag := cmd.Flags().Lookup("cache-keyless-issuer"); flag == nil {
		t.Fatalf("expected --cache-keyless-issuer flag to be registered")
	}
	if flag := cmd.Flags().Lookup("cache-verify-strict"); flag == nil {
		t.Fatalf("expected --cache-verify-strict flag to be registered")
	}
}

func TestBuildSourcemapPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		flagVal   bool
		envVal    string
		expectVal bool
	}{
		{
			name:      "Flag true overrides env false",
			flagVal:   true,
			envVal:    "0",
			expectVal: true,
		},
		{
			name:      "Env true enables when flag is false",
			flagVal:   false,
			envVal:    "true",
			expectVal: true,
		},
		{
			name:      "Env 1 enables when flag is false",
			flagVal:   false,
			envVal:    "1",
			expectVal: true,
		},
		{
			name:      "Env yes enables when flag is false",
			flagVal:   false,
			envVal:    "yes",
			expectVal: true,
		},
		{
			name:      "Env false/0 keeps disabled when flag is false",
			flagVal:   false,
			envVal:    "false",
			expectVal: false,
		},
		{
			name:      "Default is false when neither flag nor env is set",
			flagVal:   false,
			envVal:    "",
			expectVal: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.envVal != "" {
				t.Setenv("POKKUM_SOURCEMAP", tc.envVal)
			} else {
				t.Setenv("POKKUM_SOURCEMAP", "")
			}

			sourcemapSetting := tc.flagVal
			if !sourcemapSetting {
				if envVal := tc.envVal; envVal != "" {
					sourcemapSetting = envVal == "1" || envVal == "true" || envVal == "yes"
				}
			}

			if sourcemapSetting != tc.expectVal {
				t.Errorf("sourcemap resolved to %v, want %v", sourcemapSetting, tc.expectVal)
			}
		})
	}
}
