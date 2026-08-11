package sveltekit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestInstrumentationExists(t *testing.T) {
	tempDir := t.TempDir()

	if InstrumentationExists(tempDir) {
		t.Errorf("expected false for empty directory")
	}

	srcDir := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("failed to create src dir: %v", err)
	}

	instPath := filepath.Join(srcDir, "instrumentation.server.ts")
	if err := os.WriteFile(instPath, []byte("// custom setup"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	if !InstrumentationExists(tempDir) {
		t.Errorf("expected true when src/instrumentation.server.ts exists")
	}
}

func TestGenerateInstrumentationServer(t *testing.T) {
	opts := ports.TelemetryOptions{
		TracesEndpoint:  "http://otel-collector:4318/v1/traces",
		MetricsEndpoint: "http://otel-collector:4318/v1/metrics",
		SampleRate:      0.25,
		MetricsOnly:     true,
	}

	code := GenerateInstrumentationServer(opts)

	if !strings.Contains(code, "http://otel-collector:4318/v1/traces") {
		t.Errorf("expected traces endpoint in code")
	}
	if !strings.Contains(code, "http://otel-collector:4318/v1/metrics") {
		t.Errorf("expected metrics endpoint in code")
	}
	if !strings.Contains(code, "0.250000") && !strings.Contains(code, "0.25") {
		t.Errorf("expected sample rate 0.25 in code")
	}
	if !strings.Contains(code, "ALWAYS_OFF") {
		t.Errorf("expected ALWAYS_OFF for metrics-only mode")
	}
}

func TestPrepareVirtualInstrumentation_Precedence(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	_ = os.WriteFile(filepath.Join(srcDir, "instrumentation.server.js"), []byte("// existing"), 0o644)

	res, err := PrepareVirtualInstrumentation(tempDir, ports.TelemetryOptions{Enabled: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !res.Skipped {
		t.Errorf("expected PrepareVirtualInstrumentation to skip when user file exists")
	}
}

func TestPrepareVirtualInstrumentation_Injection(t *testing.T) {
	tempDir := t.TempDir()

	res, err := PrepareVirtualInstrumentation(tempDir, ports.TelemetryOptions{
		Enabled:        true,
		TracesEndpoint: "http://localhost:4318/v1/traces",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if res.Skipped {
		t.Errorf("expected PrepareVirtualInstrumentation not to skip for clean project")
	}

	if _, err := os.Stat(res.VirtualPath); err != nil {
		t.Errorf("expected virtual instrumentation file to be written at %s", res.VirtualPath)
	}
}
