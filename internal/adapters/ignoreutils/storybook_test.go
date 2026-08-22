package ignoreutils_test

import (
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/ignoreutils"
)

// Storybook's built output must not be scanned as source.
//
// A real project failed its first build with 11 "hardcoded secret" findings, all
// in storybook-static/sb-manager/globals-runtime.js — minified Storybook output
// matched by the generic rule. The guard's own advice did not help: a
// pokkum:allow-secret comment cannot be added to generated output, leaving a
// regex against machine-generated code as the only route.
func TestDefaultPatterns_ExcludeStorybookOutputButNotItsConfig(t *testing.T) {
	m, err := ignoreutils.New(ignoreutils.DefaultPatterns())
	if err != nil {
		t.Fatal(err)
	}

	excluded := []string{
		"storybook-static/sb-manager/globals-runtime.js",
		"storybook-static/index.html",
	}
	for _, p := range excluded {
		if !m.Match(p, false) {
			t.Errorf("%s should be excluded: it is generated output, not source", p)
		}
	}

	// .storybook/ is hand-written configuration and belongs in the scan — a
	// real credential there is exactly what the guard exists to catch.
	included := []string{
		".storybook/main.ts",
		".storybook/preview.ts",
		"src/lib/atoms/Button.svelte",
	}
	for _, p := range included {
		if m.Match(p, false) {
			t.Errorf("%s must NOT be excluded; only the storybook OUTPUT directory is generated", p)
		}
	}
}
