package secretguard

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// benchSourceFragments are ordinary SvelteKit source lines: no secrets, but
// plenty of identifiers that brush up against the generic key rule (config,
// options, token-ish names) without matching it. The no-match path is the
// common case and the expensive one — every rule has to be tried against
// every line before the file can be declared clean — so the corpus is
// deliberately secret-free.
var benchSourceFragments = []string{
	`import { json } from '@sveltejs/kit';`,
	`import type { RequestHandler } from './$types';`,
	`export const prerender = false;`,
	`const options = { method: 'POST', headers: { 'content-type': 'application/json' } };`,
	`export async function load({ params, fetch, depends }) {`,
	`	const res = await fetch(` + "`/api/items/${params.id}`" + `);`,
	`	if (!res.ok) throw error(res.status, 'could not load item');`,
	`	return { item: await res.json() };`,
	`}`,
	`const config = { adapter: adapter({ out: 'build' }), alias: { $lib: './src/lib' } };`,
	`export const actions = { default: async ({ request, cookies }) => {`,
	`	const data = await request.formData();`,
	`	cookies.set('session', sessionId, { path: '/', httpOnly: true });`,
	`}};`,
	`<script lang="ts">let count = $state(0); const inc = () => count++;</script>`,
	`.card { border-radius: 0.5rem; padding: 1rem; background: var(--surface); }`,
	`{ "name": "app", "type": "module", "scripts": { "build": "vite build" } }`,
	`// TODO: move the retry/backoff policy into a shared helper`,
	`const DATABASE_URL = env.DATABASE_URL ?? 'postgres://localhost:5432/dev';`,
	`const apiBase = import.meta.env.VITE_API_BASE ?? 'http://localhost:3000';`,
}

// benchSourceFile renders a plausible source file of roughly size bytes.
func benchSourceFile(rng *rand.Rand, size int) []byte {
	var b strings.Builder
	b.Grow(size + 128)
	for b.Len() < size {
		b.WriteString(benchSourceFragments[rng.Intn(len(benchSourceFragments))])
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// writeBenchSourceTree materialises ~200 source files under root, shaped like
// a real SvelteKit project: routes, lib, tests, config at the root, plus a
// static/ subtree and a node_modules/ that ScanDirectory must prune rather
// than walk.
func writeBenchSourceTree(tb testing.TB, root string, files int) {
	tb.Helper()
	rng := rand.New(rand.NewSource(0x5EC2E7))

	write := func(rel string, content []byte) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			tb.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			tb.Fatalf("write %s: %v", p, err)
		}
	}

	shapes := []string{
		"src/routes/%s/+page.svelte",
		"src/routes/%s/+page.server.ts",
		"src/routes/api/%s/+server.ts",
		"src/lib/components/%s.svelte",
		"src/lib/server/%s.ts",
		"src/lib/utils/deep/nested/%s.ts",
		"tests/%s.spec.ts",
		"static/%s.json",
	}
	for i := 0; i < files; i++ {
		rel := fmt.Sprintf(shapes[i%len(shapes)], fmt.Sprintf("mod%d", i))
		// A few kilobytes each, with the occasional larger module.
		size := 1500 + rng.Intn(6000)
		if i%25 == 0 {
			size = 60000
		}
		write(rel, benchSourceFile(rng, size))
	}

	write("package.json", []byte(`{"name":"bench-app","type":"module"}`))
	write("svelte.config.js", benchSourceFile(rng, 800))
	write(".env", []byte("DATABASE_URL=postgres://localhost:5432/dev\n"))

	// A vendored dependency tree the walk must skip entirely by name. If it
	// were walked instead, this benchmark would silently be measuring
	// something ten times larger than it claims.
	for i := 0; i < 50; i++ {
		write(fmt.Sprintf("node_modules/pkg-%d/dist/index.js", i), benchSourceFile(rng, 4000))
	}
}

// BenchmarkScanDirectory measures a full clean scan of a project source tree:
// the ignore-matcher walk, the per-file binary sniff, and every rule against
// every line of every file.
func BenchmarkScanDirectory(b *testing.B) {
	root := b.TempDir()
	writeBenchSourceTree(b, root, 200)

	adapter := NewAdapter()
	req := ports.SecretScanRequest{ProjectDir: root}

	// Fixture floor. Two ways this benchmark could measure nothing while
	// looking healthy: the tree could contain a planted secret (turning the
	// common no-match path into an early-finding path), or the walk could be
	// bailing out and scanning almost nothing. Assert clean, then assert the
	// scan really did read the tree.
	res, err := adapter.ScanDirectory(context.Background(), req)
	if err != nil {
		b.Fatalf("ScanDirectory: %v", err)
	}
	if !res.Passed {
		b.Fatalf("degenerate fixture: expected a clean tree, got %d matches and %d skips", len(res.Matches), len(res.Skipped))
	}

	var scanned int
	var scannedBytes int64
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		scanned++
		scannedBytes += info.Size()
		return nil
	})
	if err != nil {
		b.Fatalf("measure fixture: %v", err)
	}
	if scanned < 200 {
		b.Fatalf("degenerate fixture: only %d scannable files under %s", scanned, root)
	}
	b.Logf("fixture: %d scannable files, %d bytes", scanned, scannedBytes)

	b.SetBytes(scannedBytes)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := adapter.ScanDirectory(context.Background(), req)
		if err != nil {
			b.Fatalf("ScanDirectory: %v", err)
		}
		if !got.Passed {
			b.Fatal("unexpected finding on a clean tree")
		}
	}
}

// benchMinifiedBundle renders a minified-bundle-shaped file: a handful of
// enormous lines rather than many short ones. Line length is the parameter
// that matters here — scanFile splits on '\n' and runs every rule against
// each resulting line, so one 500 KB line is a very different workload from
// 10,000 fifty-byte lines even at identical total size.
func benchMinifiedBundle(size int) []byte {
	rng := rand.New(rand.NewSource(0x81ED1E))
	chunks := []string{
		`function e(n,t){return n&&n.__esModule?n:{default:n}}`,
		`const t=Object.freeze({__proto__:null,get default(){return o}})`,
		`var r=function(n){return n.replace(/[\s\xA0]+$/g,"")}`,
		`o.exports=function(n,t,r){return n in t?Object.defineProperty(t,n,{value:r}):t[n]=r,t}`,
		`class i extends s{constructor(n){super(n),this.state={loading:!0,error:null}}}`,
		`await import("./chunks/entry.client.js").then(n=>n.start(a,l,{env:{}}))`,
		`const c={apiBase:"https://api.example.com",timeout:3e4,retries:3}`,
	}
	var b strings.Builder
	b.Grow(size + 1024)
	// ~40 lines total, so each is on the order of 100 KB at 4 MB.
	const lines = 40
	perLine := size / lines
	for l := 0; l < lines; l++ {
		start := b.Len()
		for b.Len()-start < perLine {
			b.WriteString(chunks[rng.Intn(len(chunks))])
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// BenchmarkScanFileLarge measures the single-file no-match path over one
// ~4 MB minified bundle with very long lines — the shape a real SvelteKit
// build's server/client chunks have, and the one the 16 MiB scan ceiling
// exists to keep inside coverage. scanFile is unexported, so this lives in
// the package's internal test package alongside guard_test.go.
func BenchmarkScanFileLarge(b *testing.B) {
	const size = 4 << 20

	dir := b.TempDir()
	path := filepath.Join(dir, "bundle.js")
	content := benchMinifiedBundle(size)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		b.Fatalf("write bundle: %v", err)
	}

	// Fixture floor. A skip (over the size ceiling) or a finding would both
	// short-circuit the very path this benchmark claims to measure, and both
	// would still report a healthy-looking ns/op.
	matches, skip, err := scanFile(path, "bundle.js", nil, defaultMaxFileSizeBytes)
	if err != nil {
		b.Fatalf("scanFile: %v", err)
	}
	if skip != nil {
		b.Fatalf("degenerate fixture: file was skipped, not scanned (%s)", skip.Reason)
	}
	if len(matches) != 0 {
		b.Fatalf("degenerate fixture: expected the no-match path, got %d findings", len(matches))
	}
	longest := 0
	for _, line := range strings.Split(string(content), "\n") {
		if len(line) > longest {
			longest = len(line)
		}
	}
	if longest < 10000 {
		b.Fatalf("degenerate fixture: longest line is only %d bytes, this is not minified-bundle shaped", longest)
	}
	b.Logf("fixture: %d bytes, longest line %d bytes", len(content), longest)

	b.SetBytes(int64(len(content)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, gotSkip, err := scanFile(path, "bundle.js", nil, defaultMaxFileSizeBytes)
		if err != nil {
			b.Fatalf("scanFile: %v", err)
		}
		if gotSkip != nil || len(got) != 0 {
			b.Fatal("unexpected skip or finding")
		}
	}
}
