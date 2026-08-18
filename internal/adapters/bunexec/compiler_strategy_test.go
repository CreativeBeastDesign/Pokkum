package bunexec

import (
	"context"
	"fmt"
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

// svCreateSvelteConfigFmt is the standard SvelteKit svelte.config.js shape
// (kit.adapter, vitePreprocess) for configuring an adapter directly in that
// file rather than via vite.config.ts's sveltekit() plugin options.
//
// Correction (2026-08-17, found during adversarial review of the diff that
// added this constant): its doc comment originally claimed this was
// "captured verbatim from a real `bunx sv create` project" — checked
// directly against real tooling and that claim is false. Neither
// `bunx sv create --add sveltekit-adapter=adapter:node` nor
// `bunx sv add sveltekit-adapter=adapter:node` against an existing project
// (sv@0.17.0) ever writes a svelte.config.js at all; both configure the
// adapter exclusively in vite.config.ts (see realSvCreateAdapterNodeViteConfig
// in sveltekitutils/project_test.go for what that real output actually looks
// like). This constant is therefore a hand-written-but-standard SvelteKit
// config shape — the one any pre-migration or manually-authored project
// would have, and syntactically identical to what SvelteKit's own docs show
// for configuring an adapter in svelte.config.js — not a captured real-tool
// artifact. It is still a valid input for what this test exercises (Prepare's
// dispatch when svelte.config.js governs, i.e. no vite.config.ts is present
// in the fixture dir), since AdapterConfigured only ever checks that the
// package name is referenced, not this file's specific provenance; the
// inaccurate claim is the only thing being corrected here, not the fixture's
// suitability for its actual purpose. See Lessons.md's two 2026-08-16
// fixture-fidelity post-mortems and mem:self_review_checklist row 12/14 for
// why an unverified "captured verbatim" claim is worth catching on its own.
const svCreateSvelteConfigFmt = `import adapter from '%s';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

export default {
	preprocess: vitePreprocess(),
	kit: {
		adapter: adapter()
	}
};
`

func TestPrepare_StrategyDispatch(t *testing.T) {
	cases := []struct {
		name string

		strategy ports.BuildStrategy

		// wantTargetAdapter is the npm package name Prepare must inject into
		// the virtual svelte.config.js for this strategy.
		wantTargetAdapter string

		// svelteConfig is this case's svelte.config.js. Prepare fails fast
		// unless it configures the strategy's own adapter (see
		// checkEffectiveAdapter), so each strategy needs its own — one shared
		// config cannot be correct for three different adapters.
		svelteConfig string

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
			svelteConfig:      fmt.Sprintf(svCreateSvelteConfigFmt, "@sveltejs/adapter-node"),
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
			// The jesterkit adapter has no `sv create` template of its own;
			// validSvelteConfig mirrors testdata/fixtures/sveltekit-basic's
			// real, working svelte.config.js, which is what the real-bun e2e
			// harness builds against.
			svelteConfig: validSvelteConfig,
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
			svelteConfig:      fmt.Sprintf(svCreateSvelteConfigFmt, "@sveltejs/adapter-static"),
			wantEntrypoint: func(dir string) string {
				return ""
			},
			wantOutputDir: func(dir string) string {
				return filepath.Join(dir, ".svelte-kit", "output")
			},
			// Real adapter-static output nests prerendered pages one level
			// deeper, under a "pages" subdirectory (see
			// testdata/fixtures/sveltekit-static's committed
			// .svelte-kit/output/prerendered/pages/index.html) — the
			// staticBuildScript-based tests below exercise Prepare's flatten
			// step against this shape directly; this case only needs the
			// directory to exist for the entrypoint/outputDir/patch-dispatch
			// assertions above, so a minimal nested fixture is enough.
			bunScript: `set -e
mkdir -p .svelte-kit/output/prerendered/pages
printf '<h1>index</h1>' > .svelte-kit/output/prerendered/pages/index.html
exit 0
`,
			wantPatchInvoked: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := newProjectDir(t, validPackageJSON, tc.svelteConfig)
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

			// targetAdapter selection: each case's svelteConfig fixture only
			// references its own strategy's adapter package (see the field's
			// doc comment above), so checkEffectiveAdapter would already have
			// failed Prepare with core.ErrAdapterMisconfigured above (line
			// ~196) had targetAdapter been computed wrong for this strategy —
			// the now-removed PrepareVirtualConfig write this used to inspect
			// was dead output nothing ever read (see Lessons.md 2026-08-17),
			// dropped from Prepare rather than tested against.

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

// TestPrepare_Static_FlattensNestedPrerenderedOutput drives Prepare's static
// branch, through the real function (not FlattenPrerenderedOutput called
// directly, unlike prerendered_flatten_test.go), against a fake `bun run
// build` that emits prerendered output exactly the way a real
// @sveltejs/adapter-static build does: nested under prerendered/pages/,
// cross-checked against the real committed fixture at
// testdata/fixtures/sveltekit-static/.svelte-kit/output/prerendered/pages/
// (which has index.html and about.html, nothing else). Before
// FlattenPrerenderedOutput existed, Prepare's only check here was
// os.Stat(outputDir/"prerendered") existing — which this exact nested shape
// already satisfied — so this test fails against that pre-fix code (the
// asserted flat paths never existed) despite Prepare returning no error at
// all; it is not a Prepare-error regression test, it is a
// packaged-content-shape regression test, matching
// mem:self_review_checklist row 20's "assert the exact final path, not just
// that content landed somewhere."
func TestPrepare_Static_FlattensNestedPrerenderedOutput(t *testing.T) {
	svelteConfig := fmt.Sprintf(svCreateSvelteConfigFmt, "@sveltejs/adapter-static")
	dir := newProjectDir(t, validPackageJSON, svelteConfig)
	putFakeBunOnPath(t, `set -e
mkdir -p .svelte-kit/output/prerendered/pages
printf '<h1>index</h1>' > .svelte-kit/output/prerendered/pages/index.html
printf '<h1>about</h1>' > .svelte-kit/output/prerendered/pages/about.html
exit 0
`)
	c := NewCompiler(discardLogger())

	res, err := c.Prepare(context.Background(), ports.PrepareRequest{
		ProjectDir:      dir,
		Strategy:        ports.StrategyStatic,
		SourceDateEpoch: time.Unix(0, 0),
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v, want nil", err)
	}

	prerenderedDir := filepath.Join(res.OutputDir, "prerendered")
	for _, want := range []string{"index.html", "about.html"} {
		p := filepath.Join(prerenderedDir, want)
		if _, statErr := os.Stat(p); statErr != nil {
			t.Errorf("expected flattened prerendered file %s to exist after Prepare, stat err = %v", p, statErr)
		}
	}
	if _, statErr := os.Stat(filepath.Join(prerenderedDir, "pages")); !os.IsNotExist(statErr) {
		t.Errorf("expected prerendered/pages to no longer exist after Prepare flattened it, stat err = %v", statErr)
	}
	// The nested source shape must be gone, not merely shadowed — a stray
	// prerendered/pages/index.html left behind would mean the packager could
	// still ship the wrong path alongside the right one.
	if _, statErr := os.Stat(filepath.Join(prerenderedDir, "pages", "index.html")); !os.IsNotExist(statErr) {
		t.Errorf("expected prerendered/pages/index.html to no longer exist, stat err = %v", statErr)
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
// .svelte-kit/output staging tree Prepare expects for a static build. The
// prerendered page is written nested under "pages/", matching real
// @sveltejs/adapter-static output (see testdata/fixtures/sveltekit-static's
// committed .svelte-kit/output/prerendered/pages/index.html) rather than
// directly under "prerendered/" — Prepare's flatten step (
// FlattenPrerenderedOutput) is what's responsible for turning this into the
// flat shape the packager and pokkum-static expect, so the fixture must
// start in the real, nested state for that step to mean anything. extra is
// emitted verbatim after the prerendered dir is created (used to optionally
// add client/<fallback>).
func staticBuildScript(extra string) string {
	return "set -e\nmkdir -p .svelte-kit/output/prerendered/pages\n" +
		"printf '<h1>index</h1>' > .svelte-kit/output/prerendered/pages/index.html\n" +
		extra + "exit 0\n"
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
