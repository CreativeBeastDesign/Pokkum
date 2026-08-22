package bunexec

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
)

func routesProject(t *testing.T, viteConfig string) string {
	t.Helper()
	dir := t.TempDir()
	for _, rel := range []string{"src/routes/+page.svelte", "src/routes/dev/+page.svelte", "src/routes/about/+page.svelte"} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("<h1/>"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if viteConfig != "" {
		if err := os.WriteFile(filepath.Join(dir, "vite.config.ts"), []byte(viteConfig), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestStageRoutesMirror_RefusesPreserveSymlinks: the mirror is built from
// symlinks, and preserving them makes every relative import that escapes the
// routes tree fail to resolve. Verified against a real build, where it dies
// with UNRESOLVED_IMPORT — declining here with an accurate reason beats failing
// mid-build with a confusing one.
func TestStageRoutesMirror_RefusesPreserveSymlinks(t *testing.T) {
	dir := routesProject(t, `import { sveltekit } from '@sveltejs/kit/vite';
export default { resolve: { preserveSymlinks: true }, plugins: [sveltekit()] };`)

	_, err := stageRoutesMirror(dir, []string{"/dev"}, "2.70.2", discardLogger())
	if err == nil {
		t.Fatal("stageRoutesMirror() succeeded with preserveSymlinks: true, which produces a build that cannot resolve its own imports")
	}
	if !errors.Is(err, core.ErrInvalidRequest) {
		t.Errorf("error = %v, want it to wrap core.ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), "preserveSymlinks") {
		t.Errorf("error does not name the offending setting: %v", err)
	}
}

// TestStageRoutesMirror_SkipsBelowMinimumKitVersion: below 2.62.0 there is no
// way to pass config inline to the sveltekit() plugin, so overriding
// kit.files.routes would mean editing the user's svelte.config.js.
func TestStageRoutesMirror_SkipsBelowMinimumKitVersion(t *testing.T) {
	dir := routesProject(t, "")

	out, err := stageRoutesMirror(dir, []string{"/dev"}, "2.61.0", discardLogger())
	if err != nil {
		t.Fatalf("error = %v, want a skip rather than a failure", err)
	}
	if out.RoutesDir != "" {
		t.Errorf("RoutesDir = %q, want empty — no mirror should be staged", out.RoutesDir)
	}
	if out.Skipped == "" {
		t.Error("Skipped is empty, so the caller cannot tell the user why their exclusions did nothing")
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".pokkum", "routes")); !os.IsNotExist(statErr) {
		t.Error("a mirror was staged despite the version gate")
	}
}

func TestStageRoutesMirror_StagesMirrorOnSupportedVersion(t *testing.T) {
	dir := routesProject(t, "")

	out, err := stageRoutesMirror(dir, []string{"/dev"}, "2.70.2", discardLogger())
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out.RoutesDir != routesMirrorRelDir {
		t.Errorf("RoutesDir = %q, want %q", out.RoutesDir, routesMirrorRelDir)
	}
	if len(out.ExcludedRoutes) != 1 || out.ExcludedRoutes[0] != "/dev" {
		t.Errorf("ExcludedRoutes = %v, want [/dev]", out.ExcludedRoutes)
	}
	// The mirror must carry the kept routes, or the build loses them.
	for _, keep := range []string{"about", "+page.svelte"} {
		if _, statErr := os.Stat(filepath.Join(dir, ".pokkum", "routes", keep)); statErr != nil {
			t.Errorf("mirror is missing %s: %v", keep, statErr)
		}
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".pokkum", "routes", "dev")); !os.IsNotExist(statErr) {
		t.Error("the excluded route is present in the mirror")
	}
}

// TestStageRoutesMirror_NoMatchStagesNothing: pointing the build at a mirror
// that excludes nothing is pure added risk, so it is not done.
func TestStageRoutesMirror_NoMatchStagesNothing(t *testing.T) {
	dir := routesProject(t, "")

	out, err := stageRoutesMirror(dir, []string{"/nope"}, "2.70.2", discardLogger())
	if err != nil {
		t.Fatalf("error = %v", err)
	}
	if out.RoutesDir != "" {
		t.Errorf("RoutesDir = %q, want empty when nothing matched", out.RoutesDir)
	}
	if len(out.UnmatchedPatterns) != 1 {
		t.Errorf("UnmatchedPatterns = %v, want [/nope]", out.UnmatchedPatterns)
	}
	if _, statErr := os.Stat(filepath.Join(dir, ".pokkum", "routes")); !os.IsNotExist(statErr) {
		t.Error("an empty mirror was left behind")
	}
}

func TestKitSupportsInlineConfig(t *testing.T) {
	cases := map[string]bool{
		"2.62.0": true, "2.70.2": true, "3.0.0": true, "2.61.9": false,
		"1.99.9": false, "^2.70.0": true, "2.62.0-next.1": true,
		"": true, "not-a-version": true, // unreadable: attempt rather than refuse
	}
	for in, want := range cases {
		if got := kitSupportsInlineConfig(in); got != want {
			t.Errorf("kitSupportsInlineConfig(%q) = %v, want %v", in, got, want)
		}
	}
}
