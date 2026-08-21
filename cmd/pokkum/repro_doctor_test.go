package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/slsa"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

// `repro doctor --perturb` used to do nothing at all.
//
// The flag promised "dual builds in perturbed environment to bisect stage
// non-determinism". It was echoed into the output and never read again: an
// adversarial field test caught it completing in 16ms — no build, no bisection —
// printing the same "Static reproducibility preflight passed" as every other
// mode, on a project whose consecutive builds demonstrably produced different
// image digests.
//
// That is a fake implementation in the sense CLAUDE.md §3 prohibits: a feature
// that reports success without doing the thing. Until the mode exists, refusing
// is the only honest behaviour, because the user asked a question the tool
// cannot answer and was told the answer was good.
func TestReproDoctor_PerturbRefusesInsteadOfClaimingSuccess(t *testing.T) {
	err := runReproDoctor(context.Background(), discardLogger(), &reproDoctorOptions{dir: t.TempDir(), perturb: true})
	if err == nil {
		t.Fatal("--perturb returned nil: it reports success while performing no build, which is exactly the defect")
	}
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Errorf("error should be an invalid-request sentinel, got: %v", err)
	}
	for _, want := range []string{"not implemented", "pokkum verify"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q so the user knows what to do instead, got: %v", want, err)
		}
	}
}

// TestReproDoctor_SummaryReflectsTheChecks pins the other half. The summary line
// was the constant "preflight passed", printed even when both checks reported
// [! WARN] — so a project with no SOURCE_DATE_EPOCH and a dirty tree was told its
// preflight passed. The per-check lines were right; the conclusion was not.
func TestReproDoctor_SummaryReflectsTheChecks(t *testing.T) {
	pass := reproSummary(true)
	fail := reproSummary(false)

	if pass == fail {
		t.Fatal("the summary does not depend on the checks at all")
	}
	if !strings.Contains(pass, "passed") {
		t.Errorf("deterministic summary should read as a pass, got: %q", pass)
	}
	if strings.Contains(fail, "passed") {
		t.Errorf("non-deterministic summary must not contain 'passed', got: %q", fail)
	}
	// Even the passing text must not overclaim: these are preconditions, not
	// evidence that a build reproduces.
	if !strings.Contains(fail, "does not prove") {
		t.Errorf("summary should say a preflight does not prove reproducibility, got: %q", fail)
	}
}

// TestReproDoctor_GitCheckCanActuallyFail is the regression guard for a check
// that was a constant.
//
// gitClean was initialised true and the only assignment inside the .git stat
// set it true again — no git command ran, so "No dirty uncommitted working tree
// modifications detected" was printed for every tree and the check could never
// fail. It fed allDeterministic, and a field report cited it as corroborating
// another signal, which a check that agrees with everything cannot do.
//
// The test therefore asserts the check DISAGREES between a clean and a dirty
// tree. Asserting only that a clean tree passes would have passed before the
// fix as well.
func TestReproDoctor_GitCheckCanActuallyFail(t *testing.T) {
	dir := t.TempDir()
	run := func(name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v: %s", name, args, err, out)
		}
	}
	run("git", "init", "-q")
	run("git", "config", "user.email", "t@example.com")
	run("git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "src.txt"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "src.txt")
	run("git", "commit", "-qm", "initial")

	dirty0, err0 := slsa.WorkingTreeDirty(context.Background(), dir)
	if err0 != nil {
		t.Fatalf("[TEST SETUP] git could not be consulted: %v", err0)
	}
	if dirty0 {
		t.Fatal("[TEST SETUP] a freshly committed tree reported dirty")
	}

	// A tracked modification must flip the verdict.
	if err := os.WriteFile(filepath.Join(dir, "src.txt"), []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if dirty, err := slsa.WorkingTreeDirty(context.Background(), dir); err != nil || !dirty {
		t.Errorf("a modified tracked file did not report dirty (dirty=%v err=%v); the check cannot distinguish any tree from any other", dirty, err)
	}
}

// TestGitCheckDetails_DistinguishesAllThreeOutcomes pins the reporting, which
// previously described every outcome as "no dirty modifications detected" —
// including a directory that is not a git repository at all, a different claim
// from a clean tree.
func TestGitCheckDetails_DistinguishesAllThreeOutcomes(t *testing.T) {
	notRepo := gitCheckDetails(false, true, nil)
	cleanRepo := gitCheckDetails(true, true, nil)
	dirtyRepo := gitCheckDetails(true, false, nil)
	// A fourth outcome: git itself could not be consulted. Reporting that as
	// "clean" is the fail-open this check exists to prevent.
	unknown := gitCheckDetails(true, false, errors.New("git: exec format error"))

	if unknown == cleanRepo || unknown == dirtyRepo || unknown == notRepo {
		t.Errorf("an inconclusive git check is not distinguishable from a real verdict:\n  unknown: %q\n  clean:   %q\n  dirty:   %q", unknown, cleanRepo, dirtyRepo)
	}
	if notRepo == cleanRepo || cleanRepo == dirtyRepo || notRepo == dirtyRepo {
		t.Errorf("outcomes are not distinguishable:\n  not-a-repo: %q\n  clean:      %q\n  dirty:      %q", notRepo, cleanRepo, dirtyRepo)
	}
	if !strings.Contains(notRepo, "Not a git repository") {
		t.Errorf("a missing repository should say so, got %q", notRepo)
	}
}

// TestReproDoctor_ReportsDirtyTreeThroughTheCommand is the guard that actually
// binds the CALLER.
//
// The sibling test above exercises slsa.WorkingTreeDirty directly, and a
// fail-first check showed that is not enough: reverting repro_doctor's own fix —
// putting `gitClean = true` back — leaves it green, because the helper is still
// correct. The defect was never in the helper; it was that runReproDoctor never
// called one. So this drives the real command and reads what it printed.
func TestReproDoctor_ReportsDirtyTreeThroughTheCommand(t *testing.T) {
	dir := t.TempDir()
	run := func(name string, args ...string) {
		t.Helper()
		c := exec.Command(name, args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v: %s", name, args, err, out)
		}
	}
	run("git", "init", "-q")
	run("git", "config", "user.email", "t@example.com")
	run("git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "src.txt"), []byte("v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "src.txt")
	run("git", "commit", "-qm", "initial")

	gitCheckPassed := func() bool {
		t.Helper()
		orig := os.Stdout
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		os.Stdout = w
		runErr := runReproDoctor(context.Background(), discardLogger(), &reproDoctorOptions{dir: dir, output: "json"})
		_ = w.Close()
		os.Stdout = orig
		if runErr != nil {
			t.Fatalf("runReproDoctor: %v", runErr)
		}
		out, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		var env struct {
			Data struct {
				Checks []struct {
					Check  string `json:"check"`
					Passed bool   `json:"passed"`
				} `json:"checks"`
			} `json:"data"`
		}
		if err := json.Unmarshal(out, &env); err != nil {
			t.Fatalf("parsing repro doctor JSON: %v\n%s", err, out)
		}
		for _, c := range env.Data.Checks {
			if strings.Contains(c.Check, "Git") {
				return c.Passed
			}
		}
		t.Fatalf("no git check in output: %s", out)
		return false
	}

	if !gitCheckPassed() {
		t.Fatal("[TEST SETUP] a freshly committed tree was reported dirty by the command")
	}

	if err := os.WriteFile(filepath.Join(dir, "src.txt"), []byte("v2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if gitCheckPassed() {
		t.Error("repro doctor reported a clean git repository for a tree with a modified tracked file; the check is not consulting git")
	}
}
