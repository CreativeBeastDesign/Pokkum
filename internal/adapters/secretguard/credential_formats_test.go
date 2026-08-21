package secretguard_test

import (
	"testing"
)

// TestValueShapeRules_CatchCommonFormatsWithInnocuousNames is the regression
// test for the adversarial field-test finding (roadmap F9): with a
// deliberately unremarkable variable name (no "key"/"token"/"secret"/
// "password" anywhere in it), the generic name-based rule never fires, so
// detection of these formats depends entirely on a value-shape rule
// existing for them — exactly like the pre-existing AWS Access Key ID and
// RSA Private Key rules. Before this change, five of these seven were
// missed outright; only the AWS key and the RSA key were caught (see
// TestSecretGuard_DetectsRSAPrivateKey / the AWS key coverage below for
// those two — this test isolates the five that were missing).
//
// Every fixture below uses "const a", "const b", ... on purpose: a name
// containing "key"/"token"/"secret"/"password" would let the pre-existing
// generic keyword rule mask a missing (or broken) value-shape rule.
func TestValueShapeRules_CatchCommonFormatsWithInnocuousNames(t *testing.T) {
	for name, tc := range map[string]struct {
		src      string
		wantRule string
	}{
		"GitHub personal access token": {
			// 38 chars after ghp_ — deliberately NOT the classic-PAT exact
			// length of 36. The pre-existing rule required exactly 36 chars
			// (`ghp_[a-zA-Z0-9]{36}`), so a token of any other length,
			// bounded on both sides by \b, silently failed to match. Real
			// GitHub tokens are not all exactly 36 chars (fine-grained PATs
			// are much longer), so an exact-length rule was already too
			// narrow even before this specific value.
			src:      `const a = "ghp_1234567890abcdefghijklmnopqrstuvwxyzAB";`,
			wantRule: "GitHub Personal Access Token",
		},
		"GitHub OAuth/App token (gho_)": {
			src:      `const b = "gho_1234567890abcdefghijklmnopqrstuvwxyzAB";`,
			wantRule: "GitHub App Token",
		},
		"GitHub user-to-server token (ghu_)": {
			src:      `const c = "ghu_1234567890abcdefghijklmnopqrstuvwxyzAB";`,
			wantRule: "GitHub App Token",
		},
		"GitHub server-to-server token (ghs_)": {
			src:      `const d = "ghs_1234567890abcdefghijklmnopqrstuvwxyzAB";`,
			wantRule: "GitHub App Token",
		},
		"Slack bot token": {
			src:      `const e = "` + slackTestToken() + `";`,
			wantRule: "Slack Token",
		},
		"Stripe live secret key": {
			src:      `const f = "` + stripeTestKey() + `";`,
			wantRule: "Stripe Live Secret Key",
		},
		"GitLab personal access token": {
			src:      `const g = "glpat-ABCDEFGHIJKLMNOPQRST";`,
			wantRule: "GitLab Personal Access Token",
		},
		"JWT": {
			src:      `const h = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.4Adcj3UFYzPUVaVF43FmMab6RlaQD8A9V8wFzzht-KQ";`,
			wantRule: "JSON Web Token (JWT)",
		},
	} {
		t.Run(name, func(t *testing.T) {
			res := scanSource(t, "src/config.ts", tc.src)
			if res.Passed {
				t.Fatalf("expected credential to be flagged even with an innocuous variable name, got Passed=true for: %s", tc.src)
			}
			found := false
			for _, m := range res.Matches {
				if m.RuleName == tc.wantRule {
					found = true
				}
			}
			if !found {
				t.Errorf("expected a match with RuleName=%q, got matches=%+v", tc.wantRule, res.Matches)
			}
		})
	}
}

// TestValueShapeRules_AllowMarkerExemptsGitHubToken proves the mandatory
// interaction from the F9 requirements: the pre-existing pokkum:allow-secret
// inline marker must keep exempting matches produced by the NEW rules, not
// just the pre-existing ones.
func TestValueShapeRules_AllowMarkerExemptsGitHubToken(t *testing.T) {
	src := "const a = \"ghp_1234567890abcdefghijklmnopqrstuvwxyzAB\"; // pokkum:allow-secret\n"
	res := scanSource(t, "src/config.ts", src)
	for _, m := range res.Matches {
		if m.RuleName == "GitHub Personal Access Token" {
			t.Errorf("expected the marked line to be exempt, got match: %+v", m)
		}
	}
}

// TestValueShapeRules_AllowPatternExemptsStripeKey proves the
// security.allow_secret_patterns / AllowPatterns config path also keeps
// working for a new rule, not only the marker.
func TestValueShapeRules_AllowPatternExemptsStripeKey(t *testing.T) {
	src := `const a = "` + stripeTestKey() + `";`

	without := scanSource(t, "src/config.ts", src)
	if without.Passed {
		t.Fatalf("premise broken: expected the Stripe key to be flagged without an allow pattern")
	}

	with := scanSource(t, "src/config.ts", src, stripeTestKey())
	if !with.Passed {
		t.Errorf("expected the allow pattern to exempt the Stripe key match, got matches=%+v", with.Matches)
	}
}

// TestValueShapeRules_FalsePositiveCorpus is the mandatory false-positive
// guard: realistic non-secret content that must NOT be flagged by any of
// the new value-shape rules. There is prior art in this package
// (TestGenericRule_RejectsMinifiedCode, generic-secret-rule-matched-minified-code
// in Lessons.md) for exactly this failure mode — a rule that fires on
// ordinary code or data teaches people to switch the scanner off, which is
// worse than a missing rule.
func TestValueShapeRules_FalsePositiveCorpus(t *testing.T) {
	corpus := map[string]string{
		"minified JS bundle chunk": `!function(e,t){"object"==typeof exports&&"object"==typeof module?module.exports=t():"function"==typeof define&&define.amd?define([],t):"object"==typeof exports?exports.chunk=t():e.chunk=t()}(this,function(){return{}});var a=1,b=function(e,t){return e+t};window.__INITIAL_STATE__=JSON.parse("{}");`,

		"base64 PNG data URI": `const icon = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=";`,

		"sha256 hex digest": `const integrity = "sha256-4S8+41KfLQfIeu69bSCzWMz5jT5tI0KYt3xC5nQxOo0=".slice(0); const digest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85";`,

		"UUID": `const requestId = "550e8400-e29b-41d4-a716-446655440000";`,

		"git commit hash": `const commit = "a1b2c3d4e5f6789012345678901234567890abcd";`,
	}

	for name, src := range corpus {
		t.Run(name, func(t *testing.T) {
			res := scanSource(t, "build/client/_app/immutable/chunks/x.js", src)
			if len(res.Matches) != 0 {
				t.Errorf("%s: %q must not be flagged, got matches=%+v", name, src, res.Matches)
			}
			if !res.Passed {
				t.Errorf("%s: expected Passed=true, got false", name)
			}
		})
	}
}

// Two of these fixtures are assembled at runtime rather than written as
// literals. GitHub's push protection recognises Slack and Stripe token shapes
// and refuses the push — correctly, since it cannot know a value in a test is
// synthetic, and a repository is exactly where a real one would do damage. The
// rules under test see the identical concatenated string, so coverage is
// unchanged; only the bytes at rest differ.
func slackTestToken() string {
	return "xoxb-" + "111111111111-" + "222222222222-" + "abcdefghijklmnopqrstuvwx"
}

func stripeTestKey() string {
	return "sk_" + "live_" + "51H1234567890abcdefghijklmn"
}
