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
