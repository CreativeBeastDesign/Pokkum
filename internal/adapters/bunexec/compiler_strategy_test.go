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
			bunScript: `set -e
mkdir -p build
printf 'export default {};\n' > build/index.js
cat > build/assets.generated.ts <<'EOF'
` + validAssetsGenerated + `EOF
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
