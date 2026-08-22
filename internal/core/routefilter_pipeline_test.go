package core_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// recordingRouteFilter captures whether the pipeline actually reached the
// filter, and with what.
type recordingRouteFilter struct {
	calls  []ports.RouteFilterRequest
	result ports.RouteFilterResult
	err    error
}

func (r *recordingRouteFilter) FilterRoutes(_ context.Context, req ports.RouteFilterRequest) (ports.RouteFilterResult, error) {
	r.calls = append(r.calls, req)
	return r.result, r.err
}

// TestBuild_InvokesRouteFilterWhenRoutesAreExcluded exists because a branch
// that never executes is indistinguishable from a working one in every other
// test: the build stays green, the flag parses, the config merges, and no
// route is ever removed. Asserting the port was *called* — not merely wired —
// is the only thing that catches that (self_review_checklist rows 16b and 27).
func TestBuild_InvokesRouteFilterWhenRoutesAreExcluded(t *testing.T) {
	deps := newFullDeps(io.Discard)
	rf := &recordingRouteFilter{}
	deps.RouteFilter = rf

	req := core.BuildRequest{
		ProjectDir:    "/abs/project",
		Repo:          "ghcr.io/example/app",
		Platforms:     []core.Platform{core.LinuxAMD64},
		Tags:          []string{"v1.0.0"},
		ExcludeRoutes: []string{"/storybook", "/admin/**"},
	}

	if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if len(rf.calls) != 1 {
		t.Fatalf("RouteFilter called %d times, want exactly 1", len(rf.calls))
	}
	got := rf.calls[0]
	if len(got.Patterns) != 2 || got.Patterns[0] != "/storybook" || got.Patterns[1] != "/admin/**" {
		t.Errorf("Patterns = %v, want [/storybook /admin/**]", got.Patterns)
	}
	if got.PrerenderedDir == "" {
		t.Error("PrerenderedDir is empty, so the filter would scan nothing")
	}
}

// TestBuild_SkipsRouteFilterWhenNoRoutesAreExcluded pins the other direction:
// the overwhelmingly common build configures no exclusions and must not pay
// for a tree walk, nor risk deleting anything.
func TestBuild_SkipsRouteFilterWhenNoRoutesAreExcluded(t *testing.T) {
	deps := newFullDeps(io.Discard)
	rf := &recordingRouteFilter{}
	deps.RouteFilter = rf

	req := core.BuildRequest{
		ProjectDir: "/abs/project",
		Repo:       "ghcr.io/example/app",
		Platforms:  []core.Platform{core.LinuxAMD64},
		Tags:       []string{"v1.0.0"},
	}

	if _, err := core.Build(context.Background(), deps, req, core.BuildOptions{}); err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(rf.calls) != 0 {
		t.Errorf("RouteFilter called %d times with no exclusions configured, want 0", len(rf.calls))
	}
}

// TestBuild_RouteFilterErrorFailsTheBuild covers the one case that is not a
// warning: if the filter itself fails, the routes the operator asked to
// exclude may still be in the tree, and shipping the image anyway would
// silently include exactly what they asked to keep out.
func TestBuild_RouteFilterErrorFailsTheBuild(t *testing.T) {
	deps := newFullDeps(io.Discard)
	sentinel := errors.New("permission denied")
	deps.RouteFilter = &recordingRouteFilter{err: sentinel}

	req := core.BuildRequest{
		ProjectDir:    "/abs/project",
		Repo:          "ghcr.io/example/app",
		Platforms:     []core.Platform{core.LinuxAMD64},
		Tags:          []string{"v1.0.0"},
		ExcludeRoutes: []string{"/storybook"},
	}

	_, err := core.Build(context.Background(), deps, req, core.BuildOptions{})
	if err == nil {
		t.Fatal("Build() succeeded despite the route filter failing; the excluded route may still be in the image")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("Build() error = %v, want it to wrap %v", err, sentinel)
	}
}
