package secretguard_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/secretguard"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// The inline marker exists because of a real false positive: a sanitizer whose
// job is redacting secrets was flagged for containing the literal
// `password: '[REDACTED]'` in its replacement strings. Suppressing that with a
// regex means writing the offending content into a committed config file, which
// is fine for a placeholder and wrong for a genuine secret — so a marker that
// describes nothing is the safer default.
//
// The fixture below is that exact code, so the test fails if the scanner ever
// stops flagging it (which would make the marker untested) or starts ignoring
// the marker.
const sanitizerSource = `// Sanitize sensitive data patterns (passwords, tokens, etc.)
const sanitized = cleaned
  .replace(/password\s*[:=]\s*['"][^'"]*['"]/gi, "password: '[REDACTED]'")
  .replace(/token\s*[:=]\s*['"][^'"]*['"]/gi, "token: '[REDACTED]'")
  .replace(/secret\s*[:=]\s*['"][^'"]*['"]/gi, "secret: '[REDACTED]'");
`

func scanSource(t *testing.T, name, content string, allow ...string) ports.SecretScanResult {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := secretguard.NewAdapter().ScanDirectory(context.Background(), ports.SecretScanRequest{
		ProjectDir:    dir,
		AllowPatterns: allow,
	})
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}
	return res
}

// TestAllowMarker_PremiseHolds guards the fixture itself: if the scanner stopped
// flagging this code, every assertion below would pass vacuously.
func TestAllowMarker_PremiseHolds(t *testing.T) {
	res := scanSource(t, "src/lib/debugging.ts", sanitizerSource)
	if len(res.Matches) == 0 {
		t.Fatal("premise broken: the sanitizer fixture is no longer flagged, so the marker tests below would prove nothing")
	}
	if res.Passed {
		t.Error("a scan with matches must not report Passed")
	}
}

func TestAllowMarker_SameLineExempts(t *testing.T) {
	annotated := strings.ReplaceAll(sanitizerSource,
		`"password: '[REDACTED]'")`,
		`"password: '[REDACTED]'") // `+ports.AllowSecretMarker)
	res := scanSource(t, "src/lib/debugging.ts", annotated)
	for _, m := range res.Matches {
		if m.LineNumber == 3 {
			t.Errorf("line 3 carries the marker on itself and must be exempt, got %+v", m)
		}
	}
}

// TestAllowMarker_PrecedingLineExempts covers the style that matters in practice:
// the flagged lines here are already long, and requiring the marker on the line
// itself would make the feature unusable exactly where lines are longest.
func TestAllowMarker_PrecedingLineExempts(t *testing.T) {
	lines := strings.Split(sanitizerSource, "\n")
	// Insert the marker above the token replacement (originally line 4).
	withMarker := append([]string{}, lines[:3]...)
	withMarker = append(withMarker, "  // "+ports.AllowSecretMarker)
	withMarker = append(withMarker, lines[3:]...)
	res := scanSource(t, "src/lib/debugging.ts", strings.Join(withMarker, "\n"))

	for _, m := range res.Matches {
		if m.LineNumber == 5 {
			t.Errorf("the line below a marker must be exempt, got %+v", m)
		}
	}
}

// TestAllowMarker_DoesNotExemptNeighbours is the half that keeps this from being
// a blanket off-switch: a marker must cover its own line and the one below it,
// and nothing else. Marking one redaction line must leave the other two flagged.
func TestAllowMarker_DoesNotExemptNeighbours(t *testing.T) {
	annotated := strings.ReplaceAll(sanitizerSource,
		`"password: '[REDACTED]'")`,
		`"password: '[REDACTED]'") // `+ports.AllowSecretMarker)

	plain := scanSource(t, "src/lib/debugging.ts", sanitizerSource)
	marked := scanSource(t, "src/lib/debugging.ts", annotated)

	if len(marked.Matches) >= len(plain.Matches) {
		t.Errorf("marking one line should reduce the findings: plain=%d marked=%d", len(plain.Matches), len(marked.Matches))
	}
	if len(marked.Matches) == 0 {
		t.Error("marking one line must not exempt the whole file; the other redaction lines should still be flagged")
	}
}

// TestAllowMarker_WorksInAnyCommentSyntax: the marker is matched as a substring
// precisely so the scanner needs no per-language knowledge, and this pins that.
func TestAllowMarker_WorksInAnyCommentSyntax(t *testing.T) {
	for _, c := range []struct{ name, comment string }{
		{"slash", "// " + ports.AllowSecretMarker},
		{"hash", "# " + ports.AllowSecretMarker},
		{"block", "/* " + ports.AllowSecretMarker + " */"},
		{"html", "<!-- " + ports.AllowSecretMarker + " -->"},
	} {
		t.Run(c.name, func(t *testing.T) {
			src := c.comment + "\nconst password = \"hunter2hunter2hunter2\";\n"
			if res := scanSource(t, "src/x.ts", src); len(res.Matches) != 0 {
				t.Errorf("%s comment did not exempt the following line: %+v", c.name, res.Matches)
			}
		})
	}
}

// TestAllowMarker_ConfigPatternStillWorksForGeneratedOutput covers why both
// mechanisms exist. A minified bundle carries the redaction strings compiled from
// annotated source but cannot carry the comment, so only a pattern reaches it —
// which is why the failure message names both.
func TestAllowMarker_ConfigPatternStillWorksForGeneratedOutput(t *testing.T) {
	minified := `const a=1;const b="password: '[REDACTED]'";const c="token: '[REDACTED]'";`
	if res := scanSource(t, "build/client/_app/immutable/chunks/lZKtnC6z.js", minified); len(res.Matches) == 0 {
		t.Fatal("premise broken: minified output with redaction strings is no longer flagged")
	}
	// The pattern a real project would add for exactly this case.
	res := scanSource(t, "build/client/_app/immutable/chunks/lZKtnC6z.js", minified, `\[REDACTED\]`)
	if len(res.Matches) != 0 {
		t.Errorf("an allow pattern must still exempt generated output the marker cannot reach: %+v", res.Matches)
	}
}
