package diagnosticsutils

import (
	"fmt"
	"io"
	"strings"
)

// IsUtilityPackage declares that diagnosticsutils is a helper module and not a direct port adapter.
const IsUtilityPackage = true

// DiagnosticsResult holds analysis of a container or build failure log.
type DiagnosticsResult struct {
	ExitCode       int    `json:"exit_code"`
	ProbableCause  string `json:"probable_cause"`
	Recommendation string `json:"recommendation"`
}

// AnalyzeFailure inspects exit code and error message/log tail to determine root cause heuristics.
func AnalyzeFailure(exitCode int, logTail string) DiagnosticsResult {
	res := DiagnosticsResult{
		ExitCode:       exitCode,
		ProbableCause:  "Unknown container runtime error",
		Recommendation: "Check container logs with `--debug` or inspect application source code.",
	}

	if exitCode == 127 {
		res.ProbableCause = "Executable not found inside container (missing binary or incompatible libc dynamic library)"
		res.Recommendation = "Ensure Bun binary path is valid and base image uses distroless/cc-debian12 or chainguard/glibc-dynamic."
		return res
	}

	if exitCode == 137 || exitCode == 139 {
		res.ProbableCause = "Out of memory (OOMKilled) or segmentation fault"
		res.Recommendation = "Increase memory limit in container resource specs or check native .node addon memory allocations."
		return res
	}

	lowerLog := strings.ToLower(logTail)
	if strings.Contains(lowerLog, "cannot find module") || strings.Contains(lowerLog, "module_not_found") {
		res.ProbableCause = "Missing runtime dependency or node module"
		res.Recommendation = "Run `bun install` and verify package.json dependencies."
		return res
	}

	if strings.Contains(lowerLog, "eaddrinuse") || strings.Contains(lowerLog, "address already in use") {
		res.ProbableCause = "Port conflict during local container launch"
		res.Recommendation = "Free up the local port or pass an alternative host port binding."
		return res
	}

	return res
}

// PrintDiagnostics formats failure diagnosis to standard error output.
func PrintDiagnostics(w io.Writer, exitCode int, logTail string) {
	diag := AnalyzeFailure(exitCode, logTail)

	fmt.Fprintln(w, "\n--- Interactive Failure Diagnostics ---")
	fmt.Fprintf(w, "Exit Code:       %d\n", diag.ExitCode)
	fmt.Fprintf(w, "Probable Cause:  %s\n", diag.ProbableCause)
	fmt.Fprintf(w, "Recommendation:  %s\n", diag.Recommendation)
	fmt.Fprintln(w, "--------------------------------------")
}
