package diagnosticsutils_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/diagnosticsutils"
)

func TestAnalyzeFailure_MissingExecutable(t *testing.T) {
	diag := diagnosticsutils.AnalyzeFailure(127, "")
	if diag.ExitCode != 127 {
		t.Errorf("expected exit code 127, got %d", diag.ExitCode)
	}
	if !strings.Contains(diag.ProbableCause, "Executable not found") {
		t.Errorf("unexpected cause: %s", diag.ProbableCause)
	}
}

func TestAnalyzeFailure_PortConflict(t *testing.T) {
	diag := diagnosticsutils.AnalyzeFailure(1, "Error: listen EADDRINUSE: address already in use :::3000")
	if !strings.Contains(diag.ProbableCause, "Port conflict") {
		t.Errorf("unexpected cause: %s", diag.ProbableCause)
	}
}

func TestPrintDiagnostics(t *testing.T) {
	var buf bytes.Buffer
	diagnosticsutils.PrintDiagnostics(&buf, 137, "OOMKilled")

	out := buf.String()
	if !strings.Contains(out, "Interactive Failure Diagnostics") {
		t.Errorf("missing header in output: %s", out)
	}
	if !strings.Contains(out, "Out of memory") {
		t.Errorf("missing OOM cause in output: %s", out)
	}
}
