package staticviability

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// This package is a thin pass-through to sveltekitutils, and the classifier's
// real edge cases are tested where the classifier lives. The one piece of
// logic that exists only here is the ctx.Err() check ahead of the delegation,
// and it was covered by nothing: internal/core/staticgate_test.go drives the
// gate against a stub analyzer rather than this adapter, and the sveltekitutils
// tests never construct it.
//
// That branch is worth a test rather than a shrug because of which way it
// fails. If it stopped honouring cancellation it would scan anyway and return
// a real verdict, and a StaticBlocked verdict refuses a build — so a
// regression here turns "the build was cancelled" into "your project cannot
// build statically", which is a different and much more alarming claim.

// writeProjectWithBlocker creates a project whose source the real analyser
// classifies as blocked, so "delegated and scanned" is observably different
// from "short-circuited on cancellation".
func writeProjectWithBlocker(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	routes := filepath.Join(dir, "src", "routes")
	if err := os.MkdirAll(routes, 0o755); err != nil {
		t.Fatalf("[TEST SETUP] mkdir routes: %v", err)
	}
	// Form actions are an unconditional blocker — no flag retires them.
	if err := os.WriteFile(filepath.Join(routes, "+page.server.ts"),
		[]byte("export const actions = {\n\tdefault: async () => ({}),\n};\n"), 0o644); err != nil {
		t.Fatalf("[TEST SETUP] write +page.server.ts: %v", err)
	}
	return dir
}

// TestAdapter_ScansWhenContextIsLive is the premise check for the test below:
// if the fixture stopped producing a decisive verdict, the cancellation test
// would pass while proving nothing, because everything would be StaticUnknown.
func TestAdapter_ScansWhenContextIsLive(t *testing.T) {
	dir := writeProjectWithBlocker(t)

	got := (&Adapter{}).AnalyzeStaticViability(context.Background(), ports.StaticViabilityRequest{ProjectDir: dir})

	if got.Verdict != ports.StaticBlocked {
		t.Fatalf("[TEST SETUP] fixture no longer produces a decisive verdict: got %q (%s); "+
			"the cancellation test below would compare two StaticUnknowns and prove nothing",
			got.Verdict, got.UndecidedWhy)
	}
	if len(got.Blockers) == 0 {
		t.Error("StaticBlocked verdict carried no blockers; the delegation is not returning the real report")
	}
}

// TestAdapter_CancelledContextIsUnknownNotAVerdict covers the branch that
// exists only in this package.
func TestAdapter_CancelledContextIsUnknownNotAVerdict(t *testing.T) {
	dir := writeProjectWithBlocker(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got := (&Adapter{}).AnalyzeStaticViability(ctx, ports.StaticViabilityRequest{ProjectDir: dir})

	if got.Verdict != ports.StaticUnknown {
		t.Errorf("cancelled context produced verdict %q, want %q.\n"+
			"\tThe adapter scanned despite cancellation. Because a StaticBlocked verdict refuses a\n"+
			"\tbuild, this turns \"the build was cancelled\" into \"this project cannot build\n"+
			"\tstatically\" — a different and far more alarming claim than the truth.",
			got.Verdict, ports.StaticUnknown)
	}
	if len(got.Blockers) != 0 {
		t.Errorf("cancelled context returned %d blocker(s); a scan that did not run has no findings", len(got.Blockers))
	}
	if got.UndecidedWhy == "" {
		t.Error("StaticUnknown carried no UndecidedWhy; a caller cannot distinguish " +
			"\"cancelled\" from any other reason the scan could not decide")
	}
}
