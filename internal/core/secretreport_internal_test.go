package core

// Internal test package: these exercise logSecretMatches, which is unexported.
// Every other test file here is package core_test, which cannot see it — and the
// alternative, exporting reporting internals purely so an external test can
// reach them, would widen the package's API for a test's convenience.

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestLogSecretMatches_ReportsLocationsAndRedactsValues pins both halves of the
// secret-guard reporting contract, which are in tension.
//
// The gap it closes: the failure used to carry only a count — "detected 4
// hardcoded secret(s) in <dir>" — so an operator was told their build contained
// secrets and given nothing whatsoever to act on. Reported by a maintainer
// running a real build and asking, reasonably, where they were. The
// skipped-files branch already listed paths, so this was an internal
// inconsistency as much as a missing feature.
//
// The tension: ports.SecretMatch carries the matched substring, which IS the
// secret. Emitting it to make the report useful would copy the value into
// terminal scrollback and CI logs — a bad trade for a tool whose job is keeping
// secrets out of places they should not be. So locations must appear and values
// must not, and both directions are asserted here; a future change that "helpfully"
// adds the snippet back should fail.
func TestLogSecretMatches_ReportsLocationsAndRedactsValues(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	const secret = "ghp_1234567890abcdefghijklmnopqrstuvwxyz"
	matches := []ports.SecretMatch{
		// Deliberately out of order, to prove the reporting sorts rather than
		// echoing filesystem walk order.
		{FilePath: "src/lib/zeta.ts", LineNumber: 9, RuleName: "AWS Access Key ID", SecretSnippet: "AKIAIOSFODNN7EXAMPLE"},
		{FilePath: "src/lib/alpha.ts", LineNumber: 2, RuleName: "GitHub Personal Access Token", SecretSnippet: secret},
	}
	logSecretMatches(log, "pre-build source", matches, false)
	out := buf.String()

	for _, want := range []string{
		"src/lib/alpha.ts", "line=2", "GitHub Personal Access Token",
		"src/lib/zeta.ts", "line=9", "AWS Access Key ID",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q — a finding an operator cannot locate is not actionable.\nGot:\n%s", want, out)
		}
	}

	// The whole point of redaction.
	if strings.Contains(out, secret) {
		t.Error("the secret value was written to the log; that copies it into scrollback and CI output")
	}
	if strings.Contains(out, "AKIAIOSFODNN7EXAMPLE") {
		t.Error("the AWS key value was written to the log")
	}

	// Sorted by file: alpha before zeta, regardless of input order.
	if ai, zi := strings.Index(out, "alpha.ts"), strings.Index(out, "zeta.ts"); ai > zi {
		t.Errorf("findings must be reported in a stable sorted order, got:\n%s", out)
	}
}

// TestLogSecretMatches_CapsTheListing covers a minified bundle, where one logical
// line can carry hundreds of matches: the report must stay readable and must say
// how many it withheld rather than silently truncating, which would understate
// the problem.
func TestLogSecretMatches_CapsTheListing(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	matches := make([]ports.SecretMatch, 0, maxReportedSecretMatches+7)
	for i := 0; i < maxReportedSecretMatches+7; i++ {
		matches = append(matches, ports.SecretMatch{
			FilePath:   fmt.Sprintf("build/chunk-%03d.js", i),
			LineNumber: 1,
			RuleName:   "Generic API Key",
		})
	}
	logSecretMatches(log, "post-build output", matches, false)
	out := buf.String()

	if got := strings.Count(out, "secret guard: hardcoded secret"); got != maxReportedSecretMatches {
		t.Errorf("listed %d findings individually, want the cap of %d", got, maxReportedSecretMatches)
	}
	if !strings.Contains(out, "remaining=7") {
		t.Errorf("the withheld count must be reported, or the listing understates the problem.\nGot:\n%s", out)
	}
}

// TestLogSecretMatches_NilLoggerIsSafe: the scan runs on paths where a caller may
// not have supplied a logger, and reporting must never be the thing that panics a
// build that was already failing.
func TestLogSecretMatches_NilLoggerIsSafe(t *testing.T) {
	logSecretMatches(nil, "pre-build source", []ports.SecretMatch{{FilePath: "a.ts", LineNumber: 1, RuleName: "r"}}, false)
}

// TestLogSecretMatches_ColumnIsReportedForMinifiedOutput covers the case that
// made a line number insufficient: a 44 KB minified chunk is one logical line, so
// "line 3" points at the whole file and the operator has nowhere to look.
func TestLogSecretMatches_ColumnIsReportedForMinifiedOutput(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logSecretMatches(log, "pre-build source", []ports.SecretMatch{
		{FilePath: "build/client/_app/immutable/chunks/lZKtnC6z.js", LineNumber: 3, Column: 18342, RuleName: "Generic Hardcoded Password Assignment"},
	}, false)
	if out := buf.String(); !strings.Contains(out, "col=18342") {
		t.Errorf("the column must be reported, or a finding in minified output is unnavigable:\n%s", out)
	}
}

// TestLogSecretMatches_ShowValuesIsOptIn pins both sides of the reveal switch.
// Default-off is the security property; on-when-asked is what makes triaging a
// minified false positive possible at all.
func TestLogSecretMatches_ShowValuesIsOptIn(t *testing.T) {
	const secret = "ghp_1234567890abcdefghijklmnopqrstuvwxyz"
	match := ports.SecretMatch{FilePath: "a.ts", LineNumber: 1, Column: 7, RuleName: "GitHub PAT", SecretSnippet: secret}

	t.Run("redacted by default", func(t *testing.T) {
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))
		logSecretMatches(log, "s", []ports.SecretMatch{match}, false)
		out := buf.String()
		if strings.Contains(out, secret) {
			t.Error("the value must not appear unless explicitly requested")
		}
		// The operator has to be able to discover how to see it.
		if !strings.Contains(out, "show-secret-values") {
			t.Errorf("the redaction notice should name the flag that reveals it:\n%s", out)
		}
	})

	t.Run("revealed when requested", func(t *testing.T) {
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, nil))
		logSecretMatches(log, "s", []ports.SecretMatch{match}, true)
		if !strings.Contains(buf.String(), secret) {
			t.Errorf("--show-secret-values must actually reveal the match:\n%s", buf.String())
		}
	})
}
