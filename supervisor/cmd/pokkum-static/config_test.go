package main

import (
	"bytes"
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseConfig(t *testing.T) {
	env := map[string]string{
		"PORT":                "4000",
		"POKKUM_PROBE_PORT":   "8082",
		"POKKUM_STATIC_ROOTS": strings.Join([]string{"/app/client", "/app/prerendered"}, string(filepath.ListSeparator)),
		"POKKUM_LOG_LEVEL":    "debug",
	}
	cfg, _, err := parseConfig(nil, func(k string) string { return env[k] }, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Port != 4000 {
		t.Errorf("Port = %d, want 4000", cfg.Port)
	}
	if cfg.ProbePort != 8082 {
		t.Errorf("ProbePort = %d, want 8082", cfg.ProbePort)
	}
	if len(cfg.Roots) != 2 || cfg.Roots[0] != "/app/client" || cfg.Roots[1] != "/app/prerendered" {
		t.Errorf("Roots = %v", cfg.Roots)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel = %v, want debug", cfg.LogLevel)
	}
}

func TestParseConfig_Defaults(t *testing.T) {
	cfg, _, err := parseConfig(nil, func(string) string { return "" }, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Port != 3000 {
		t.Errorf("Port = %d, want default 3000", cfg.Port)
	}
	if cfg.ProbePort != 8081 {
		t.Errorf("ProbePort = %d, want default 8081", cfg.ProbePort)
	}
	if !reflect.DeepEqual(cfg.Roots, StaticServerRoots()) {
		t.Errorf("Roots = %v, want default %v", cfg.Roots, StaticServerRoots())
	}
}

func TestParseConfig_InvalidEnvDegrades(t *testing.T) {
	env := map[string]string{
		"PORT":              "notaport",
		"POKKUM_PROBE_PORT": "999999",
		"POKKUM_LOG_LEVEL":  "bogus",
	}
	var out bytes.Buffer
	cfg, warnings, err := parseConfig(nil, func(k string) string { return env[k] }, &out)
	if err != nil {
		t.Fatalf("parseConfig should not fail on bad env: %v", err)
	}
	if cfg.Port != 3000 {
		t.Errorf("Port = %d, want fallback 3000", cfg.Port)
	}
	if cfg.ProbePort != 8081 {
		t.Errorf("ProbePort = %d, want fallback 8081", cfg.ProbePort)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel = %v, want fallback info", cfg.LogLevel)
	}
	if len(warnings) != 3 {
		t.Errorf("got %d warnings, want 3: %v", len(warnings), warnings)
	}
}

// TestParseConfig_RejectsEqualPorts asserts that an explicitly collapsed
// configuration (PORT == POKKUM_PROBE_PORT via env) is rejected outright,
// rather than silently leaving /healthz and /readyz served by nothing (see
// main.go's now-unconditional probe listener and CLAUDE.md's decision to
// reject rather than merge muxes).
func TestParseConfig_RejectsEqualPorts(t *testing.T) {
	env := map[string]string{
		"PORT":              "9000",
		"POKKUM_PROBE_PORT": "9000",
	}
	_, _, err := parseConfig(nil, func(k string) string { return env[k] }, &bytes.Buffer{})
	if err == nil {
		t.Fatal("parseConfig should reject PORT == POKKUM_PROBE_PORT, got nil error")
	}
	if !strings.Contains(err.Error(), "PORT") || !strings.Contains(err.Error(), "POKKUM_PROBE_PORT") {
		t.Errorf("error %q must name both PORT and POKKUM_PROBE_PORT", err.Error())
	}
}

// TestParseConfig_RejectsEqualPortsViaFlags is the same rejection reached
// through -port/-probe-port instead of the environment, confirming
// validate() runs after flag overrides are applied, not just after env
// parsing.
func TestParseConfig_RejectsEqualPortsViaFlags(t *testing.T) {
	_, _, err := parseConfig([]string{"-port", "5000", "-probe-port", "5000"}, func(string) string { return "" }, &bytes.Buffer{})
	if err == nil {
		t.Fatal("parseConfig should reject -port == -probe-port, got nil error")
	}
	if !strings.Contains(err.Error(), "PORT") || !strings.Contains(err.Error(), "POKKUM_PROBE_PORT") {
		t.Errorf("error %q must name both PORT and POKKUM_PROBE_PORT", err.Error())
	}
}

// TestParseConfig_RejectsDefaultProbePortCollision proves the collapsed
// configuration is reachable through the *defaults* alone: a user who sets
// PORT=8081 (a plausible, deliberately chosen port) without touching
// POKKUM_PROBE_PORT collides with defaultProbePort silently unless this is
// caught — Port's own default (3000) never equals ProbePort's default
// (8081), so the only way to reach "both are the default" is one side being
// explicit and the other left at its default value.
func TestParseConfig_RejectsDefaultProbePortCollision(t *testing.T) {
	env := map[string]string{"PORT": "8081"}
	_, _, err := parseConfig(nil, func(k string) string { return env[k] }, &bytes.Buffer{})
	if err == nil {
		t.Fatal("parseConfig should reject PORT colliding with the default probe port, got nil error")
	}
	if !strings.Contains(err.Error(), "PORT") || !strings.Contains(err.Error(), "POKKUM_PROBE_PORT") {
		t.Errorf("error %q must name both PORT and POKKUM_PROBE_PORT", err.Error())
	}
}

// TestParseConfig_DistinctPortsStillPass guards against the rejection above
// being over-broad: a normal two-port configuration must keep parsing
// cleanly with no error and no warning.
func TestParseConfig_DistinctPortsStillPass(t *testing.T) {
	env := map[string]string{
		"PORT":              "4000",
		"POKKUM_PROBE_PORT": "8082",
	}
	cfg, warnings, err := parseConfig(nil, func(k string) string { return env[k] }, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Port != 4000 || cfg.ProbePort != 8082 {
		t.Errorf("Port/ProbePort = %d/%d, want 4000/8082", cfg.Port, cfg.ProbePort)
	}
	if len(warnings) != 0 {
		t.Errorf("got unexpected warnings for a valid distinct-port config: %v", warnings)
	}
}

func TestParseConfig_VersionFlag(t *testing.T) {
	_, _, err := parseConfig([]string{"-version"}, func(string) string { return "" }, &bytes.Buffer{})
	if !errors.Is(err, errVersionRequested) {
		t.Fatalf("want errVersionRequested, got %v", err)
	}
}

func TestParseConfig_FallbackFromEnv(t *testing.T) {
	env := map[string]string{"POKKUM_STATIC_FALLBACK": "/app/client/200.html"}
	cfg, _, err := parseConfig(nil, func(k string) string { return env[k] }, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Fallback != "/app/client/200.html" {
		t.Errorf("Fallback = %q, want /app/client/200.html", cfg.Fallback)
	}
}

func TestParseConfig_FallbackFlagOverridesEnv(t *testing.T) {
	env := map[string]string{"POKKUM_STATIC_FALLBACK": "/app/client/fromenv.html"}
	cfg, _, err := parseConfig([]string{"-fallback", "/app/client/fromflag.html"}, func(k string) string { return env[k] }, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Fallback != "/app/client/fromflag.html" {
		t.Errorf("Fallback = %q, want flag value to win (flag > env)", cfg.Fallback)
	}
}

func TestParseConfig_FallbackEmptyByDefault(t *testing.T) {
	cfg, _, err := parseConfig(nil, func(string) string { return "" }, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if cfg.Fallback != "" {
		t.Errorf("Fallback = %q, want empty default (plain-404 behavior preserved)", cfg.Fallback)
	}
}
