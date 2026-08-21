package integration

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The runtime smoke tests are the only tests in this repo that boot a produced
// image and ask whether it actually runs, and every one of them is guarded by
// environmental preconditions (`bun` on PATH, installed fixture dependencies, a
// reachable container runtime, a network path to the base image registry). On a
// developer laptop those guards are correct: a missing daemon is not a defect in
// the image, and turning it into a failure would only produce noise.
//
// In CI they are the opposite of correct. `ubuntu-latest` ships Docker running,
// the job installs Bun and the fixture dependencies itself, and the runner has
// network — so every precondition is guaranteed to hold, and a SKIP there does
// not mean "not applicable", it means the single piece of boot coverage this
// repo has silently stopped executing while the step still reports `ok`. That is
// mem:self_review_checklist row 47's exact shape ("a step whose test body can
// t.Skip reports ok while asserting nothing"), and row 39's ("any predicate
// deciding skip vs. fail is load-bearing safety logic").
//
// POKKUM_REQUIRE_RUNTIME_SMOKE is how the environment declares "here, these
// preconditions are guaranteed": when it is set, every gate below turns from
// t.Skip into t.Fatal, naming the precondition that was not met. CI sets it (see
// .github/workflows/ci.yml's "Runtime Smoke Tests" step, which additionally
// greps the run output for SKIP and asserts a floor of passing smoke tests, so
// even a skip that never reaches this helper — `-short`, a `-run` pattern that
// matches nothing — cannot pass for success).
const requireRuntimeSmokeEnv = "POKKUM_REQUIRE_RUNTIME_SMOKE"

// runtimeSmokeRequired reports whether the environment declares that the
// runtime smoke tests MUST run here rather than being allowed to skip.
//
// Unset, empty, and any value strconv.ParseBool reads as false mean "not
// required" — the developer-laptop default, where skipping is the right
// behaviour. Any other non-empty value (including one ParseBool cannot read at
// all, e.g. "yes") means required: the failure directions are asymmetric, so an
// unreadable value resolves toward running the gate rather than toward silently
// disabling it. A typo'd `POKKUM_REQUIRE_RUNTIME_SMOKE=ture` in a workflow must
// not quietly restore the very hole this exists to close.
func runtimeSmokeRequired() bool {
	raw, ok := os.LookupEnv(requireRuntimeSmokeEnv)
	if !ok {
		return false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false
	}
	if v, err := strconv.ParseBool(raw); err == nil {
		return v
	}
	return true
}

// smokeGateT is the slice of *testing.T that smokeGateSkipf needs. It exists so
// the skip-vs-fail decision can be tested in BOTH directions (row 39) without a
// real *testing.T — a test cannot observe its own t.Skip/t.Fatal, so a fake
// recorder is the only way to assert that the predicate fails when it must and
// skips when it must.
type smokeGateT interface {
	Helper()
	Skipf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// smokeGateSkipf skips the calling runtime smoke test with the given reason, or
// fails it outright when POKKUM_REQUIRE_RUNTIME_SMOKE declares the precondition
// is guaranteed to hold in this environment.
//
// Every environmental gate in a runtime smoke test must go through this helper
// rather than calling t.Skip directly — a gate that bypasses it is exactly the
// silent hole the env var exists to close, and it would not be visible in the
// step's green result.
func smokeGateSkipf(t smokeGateT, format string, args ...any) {
	t.Helper()
	reason := fmt.Sprintf(format, args...)
	if runtimeSmokeRequired() {
		t.Fatalf("%s is set, so this environment guarantees the runtime smoke preconditions and the test MUST run — "+
			"but it was about to skip: %s. This is the only test in this repo that boots a produced image; "+
			"a skip here is a silent loss of all boot coverage, not a clean pass.", requireRuntimeSmokeEnv, reason)
		return
	}
	t.Skipf("%s", reason)
}

// recordingGate is a smokeGateT that records which branch was taken instead of
// actually skipping or failing.
type recordingGate struct {
	skipped bool
	failed  bool
	msg     string
}

func (r *recordingGate) Helper() {}

func (r *recordingGate) Skipf(format string, args ...any) {
	r.skipped = true
	r.msg = fmt.Sprintf(format, args...)
}

func (r *recordingGate) Fatalf(format string, args ...any) {
	r.failed = true
	r.msg = fmt.Sprintf(format, args...)
}

// TestSmokeGate_SkipVsFailInBothDirections pins the predicate that decides
// whether a missing precondition is a skip or a failure. Row 39: such a
// predicate is load-bearing safety logic and must be tested in both directions,
// because the two errors are asymmetric — too narrow merely restores a flake,
// too broad silently converts the repo's only boot-coverage gate into a
// fail-open.
func TestSmokeGate_SkipVsFailInBothDirections(t *testing.T) {
	cases := []struct {
		name     string
		set      bool
		value    string
		wantFail bool
	}{
		{name: "unset means skip (developer laptop default)", set: false, wantFail: false},
		{name: "empty means skip", set: true, value: "", wantFail: false},
		{name: "whitespace means skip", set: true, value: "   ", wantFail: false},
		{name: "0 means skip", set: true, value: "0", wantFail: false},
		{name: "false means skip", set: true, value: "false", wantFail: false},
		{name: "1 means fail", set: true, value: "1", wantFail: true},
		{name: "true means fail", set: true, value: "true", wantFail: true},
		{name: "unparseable non-empty value means fail, never silently skip", set: true, value: "yes", wantFail: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set {
				t.Setenv(requireRuntimeSmokeEnv, tc.value)
			} else {
				// t.Setenv restores the previous value at test end; Unsetenv
				// through it is not available, so unset explicitly and let the
				// outer process environment be restored by the same mechanism.
				t.Setenv(requireRuntimeSmokeEnv, "")
				if err := os.Unsetenv(requireRuntimeSmokeEnv); err != nil {
					t.Fatalf("os.Unsetenv: %v", err)
				}
			}

			rec := &recordingGate{}
			smokeGateSkipf(rec, "no reachable container runtime (%s)", "docker")

			if tc.wantFail {
				if !rec.failed || rec.skipped {
					t.Fatalf("with %s=%q: got failed=%v skipped=%v, want failed=true skipped=false",
						requireRuntimeSmokeEnv, tc.value, rec.failed, rec.skipped)
				}
				if !strings.Contains(rec.msg, requireRuntimeSmokeEnv) {
					t.Errorf("failure message must name %s so the operator knows why a skip became a failure; got %q",
						requireRuntimeSmokeEnv, rec.msg)
				}
			} else {
				if !rec.skipped || rec.failed {
					t.Fatalf("with %s set=%v value=%q: got failed=%v skipped=%v, want failed=false skipped=true",
						requireRuntimeSmokeEnv, tc.set, tc.value, rec.failed, rec.skipped)
				}
			}

			// Either way, the underlying reason must survive: a gate that
			// reports "precondition not met" without saying which one turns a
			// two-second diagnosis into a bisect.
			if !strings.Contains(rec.msg, "no reachable container runtime (docker)") {
				t.Errorf("reason text was dropped from the gate message; got %q", rec.msg)
			}
		})
	}
}
