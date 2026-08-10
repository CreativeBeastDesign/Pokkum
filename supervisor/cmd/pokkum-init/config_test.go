package main

import (
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

func TestParseConfigDefaults(t *testing.T) {
	cfg, warnings, err := parseConfig([]string{"--", "/app/server"}, env(nil), io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	// These are the image runtime contract, mirrored from ports/packager.go.
	// If any of them drifts, the packager, the manifest generator and the
	// supervisor stop agreeing about the same container.
	if cfg.Port != 3000 || cfg.ProbePort != 8081 || cfg.ShutdownTimeout != 30*time.Second || cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("defaults = %+v, want port 3000, probe 8081, 30s, info", cfg)
	}
	if len(cfg.Command) != 1 || cfg.Command[0] != "/app/server" {
		t.Fatalf("command = %v, want [/app/server]", cfg.Command)
	}
}

func TestParseConfigFromEnvironment(t *testing.T) {
	cfg, warnings, err := parseConfig([]string{"--", "/app/server"}, env(map[string]string{
		envPort:            "8080",
		envProbePort:       "9000",
		envShutdownTimeout: "5s",
		envLogLevel:        "debug",
	}), io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if cfg.Port != 8080 || cfg.ProbePort != 9000 || cfg.ShutdownTimeout != 5*time.Second || cfg.LogLevel != slog.LevelDebug {
		t.Fatalf("cfg = %+v", cfg)
	}
}

// TestParseConfigFlagsOverrideEnvironment pins the precedence the project
// requires: flag beats environment beats default.
func TestParseConfigFlagsOverrideEnvironment(t *testing.T) {
	args := []string{"-port=4000", "-probe-port=4001", "-shutdown-timeout=1s", "-log-level=warn", "--", "/app/server", "-port=ignored"}
	cfg, _, err := parseConfig(args, env(map[string]string{
		envPort:            "8080",
		envProbePort:       "9000",
		envShutdownTimeout: "5s",
		envLogLevel:        "debug",
	}), io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Port != 4000 || cfg.ProbePort != 4001 || cfg.ShutdownTimeout != time.Second || cfg.LogLevel != slog.LevelWarn {
		t.Fatalf("cfg = %+v", cfg)
	}
	// The child's own "-port" must reach the child untouched. That is the whole
	// reason the entrypoint uses an explicit "--".
	if len(cfg.Command) != 2 || cfg.Command[1] != "-port=ignored" {
		t.Fatalf("command = %v, want the child's flags passed through", cfg.Command)
	}
}

// TestParseConfigTolerantOfBadEnvironment covers a documented requirement: a
// malformed environment variable must degrade to the default rather than stop
// the container from starting. A pod that will not boot because someone typed
// "30" instead of "30s" is a worse outcome than one that shuts down on the
// default schedule and says so.
func TestParseConfigTolerantOfBadEnvironment(t *testing.T) {
	cfg, warnings, err := parseConfig([]string{"--", "/app/server"}, env(map[string]string{
		envPort:            "not-a-number",
		envProbePort:       "70000",
		envShutdownTimeout: "30",
		envLogLevel:        "loud",
	}), io.Discard)
	if err != nil {
		t.Fatalf("parseConfig must not fail on a bad environment: %v", err)
	}
	if cfg.Port != 3000 || cfg.ProbePort != 8081 || cfg.ShutdownTimeout != 30*time.Second || cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("cfg = %+v, want every bad value replaced by its default", cfg)
	}
	if len(warnings) != 4 {
		t.Fatalf("warnings = %v, want one per rejected variable", warnings)
	}
}

// TestParseConfigNegativeTimeoutFromEnv checks that a non-positive duration is
// rejected too. Zero would mean "SIGKILL immediately", which silently converts
// every rollout into a hard kill.
func TestParseConfigNegativeTimeoutFromEnv(t *testing.T) {
	for _, raw := range []string{"0s", "-5s"} {
		cfg, warnings, err := parseConfig([]string{"--", "/app/server"},
			env(map[string]string{envShutdownTimeout: raw}), io.Discard)
		if err != nil {
			t.Fatalf("parseConfig(%q): %v", raw, err)
		}
		if cfg.ShutdownTimeout != 30*time.Second {
			t.Fatalf("%q gave timeout %s, want the 30s default", raw, cfg.ShutdownTimeout)
		}
		if len(warnings) != 1 {
			t.Fatalf("%q gave warnings %v, want exactly one", raw, warnings)
		}
	}
}

// TestParseConfigRejectsBadFlags is the counterpart: flags come from a human at
// a terminal, so a bad one is a mistake worth reporting rather than absorbing.
func TestParseConfigRejectsBadFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"unknown flag", []string{"-nope", "--", "/app/server"}},
		{"bad duration", []string{"-shutdown-timeout=30", "--", "/app/server"}},
		{"zero timeout", []string{"-shutdown-timeout=0s", "--", "/app/server"}},
		{"bad level", []string{"-log-level=loud", "--", "/app/server"}},
		{"port out of range", []string{"-port=70000", "--", "/app/server"}},
		{"probe port out of range", []string{"-probe-port=0", "--", "/app/server"}},
		{"no command", []string{"--"}},
		{"no command at all", nil},
		{"stray argument before separator", []string{"oops", "-port=1", "--", "/app/server"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := parseConfig(tc.args, env(nil), io.Discard); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// TestParseConfigWithoutSeparator keeps the convenience path working for a
// human debugging a container by hand, while the entrypoint the packager builds
// always uses the explicit form.
func TestParseConfigWithoutSeparator(t *testing.T) {
	cfg, _, err := parseConfig([]string{"-port=4000", "/app/server", "arg1"}, env(nil), io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Port != 4000 {
		t.Fatalf("port = %d, want 4000", cfg.Port)
	}
	if len(cfg.Command) != 2 || cfg.Command[0] != "/app/server" || cfg.Command[1] != "arg1" {
		t.Fatalf("command = %v", cfg.Command)
	}
}

func TestParseConfigCollidingPortsWarns(t *testing.T) {
	_, warnings, err := parseConfig([]string{"-port=8081", "--", "/app/server"}, env(nil), io.Discard)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "8081") {
		t.Fatalf("warnings = %v, want one about the port collision", warnings)
	}
}

func TestParseConfigVersion(t *testing.T) {
	var out strings.Builder
	_, _, err := parseConfig([]string{"-version"}, env(nil), &out)
	if !errors.Is(err, errVersionRequested) {
		t.Fatalf("err = %v, want errVersionRequested", err)
	}
	if !strings.Contains(out.String(), "pokkum-init") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestSplitArgs(t *testing.T) {
	tests := []struct {
		args  []string
		flags []string
		child []string
		saw   bool
	}{
		{nil, nil, nil, false},
		{[]string{"--"}, []string{}, []string{}, true},
		{[]string{"-a", "--", "cmd", "--", "x"}, []string{"-a"}, []string{"cmd", "--", "x"}, true},
		{[]string{"-a", "cmd"}, []string{"-a", "cmd"}, nil, false},
	}
	for _, tc := range tests {
		flags, child, saw := splitArgs(tc.args)
		if saw != tc.saw || !eq(flags, tc.flags) || !eq(child, tc.child) {
			t.Errorf("splitArgs(%v) = %v, %v, %v; want %v, %v, %v",
				tc.args, flags, child, saw, tc.flags, tc.child, tc.saw)
		}
	}
}

func eq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
