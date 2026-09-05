package secretguard

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"regexp/syntax"
	"strings"
	"testing"
	"unicode"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// This file is the safety net for the scanning-speed work: the per-file rule
// prefilter (activeRules), the []byte match path, and the DirEntry-sourced
// file size.
//
// The premise it defends is the one from Lessons.md 2026-08-18 — this exact
// scanner once reported a clean directory it had never actually looked at. A
// prefilter is, by construction, code whose whole job is to NOT run a rule, so
// a wrong prefilter reproduces that incident precisely and quietly. Nothing in
// this file trusts a comment about a regex; every claim is either differenced
// against the pre-optimisation implementation kept below, or derived by
// machine from the pattern itself.

// -----------------------------------------------------------------------------
// The reference implementation
// -----------------------------------------------------------------------------

// scanBytesReference is the per-file scan loop exactly as it stood before the
// prefilter and the []byte match path: every rule against every line, with the
// line converted to a string first. It is the oracle for every differential
// assertion in this file, and it must not be "improved" — its value is that it
// is the code whose output the adapter's existing behaviour was defined by.
func scanBytesReference(data []byte, relPath string, allowPatterns []*regexp.Regexp) []ports.SecretMatch {
	var matches []ports.SecretMatch
	lines := bytes.Split(data, []byte("\n"))
	for i, lineBytes := range lines {
		lineNo := i + 1
		line := string(lineBytes)

		if lineIsAnnotated(lines, i) {
			continue
		}

		allowed := false
		for _, allowRE := range allowPatterns {
			if allowRE.MatchString(line) {
				allowed = true
				break
			}
		}
		if allowed {
			continue
		}

		for _, r := range defaultSecretRules {
			for _, loc := range r.Pattern.FindAllStringIndex(line, -1) {
				snippet := line[loc[0]:loc[1]]
				if r.Validate != nil && !r.Validate(snippet) {
					continue
				}
				if len(snippet) > 40 {
					snippet = snippet[:37] + "..."
				}
				matches = append(matches, ports.SecretMatch{
					FilePath:      relPath,
					LineNumber:    lineNo,
					Column:        loc[0] + 1,
					RuleName:      r.Name,
					SecretSnippet: snippet,
				})
			}
		}
	}
	return matches
}

// -----------------------------------------------------------------------------
// The corpus
// -----------------------------------------------------------------------------

type corpusCase struct {
	name string
	body string
	// wantRules, when non-empty, names the rules this case is asserted to
	// trip. It exists so a case meant to be a positive cannot silently rot
	// into a negative and still pass the differential (both sides agreeing on
	// "no findings" is agreement, but it is not coverage).
	wantRules []string
	allow     []string
}

// Every credential-shaped specimen below is assembled from fragments rather
// than written as one literal, and that is load-bearing rather than stylistic.
//
// GitHub's push protection scans a repository's contents and rejects a push
// carrying anything that reads as a live credential. It cannot know a value in
// a test is synthetic, and it is right not to guess: a repository is precisely
// where a real one would do damage. This file's first version wrote these out
// in full and the push was refused, naming the Slack and Stripe shapes — the
// identical outcome recorded in Lessons.md on 2026-08-21, whose preventative
// rule (assemble, never embed) `credential_formats_test.go` already follows.
// The rule was written over the class and, once again, applied only to the file
// it was found in; this is that gap closed for the second file.
//
// The concatenation is invisible to the code under test — Go folds these at
// compile time, so the scanner sees byte-for-byte what a single literal would
// have produced. Coverage is unchanged; only the bytes at rest differ.
const (
	ghpTok   = "ghp_" + "aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5" // 36 after the prefix
	ghoTok   = "gho_" + "aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5"
	ghsTok   = "ghs_" + "aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5"
	ghuTok   = "ghu_" + "aB3dE5fG7hI9jK1lM3nO5pQ7rS9tU1vW3xY5"
	awsKey   = "AKIA" + "IOSFODNN7EXAMPLE"
	slackB   = "xoxb-" + "123456789012-" + "1234567890123-" + "AbCdEfGhIjKlMnOpQrStUvWx"
	slackP   = "xoxp-" + "123456789012-" + "1234567890123-" + "1234567890123-" + "AbCdEfGhIjKlMn"
	stripeK  = "sk_" + "live_" + "4eC39HqLyjWDarjtT1zdp7dc"
	gitlabK  = "glpat-" + "ABCDEFGHIJ1234567890"
	googleK  = "AIzaSy" + "A1B2C3D4E5F6G7H8I9J0K1L2M3N4O5P6Q"
	jwtTok   = "eyJ" + "hbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." + "eyJ" + "zdWIiOiIxMjM0NTY3ODkwIn0." + "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	rsaBegin = "-----BEGIN RSA PRIVATE KEY-----"
	pkBegin  = "-----BEGIN PRIVATE KEY-----"
)

// longMinifiedLine returns a single line of at least n bytes with payload
// spliced into the middle of it — far past bufio.Scanner's old 64KB token
// limit, and far past any point a chunked prefilter window would fall on.
func longMinifiedLine(payload string, n int) string {
	var b strings.Builder
	filler := `function e(n,t){return n&&n.__esModule?n:{default:n}},const t=Object.freeze({__proto__:null}),`
	for b.Len() < n/2 {
		b.WriteString(filler)
	}
	b.WriteString(payload)
	for b.Len() < n {
		b.WriteString(filler)
	}
	return b.String()
}

func differentialCorpus() []corpusCase {
	cases := []corpusCase{
		// --- one positive for every one of the ten rules ---
		{name: "rule/rsa-private-key", body: "const k = `" + rsaBegin + "\nMIIEow...`;\n", wantRules: []string{"RSA Private Key"}},
		{name: "rule/plain-private-key", body: pkBegin + "\n", wantRules: []string{"RSA Private Key"}},
		{name: "rule/aws-access-key", body: "AWS_ACCESS_KEY_ID=" + awsKey + "\n", wantRules: []string{"AWS Access Key ID"}},
		{name: "rule/github-pat", body: "const t = '" + ghpTok + "';\n", wantRules: []string{"GitHub Personal Access Token"}},
		{name: "rule/github-app-oauth", body: "const t = '" + ghoTok + "';\n", wantRules: []string{"GitHub App Token"}},
		{name: "rule/github-app-server", body: "const t = '" + ghsTok + "';\n", wantRules: []string{"GitHub App Token"}},
		{name: "rule/github-app-user", body: "const t = '" + ghuTok + "';\n", wantRules: []string{"GitHub App Token"}},
		{name: "rule/slack-bot", body: "SLACK=" + slackB + "\n", wantRules: []string{"Slack Token"}},
		{name: "rule/slack-user", body: "SLACK=" + slackP + "\n", wantRules: []string{"Slack Token"}},
		{name: "rule/stripe-live", body: "const s = \"" + stripeK + "\";\n", wantRules: []string{"Stripe Live Secret Key"}},
		{name: "rule/gitlab-pat", body: "CI=" + gitlabK + "\n", wantRules: []string{"GitLab Personal Access Token"}},
		{name: "rule/jwt", body: "const a = '" + jwtTok + "';\n", wantRules: []string{"JSON Web Token (JWT)"}},
		{name: "rule/google-api-key", body: "const g = '" + googleK + "';\n", wantRules: []string{"Google API Key"}},
		{name: "rule/generic-assignment", body: "const apiToken = \"supersecretvalue123\";\n", wantRules: []string{"Generic Hardcoded Password Assignment"}},

		// --- the generic rule's key spellings, each a separate fold literal ---
		{name: "generic/password", body: "dbPassword: 'hunter2hunter2'\n", wantRules: []string{"Generic Hardcoded Password Assignment"}},
		{name: "generic/secret", body: "clientSecret = \"abcdefgh12345\"\n", wantRules: []string{"Generic Hardcoded Password Assignment"}},
		{name: "generic/apikey", body: "myapikey: \"abcdefgh12345\"\n", wantRules: []string{"Generic Hardcoded Password Assignment"}},
		{name: "generic/api_key", body: "SOME_API_KEY = 'abcdefgh12345'\n", wantRules: []string{"Generic Hardcoded Password Assignment"}},
		{name: "generic/screaming-case", body: "DB_PASSWORD=\"abcdefgh12345\"\n", wantRules: []string{"Generic Hardcoded Password Assignment"}},
		{name: "generic/mixed-case-key", body: "sTrIpEsEcReT:'abcdefgh12345'\n", wantRules: []string{"Generic Hardcoded Password Assignment"}},

		// --- Unicode simple-case-fold escapes: an ASCII-only fold prefilter
		//     would drop the generic rule for these and lose the finding ---
		{name: "fold/long-s-secret", body: "x\u017fecret: \"abcdefgh12345\"\n", wantRules: []string{"Generic Hardcoded Password Assignment"}},
		{name: "fold/kelvin-token", body: "to\u212Aen: \"abcdefgh12345\"\n", wantRules: []string{"Generic Hardcoded Password Assignment"}},
		{name: "fold/long-s-password", body: "pa\u017f\u017fword=\"abcdefgh12345\"\n", wantRules: []string{"Generic Hardcoded Password Assignment"}},

		// --- allow marker / allow pattern ---
		{name: "allow/marker-same-line", body: "const t = '" + ghpTok + "'; // pokkum:allow-secret\n"},
		{name: "allow/marker-line-above", body: "// pokkum:allow-secret\nconst t = '" + ghpTok + "';\n"},
		{name: "allow/marker-does-not-cover-two-below", body: "// pokkum:allow-secret\nconst a = 1;\nconst t = '" + ghpTok + "';\n", wantRules: []string{"GitHub Personal Access Token"}},
		{name: "allow/pattern", body: "const t = '" + ghpTok + "'; // fixture\n", allow: []string{`// fixture`}},

		// --- positional edge cases ---
		{name: "edge/first-line", body: "const t = '" + ghpTok + "';\nmore();\n", wantRules: []string{"GitHub Personal Access Token"}},
		{name: "edge/last-line-no-trailing-newline", body: "more();\nconst t = '" + ghpTok + "';", wantRules: []string{"GitHub Personal Access Token"}},
		{name: "edge/only-line-no-trailing-newline", body: "const t = '" + ghpTok + "';", wantRules: []string{"GitHub Personal Access Token"}},
		{name: "edge/multiple-secrets-one-line", body: "a='" + ghpTok + "';b='" + stripeK + "';c='" + awsKey + "';\n",
			wantRules: []string{"AWS Access Key ID", "GitHub Personal Access Token", "Stripe Live Secret Key"}},
		{name: "edge/same-rule-twice-one-line", body: "a='" + ghpTok + "';b='" + ghoTok + "';c='" + ghsTok + "';\n",
			wantRules: []string{"GitHub App Token", "GitHub Personal Access Token"}},
		{name: "edge/crlf", body: "a();\r\nconst t = '" + ghpTok + "';\r\n", wantRules: []string{"GitHub Personal Access Token"}},
		{name: "edge/empty", body: ""},
		{name: "edge/only-newlines", body: "\n\n\n\n"},
		{name: "edge/no-newline-at-all", body: "console.log('hi')"},

		// --- literal present, full pattern absent (the prefilter admits the
		//     rule, the rule then declines — must still agree with reference) ---
		{name: "near/ghp-too-short", body: "const t = 'ghp_short';\n"},
		{name: "near/akia-too-short", body: "AKIA1234\n"},
		{name: "near/begin-certificate", body: "-----BEGIN CERTIFICATE-----\n"},
		{name: "near/xoxb-bare", body: "xoxb-\n"},
		{name: "near/sk-live-short", body: "sk_live_abc\n"},
		{name: "near/glpat-short", body: "glpat-abc\n"},
		{name: "near/aizasy-short", body: "AIzaSyShort\n"},
		{name: "near/eyj-not-a-jwt", body: "const b = 'eyJabcdef.0123456789abc.0123456789abc';\n"},
		{name: "near/generic-value-too-short", body: "password: 'short'\n"},
		{name: "near/generic-prefix-not-suffix", body: "passwordHash: 'abcdefgh12345'\n"},
		{name: "near/stop-words", body: "tokenizer = 'abcdefgh12345'; monkey: 'abcdefgh12345';\n"},
		{name: "near/keyword-but-no-assignment", body: "// discussion of the password policy and secret rotation\n"},

		// --- minified shapes: one enormous line, secret deep inside it ---
		{name: "minified/secret-mid-huge-line", body: longMinifiedLine("k='"+ghpTok+"';", 200_000) + "\n", wantRules: []string{"GitHub Personal Access Token"}},
		{name: "minified/generic-mid-huge-line", body: longMinifiedLine("authToken:\"abcdefgh12345\",", 200_000) + "\n", wantRules: []string{"Generic Hardcoded Password Assignment"}},
		{name: "minified/clean-huge-line", body: longMinifiedLine("", 200_000) + "\n"},
	}

	// --- a keyword straddling the prefilter's fold-window boundary ---
	//
	// One case per byte alignment, each with EXACTLY ONE occurrence. A single
	// body containing the secret at many alignments cannot isolate an overlap
	// bug: one occurrence landing wholly inside a window keeps the rule active
	// and hides every straddling one. With one occurrence per case, an
	// off-by-one in containsAnyFold's overlap turns specific cases red.
	const straddle = "aPiKeY: \"abcdefgh12345\""
	for off := 0; off <= len(straddle)+2; off++ {
		pad := foldWindowBytes - len(straddle) + off
		cases = append(cases, corpusCase{
			name:      fmt.Sprintf("window/straddle-offset-%02d", off),
			body:      strings.Repeat("x", pad) + straddle + "\n",
			wantRules: []string{"Generic Hardcoded Password Assignment"},
		})
	}

	return cases
}

// -----------------------------------------------------------------------------
// The differential
// -----------------------------------------------------------------------------

func compileAllow(t testing.TB, pats []string) []*regexp.Regexp {
	t.Helper()
	var out []*regexp.Regexp
	for _, p := range pats {
		out = append(out, regexp.MustCompile(p))
	}
	return out
}

func formatMatches(ms []ports.SecretMatch) string {
	if len(ms) == 0 {
		return "  (none)"
	}
	var b strings.Builder
	for _, m := range ms {
		fmt.Fprintf(&b, "  %s:%d:%d  %s  %q\n", m.FilePath, m.LineNumber, m.Column, m.RuleName, m.SecretSnippet)
	}
	return strings.TrimRight(b.String(), "\n")
}

// TestPrefilter_DifferentialAgainstReference is the load-bearing test of this
// change. For every corpus case it asserts the optimised scanBytes produces
// findings IDENTICAL to scanBytesReference — same count, same order, same line
// numbers, same columns, same rule names, same snippets. A prefilter that
// wrongly excludes a rule cannot survive it, because the reference has no
// prefilter at all.
func TestPrefilter_DifferentialAgainstReference(t *testing.T) {
	cases := differentialCorpus()
	if len(cases) < 40 {
		t.Fatalf("corpus floor: only %d cases; this test is meant to be broad", len(cases))
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allow := compileAllow(t, tc.allow)
			data := []byte(tc.body)

			want := scanBytesReference(data, "f.js", allow)
			got := scanBytes(data, "f.js", allow, &scanScratch{})

			if len(want) != len(got) {
				t.Fatalf("finding count differs: reference %d, optimised %d\nreference:\n%s\noptimised:\n%s",
					len(want), len(got), formatMatches(want), formatMatches(got))
			}
			for i := range want {
				if want[i] != got[i] {
					t.Fatalf("finding %d differs:\n reference: %+v\n optimised: %+v", i, want[i], got[i])
				}
			}

			gotRules := map[string]bool{}
			for _, m := range got {
				gotRules[m.RuleName] = true
			}
			for _, want := range tc.wantRules {
				if !gotRules[want] {
					t.Errorf("case declares it trips %q but nothing did; findings:\n%s", want, formatMatches(got))
				}
			}
			if len(tc.wantRules) == 0 && len(got) != 0 {
				t.Errorf("case declares no findings but produced:\n%s", formatMatches(got))
			}
		})
	}
}

// TestPrefilter_CorpusCoversEveryRule is the coverage floor for the
// differential above (checklist row 47: a check that ran nothing must not look
// like a check that found nothing). A differential where both sides find
// nothing everywhere agrees perfectly and proves nothing, so assert that every
// rule is positively tripped by at least one corpus case.
//
// It is a separate top-level test rather than a tail assertion inside the
// differential because the differential runs each case as a subtest, and any
// `-run .../subtest` filter would otherwise turn this floor into noise.
func TestPrefilter_CorpusCoversEveryRule(t *testing.T) {
	covered := map[string]bool{}
	for _, tc := range differentialCorpus() {
		for _, m := range scanBytes([]byte(tc.body), "f.js", compileAllow(t, tc.allow), &scanScratch{}) {
			covered[m.RuleName] = true
		}
	}
	for _, r := range defaultSecretRules {
		if !covered[r.Name] {
			t.Errorf("rule %q is never positively exercised by the corpus — the differential cannot see a prefilter that drops it", r.Name)
		}
	}
	t.Logf("corpus positively trips %d/%d rules", len(covered), len(defaultSecretRules))
}

// TestPrefilter_DifferentialThroughScanDirectory closes the caller chain: the
// differential above exercises scanBytes directly, but ScanDirectory is what
// the build calls, and it now supplies the file size from the DirEntry and a
// shared scratch buffer. This runs the whole corpus as one directory.
func TestPrefilter_DifferentialThroughScanDirectory(t *testing.T) {
	dir := t.TempDir()
	cases := differentialCorpus()

	var wantAll []ports.SecretMatch
	for i, tc := range cases {
		if len(tc.allow) != 0 {
			continue // allow patterns are global to a scan; kept to scanBytes above
		}
		rel := fmt.Sprintf("f%03d.js", i)
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(tc.body), 0o644); err != nil {
			t.Fatal(err)
		}
		wantAll = append(wantAll, scanBytesReference([]byte(tc.body), rel, nil)...)
	}

	res, err := NewAdapter().ScanDirectory(t.Context(), ports.SecretScanRequest{ProjectDir: dir})
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}
	if len(res.Skipped) != 0 {
		t.Fatalf("nothing here is over the ceiling; unexpected skips: %+v", res.Skipped)
	}
	if len(wantAll) == 0 {
		t.Fatal("degenerate: the reference found nothing at all across the whole corpus")
	}

	key := func(m ports.SecretMatch) string {
		return fmt.Sprintf("%s|%d|%d|%s|%s", m.FilePath, m.LineNumber, m.Column, m.RuleName, m.SecretSnippet)
	}
	wantSet := map[string]int{}
	for _, m := range wantAll {
		wantSet[key(m)]++
	}
	gotSet := map[string]int{}
	for _, m := range res.Matches {
		gotSet[key(m)]++
	}
	for k, n := range wantSet {
		if gotSet[k] != n {
			t.Errorf("MISSED DETECTION: reference found %s x%d, ScanDirectory found x%d", k, n, gotSet[k])
		}
	}
	for k, n := range gotSet {
		if wantSet[k] != n {
			t.Errorf("EXTRA DETECTION: ScanDirectory found %s x%d, reference found x%d", k, n, wantSet[k])
		}
	}
	t.Logf("differenced %d findings across %d files", len(wantAll), len(cases))
}

// -----------------------------------------------------------------------------
// The necessary-condition property, checked directly
// -----------------------------------------------------------------------------

// admitsRule reports whether the production prefilter keeps rule index i alive
// for data. It deliberately calls activeRules rather than reimplementing the
// decision, so the property below is a statement about shipped code.
func admitsRule(data []byte, idx int) bool {
	s := &scanScratch{}
	for _, i := range s.activeRules(data) {
		if i == idx {
			return true
		}
	}
	return false
}

// assertNecessary is the invariant the whole optimisation rests on: if a rule's
// pattern matches anywhere in a body, the prefilter must have kept that rule.
// The converse is allowed (the prefilter may be over-inclusive); only
// under-inclusion loses a detection.
func assertNecessary(t *testing.T, body []byte, label string) {
	t.Helper()
	for _, lineBytes := range bytes.Split(body, []byte("\n")) {
		for i := range defaultSecretRules {
			r := &defaultSecretRules[i]
			if !r.Pattern.Match(lineBytes) {
				continue
			}
			if !admitsRule(body, i) {
				t.Fatalf("PREFILTER LOST A DETECTION (%s): rule %q matches %q but activeRules dropped it for the file",
					label, r.Name, truncate(string(lineBytes), 120))
			}
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func TestPrefilter_RequiredLiteralsAreNecessaryOverCorpus(t *testing.T) {
	for _, tc := range differentialCorpus() {
		assertNecessary(t, []byte(tc.body), tc.name)
	}
}

// TestPrefilter_RequiredLiteralsAreNecessaryOverMutations hammers the property
// with mechanically mutated real secrets: every single-byte deletion, case
// flip and truncation of each specimen, embedded in filler. Mutation is what
// produces the dangerous inputs — a token one byte short of matching, a key
// whose case makes an ASCII-fold prefilter disagree with Go's (?i).
func TestPrefilter_RequiredLiteralsAreNecessaryOverMutations(t *testing.T) {
	specimens := []string{
		rsaBegin, pkBegin, awsKey, ghpTok, ghoTok, ghsTok, ghuTok,
		slackB, slackP, stripeK, gitlabK, googleK, jwtTok,
		`apiToken = "supersecretvalue123"`,
		`DB_PASSWORD: 'hunter2hunter2'`,
		"x\u017fecret: \"abcdefgh12345\"",
		"to\u212Aen: \"abcdefgh12345\"",
	}
	rng := rand.New(rand.NewSource(0xC0FFEE))
	checked := 0
	for _, sp := range specimens {
		variants := []string{sp}
		for i := 0; i < len(sp); i++ {
			variants = append(variants, sp[:i]+sp[i+1:])        // single-byte deletion
			variants = append(variants, flipCaseAt(sp, i))      // single-byte case flip
			variants = append(variants, sp[:i])                 // truncation
			variants = append(variants, sp[:i]+"\n"+sp[i:])     // newline splice
			variants = append(variants, sp[:i]+"\u017f"+sp[i:]) // fold escape splice
			variants = append(variants, sp[:i]+"\u212A"+sp[i:]) // kelvin splice
		}
		for _, v := range variants {
			bodies := []string{
				v,
				"prefix();\n" + v + "\nsuffix();\n",
				strings.Repeat("z", rng.Intn(64)) + v,
			}
			for _, b := range bodies {
				assertNecessary(t, []byte(b), "mutation")
				checked++
			}
		}
	}
	if checked < 5000 {
		t.Fatalf("floor: only %d mutated bodies checked", checked)
	}
	t.Logf("checked the necessary-condition property over %d mutated bodies", checked)
}

func flipCaseAt(s string, i int) string {
	b := []byte(s)
	c := b[i]
	switch {
	case c >= 'a' && c <= 'z':
		b[i] = c - 32
	case c >= 'A' && c <= 'Z':
		b[i] = c + 32
	}
	return string(b)
}

// -----------------------------------------------------------------------------
// Machine-derived facts about the rules themselves
// -----------------------------------------------------------------------------

// patternIsCaseInsensitive reports whether any node of the compiled pattern
// carries syntax.FoldCase — i.e. whether a byte-exact literal search over the
// subject is a sound necessary condition for it.
func patternIsCaseInsensitive(t *testing.T, pattern string) bool {
	t.Helper()
	re, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		t.Fatalf("parse %q: %v", pattern, err)
	}
	var walk func(*syntax.Regexp) bool
	walk = func(n *syntax.Regexp) bool {
		if n.Flags&syntax.FoldCase != 0 {
			return true
		}
		for _, sub := range n.Sub {
			if walk(sub) {
				return true
			}
		}
		return false
	}
	return walk(re)
}

// TestPrefilter_CaseSensitivityMatchesLiteralKind checks the pairing the whole
// scheme depends on: a rule using byte-exact RequiredAny must not be
// case-insensitive, and a case-insensitive rule must use RequiredAnyFold. This
// is derived from each pattern's parse tree, so adding a `(?i)` to a rule
// without moving its literals fails here rather than silently losing findings.
func TestPrefilter_CaseSensitivityMatchesLiteralKind(t *testing.T) {
	sawExact, sawFold := 0, 0
	for i := range defaultSecretRules {
		r := &defaultSecretRules[i]
		fold := patternIsCaseInsensitive(t, r.Pattern.String())
		switch {
		case len(r.RequiredAny) > 0:
			sawExact++
			if fold {
				t.Errorf("rule %q is case-insensitive but uses byte-exact RequiredAny; a differently-cased secret would be dropped by the prefilter", r.Name)
			}
		case len(r.RequiredAnyFold) > 0:
			sawFold++
			if !fold {
				t.Logf("note: rule %q uses RequiredAnyFold but is case-sensitive (sound, just less selective)", r.Name)
			}
			for _, lit := range r.RequiredAnyFold {
				if !bytes.Equal(lit, bytes.ToLower(lit)) {
					t.Errorf("rule %q RequiredAnyFold entry %q must be lowercase ASCII", r.Name, lit)
				}
			}
		default:
			t.Logf("note: rule %q has no prefilter literal and stays active for every file", r.Name)
		}
	}
	if sawExact == 0 || sawFold == 0 {
		t.Fatalf("floor: expected both kinds of prefilter to be in use, got exact=%d fold=%d", sawExact, sawFold)
	}
}

// TestPrefilter_FoldEscapesAreComplete re-derives foldEscapeSequences from
// Go's own Unicode tables by brute force. Go implements `(?i)` with Unicode
// simple case folding, so an ASCII-only fold prefilter is only sound if every
// non-ASCII rune that folds onto a letter of a RequiredAnyFold literal is
// accounted for. Guessing that set is exactly the mistake that would quietly
// lose a detection; this computes it.
func TestPrefilter_FoldEscapesAreComplete(t *testing.T) {
	letters := map[rune]bool{}
	for i := range defaultSecretRules {
		for _, lit := range defaultSecretRules[i].RequiredAnyFold {
			for _, r := range string(lit) {
				letters[r] = true
			}
		}
	}
	if len(letters) == 0 {
		t.Fatal("degenerate: no folded literals to derive escapes from")
	}

	want := map[string]rune{}
	for r := rune(0x80); r <= unicode.MaxRune; r++ {
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < 0x80 && letters[f] {
				want[string(r)] = r
			}
		}
	}

	have := map[string]bool{}
	for _, seq := range foldEscapeSequences {
		have[string(seq)] = true
	}
	for s, r := range want {
		if !have[s] {
			t.Errorf("U+%04X (%q) folds onto an ASCII letter used by a RequiredAnyFold literal but is NOT in foldEscapeSequences — the prefilter would drop the generic rule for a file containing it", r, r)
		}
	}
	for s := range have {
		if _, ok := want[s]; !ok {
			t.Errorf("foldEscapeSequences contains %q, which does not fold onto any literal letter (harmless but dead)", s)
		}
	}
	t.Logf("derived %d non-ASCII fold escapes from unicode.SimpleFold", len(want))
}

// -----------------------------------------------------------------------------
// containsAnyFold against a regexp oracle
// -----------------------------------------------------------------------------

var foldOracle = regexp.MustCompile(`(?i)password|secret|token|api_?key`)

func genericFoldLiterals(t testing.TB) [][]byte {
	t.Helper()
	for i := range defaultSecretRules {
		if len(defaultSecretRules[i].RequiredAnyFold) > 0 {
			return defaultSecretRules[i].RequiredAnyFold
		}
	}
	t.Fatal("no rule carries RequiredAnyFold")
	return nil
}

// prefilterSaysKeywordPresent mirrors the two-part fold decision activeRules
// makes: an explicit fold escape anywhere, or an ASCII-folded window hit.
func prefilterSaysKeywordPresent(t testing.TB, data []byte) bool {
	var scratch []byte
	return containsAny(data, foldEscapeSequences) || containsAnyFold(data, genericFoldLiterals(t), &scratch)
}

func TestContainsAnyFold_MatchesRegexpOracle(t *testing.T) {
	cases := []string{
		"", "token", "TOKEN", "ToKeN", "tokn", "passwor", "password", "PASSWORD",
		"apikey", "APIKEY", "api_key", "API_KEY", "apiKey", "aPi_KeY",
		"secret", "SECRET", "s\u017fecret", "\u017fecret", "to\u212Aen", "TO\u212AEN",
		strings.Repeat("x", foldWindowBytes-3) + "token",
		strings.Repeat("x", foldWindowBytes-1) + "token",
		strings.Repeat("x", foldWindowBytes) + "TOKEN",
		strings.Repeat("x", foldWindowBytes+1) + "PaSsWoRd",
		strings.Repeat("x", 3*foldWindowBytes-4) + "api_key",
	}
	for _, s := range cases {
		want := foldOracle.MatchString(s)
		got := prefilterSaysKeywordPresent(t, []byte(s))
		if want && !got {
			t.Errorf("UNDER-INCLUSIVE (loses detections) for %q: oracle=true prefilter=false", truncate(s, 60))
		}
	}
}

// TestContainsAnyFold_WindowBoundaryExhaustive walks a keyword byte by byte
// across two full fold windows. An off-by-one in the window overlap shows up
// here as a single failing offset, not as a rare production miss.
func TestContainsAnyFold_WindowBoundaryExhaustive(t *testing.T) {
	lits := genericFoldLiterals(t)
	for _, kw := range []string{"password", "SECRET", "ToKeN", "api_key", "APIKEY"} {
		for off := foldWindowBytes - len(kw) - 2; off <= foldWindowBytes+2; off++ {
			body := append(bytes.Repeat([]byte("x"), off), kw...)
			var scratch []byte
			if !containsAnyFold(body, lits, &scratch) {
				t.Fatalf("window overlap bug: %q placed at offset %d is invisible to containsAnyFold", kw, off)
			}
		}
	}
	// And the negative control: the same shapes without the keyword must not
	// be reported present, or this test could not fail at all.
	var scratch []byte
	if containsAnyFold(bytes.Repeat([]byte("x"), 3*foldWindowBytes), lits, &scratch) {
		t.Fatal("control failed: containsAnyFold reports a keyword in a buffer of only 'x'")
	}
}

// -----------------------------------------------------------------------------
// Structural guards
// -----------------------------------------------------------------------------

// TestScanner_NoBufioScannerAnywhereInPackage is the standing guard for
// Lessons.md 2026-08-18. bufio.Scanner's 64KB default token limit is what made
// this adapter report a clean directory it had never read; reintroducing it —
// including as an innocent-looking convenience in a helper — must break the
// test step, not wait for a field report.
//
// The check is AST-based, not textual. A grep for "bufio.Scanner" matches this
// very comment, guard.go's explanation of why it stopped using one, and the
// Lessons.md reference in guard_test.go — a guard that fires on prose about
// itself is a guard nobody can keep green, so it gets disabled and then the
// real regression walks in.
func TestScanner_NoBufioScannerAnywhereInPackage(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	scanned := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		file, err := parser.ParseFile(fset, e.Name(), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "bufio" {
				return true
			}
			if sel.Sel.Name == "Scanner" || sel.Sel.Name == "NewScanner" {
				t.Errorf("%s:%d uses bufio.%s; see Lessons.md 2026-08-18 (64KB token limit -> silent false-clean on minified bundles)",
					e.Name(), fset.Position(sel.Pos()).Line, sel.Sel.Name)
			}
			return true
		})
	}
	// Floor: a check that parsed no files would pass silently (row 47).
	if scanned < 5 {
		t.Fatalf("only %d .go files inspected; this guard is not looking at the package", scanned)
	}
	t.Logf("inspected %d .go files for bufio.Scanner use", scanned)
}

// TestScanFile_SymlinkToOversizedFileIsStillSkipped guards the DirEntry-size
// shortcut. fs.DirEntry.Info is lstat-shaped: for a symlink it reports the
// length of the target PATH (a couple of dozen bytes), not the size of the
// file that will actually be opened and read. Taking that number at face value
// would turn a file over the ceiling into a "scanned clean" — the exact
// silent-false-clean shape this adapter exists to avoid.
func TestScanFile_SymlinkToOversizedFileIsStillSkipped(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target", "huge.js")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("a"), 4096)
	if err := os.WriteFile(target, big, 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.js")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// Premise check: the link's own DirEntry really does report a small size,
	// otherwise this test proves nothing.
	if fi, err := os.Lstat(link); err == nil && fi.Size() >= int64(len(big)) {
		t.Skipf("platform reports symlink size %d, not the short target path; premise does not hold here", fi.Size())
	}

	res, err := NewAdapter().ScanDirectory(t.Context(), ports.SecretScanRequest{
		ProjectDir:       dir,
		MaxFileSizeBytes: 1024,
	})
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}
	var linkSkipped bool
	for _, s := range res.Skipped {
		if s.FilePath == "link.js" {
			linkSkipped = true
		}
	}
	if !linkSkipped {
		t.Fatalf("a symlink to a file over the ceiling must be recorded as a skip (fail closed), got skipped=%+v matches=%+v passed=%v",
			res.Skipped, res.Matches, res.Passed)
	}
	if res.Passed {
		t.Error("a scan with a skip must not report Passed")
	}
}

// TestScanFile_BinarySniffStillPrecedesSizeCheck pins the check ORDER. A large
// binary asset must stay a silent skip; if the size check moved ahead of the
// sniff it would become a ports.SecretSkip and fail the build on every project
// shipping a big image or wasm blob.
func TestScanFile_BinarySniffStillPrecedesSizeCheck(t *testing.T) {
	dir := t.TempDir()
	blob := make([]byte, 8192)
	blob[10] = 0x00
	if err := os.WriteFile(filepath.Join(dir, "asset.bin"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := NewAdapter().ScanDirectory(t.Context(), ports.SecretScanRequest{
		ProjectDir:       dir,
		MaxFileSizeBytes: 1024, // far below the file's size
	})
	if err != nil {
		t.Fatalf("ScanDirectory: %v", err)
	}
	if !res.Passed || len(res.Skipped) != 0 {
		t.Fatalf("an oversized BINARY file must be skipped silently, not recorded as a coverage gap: passed=%v skipped=%+v", res.Passed, res.Skipped)
	}
}

// TestReadRemainder_ReadsWholeFileWhenSizeHintIsWrong covers the other half of
// the DirEntry-size shortcut: a stale or understated hint must not truncate
// the read. A truncated read is a silent false-clean.
func TestReadRemainder_ReadsWholeFileWhenSizeHintIsWrong(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.js")
	body := strings.Repeat("filler;", 5000) + "const t='" + ghpTok + "';"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, hint := range []int64{-1, 0, 1, 512, int64(len(body)) / 2, int64(len(body)), int64(len(body)) * 4} {
		matches, skip, err := scanFile(path, "f.js", nil, int64(len(body))*8, hint, nil)
		if err != nil {
			t.Fatalf("hint %d: %v", hint, err)
		}
		if skip != nil {
			t.Fatalf("hint %d: unexpected skip %+v", hint, skip)
		}
		if len(matches) != 1 || matches[0].RuleName != "GitHub Personal Access Token" {
			t.Fatalf("hint %d: token at end of file not found (truncated read?): %+v", hint, matches)
		}
	}
}

// -----------------------------------------------------------------------------
// Fuzz
// -----------------------------------------------------------------------------

func seedFuzz(f *testing.F) {
	for _, tc := range differentialCorpus() {
		f.Add([]byte(tc.body))
	}
	f.Add([]byte(nil))
	f.Add([]byte("\n"))
	f.Add([]byte("\u017f\u212a"))
}

// FuzzScanBytesDifferential is the broadest statement of the invariant: for
// ARBITRARY bytes, the optimised scanner and the pre-optimisation reference
// must produce byte-identical findings.
func FuzzScanBytesDifferential(f *testing.F) {
	seedFuzz(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		want := scanBytesReference(data, "f.js", nil)
		got := scanBytes(data, "f.js", nil, &scanScratch{})
		if len(want) != len(got) {
			t.Fatalf("count differs (%d vs %d) for %q\nreference:\n%s\noptimised:\n%s",
				len(want), len(got), truncate(string(data), 200), formatMatches(want), formatMatches(got))
		}
		for i := range want {
			if want[i] != got[i] {
				t.Fatalf("finding %d differs for %q:\n reference %+v\n optimised %+v", i, truncate(string(data), 200), want[i], got[i])
			}
		}
	})
}

// FuzzPrefilterNecessary states the prefilter's contract on its own, without
// the rest of the scanner in the way: whatever the input, a rule whose pattern
// matches must not have been filtered out.
func FuzzPrefilterNecessary(f *testing.F) {
	seedFuzz(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		for _, lineBytes := range bytes.Split(data, []byte("\n")) {
			for i := range defaultSecretRules {
				if defaultSecretRules[i].Pattern.Match(lineBytes) && !admitsRule(data, i) {
					t.Fatalf("prefilter dropped rule %q which matches %q", defaultSecretRules[i].Name, truncate(string(lineBytes), 120))
				}
			}
		}
	})
}

// FuzzContainsAnyFold differences the windowed ASCII-fold search against a
// plain `(?i)` regexp over the same keywords. Over-inclusion is fine;
// under-inclusion is a lost detection.
func FuzzContainsAnyFold(f *testing.F) {
	seedFuzz(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		if foldOracle.Match(data) && !prefilterSaysKeywordPresent(t, data) {
			t.Fatalf("under-inclusive fold prefilter for %q", truncate(string(data), 200))
		}
	})
}

// -----------------------------------------------------------------------------
// Reference-vs-optimised micro-benchmarks
// -----------------------------------------------------------------------------
//
// BenchmarkScanFileLarge / BenchmarkScanDirectory in bench_test.go measure the
// adapter as it ships. These two measure the SAME bytes through the reference
// loop and through the optimised one, so the match-path change can be read off
// on its own — no prefilter, no I/O, no file-size shortcut in the way.

func benchGenericActiveBody() []byte { return benchMinifiedBundleWithKeywords(4 << 20) }

func BenchmarkScanBytes_Reference_GenericActive(b *testing.B) {
	data := benchGenericActiveBody()
	if len(scanBytesReference(data, "f.js", nil)) != 0 {
		b.Fatal("degenerate fixture: expected the no-match path")
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if len(scanBytesReference(data, "f.js", nil)) != 0 {
			b.Fatal("unexpected finding")
		}
	}
}

func BenchmarkScanBytes_Optimised_GenericActive(b *testing.B) {
	data := benchGenericActiveBody()
	scratch := &scanScratch{}
	if len(scanBytes(data, "f.js", nil, scratch)) != 0 {
		b.Fatal("degenerate fixture: expected the no-match path")
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if len(scanBytes(data, "f.js", nil, scratch)) != 0 {
			b.Fatal("unexpected finding")
		}
	}
}
