package ignoreutils

import (
	"fmt"
	"testing"
)

// benchPathCorpus builds a few hundred project-relative paths at varying
// depth, shaped like a real SvelteKit project rather than a uniform synthetic
// tree. Depth is the parameter that matters: Match walks every ancestor
// prefix of relPath and evaluates every rule against each one, so a
// six-segment path costs six times a one-segment path.
//
// The mix deliberately spans all three outcomes DefaultPatterns produces:
// paths excluded by a basename glob (*.map, .env*), paths excluded by an
// ancestor directory (.git/, storybook-static/), and the common case of a
// path no rule matches at all — which is the expensive one, since it can
// only be decided after every rule has been tried against every ancestor.
func benchPathCorpus() []string {
	var corpus []string

	// Depth 1-2: project root and top-level source.
	corpus = append(corpus,
		"package.json", "svelte.config.js", "vite.config.ts", "tsconfig.json",
		"README.md", ".env", ".env.local", ".env.production", ".gitignore",
		"src/app.html", "src/app.d.ts", "src/hooks.server.ts",
	)

	// Depth 3-6: routes, components, static assets, build output.
	for i := 0; i < 30; i++ {
		corpus = append(corpus,
			fmt.Sprintf("src/routes/blog/%d/+page.svelte", i),
			fmt.Sprintf("src/routes/blog/%d/+page.server.ts", i),
			fmt.Sprintf("src/lib/components/ui/button/Button%d.svelte", i),
			fmt.Sprintf("src/lib/server/db/queries/select%d.ts", i),
			fmt.Sprintf("src/lib/utils/format/date/relative%d.ts", i),
			fmt.Sprintf("static/images/gallery/thumb%d.webp", i),
			fmt.Sprintf("build/client/_app/immutable/chunks/entry.%d.js", i),
			fmt.Sprintf("build/client/_app/immutable/chunks/entry.%d.js.map", i),
			fmt.Sprintf(".svelte-kit/output/server/nodes/%d.js", i),
			fmt.Sprintf("storybook-static/sb-manager/globals-runtime-%d.js", i),
			fmt.Sprintf(".git/objects/%02x/%040x", i, i),
			fmt.Sprintf("node_modules/.cache/vite/deps/dep_%d.js", i),
		)
	}
	return corpus
}

// benchMatchSink defeats dead-code elimination of the Match call.
var benchMatchSink bool

// BenchmarkMatcherMatch measures one Match decision against the package's
// default patterns, averaged over the corpus above. ns/op is per path:
// secretguard's ScanDirectory and remotecacheutils' ComputeSourceTreeHash both
// call Match once per walked entry, so per-path is the unit that scales with
// project size.
func BenchmarkMatcherMatch(b *testing.B) {
	m, err := New(DefaultPatterns())
	if err != nil {
		b.Fatalf("compile default patterns: %v", err)
	}

	corpus := benchPathCorpus()

	// Fixture floor: a corpus that landed entirely on one verdict would
	// benchmark one branch while claiming to cover both.
	var matched int
	for _, p := range corpus {
		if m.Match(p, false) {
			matched++
		}
	}
	if matched == 0 || matched == len(corpus) {
		b.Fatalf("degenerate corpus: %d/%d paths matched", matched, len(corpus))
	}
	b.Logf("corpus: %d paths, %d ignored, %d kept", len(corpus), matched, len(corpus)-matched)

	b.Run("default_patterns", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			benchMatchSink = m.Match(corpus[i%len(corpus)], false)
		}
	})

	// A project's own .pokkumignore is layered on top of the defaults, which
	// is what a real build actually evaluates — Load returns exactly this
	// concatenation. More rules means more work per ancestor prefix.
	projectPatterns := []string{
		"coverage/", "*.tsbuildinfo", "playwright-report/", "test-results/",
		"src/lib/paraglide/", "!src/lib/paraglide/runtime.js", "**/*.stories.svelte",
	}
	withProject, err := New(append(DefaultPatterns(), projectPatterns...))
	if err != nil {
		b.Fatalf("compile default+project patterns: %v", err)
	}
	b.Run("default_plus_project_patterns", func(b *testing.B) {
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			benchMatchSink = withProject.Match(corpus[i%len(corpus)], false)
		}
	})
}
