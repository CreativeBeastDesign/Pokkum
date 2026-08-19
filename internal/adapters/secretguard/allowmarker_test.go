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

// The inline marker exists for findings that are real matches but deliberate. The
// original motivating case — a sanitizer flagged for containing
// `password: '[REDACTED]'` — is no longer flagged at all, because tightening the
// generic rule to exclude JS structural punctuation also excluded the brackets in
// `[REDACTED]`. That is a better outcome than needing a marker, and it is why this
// fixture uses a value the rule genuinely still matches: a test whose premise has
// silently stopped holding proves nothing about the marker.
const markedSecretSource = `export const config = {
  password: "deadbeefcafe1234",
  secret: "0badc0ffee123456",
  token: "abad1dea99887766",
}
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
	res := scanSource(t, "src/lib/config.ts", markedSecretSource)
	if len(res.Matches) == 0 {
		t.Fatal("premise broken: the credential fixture is no longer flagged, so the marker tests below would prove nothing")
	}
	if res.Passed {
		t.Error("a scan with matches must not report Passed")
	}
}

func TestAllowMarker_SameLineExempts(t *testing.T) {
	annotated := strings.Replace(markedSecretSource,
		`"deadbeefcafe1234",`,
		`"deadbeefcafe1234", // `+ports.AllowSecretMarker, 1)
	res := scanSource(t, "src/lib/config.ts", annotated)
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
	lines := strings.Split(markedSecretSource, "\n")
	// Insert the marker above fallbackPassword (originally line 4).
	withMarker := append([]string{}, lines[:3]...)
	withMarker = append(withMarker, "  // "+ports.AllowSecretMarker)
	withMarker = append(withMarker, lines[3:]...)
	res := scanSource(t, "src/lib/config.ts", strings.Join(withMarker, "\n"))

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
	annotated := strings.Replace(markedSecretSource,
		`"deadbeefcafe1234",`,
		`"deadbeefcafe1234", // `+ports.AllowSecretMarker, 1)

	plain := scanSource(t, "src/lib/config.ts", markedSecretSource)
	marked := scanSource(t, "src/lib/config.ts", annotated)

	if len(marked.Matches) >= len(plain.Matches) {
		t.Errorf("marking one line should reduce the findings: plain=%d marked=%d", len(plain.Matches), len(marked.Matches))
	}
	if len(marked.Matches) == 0 {
		t.Error("marking one line must not exempt the whole file; the other credential lines should still be flagged")
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
	minified := `const a=1;const b={password:"deadbeefcafe1234"};`
	if res := scanSource(t, "build/client/_app/immutable/chunks/lZKtnC6z.js", minified); len(res.Matches) == 0 {
		t.Fatal("premise broken: minified output with a credential is no longer flagged")
	}
	// The pattern a real project would add for exactly this case.
	res := scanSource(t, "build/client/_app/immutable/chunks/lZKtnC6z.js", minified, `deadbeefcafe1234`)
	if len(res.Matches) != 0 {
		t.Errorf("an allow pattern must still exempt generated output the marker cannot reach: %+v", res.Matches)
	}
}

// TestGenericRule_RejectsMinifiedCode is the regression test for a reported false
// positive: a `token:` key in a minified bundle whose captured "secret" was
// `,!!_),!_){this.error=` — code, not a credential. The rule accepted any 8+ run
// of non-quote, non-space bytes, and minified output is full of those.
//
// Both directions matter more than usual here. A secret scanner that cries wolf
// gets switched off, so the false positive is a real cost; but a scanner tightened
// until it misses punctuation-heavy passwords has failed at its actual job, which
// is the worse error. So the tightening excludes JS structural punctuation and
// nothing more.
func TestGenericRule_RejectsMinifiedCode(t *testing.T) {
	flagged := func(t *testing.T, src string) bool {
		t.Helper()
		return len(scanSource(t, "src/x.ts", src).Matches) > 0
	}

	t.Run("rejects code shapes", func(t *testing.T) {
		for name, src := range map[string]string{
			"reported case":       `const a=1;var b={token:",!!_),!_){this.error="};`,
			"braces in value":     `password:"a){b}c;d=efgh"`,
			"call in value":       `secret: "fn(arg,arg2)xyz"`,
			"array index":         `api_key = "a[0]bcdefgh"`,
			"statement separator": `token: "abc;def;ghij"`,
		} {
			if flagged(t, src) {
				t.Errorf("%s: %q must not be flagged; it is code, not a credential", name, src)
			}
		}
	})

	t.Run("still catches real credentials", func(t *testing.T) {
		for name, src := range map[string]string{
			"punctuation password": `const password = "p@ss,w0rd!x";`,
			"google api key":       `api_key = "AIzaSyABCDEFGHIJ1234567890abcdef";`,
			"jwt-ish token":        `token: "eyJhbGci.eyJzdWIi.SflKxwRJ"`,
			"base64 with padding":  `secret="dGhpcyBpcyBhIHRlc3Q="`,
			"hex secret":           `password: "deadbeefcafe1234"`,
		} {
			if !flagged(t, src) {
				t.Errorf("%s: %q must still be flagged — missing a real secret is the worse failure", name, src)
			}
		}
	})
}
