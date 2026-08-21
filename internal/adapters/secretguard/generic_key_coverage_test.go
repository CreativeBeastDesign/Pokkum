package secretguard_test

import (
	"strings"
	"testing"
)

// TestGenericRule_MatchesSuffixedKeyForms is the regression test for roadmap
// item generic-secret-rule-key-coverage: the old
// `\b(?:password|secret|api_key|token)` key alternation required the key to
// START with one of those words, so the dominant real-world naming
// conventions in JS/TS — camelCase, PascalCase, snake_case and
// SCREAMING_SNAKE, all suffixed or prefixed rather than exact — never
// matched at all. Every key form here is a false negative under the old
// rule and must be caught by the widened one.
func TestGenericRule_MatchesSuffixedKeyForms(t *testing.T) {
	for name, src := range map[string]string{
		// The seven spellings the roadmap item names explicitly, each in the
		// form a JS/TS project actually writes it.
		"apiKey (camelCase, bare)":     `apiKey = "abcdefgh12345678"`,
		"api_key (snake_case, bare)":   `api_key = "abcdefgh12345678"`,
		"apikey (no separator, bare)":  `apikey = "abcdefgh12345678"`,
		"accessToken (camelCase)":      `accessToken = "abcdefgh12345678"`,
		"clientSecret (camelCase)":     `clientSecret = "abcdefgh12345678"`,
		"dbPassword (camelCase)":       `dbPassword = "abcdefgh12345678"`,
		"refreshToken (bare property)": `refreshToken:"abcdefgh12345678"`,

		// Further real-world spellings around the same four keywords.
		"camelCase suffix (password)":      `const fallbackPassword = "hunter2hunter2";`,
		"camelCase suffix, second example": `const thirdPassword = "abcdefgh12345678";`,
		"camelCase suffix (token)":         `apiToken: "abcdefgh12345678",`,
		"camelCase suffix, auth token":     `authToken = "abcdefgh12345678"`,
		"SCREAMING_SNAKE prefix":           `DB_PASSWORD="abcdefgh12345678"`,
		"SCREAMING_SNAKE api key":          `STRIPE_API_KEY = "abcdefgh12345678"`,
		"camelCase suffix (secret)":        `stripeSecret: 'abcdefgh12345678'`,
		"snake_case api_key":               `stripe_api_key = "abcdefgh12345678"`,
		"no-underscore apikey":             `myApikey = "abcdefgh12345678"`,
		"camelCase apiKey with prefix":     `stripeApiKey: "abcdefgh12345678"`,
	} {
		t.Run(name, func(t *testing.T) {
			res := scanSource(t, "src/config.ts", "export const config = {\n  "+src+"\n};\n")
			if len(res.Matches) == 0 {
				t.Errorf("expected key form %q to be flagged as a generic secret, got no matches", src)
			}
			if res.Passed {
				t.Errorf("a scan with a real credential match must not report Passed=true: %q", src)
			}
		})
	}
}

// TestGenericRule_SuffixWideningDoesNotReintroduceMinifiedFalsePositives is
// the CRITICAL sibling proof: roadmap item generic-secret-rule-key-coverage
// must not regress generic-secret-rule-matched-minified-code. Widening the
// key from an exact word to a suffix match increases how often the KEY side
// of the rule can fire, so this proves the same minified/bundled-code shapes
// that were fixed for the exact-word keys ("token:", "password:", ...) are
// still rejected when the SAME code shapes appear behind a suffixed key
// name instead ("myToken:", "dbPassword:", ...). The false-positive
// protection lives entirely in the VALUE class (no brackets/braces/
// parens/semicolons, 8+ chars), which the key-side widening does not touch
// — this test proves that empirically rather than by inspection alone.
func TestGenericRule_SuffixWideningDoesNotReintroduceMinifiedFalsePositives(t *testing.T) {
	for name, src := range map[string]string{
		"reported case, suffixed key":       `const a=1;var b={myToken:",!!_),!_){this.error="};`,
		"braces in value, suffixed key":     `dbPassword:"a){b}c;d=efgh"`,
		"call in value, suffixed key":       `clientSecret: "fn(arg,arg2)xyz"`,
		"array index, suffixed key":         `myApiKey = "a[0]bcdefgh"`,
		"statement separator, suffixed key": `refreshToken: "abc;def;ghij"`,
	} {
		t.Run(name, func(t *testing.T) {
			res := scanSource(t, "build/client/_app/immutable/chunks/x.js", src)
			if len(res.Matches) != 0 {
				t.Errorf("%s: %q must not be flagged; it is code, not a credential (matches=%+v)", name, src, res.Matches)
			}
			if !res.Passed {
				t.Errorf("%s: %q must pass cleanly", name, src)
			}
		})
	}
}

// TestGenericRule_DoesNotMatchStopWordsOrPrefixOnlyKeys guards the two ways
// a naive "contains" widening (rather than a suffix-anchored one) would have
// overreached: (a) real English words/identifiers that happen to CONTAIN one
// of the keyword substrings without ending in it (tokenizer, tokenize,
// secretary, passwordless, keyboard, keys, keyof, monkey), and (b) identifiers
// where the keyword is a PREFIX rather than a SUFFIX (passwordHash,
// tokenStore) — this rule only ever claimed to catch identifiers that ARE a
// password/secret/token/api key, not every identifier that merely mentions
// one. All of these carry a credential-shaped value (8+ chars, no excluded
// punctuation) so a match here could only be explained by the key side,
// isolating exactly what this test intends to check.
//
// Note there is deliberately NO literal stop-word list in guard.go for these
// to be checked against. End-anchoring the keyword at `\s*[:=]` excludes every
// one of them structurally, and a maintained list of exempted words would be
// an allowlist that decays: each entry silently becomes either dead weight or
// a pre-authorised blind spot for a key genuinely named that way
// (mem:self_review_checklist row 46). The list lives here, in the test, as
// coverage of the structural rule rather than as configuration the rule reads.
func TestGenericRule_DoesNotMatchStopWordsOrPrefixOnlyKeys(t *testing.T) {
	for name, src := range map[string]string{
		// (a) keyword present but not at the end of the identifier.
		"tokenizer (token + izer)":        `tokenizer = "abcdefgh12345678"`,
		"tokenize (token + ize)":          `tokenize = "abcdefgh12345678"`,
		"secretary (secret + ary)":        `secretary = "abcdefgh12345678"`,
		"passwordless (password + less)":  `passwordless = "abcdefgh12345678"`,
		"keyboard (key, and not api_key)": `keyboard = "abcdefgh12345678"`,
		"monkey (key, and not api_key)":   `monkey = "abcdefgh12345678"`,
		"keys (key, plural, not api_key)": `keys = "abcdefgh12345678"`,
		"keyof (key + of, not api_key)":   `keyof = "abcdefgh12345678"`,

		// (b) keyword is the identifier's PREFIX rather than its suffix.
		"passwordHash (keyword is a prefix, not a suffix)": `passwordHash = "abcdefgh12345678"`,
		"tokenStore (keyword is a prefix, not a suffix)":   `tokenStore = "abcdefgh12345678"`,
	} {
		t.Run(name, func(t *testing.T) {
			res := scanSource(t, "src/util.ts", src)
			if len(res.Matches) != 0 {
				t.Errorf("%s: %q must not be flagged by the generic rule, got matches=%+v", name, src, res.Matches)
			}
		})
	}
}

// TestGenericRule_ColumnPointsAtMatchStart pins SecretMatch.Column's actual
// contract: guard.go's scanFile computes Column as loc[0]+1 from
// regexp.FindAllStringIndex, i.e. the start of the WHOLE regex match (the
// beginning of the flagged identifier), not the start of the capturing
// group around the value. Widening the key from a fixed word to
// `[a-z0-9_]*` + keyword changes WHERE that match starts for a suffixed key
// (e.g. at "fallback", not at "Password") — this test proves the reported
// column still lands exactly at the true start of the matched identifier
// for both an exact-word key and a newly-covered suffixed key, computed
// independently via strings.Index rather than by copying the code's own
// arithmetic.
func TestGenericRule_ColumnPointsAtMatchStart(t *testing.T) {
	for name, tc := range map[string]struct {
		line         string
		expectSubstr string // the substring whose start column is expected
	}{
		"exact-word key (password)":         {`  password: "deadbeefcafe1234",`, "password"},
		"suffixed key (fallbackPassword)":   {`const fallbackPassword = "hunter2hunter2";`, "fallbackPassword"},
		"SCREAMING_SNAKE key (DB_PASSWORD)": {`DB_PASSWORD="abcdefgh12345678"`, "DB_PASSWORD"},
	} {
		t.Run(name, func(t *testing.T) {
			res := scanSource(t, "src/x.ts", tc.line+"\n")
			if len(res.Matches) == 0 {
				t.Fatalf("premise broken: %q produced no matches", tc.line)
			}
			wantCol := strings.Index(tc.line, tc.expectSubstr) + 1
			found := false
			for _, m := range res.Matches {
				if m.Column == wantCol {
					found = true
				}
			}
			if !found {
				cols := make([]int, 0, len(res.Matches))
				for _, m := range res.Matches {
					cols = append(cols, m.Column)
				}
				t.Errorf("expected a match with Column=%d (start of %q in %q), got columns=%v", wantCol, tc.expectSubstr, tc.line, cols)
			}
		})
	}
}
