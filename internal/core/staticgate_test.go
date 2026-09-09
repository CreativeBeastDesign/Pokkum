package core_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// stubAnalyzer returns a fixed report and records whether it was consulted.
type stubAnalyzer struct {
	report ports.StaticReport
	calls  int
	gotDir string
}

func (s *stubAnalyzer) AnalyzeStaticViability(_ context.Context, req ports.StaticViabilityRequest) ports.StaticReport {
	s.calls++
	s.gotDir = req.ProjectDir
	return s.report
}

// newTestDeps returns Deps able to complete a static build, so a refusal in
// these tests can only have come from the gate under test.
func newTestDeps(t *testing.T) core.Deps {
	t.Helper()
	deps := newFullDeps(io.Discard)
	deps.StaticServer = &mockStaticServerProvider{}
	return deps
}

// staticBuildRequest is a request that builds successfully with these mocks,
// so that any error is attributable to the gate rather than to the fixture.
func staticBuildRequest(t *testing.T) core.BuildRequest {
	t.Helper()
	req := core.BuildRequest{
		ProjectDir: t.TempDir(),
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
	}
	req.Compile.Strategy = core.StrategyStatic
	return req
}

func blockedReport(files ...string) ports.StaticReport {
	r := ports.StaticReport{Verdict: ports.StaticBlocked, FilesScanned: 7, RoutesDir: "src/routes"}
	for _, f := range files {
		r.Blockers = append(r.Blockers, ports.StaticFinding{File: f, Reason: "declares form actions"})
	}
	return r
}

// TestStaticViabilityGate_RefusesABlockedStaticBuild is the reach assertion:
// core.Build itself must refuse, before it reaches the compiler.
func TestStaticViabilityGate_RefusesABlockedStaticBuild(t *testing.T) {
	analyzer := &stubAnalyzer{report: blockedReport("src/routes/signup/+page.server.ts")}
	deps := newTestDeps(t)
	deps.StaticViabilityAnalyzer = analyzer

	req := staticBuildRequest(t)
	_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})

	if err == nil {
		t.Fatal("core.Build succeeded on a blocked static project; the gate did not fire")
	}
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Errorf("error does not wrap ErrInvalidRequest: %v", err)
	}
	// The message must name the offending file — an error that says only
	// "this will not work" leaves the user to guess which route is the problem.
	if !strings.Contains(err.Error(), "src/routes/signup/+page.server.ts") {
		t.Errorf("error does not name the blocking file: %v", err)
	}
	if !strings.Contains(err.Error(), "--strategy=layered") {
		t.Errorf("error does not offer the way forward: %v", err)
	}
	if !strings.Contains(err.Error(), "--allow-server-code-in-static") {
		t.Errorf("error does not mention the override: %v", err)
	}
	if analyzer.calls != 1 {
		t.Errorf("analyzer called %d times, want 1", analyzer.calls)
	}
	if analyzer.gotDir != req.ProjectDir {
		t.Errorf("analyzer got ProjectDir %q, want %q", analyzer.gotDir, req.ProjectDir)
	}
}

// TestStaticViabilityGate_ListsEveryBlocker: a user with four bad routes should
// learn about four, not fix one and re-run to discover the next.
func TestStaticViabilityGate_ListsEveryBlocker(t *testing.T) {
	files := []string{"a/+server.ts", "b/+server.ts", "c/+page.server.ts"}
	deps := newTestDeps(t)
	deps.StaticViabilityAnalyzer = &stubAnalyzer{report: blockedReport(files...)}

	_, err := core.Build(context.Background(), deps, staticBuildRequest(t), core.BuildOptions{})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, f := range files {
		if !strings.Contains(err.Error(), f) {
			t.Errorf("error omits blocker %q:\n%v", f, err)
		}
	}
	if !strings.Contains(err.Error(), "3 files") {
		t.Errorf("error does not report the count: %v", err)
	}
}

// TestStaticViabilityGate_UnknownNeverFailsTheBuild is the over-rejection
// guard, and the most important test in this file.
//
// Lessons.md 2026-08-17: Preflight treated a missing svelte.config.js as proof
// the directory was not a SvelteKit project and blocked every project
// `sv create` produces. An "unknown" verdict is the absence of evidence — a
// project layout this scan does not understand — and must never fail a build.
func TestStaticViabilityGate_UnknownNeverFailsTheBuild(t *testing.T) {
	deps := newTestDeps(t)
	deps.StaticViabilityAnalyzer = &stubAnalyzer{report: ports.StaticReport{
		Verdict:      ports.StaticUnknown,
		UndecidedWhy: "no routes directory at src/routes",
	}}

	_, err := core.Build(context.Background(), deps, staticBuildRequest(t), core.BuildOptions{})

	// err == nil, not "no error mentioning prerendering". These mocks complete
	// a static build, so the strong assertion is available — and a weaker
	// `!strings.Contains(...)` would pass for a build that failed for some
	// entirely different reason (mem:self_review_checklist row 45).
	if err != nil {
		t.Fatalf("an unscannable project was refused; absence of evidence is not evidence of a problem: %v", err)
	}
}

// TestStaticViabilityGate_ViableBuildIsNotRefused: the happy path must not be
// collateral damage.
func TestStaticViabilityGate_ViableBuildIsNotRefused(t *testing.T) {
	deps := newTestDeps(t)
	deps.StaticViabilityAnalyzer = &stubAnalyzer{report: ports.StaticReport{
		Verdict: ports.StaticViable, FilesScanned: 4,
	}}

	if _, err := core.Build(context.Background(), deps, staticBuildRequest(t), core.BuildOptions{}); err != nil {
		t.Fatalf("a viable project was refused: %v", err)
	}
}

// TestStaticViabilityGate_OverrideProceeds: the escape hatch must actually work,
// or a false positive is an outage with no way out.
func TestStaticViabilityGate_OverrideProceeds(t *testing.T) {
	deps := newTestDeps(t)
	deps.StaticViabilityAnalyzer = &stubAnalyzer{report: blockedReport("src/routes/api/+server.ts")}

	req := staticBuildRequest(t)
	req.AllowServerCodeInStatic = true
	if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
		t.Fatalf("--allow-server-code-in-static did not override the gate: %v", err)
	}
}

// TestStaticViabilityGate_NotConsultedForOtherStrategies: every other strategy
// ships a server, so none of these findings apply. Consulting the analyzer at
// all would be wasted work at best and a spurious refusal at worst.
func TestStaticViabilityGate_NotConsultedForOtherStrategies(t *testing.T) {
	for _, strategy := range []core.BuildStrategy{core.StrategyLayered, core.StrategyExe} {
		t.Run(string(strategy), func(t *testing.T) {
			analyzer := &stubAnalyzer{report: blockedReport("src/routes/api/+server.ts")}
			deps := newTestDeps(t)
			deps.StaticViabilityAnalyzer = analyzer

			req := staticBuildRequest(t)
			req.Compile.Strategy = strategy
			_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
			if err != nil && strings.Contains(err.Error(), "cannot be prerendered") {
				// Not err == nil here: a layered/exe build needs mocks a static
				// one does not, so an unrelated error is expected. What must
				// never appear is a refusal from THIS gate.
				t.Fatalf("strategy %s was refused by the static gate: %v", strategy, err)
			}
			if analyzer.calls != 0 {
				t.Errorf("analyzer consulted %d times for strategy %s, want 0", analyzer.calls, strategy)
			}
		})
	}
}

// TestStaticViabilityGate_NilAnalyzerSkips documents the fail-open the many
// test callers rely on. It is safe ONLY because cmd/pokkum's composition-root
// test asserts production always wires one — see that test.
func TestStaticViabilityGate_NilAnalyzerSkips(t *testing.T) {
	deps := newTestDeps(t)
	deps.StaticViabilityAnalyzer = nil

	if _, err := core.Build(context.Background(), deps, staticBuildRequest(t), core.BuildOptions{}); err != nil {
		t.Fatalf("a nil analyzer produced a static-viability refusal: %v", err)
	}
}
