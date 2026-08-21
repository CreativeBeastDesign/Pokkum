package integration

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The runtime smoke tests can only do their job if CI actually runs them, and
// nothing in Go can observe that: a workflow cannot be executed locally, and a
// step that quietly skips still prints ok. So the workflow itself is asserted
// on here, the same way cmd/pokkum/lintversion_test.go asserts on the pinned
// linter version after a prebuilt binary silently linted nothing for six
// consecutive runs.
//
// Two properties are pinned, both drawn from mem:self_review_checklist row 47:
// the smoke step must be unable to report success without having run the smoke
// tests, and one broken step must not be able to retire every later
// verification step in its job.

const ciWorkflowRelPath = "../../.github/workflows/ci.yml"

type ciStep struct {
	Name string            `yaml:"name"`
	If   string            `yaml:"if"`
	Uses string            `yaml:"uses"`
	Run  string            `yaml:"run"`
	Env  map[string]string `yaml:"env"`
}

type ciJob struct {
	Name  string   `yaml:"name"`
	Steps []ciStep `yaml:"steps"`
}

type ciWorkflow struct {
	Jobs map[string]ciJob `yaml:"jobs"`
}

func loadCIWorkflow(t *testing.T) ciWorkflow {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(ciWorkflowRelPath))
	if err != nil {
		t.Fatalf("[TEST SETUP] read %s: %v", ciWorkflowRelPath, err)
	}
	var wf ciWorkflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		t.Fatalf("[TEST SETUP] parse %s: %v", ciWorkflowRelPath, err)
	}
	if len(wf.Jobs) == 0 {
		t.Fatalf("[TEST SETUP] %s declares no jobs; the parse shape is wrong, not the workflow", ciWorkflowRelPath)
	}
	return wf
}

var smokeFloorRe = regexp.MustCompile(`passed"? -lt ([0-9]+)`)

// TestCI_RuntimeSmokeStepCannotSilentlySkip pins the CI half of the guard.
//
// TestRuntimeSmoke_* are the only tests in this repo that boot a produced image
// and ask whether it runs, and each of them skips when its environment cannot
// support it — correct on a laptop, catastrophic in the one job where every
// precondition is guaranteed. This asserts that job's step keeps all three of
// the things that make a skip impossible to mistake for a pass: the env var
// that converts skips into failures, a SKIP scan of the run output (which
// catches skips no env var reaches — `-short`, a `-run` pattern matching
// nothing), and a floor on how many smoke tests passed.
func TestCI_RuntimeSmokeStepCannotSilentlySkip(t *testing.T) {
	wf := loadCIWorkflow(t)

	var steps []ciStep
	for _, job := range wf.Jobs {
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "-run 'TestRuntimeSmoke'") {
				steps = append(steps, step)
			}
		}
	}
	if len(steps) != 1 {
		t.Fatalf("expected exactly 1 CI step running the runtime smoke tests, found %d. "+
			"They are this repo's only boot coverage; if the step was renamed or split, update this test deliberately.",
			len(steps))
	}
	step := steps[0]

	// The env var's name is duplicated across a process boundary (a Go constant
	// and a YAML key). Row 51: compare them in a test rather than keeping them
	// in sync by comment.
	got, ok := step.Env[requireRuntimeSmokeEnv]
	if !ok {
		t.Fatalf("CI step %q does not set %s, so every one of the smoke tests' environmental gates would "+
			"silently skip instead of failing; env was %v", step.Name, requireRuntimeSmokeEnv, step.Env)
	}
	if v, err := strconv.ParseBool(strings.TrimSpace(got)); err == nil && !v {
		t.Errorf("CI step %q sets %s=%q, which runtimeSmokeRequired reads as \"not required\" — "+
			"that disables the guard entirely", step.Name, requireRuntimeSmokeEnv, got)
	}

	if !strings.Contains(step.Run, "--- SKIP") {
		t.Errorf("CI step %q does not scan its output for SKIP lines. The env var only covers gates that call "+
			"smokeGateSkipf; a `-run` pattern that matches nothing, or -short on the command line, produces a green "+
			"step that ran no smoke test at all", step.Name)
	}
	if !strings.Contains(step.Run, "--- PASS: TestRuntimeSmoke") {
		t.Errorf("CI step %q asserts no floor on how many smoke tests passed; row 47 wants a step that executes "+
			"nothing to be distinguishable from a clean pass", step.Name)
	}

	// The floor must equal the number of smoke tests that exist, so adding one
	// without raising it (or deleting one without lowering it) fails here rather
	// than quietly widening the hole.
	m := smokeFloorRe.FindStringSubmatch(step.Run)
	if m == nil {
		t.Fatalf("could not find the passing-test floor (`[ \"$passed\" -lt N ]`) in CI step %q:\n%s", step.Name, step.Run)
	}
	floor, err := strconv.Atoi(m[1])
	if err != nil {
		t.Fatalf("parse floor %q: %v", m[1], err)
	}
	if want := countRuntimeSmokeTests(t); floor != want {
		t.Errorf("CI step %q requires at least %d passing runtime smoke tests, but this package declares %d. "+
			"Raise the floor when adding a smoke test; never lower it to make a red run green.", step.Name, floor, want)
	}

	if !strings.Contains(step.If, "!cancelled()") {
		t.Errorf("CI step %q has if: %q; without !cancelled() an earlier failing step retires it silently", step.Name, step.If)
	}
}

// countRuntimeSmokeTests counts the TestRuntimeSmoke_* functions declared in
// this package, by reading its source rather than by keeping a number in a
// comment.
func countRuntimeSmokeTests(t *testing.T) int {
	t.Helper()
	entries, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("[TEST SETUP] glob package sources: %v", err)
	}
	decl := regexp.MustCompile(`(?m)^func (TestRuntimeSmoke_\w+)\(t \*testing\.T\)`)
	seen := map[string]bool{}
	for _, path := range entries {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("[TEST SETUP] read %s: %v", path, err)
		}
		for _, m := range decl.FindAllStringSubmatch(string(data), -1) {
			seen[m[1]] = true
		}
	}
	if len(seen) == 0 {
		t.Fatalf("[TEST SETUP] found no TestRuntimeSmoke_* declarations; the regexp no longer matches this package's source")
	}
	return len(seen)
}

// verificationCommand matches the commands that constitute an independent
// verification step: anything that compiles, tests, lints, scans or checks.
var verificationCommand = regexp.MustCompile(`\b(go test|go build|go vet|gofmt|govulncheck|golangci-lint|bash scripts/check-)`)

// TestCI_VerificationStepsSurviveAnEarlierFailure pins the second half of row
// 47: in GitHub Actions a step failure skips every later step in the job by
// default, so one infrastructure break retires the whole gate while the skipped
// steps merely look "not run". That is not hypothetical here — govulncheck,
// architecture purity, the race detector, the full suite, the coverage floor
// and the CLI build were all skipped for six consecutive red runs on main
// because the linter step exited non-zero first (Lessons.md, 2026-08-18).
//
// Every step that independently verifies something must therefore carry
// `if: ${{ !cancelled() }}` (or always()), so it reports for itself.
func TestCI_VerificationStepsSurviveAnEarlierFailure(t *testing.T) {
	wf := loadCIWorkflow(t)

	checked := 0
	for jobID, job := range wf.Jobs {
		for _, step := range job.Steps {
			if step.Run == "" || !verificationCommand.MatchString(step.Run) {
				continue
			}
			// Steps that build a prerequisite artifact rather than verify
			// something are preconditions, not gates: `make supervisor
			// static-server` produces the embedded PID-1 blobs every later step
			// consumes, and running the later steps anyway when it failed is
			// exactly what !cancelled() is for.
			if strings.Contains(step.Run, "make supervisor") {
				continue
			}
			checked++
			if !strings.Contains(step.If, "!cancelled()") && !strings.Contains(step.If, "always()") {
				t.Errorf("job %q step %q runs a verification command but has if: %q — "+
					"an earlier failing step would silently retire it (row 47)", jobID, step.Name, step.If)
			}
		}
	}

	// Floor assertion on the check itself, per the row it enforces: a parse
	// shape that matched nothing would otherwise report a clean pass.
	if checked < 10 {
		t.Fatalf("only %d verification steps were examined across %d jobs; the workflow parse or the command "+
			"regexp is wrong, so this test proved nothing", checked, len(wf.Jobs))
	}
}
