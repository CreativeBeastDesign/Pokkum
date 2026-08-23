package main

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------------------
// Guards over the two composite actions this repository ships:
//
//	action.yml                                (the Marketplace action)
//	.github/actions/setup-pokkum/action.yml   (CLI installer for custom jobs)
//
// Both are shell embedded in YAML, which neither `go vet` nor golangci-lint
// can see, and neither is executed by anything a developer runs locally. The
// CI job "GitHub Action Self-Test" in .github/workflows/ci.yml runs the real
// action end to end; these tests cover the two properties that job cannot
// observe — an injection is only visible when someone actually supplies a
// malicious input, and a stale version default still installs *a* working
// CLI, just the wrong one.
//
// Same rationale as the two Go tests that parse ci.yml itself: a workflow
// file cannot be executed locally, so the parts of it that must hold get
// asserted as ordinary tests.
// ---------------------------------------------------------------------------

// actionYMLFiles returns every composite action definition in the repo,
// keyed by a repo-relative path for error messages.
//
// Discovered rather than listed: a third action added later must be covered
// by these guards automatically. mem:self_review_checklist row 46 — a guard
// written after an incident has to name the class of thing being guarded
// ("every composite action we ship"), not the one file the bug first
// appeared in.
func actionYMLFiles(t *testing.T) map[string]string {
	t.Helper()
	repoRoot := filepath.Join("..", "..")

	found := map[string]string{}
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "testdata", ".gomodcache":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "action.yml" && d.Name() != "action.yaml" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, relErr := filepath.Rel(repoRoot, path)
		if relErr != nil {
			rel = path
		}
		found[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("[TEST SETUP] walking the repo for action.yml files: %v", err)
	}

	// A floor, not decoration: if the walk stops finding these files (a
	// rename, a moved directory, a SkipDir that grew too broad), every guard
	// below would pass while checking nothing. mem:self_review_checklist row
	// 47 — "did nothing" must not be indistinguishable from "found nothing".
	for _, required := range []string{"action.yml", ".github/actions/setup-pokkum/action.yml"} {
		if _, ok := found[required]; !ok {
			t.Fatalf("[TEST SETUP] %s was not discovered by the repo walk (found: %v) — "+
				"these guards would silently check nothing", required, sortedActionFiles(found))
		}
	}
	return found
}

// sortedActionFiles is a local helper rather than a reuse of
// readme_install_test.go's sortedKeys, which is typed for map[string]bool.
func sortedActionFiles(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// runBlock is one `run:` script found in a composite action, with enough
// context to point a failure at the right step.
type runBlock struct {
	file string
	step string // the step's `name:`, or "<unnamed>"
	body string
}

// actionRunBlocks parses each action definition and returns every step's
// `run:` script.
//
// Parsed with a real YAML parser rather than scanned line-by-line on purpose:
// a regex over the raw file cannot tell a `${{ }}` inside a run: script (the
// vulnerability) from one inside an input `description:` or an `env:` value
// (both fine, and both present in these files by design).
func actionRunBlocks(t *testing.T, file, content string) []runBlock {
	t.Helper()

	var doc struct {
		Runs struct {
			Steps []struct {
				Name string `yaml:"name"`
				Run  string `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatalf("[TEST SETUP] %s is not parseable YAML: %v", file, err)
	}

	blocks := make([]runBlock, 0, len(doc.Runs.Steps))
	for _, step := range doc.Runs.Steps {
		if strings.TrimSpace(step.Run) == "" {
			continue // a `uses:` step has no script
		}
		name := step.Name
		if name == "" {
			name = "<unnamed>"
		}
		blocks = append(blocks, runBlock{file: file, step: name, body: step.Run})
	}
	return blocks
}

// expressionRe matches a GitHub Actions expression, `${{ ... }}`.
var expressionRe = regexp.MustCompile(`\$\{\{[^}]*\}\}`)

// TestActionYMLNoExpressionInjection asserts no `${{ }}` expression appears
// inside any composite action's `run:` script.
//
// This is a script-injection guard, not a style rule. GitHub substitutes an
// expression into the script TEXT before bash ever parses it, so an input
// containing `$(...)`, a backtick, or a newline followed by another command
// executes on the runner with the job's full token and secret access. Both
// actions took every input this way until 2026-08-23 — `tags`, `repo`,
// `project-dir`, `platforms`, `tarball-path`, `log-level`, `version` — and a
// caller wiring `tags` to a branch name or PR title (the documented pattern:
// docs/GITHUB_ACTION.md's quickstart passes `${{ github.sha }}`) hands an
// attacker-controlled string straight into that substitution.
//
// The fix, and the shape this test enforces: pass values through the step's
// `env:` block, where they are set as environment variables rather than
// spliced into source, and read them in the script as quoted "$VARIABLES".
func TestActionYMLNoExpressionInjection(t *testing.T) {
	files := actionYMLFiles(t)

	checked := 0
	for _, file := range sortedActionFiles(files) {
		for _, block := range actionRunBlocks(t, file, files[file]) {
			checked++
			for _, line := range strings.Split(block.body, "\n") {
				// A comment inside the script is inert — it is still
				// substituted, but the result is never executed, and these
				// files deliberately discuss `${{ }}` in comments explaining
				// why it is not used.
				if strings.HasPrefix(strings.TrimSpace(line), "#") {
					continue
				}
				for _, expr := range expressionRe.FindAllString(line, -1) {
					t.Errorf("[SCRIPT INJECTION] %s, step %q: the expression %s is interpolated directly "+
						"into a run: script:\n    %s\n"+
						"GitHub substitutes this into the script text before bash parses it, so an input "+
						"carrying $(...) or a backtick executes on the runner. Pass the value through the "+
						"step's env: block instead and read it as a quoted \"$VARIABLE\".",
						block.file, block.step, expr, strings.TrimSpace(line))
				}
			}
		}
	}

	if checked == 0 {
		t.Fatalf("[TEST SETUP] zero run: blocks were extracted from %v — the parser found no scripts to "+
			"check, so this guard passed without examining anything", sortedActionFiles(files))
	}
}

// pinnedVersionRe matches a full release version, e.g. v1.0.6 or 1.0.6.
var pinnedVersionRe = regexp.MustCompile(`^v?\d+\.\d+\.\d+`)

// TestActionVersionDefaultIsNotAPinnedRelease asserts the root action's
// `version` input does not default to a hardcoded release.
//
// It defaulted to a literal 'v1.0.6'. Nothing bumped it, and nothing could
// notice it was stale: the action still installed a real, working, checksum-
// verified CLI — just not the one the caller pinned. Someone writing
// `uses: .../pokkum@v1.0.9` got the v1.0.6 CLI, and the only symptom would be
// a flag or fix that mysteriously isn't there.
//
// The supported spellings are an explicit version from the caller, "latest",
// or — the default — empty, meaning "derive it from github.action_ref", which
// reads the version out of the ref the caller actually pinned instead of a
// constant that has to be maintained by hand. mem:self_review_checklist row
// 48: a value the environment resolves is not a verified value; read it from
// the declared source of truth.
func TestActionVersionDefaultIsNotAPinnedRelease(t *testing.T) {
	const file = "action.yml"
	content := actionYMLFiles(t)[file]

	var doc struct {
		Inputs map[string]struct {
			Default string `yaml:"default"`
		} `yaml:"inputs"`
	}
	if err := yaml.Unmarshal([]byte(content), &doc); err != nil {
		t.Fatalf("[TEST SETUP] %s is not parseable YAML: %v", file, err)
	}

	versionInput, ok := doc.Inputs["version"]
	if !ok {
		t.Fatalf("[TEST SETUP] %s declares no 'version' input — this guard has nothing to check; "+
			"if the input was renamed, update this test to the new name rather than deleting it", file)
	}

	got := strings.TrimSpace(versionInput.Default)
	if pinnedVersionRe.MatchString(got) {
		t.Errorf("[STALE PIN] %s's 'version' input defaults to the hardcoded release %q.\n"+
			"Nothing in this repository bumps that string, and a stale value fails silently: the action "+
			"installs a real, verified CLI of the wrong version. Use '' (derive from github.action_ref, "+
			"so the CLI matches the action ref the caller pinned) or 'latest'.", file, got)
	}
}

// TestActionInstallStepResolvesVersionFromActionRef is the other half of the
// test above: an empty default is only correct if something actually derives
// a version from it. An empty default with no resolution logic would install
// nothing, or fall through to a bare `latest` while claiming to match the
// pinned ref.
//
// mem:self_review_checklist row 27(a): assert an effect that exists only
// inside the new branch, rather than trusting that removing the old constant
// left something in its place.
func TestActionInstallStepResolvesVersionFromActionRef(t *testing.T) {
	const file = "action.yml"
	content := actionYMLFiles(t)[file]

	if !strings.Contains(content, "github.action_ref") {
		t.Errorf("[STALE PIN] %s's 'version' input has no hardcoded default, but nothing in the file reads "+
			"github.action_ref either — so there is no source for the version the caller pinned. The install "+
			"step must derive it from the action ref when the input is empty.", file)
	}

	blocks := actionRunBlocks(t, file, content)
	var installScript string
	for _, b := range blocks {
		if strings.Contains(b.body, "install.sh") {
			installScript = b.body
			break
		}
	}
	if installScript == "" {
		t.Fatalf("[TEST SETUP] no run: block in %s invokes install.sh — the install step's shape changed and "+
			"this guard can no longer find it", file)
	}
	if !strings.Contains(installScript, "POKKUM_VERSION=") {
		t.Errorf("[INSTALL] %s's install step never sets POKKUM_VERSION, so install.sh always resolves the "+
			"latest release and an explicit 'version' input is silently ignored.", file)
	}
}
