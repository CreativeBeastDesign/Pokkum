package sveltekitutils

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
		TracesEndpoint: "http://otel-collector:4318/v1/traces",
		SampleRate:     0.25,
	}

	code := GenerateInstrumentationServer(opts)

	if !strings.Contains(code, "http://otel-collector:4318/v1/traces") {
		t.Errorf("expected traces endpoint in code")
	}
	if !strings.Contains(code, "0.250000") && !strings.Contains(code, "0.25") {
		t.Errorf("expected sample rate 0.25 in code")
	}
	if strings.Contains(code, "OTLPMetricExporter") || strings.Contains(code, "PeriodicExportingMetricReader") {
		t.Errorf("expected no metrics exporter in generated code — combining it with NodeSDK crashes under `bun build --compile` (see this function's own doc comment)")
	}
}

// TestGenerateInstrumentationServer_MetricsOnlyWarnsInsteadOfCrashing is the
// regression guard for a real bug found this session: combining an OTLP
// metrics exporter with NodeSDK crashes at runtime once compiled via
// `bun build --compile` (a real Bun bundler bug). --metrics-only must warn
// honestly at runtime, not silently construct something that will crash.
func TestGenerateInstrumentationServer_MetricsOnlyWarnsInsteadOfCrashing(t *testing.T) {
	code := GenerateInstrumentationServer(ports.TelemetryOptions{
		TracesEndpoint: "http://otel-collector:4318/v1/traces",
		MetricsOnly:    true,
	})

	if !strings.Contains(code, "not currently functional under Bun") {
		t.Errorf("expected a runtime warning explaining --metrics-only is non-functional, got:\n%s", code)
	}
	if strings.Contains(code, "OTLPMetricExporter") {
		t.Errorf("expected no OTLPMetricExporter construction anywhere in generated code, got:\n%s", code)
	}
}

// TestPrepareVirtualTelemetryEntry_SkipsWhenUserHasOwnInstrumentation is
// PR-5's Zero-Mutation-adjacent regression guard: a project with its own
// real src/instrumentation.server.ts must not also get Pokkum's wrapper —
// that would start a second OTel SDK instance on top of the user's own.
func TestPrepareVirtualTelemetryEntry_SkipsWhenUserHasOwnInstrumentation(t *testing.T) {
	tempDir := t.TempDir()
	srcDir := filepath.Join(tempDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatalf("mkdir failed: %v", err)
	}
	_ = os.WriteFile(filepath.Join(srcDir, "instrumentation.server.js"), []byte("// existing"), 0o644)

	res, err := PrepareVirtualTelemetryEntry(tempDir, filepath.Join(tempDir, "real-entry.ts"), ports.TelemetryOptions{Enabled: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Skipped {
		t.Errorf("expected PrepareVirtualTelemetryEntry to skip when the user has their own instrumentation.server.js")
	}
	if res.EntrypointPath != "" {
		t.Errorf("expected no EntrypointPath when skipped, got %q", res.EntrypointPath)
	}
}

// TestPrepareVirtualTelemetryEntry_SkipsWhenDisabled proves this is gated on
// opts.Enabled, not just on InstrumentationExists.
func TestPrepareVirtualTelemetryEntry_SkipsWhenDisabled(t *testing.T) {
	tempDir := t.TempDir()

	res, err := PrepareVirtualTelemetryEntry(tempDir, filepath.Join(tempDir, "real-entry.ts"), ports.TelemetryOptions{Enabled: false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Skipped {
		t.Errorf("expected PrepareVirtualTelemetryEntry to skip when telemetry is disabled")
	}
}

// TestPrepareVirtualTelemetryEntry_WritesBootstrapAndWrapper is the core
// regression guard: for a clean project with telemetry enabled, both
// generated files land under .pokkum/ (never the user's real src/ tree),
// and the wrapper imports the bootstrap first, the real entrypoint second —
// order matters, see PrepareVirtualTelemetryEntry's doc comment for why.
func TestPrepareVirtualTelemetryEntry_WritesBootstrapAndWrapper(t *testing.T) {
	tempDir := t.TempDir()
	realEntry := filepath.Join(tempDir, ".svelte-kit", "jesterkit-sveltekit", "temp-server", "index.ts")

	res, err := PrepareVirtualTelemetryEntry(tempDir, realEntry, ports.TelemetryOptions{
		Enabled:        true,
		TracesEndpoint: "http://localhost:4318/v1/traces",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Skipped {
		t.Errorf("expected PrepareVirtualTelemetryEntry not to skip for a clean project")
	}

	wantEntry := filepath.Join(tempDir, ".pokkum", "telemetry-entry.ts")
	if res.EntrypointPath != wantEntry {
		t.Errorf("EntrypointPath = %q, want %q", res.EntrypointPath, wantEntry)
	}

	entryCode, err := os.ReadFile(res.EntrypointPath)
	if err != nil {
		t.Fatalf("expected wrapper entry file to be written: %v", err)
	}
	bootstrapIdx := strings.Index(string(entryCode), `import "./otel-bootstrap.ts";`)
	realEntryIdx := strings.Index(string(entryCode), realEntry)
	if bootstrapIdx == -1 {
		t.Fatalf("expected wrapper to import ./otel-bootstrap.ts, got:\n%s", entryCode)
	}
	if realEntryIdx == -1 {
		t.Fatalf("expected wrapper to import the real entrypoint %s, got:\n%s", realEntry, entryCode)
	}
	if bootstrapIdx >= realEntryIdx {
		t.Errorf("expected the bootstrap import to precede the real entrypoint import (SDK must start first), got:\n%s", entryCode)
	}

	bootstrapPath := filepath.Join(tempDir, ".pokkum", "otel-bootstrap.ts")
	bootstrapCode, err := os.ReadFile(bootstrapPath)
	if err != nil {
		t.Fatalf("expected bootstrap file to be written: %v", err)
	}
	if !strings.Contains(string(bootstrapCode), "http://localhost:4318/v1/traces") {
		t.Errorf("expected the bootstrap to carry the configured traces endpoint, got:\n%s", bootstrapCode)
	}
}
