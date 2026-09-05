package pruneutils_test

import (
	"fmt"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/pruneutils"
)

// benchPackages are real npm package names (scoped and unscoped) taken from a
// SvelteKit project's node_modules, so the corpus below exercises the same
// path shapes IsJunk sees in production rather than uniform synthetic ones.
var benchPackages = []string{
	"react", "react-dom", "svelte", "vite", "esbuild", "rollup", "typescript",
	"cookie", "devalue", "kleur", "mime", "sade", "sirv", "totalist",
	"magic-string", "acorn", "picocolors", "chokidar", "postcss", "nanoid",
	"@sveltejs/kit", "@sveltejs/adapter-node", "@sveltejs/vite-plugin-svelte",
	"@jridgewell/gen-mapping", "@jridgewell/trace-mapping", "@jridgewell/sourcemap-codec",
	"@rollup/rollup-linux-x64-gnu", "@esbuild/darwin-arm64", "@polka/url",
	"@types/node", "@types/cookie",
}

// benchJunkCorpus builds a few hundred node_modules-relative paths mixing
// junk and keep verdicts in roughly the proportion a real vendored tree has
// (~30% junk). Both verdicts matter: a keep verdict is the expensive case
// because it has to fall through every DefaultJunkPatterns entry before
// returning false, while a junk verdict short-circuits at whichever pattern
// matches first.
func benchJunkCorpus() []string {
	// shapes are templated per package: %s is the package directory.
	shapes := []string{
		// Kept: real runtime files.
		"%s/index.js",
		"%s/package.json",
		"%s/dist/index.mjs",
		"%s/dist/esm/chunk-a1b2c3.js",
		"%s/dist/cjs/internal/resolve.cjs",
		"%s/src/lib/utils/deep/helper.js",
		"%s/dist/native/binding-linux-x64.node",
		// Pruned: documentation, TypeScript surface, sourcemaps, tooling.
		"%s/README.md",
		"%s/LICENSE",
		"%s/CHANGELOG.md",
		"%s/dist/index.d.ts",
		"%s/dist/index.js.map",
		"%s/.npmignore",
		"%s/test/index.test.js",
	}

	corpus := make([]string, 0, len(benchPackages)*len(shapes))
	for _, pkg := range benchPackages {
		for _, shape := range shapes {
			corpus = append(corpus, fmt.Sprintf(shape, pkg))
		}
	}
	return corpus
}

// benchJunkSink defeats dead-code elimination of the IsJunk call.
var benchJunkSink bool

// BenchmarkIsJunk measures one IsJunk decision, averaged over a corpus of
// realistic node_modules paths. ns/op is therefore per path, not per corpus
// sweep — BuildDirectoryTreeLayerWithPruning calls this once per walked file,
// so per-path is the unit an optimisation has to move.
func BenchmarkIsJunk(b *testing.B) {
	corpus := benchJunkCorpus()

	// Sanity floor on the fixture itself: a corpus that produced only one
	// verdict would benchmark one branch and silently claim to cover both.
	var junk int
	for _, p := range corpus {
		if pruneutils.IsJunk(p, false, pruneutils.PruneOptions{}) {
			junk++
		}
	}
	if junk == 0 || junk == len(corpus) {
		b.Fatalf("degenerate corpus: %d/%d paths judged junk", junk, len(corpus))
	}
	b.Logf("corpus: %d paths, %d junk, %d kept", len(corpus), junk, len(corpus)-junk)

	cases := []struct {
		name string
		opts pruneutils.PruneOptions
	}{
		{"defaults", pruneutils.PruneOptions{}},
		{"keep_sourcemap", pruneutils.PruneOptions{KeepSourcemap: true}},
		// KeepPatterns is the expensive configuration: every path is matched
		// against the user's globs before the default patterns are consulted.
		{"keep_patterns", pruneutils.PruneOptions{KeepPatterns: []string{"**/*.node", "**/dist/**/*.mjs", "**/LICENSE"}}},
		{"no_prune", pruneutils.PruneOptions{NoPrune: true}},
	}

	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				benchJunkSink = pruneutils.IsJunk(corpus[i%len(corpus)], false, tc.opts)
			}
		})
	}
}
