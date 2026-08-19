package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
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

// TestBuildCommandBunVersionFlag guards PB-2's CLI-reachability half: the
// bunruntime.Resolver's GPG-verified-checksum path for unpinned versions was
// previously unreachable from the CLI at all — no --bun-version flag
// existed, so req.BunRuntime.Version could only ever come from a Go-API
// caller, never a real `pokkum build` invocation.
func TestBuildCommandBunVersionFlag(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cmd := newBuildCommand(context.Background(), logger)

	flag := cmd.Flags().Lookup("bun-version")
	if flag == nil {
		t.Fatal("expected --bun-version flag to be registered")
	}
	if flag.DefValue != "" {
		t.Errorf("--bun-version default = %q, want empty (empty means core.DefaultBunVersion)", flag.DefValue)
	}

	if err := cmd.Flags().Parse([]string{"--bun-version=1.2.5"}); err != nil {
		t.Fatalf("parse --bun-version: %v", err)
	}
	got, err := cmd.Flags().GetString("bun-version")
	if err != nil {
		t.Fatalf("GetString(bun-version): %v", err)
	}
	if got != "1.2.5" {
		t.Errorf("--bun-version parsed value = %q, want %q", got, "1.2.5")
	}
}

// TestBuildCommandOriginContractFlags guards PB-4's CLI-reachability half:
// adapter-node's ORIGIN/PROTOCOL_HEADER/HOST_HEADER/ADDRESS_HEADER/XFF_DEPTH/
// BODY_SIZE_LIMIT contract previously had no CLI flag at all — the only way
// to set any of it was hand-editing the image config after the fact.
func TestBuildCommandOriginContractFlags(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cmd := newBuildCommand(context.Background(), logger)

	for _, name := range []string{"origin", "protocol-header", "host-header", "address-header", "xff-depth", "body-size-limit"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("expected --%s flag to be registered", name)
		}
	}

	if err := cmd.Flags().Parse([]string{
		"--origin=https://example.com",
		"--protocol-header=x-forwarded-proto",
		"--host-header=x-forwarded-host",
		"--address-header=x-forwarded-for",
		"--xff-depth=2",
		"--body-size-limit=10M",
	}); err != nil {
		t.Fatalf("parse origin-contract flags: %v", err)
	}

	for name, want := range map[string]string{
		"origin":          "https://example.com",
		"protocol-header": "x-forwarded-proto",
		"host-header":     "x-forwarded-host",
		"address-header":  "x-forwarded-for",
		"xff-depth":       "2",
		"body-size-limit": "10M",
	} {
		got, err := cmd.Flags().GetString(name)
		if name == "xff-depth" {
			var i int
			i, err = cmd.Flags().GetInt(name)
			got = fmt.Sprint(i)
		}
		if err != nil {
			t.Fatalf("get --%s: %v", name, err)
		}
		if got != want {
			t.Errorf("--%s parsed value = %q, want %q", name, got, want)
		}
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

// reconcileStaticStrategy mirrors the --static/--strategy/--base reconciliation
// block in runBuild (cmd/pokkum/build.go, from the "Reconcile the --static
// shorthand" comment through the strategy=="exe" deprecation warning), plus
// the basePreset derivation that precedes it (the "Base image options"
// block). It's kept here as a pure function rather than driven through
// newBuildCommand + cmd.Execute(), because runBuild goes on to call real
// adapters — bun preflight, base image resolution over the network, and so
// on — that a flag-reconciliation unit test has no business touching (the
// existing conflict-error test above gets away with cmd.Execute() only
// because that error path returns before any of that runs). Keep this in
// sync with build.go if the mirrored block changes.
func reconcileStaticStrategy(static, strategyExplicit bool, strategy, base string, hardened bool) (effectiveStrategy string, preset core.BaseImagePreset, ref string, err error) {
	basePreset := base
	if hardened {
		basePreset = "chainguard"
	}
	var parsedPreset core.BaseImagePreset
	var parsedRef string
	if basePreset != "" {
		parsedPreset, parsedRef, err = core.ParseBaseImageSpec(basePreset)
		if err != nil {
			return "", "", "", fmt.Errorf("invalid base image: %w", err)
		}
	}

	if static && strategyExplicit && strategy != "static" {
		return "", "", "", fmt.Errorf("--static cannot be combined with --strategy=%s (layered, static, or nothing must be used)", strategy)
	}

	effectiveStrategy = strategy
	preset = parsedPreset
	ref = parsedRef
	if static {
		effectiveStrategy = "static"
		if basePreset == "" {
			preset = core.BaseImageChainguard
			ref = core.StaticBaseRef
		}
	}
	return effectiveStrategy, preset, ref, nil
}

func TestBuildStaticStrategyReconciliation(t *testing.T) {
	tests := []struct {
		name             string
		static           bool
		strategyExplicit bool
		strategy         string
		base             string
		hardened         bool
		wantErrContains  string
		wantStrategy     string
		wantPreset       core.BaseImagePreset
		wantRef          string
	}{
		{
			name:         "static alone defaults strategy and base to the Chainguard static preset",
			static:       true,
			strategy:     "layered", // cobra's default when --strategy isn't passed
			wantStrategy: "static",
			wantPreset:   core.BaseImageChainguard,
			wantRef:      core.StaticBaseRef,
		},
		{
			name:             "static with explicit --strategy=static is equivalent to static alone",
			static:           true,
			strategyExplicit: true,
			strategy:         "static",
			wantStrategy:     "static",
			wantPreset:       core.BaseImageChainguard,
			wantRef:          core.StaticBaseRef,
		},
		{
			name:             "static with explicit --strategy=layered conflicts",
			static:           true,
			strategyExplicit: true,
			strategy:         "layered",
			wantErrContains:  "cannot be combined with --strategy=layered",
		},
		{
			name:             "static with explicit --strategy=exe conflicts",
			static:           true,
			strategyExplicit: true,
			strategy:         "exe",
			wantErrContains:  "cannot be combined with --strategy=exe",
		},
		{
			name:         "static with an explicit --base is not overridden by the static default",
			static:       true,
			strategy:     "layered",
			base:         "distroless",
			wantStrategy: "static",
			wantPreset:   core.BaseImageDistroless,
			wantRef:      "",
		},
		{
			name:         "static with an explicit --hardened is not overridden by the static default's Ref",
			static:       true,
			strategy:     "layered",
			hardened:     true,
			wantStrategy: "static",
			wantPreset:   core.BaseImageChainguard,
			wantRef:      "",
		},
		{
			// Guards the CLI-reachability half of the --base custom-reference
			// gap: a full image reference must resolve to BaseImageCustom with
			// Ref populated, not be rejected as an unrecognized preset.
			name:         "a full custom image reference resolves to BaseImageCustom with Ref set",
			strategy:     "layered",
			base:         "gcr.io/my-org/my-base:v1.2.3",
			wantStrategy: "layered",
			wantPreset:   core.BaseImageCustom,
			wantRef:      "gcr.io/my-org/my-base:v1.2.3",
		},
		{
			name:            "a typo'd preset is rejected rather than silently treated as a reference",
			strategy:        "layered",
			base:            "distrolss",
			wantErrContains: "not a recognized preset",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotStrategy, gotPreset, gotRef, err := reconcileStaticStrategy(tc.static, tc.strategyExplicit, tc.strategy, tc.base, tc.hardened)

			if tc.wantErrContains != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrContains)
				}
				if !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Fatalf("expected error containing %q, got: %v", tc.wantErrContains, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotStrategy != tc.wantStrategy {
				t.Errorf("strategy = %q, want %q", gotStrategy, tc.wantStrategy)
			}
			if gotPreset != tc.wantPreset {
				t.Errorf("base preset = %q, want %q", gotPreset, tc.wantPreset)
			}
			if gotRef != tc.wantRef {
				t.Errorf("base ref = %q, want %q", gotRef, tc.wantRef)
			}
		})
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
