package bunexec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// --- Prepare: strategy-dispatch behavior-preservation (golden master) -------
//
// This is not a test of a refactor: it pins Prepare's current per-strategy
// dispatch logic (targetAdapter selection, entrypoint/outputDir shape, which
// post-build validation runs, and whether patchPrerenderedHandler is invoked)
// so that adding a future ports.BuildStrategy value without a branch, or an
// accidental behavior change to an existing branch, fails a test instead of
// shipping silently. See compiler.go's Prepare (~line 230) for the logic this
// characterizes.

// validAssetsGenerated is a minimal assets.generated.ts that satisfies
// assetsFileRe (see assets.go) so normalizeGeneratedAssetsFile succeeds
// without needing a real bun/adapter run.
const validAssetsGenerated = `// Auto-generated asset imports
// @ts-nocheck
import asset_0 from "../client/favicon.png" with { type: "file" };

export const assetMap = new Map([
  ["/favicon.png", asset_0],
]);

export const assets = {
  asset_0,
};
`

// validHandlerJS is a minimal generated adapter-node handler.js containing a
// pattern patchPrerenderedEnv (see prerendered_patch.go) recognizes and
// rewrites.
const validHandlerJS = `const p = path.join(dir, "prerendered");
`

func TestPrepare_StrategyDispatch(t *testing.T) {
	cases := []struct {
		name string

		strategy ports.BuildStrategy

		// wantTargetAdapter is the npm package name Prepare must inject into
		// the virtual svelte.config.js for this strategy.
		wantTargetAdapter string

		// wantEntrypoint/wantOutputDir compute the expected
		// PrepareResult.EntrypointPath/OutputDir from the project dir.
		wantEntrypoint func(projectDir string) string
		wantOutputDir  func(projectDir string) string

		// bunScript is the fake "bun run build" body: it must materialize
		// whatever this strategy's post-build validation checks for, so
		// Prepare can run past bun invocation into the dispatch logic under
		// test without a real bun/adapter on PATH.
		bunScript string

		// wantPatchInvoked pins whether patchPrerenderedHandler runs
		// (ports.StrategyLayered only, per compiler.go ~line 344).
		wantPatchInvoked bool
	}{
		{
			name:              "layered",
			strategy:          ports.StrategyLayered,
			wantTargetAdapter: "@sveltejs/adapter-node",
			wantEntrypoint: func(dir string) string {
				return filepath.Join(dir, "build", "index.js")
			},
			wantOutputDir: func(dir string) string {
				return filepath.Join(dir, "build")
			},
			// Deliberately does NOT write build/assets.generated.ts: that file
			// is exclusively a @jesterkit/exe-sveltekit artifact (the exe
			// strategy's adapter). A real @sveltejs/adapter-node build (what
			// StrategyLayered actually uses) never produces one — an earlier
			// version of this fixture wrote a fake one anyway, which
			// accidentally satisfied a normalization step that was, at the
			// time, incorrectly applied to every non-static strategy instead
			// of StrategyExe only, masking that bug entirely. See Lessons.md.
			bunScript: `set -e
mkdir -p build
printf 'export default {};\n' > build/index.js
cat > build/handler.js <<'EOF'
` + validHandlerJS + `EOF
exit 0
`,
			wantPatchInvoked: true,
		},
		{
			name:              "exe",
			strategy:          ports.StrategyExe,
			wantTargetAdapter: "@jesterkit/exe-sveltekit",
			wantEntrypoint: func(dir string) string {
				return filepath.Join(dir, ".svelte-kit", "jesterkit-sveltekit", "temp-server", "index.ts")
			},
			wantOutputDir: func(dir string) string {
				return filepath.Join(dir, ".svelte-kit", "jesterkit-sveltekit")
			},
			bunScript: `set -e
mkdir -p .svelte-kit/jesterkit-sveltekit/temp-server
printf 'export default {};\n' > .svelte-kit/jesterkit-sveltekit/temp-server/index.ts
cat > .svelte-kit/jesterkit-sveltekit/temp-server/assets.generated.ts <<'EOF'
` + validAssetsGenerated + `EOF
exit 0
`,
			wantPatchInvoked: false,
		},
		{
			name:              "static",
			strategy:          ports.StrategyStatic,
			wantTargetAdapter: "@sveltejs/adapter-static",
			wantEntrypoint: func(dir string) string {
				return ""
			},
			wantOutputDir: func(dir string) string {
				return filepath.Join(dir, ".svelte-kit", "output")
			},
			bunScript: `set -e
mkdir -p .svelte-kit/output/prerendered
exit 0
`,
			wantPatchInvoked: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newProjectDir(t, validPackageJSON, validSvelteConfig)
			putFakeBunOnPath(t, tc.bunScript)
			c := NewCompiler(discardLogger())

			result, err := c.Prepare(context.Background(), ports.PrepareRequest{
				ProjectDir:      dir,
				Strategy:        tc.strategy,
				SourceDateEpoch: time.Unix(0, 0),
			})
			if err != nil {
				t.Fatalf("Prepare() error = %v, want nil", err)
			}

			// entrypoint / outputDir shape.
			wantEntrypoint := tc.wantEntrypoint(dir)
			if result.EntrypointPath != wantEntrypoint {
				t.Errorf("EntrypointPath = %q, want %q", result.EntrypointPath, wantEntrypoint)
			}
			wantOutputDir := tc.wantOutputDir(dir)
			if result.OutputDir != wantOutputDir {
				t.Errorf("OutputDir = %q, want %q", result.OutputDir, wantOutputDir)
			}

			// targetAdapter selection: the virtual svelte.config.js Prepare
			// injects must reference the strategy's adapter package.
			virtualConfig, err := os.ReadFile(filepath.Join(dir, ".pokkum", "svelte.config.js"))
			if err != nil {
				t.Fatalf("read injected virtual svelte.config.js: %v", err)
			}
			if !strings.Contains(string(virtualConfig), tc.wantTargetAdapter) {
				t.Errorf("virtual svelte.config.js does not reference target adapter %q:\n%s", tc.wantTargetAdapter, virtualConfig)
			}

			// prerendered-handler patch dispatch: layered only. Non-layered
			// strategies never get a handler.js fixture above, so if Prepare
			// mistakenly tried to patch one it would have failed already
			// (patchPrerenderedHandler errors when it finds none) — the
			// staged file's presence/absence in .pokkum is the positive
			// confirmation either way.
			stagedHandler := filepath.Join(dir, ".pokkum", "handler.js")
			_, statErr := os.Stat(stagedHandler)
			patchInvoked := statErr == nil
			if patchInvoked != tc.wantPatchInvoked {
				t.Errorf("patchPrerenderedHandler invoked = %v, want %v (staged handler stat err = %v)", patchInvoked, tc.wantPatchInvoked, statErr)
			}
			if tc.wantPatchInvoked {
				data, err := os.ReadFile(stagedHandler)
				if err != nil {
					t.Fatalf("read staged handler.js: %v", err)
				}
				if !strings.Contains(string(data), "POKKUM_PRERENDERED_DIR") {
					t.Errorf("staged handler.js was not patched to honor POKKUM_PRERENDERED_DIR:\n%s", data)
				}
			}
		})
	}
}

// --- Prepare: opt-in SPA-fallback detection (StrategyStatic) ----------------
//
// These tests drive Prepare's static branch through real projects whose
// svelte.config.js configures (or not) an adapter-static `fallback`, using the
// same fake-bun harness as TestPrepare_StrategyDispatch. They pin the
// config-driven + output-verified detection contract: the fallback leaf name
// comes from the user's config (readConfigSource), the emitted file must exist
// in the client staging, and a configured-but-unemitted (or path-escaping)
// fallback is a hard Prepare failure — never a silently dropped SPA shell.

// fallbackStaticConfig is a minimal static svelte.config.js with an
// adapter-static SPA fallback set.
const fallbackStaticConfig = `
import adapter from "@sveltejs/adapter-static";

export default {
	kit: {
		adapter: adapter({ fallback: "200.html" })
	}
};
`

// noFallbackStaticConfig is a static config with no SPA fallback.
const noFallbackStaticConfig = `
import adapter from "@sveltejs/adapter-static";

export default {
	kit: {
		adapter: adapter({})
	}
};
`

// staticBuildScript returns a fake `bun run build` body that emits the
// .svelte-kit/output staging tree Prepare expects for a static build. extra is
// emitted verbatim after the prerendered dir is created (used to optionally
// add client/<fallback>).
func staticBuildScript(extra string) string {
	return "set -e\nmkdir -p .svelte-kit/output/prerendered\n" + extra + "exit 0\n"
}

func TestPrepare_StaticFallback_Emitted(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, fallbackStaticConfig)
	putFakeBunOnPath(t, staticBuildScript("mkdir -p .svelte-kit/output/client\nprintf '<h1>spa</h1>' > .svelte-kit/output/client/200.html\n"))
	c := NewCompiler(discardLogger())

	res, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir:      dir,
		Strategy:        ports.StrategyStatic,
		SourceDateEpoch: time.Unix(0, 0),
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v, want nil", err)
	}
	if res.StaticFallbackRelPath != "200.html" {
		t.Errorf("StaticFallbackRelPath = %q, want %q", res.StaticFallbackRelPath, "200.html")
	}
}

func TestPrepare_StaticFallback_ConfiguredButNotEmitted(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, fallbackStaticConfig)
	// The bun build emits prerendered but NOT client/200.html — the SPA shell
	// was configured yet never produced.
	putFakeBunOnPath(t, staticBuildScript(""))
	c := NewCompiler(discardLogger())

	_, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir:      dir,
		Strategy:        ports.StrategyStatic,
		SourceDateEpoch: time.Unix(0, 0),
	})
	if err == nil {
		t.Fatal("Prepare succeeded, want error for a configured-but-unemitted fallback")
	}
	if !strings.Contains(err.Error(), "not emitted") {
		t.Errorf("error = %v, want to name the unemitted fallback", err)
	}
}

func TestPrepare_StaticFallback_None(t *testing.T) {
	dir := newProjectDir(t, validPackageJSON, noFallbackStaticConfig)
	putFakeBunOnPath(t, staticBuildScript(""))
	c := NewCompiler(discardLogger())

	res, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir:      dir,
		Strategy:        ports.StrategyStatic,
		SourceDateEpoch: time.Unix(0, 0),
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v, want nil", err)
	}
	if res.StaticFallbackRelPath != "" {
		t.Errorf("StaticFallbackRelPath = %q, want empty when no fallback is configured", res.StaticFallbackRelPath)
	}
}

func TestPrepare_StaticFallback_PathEscapeRejected(t *testing.T) {
	cfg := `
import adapter from "@sveltejs/adapter-static";
export default { kit: { adapter: adapter({ fallback: "../../../etc/passwd" }) } };
`
	dir := newProjectDir(t, validPackageJSON, cfg)
	putFakeBunOnPath(t, staticBuildScript(""))
	c := NewCompiler(discardLogger())

	_, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir:      dir,
		Strategy:        ports.StrategyStatic,
		SourceDateEpoch: time.Unix(0, 0),
	})
	if err == nil {
		t.Fatal("Prepare succeeded, want error for a path-escaping fallback name")
	}
	if !strings.Contains(err.Error(), "not a plain filename") {
		t.Errorf("error = %v, want to reject the non-plain fallback name", err)
	}
}
