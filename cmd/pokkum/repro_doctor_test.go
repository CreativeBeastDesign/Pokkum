package main

import (
	"errors"
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
	err := runReproDoctor(discardLogger(), &reproDoctorOptions{dir: t.TempDir(), perturb: true})
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
