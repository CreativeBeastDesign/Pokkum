package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The pinned golangci-lint version is spelled in three files — the Makefile,
// ci.yml and release.yml — and all three must agree.
//
// Why this is worth a test: the two workflows already drifted once, in the worst
// possible direction. Both pinned v1.62.2 as a *prebuilt* binary, which carries
// the Go version it was compiled with (go1.23) and therefore refused to load a
// config targeting go.mod's 1.26.6. It exited non-zero without linting anything,
// and because that failure was at the step level GitHub skipped every
// verification step behind it — CI ran zero tests for six consecutive runs on
// main while looking merely broken rather than blind. A version that is upgraded
// in one file and forgotten in another reproduces exactly that shape, silently.
//
// The version is deliberately invoked with `go run <module>@<version>` in all
// three places rather than downloaded prebuilt: `go run` builds it with the
// caller's toolchain, so the linter's Go version equals the module's by
// construction and no future Go bump can reintroduce the failure.
func TestGolangciLintVersionIsPinnedConsistently(t *testing.T) {
	// Matches `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@vX.Y.Z`
	// and the Makefile's bare `GOLANGCI_LINT := <module>@<version>` form.
	ref := regexp.MustCompile(`github\.com/golangci/golangci-lint(?:/v\d+)?/cmd/golangci-lint@(v[0-9][^\s"']*)`)

	files := map[string]string{
		"Makefile":                      filepath.Join("..", "..", "Makefile"),
		".github/workflows/ci.yml":      filepath.Join("..", "..", ".github", "workflows", "ci.yml"),
		".github/workflows/release.yml": filepath.Join("..", "..", ".github", "workflows", "release.yml"),
	}

	found := map[string][]string{}
	for label, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("[TEST SETUP] reading %s: %v", label, err)
		}
		matches := ref.FindAllStringSubmatch(string(data), -1)
		if len(matches) == 0 {
			t.Errorf("%s pins no golangci-lint version.\n"+
				"\tIf the linter was intentionally removed from this file, delete its entry here too.\n"+
				"\tIf it is invoked some other way, that way is unpinned or prebuilt — see this test's doc comment.", label)
			continue
		}
		for _, m := range matches {
			found[label] = append(found[label], m[1])
		}
	}

	// Every occurrence, in every file, must be the same version.
	versions := map[string][]string{}
	for label, vs := range found {
		for _, v := range vs {
			versions[v] = append(versions[v], label)
		}
	}
	if len(versions) > 1 {
		var lines []string
		for v, labels := range versions {
			sort.Strings(labels)
			lines = append(lines, v+" in "+strings.Join(labels, ", "))
		}
		sort.Strings(lines)
		t.Errorf("golangci-lint version has drifted across files:\n\t%s\n"+
			"\tAll three must match. A version upgraded in one file and forgotten in another is how "+
			"the linter silently stopped running before.", strings.Join(lines, "\n\t"))
	}

	// Premise check: a v1 pin cannot satisfy this repo, because the config is
	// v2-schema. Catch a downgrade that would parse but never load the config.
	for v, labels := range versions {
		if strings.HasPrefix(v, "v1.") {
			sort.Strings(labels)
			t.Errorf("%s pins golangci-lint %s, but .golangci.yml uses the v2 schema (`version: \"2\"`), "+
				"which a v1 binary cannot load. Raise the linter rather than downgrading the config.",
				strings.Join(labels, ", "), v)
		}
	}
}

// TestGolangciConfigIsV2Schema pins the other half of the pair: the config
// declares the schema version the pinned binary expects. Checked separately so a
// failure names which side moved.
func TestGolangciConfigIsV2Schema(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", ".golangci.yml"))
	if err != nil {
		t.Fatalf("[TEST SETUP] reading .golangci.yml: %v", err)
	}
	if !strings.Contains(string(data), `version: "2"`) {
		t.Error(".golangci.yml does not declare `version: \"2\"`. The pinned linter is v2, which " +
			"refuses a config without a supported version — it would fail to load and lint nothing.")
	}
}
