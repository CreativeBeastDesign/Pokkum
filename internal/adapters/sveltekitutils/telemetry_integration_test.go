package sveltekitutils_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sveltekitutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestTelemetryBootstrap_RealCompileAndRun is PR-5's core empirical
// verification (mem:self_review_checklist row 17: packaged/compiled output
// must actually execute, not just look right — and, per this session's
// investigation, "the generated TypeScript text looks syntactically
// plausible" was already proven insufficient once: an earlier version of
// GenerateInstrumentationServer had two wrong OpenTelemetry export names
// that would have broken every real telemetry-enabled compile, undetected
// because nothing had ever actually compiled and run it).
//
// This test compiles for the CURRENT host platform (no --target), not one
// of Pokkum's Linux container targets, specifically so the resulting binary
// can be executed directly in this test process — bunexec.Compiler.Compile
// itself (target selection, argv construction, hermetic sandboxing) is
// already covered by its own tests; this test is about
// PrepareVirtualTelemetryEntry's wrapper generation and Bun's real runtime
// behavior for the generated bootstrap, not about cross-compilation.
//
// Steps:
//  1. Installs the real npm packages GenerateInstrumentationServer's output
//     imports, into a fresh temp project (skips, doesn't fail, if bun or
//     network access is unavailable — matching this codebase's established
//     real-bun-test convention).
//  2. Writes a minimal "real application entrypoint" that manually creates
//     an OTel span — the same thing the documented hooks.server.ts snippet
//     does, since (also verified this session, see telemetry.go's doc
//     comment) automatic HTTP instrumentation does not work under Bun, so
//     manual span creation is the only way any real build produces spans.
//  3. Calls PrepareVirtualTelemetryEntry for real, wrapping that entrypoint.
//  4. Compiles the wrapper with a real `bun build --compile`, runs the
//     resulting real binary, and asserts a real HTTP POST reaches a fake
//     local OTLP collector standing in for TracesEndpoint — proving the SDK
//     genuinely initializes, the manually created span genuinely gets
//     exported, and the whole mechanism (wrapper generation, static import
//     ordering, Bun compilation) works end to end.
func TestTelemetryBootstrap_RealCompileAndRun(t *testing.T) {
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("no bun on PATH; skipping live telemetry bootstrap test")
	}

	projectDir := t.TempDir()
	pkgJSON := `{
  "name": "telemetry-bootstrap-test",
  "private": true,
  "dependencies": {
    "@opentelemetry/api": "^1.9.0",
    "@opentelemetry/sdk-node": "^0.55.0",
    "@opentelemetry/exporter-trace-otlp-proto": "^0.55.0",
    "@opentelemetry/sdk-trace-base": "^1.28.0"
  }
}`
	if err := os.WriteFile(filepath.Join(projectDir, "package.json"), []byte(pkgJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	installCtx, cancelInstall := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelInstall()
	installCmd := exec.CommandContext(installCtx, "bun", "install")
	installCmd.Dir = projectDir
	if out, err := installCmd.CombinedOutput(); err != nil {
		t.Skipf("bun install failed (network/registry unavailable?), skipping live telemetry bootstrap test: %v\n%s", err, out)
	}

	// Stands in for a real SvelteKit temp-server/index.ts: manually creates
	// a span (as the documented hooks.server.ts snippet does) using nothing
	// but @opentelemetry/api, then exits after giving the SDK's batch
	// processor time to flush — verified empirically this session that a
	// span created immediately before process exit is lost without this
	// wait; OpenTelemetry's default BatchSpanProcessor does not flush
	// synchronously.
	realEntry := filepath.Join(projectDir, "real-entry.ts")
	realEntryCode := `
import { trace } from "@opentelemetry/api";
const tracer = trace.getTracer("telemetry-bootstrap-test");
const span = tracer.startSpan("GET /blog/[slug]");
span.setAttribute("http.route", "/blog/[slug]");
span.end();
console.error("SPAN_CREATED");
// BatchSpanProcessor's default scheduledDelayMillis is 5000ms; wait
// comfortably longer than that so the periodic flush actually fires before
// the process exits (verified empirically this session: a shorter wait
// left the span unexported).
setTimeout(() => { process.exit(0); }, 7000);
`
	if err := os.WriteFile(realEntry, []byte(realEntryCode), 0o644); err != nil {
		t.Fatal(err)
	}

	var collectorHits atomic.Int32
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/traces" {
			collectorHits.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()

	telemetryRes, err := sveltekitutils.PrepareVirtualTelemetryEntry(projectDir, realEntry, ports.TelemetryOptions{
		Enabled:        true,
		TracesEndpoint: collector.URL + "/v1/traces",
		SampleRate:     1.0,
	})
	if err != nil {
		t.Fatalf("PrepareVirtualTelemetryEntry failed: %v", err)
	}
	if telemetryRes.Skipped {
		t.Fatal("expected PrepareVirtualTelemetryEntry not to skip for a clean project with telemetry enabled")
	}

	outPath := filepath.Join(t.TempDir(), "telemetry-app")
	compileCtx, cancelCompile := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancelCompile()
	// No --target: compiles for the current host so the test can execute
	// the resulting binary directly, unlike Pokkum's real Linux-only build
	// targets (see this test's doc comment for why that's the right scope
	// here).
	compileCmd := exec.CommandContext(compileCtx, "bun", "build", "--compile", "--outfile="+outPath, telemetryRes.EntrypointPath)
	compileCmd.Dir = projectDir
	if out, err := compileCmd.CombinedOutput(); err != nil {
		t.Fatalf("real `bun build --compile` of the telemetry wrapper failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("expected a compiled binary at %s: %v", outPath, err)
	}

	runCtx, cancelRun := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelRun()
	runCmd := exec.CommandContext(runCtx, outPath)
	runOut, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running the compiled telemetry binary failed: %v\noutput:\n%s", err, runOut)
	}
	t.Logf("compiled binary output:\n%s", runOut)

	if got := collectorHits.Load(); got == 0 {
		t.Fatalf("expected the compiled binary to export at least one real span to the fake OTLP collector, got zero hits to /v1/traces. Output:\n%s", runOut)
	}
}
