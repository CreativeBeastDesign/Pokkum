package remotecacheutils_test

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/remotecacheutils"
)

// benchSourcePool returns a block of source-shaped text used as the content
// for the synthetic project tree. Content matters here only insofar as it has
// to be real bytes SHA-256 must actually read — but keeping it realistic
// keeps the average file size honest.
func benchSourcePool(size int) []byte {
	fragments := []string{
		"import { error, json } from '@sveltejs/kit';\n",
		"export const load = async ({ params, locals }) => {\n",
		"\tconst rows = await locals.db.select().from(items).limit(50);\n",
		"\treturn { rows };\n};\n",
		"export interface Item { id: string; title: string; createdAt: Date }\n",
		"<script lang=\"ts\">\n\tlet { data } = $props();\n</script>\n",
		".wrapper { display: grid; grid-template-columns: repeat(3, minmax(0,1fr)); }\n",
		"{ \"compilerOptions\": { \"target\": \"ES2022\", \"moduleResolution\": \"bundler\" } }\n",
	}
	rng := rand.New(rand.NewSource(0xCAFE01))
	var b strings.Builder
	b.Grow(size + 256)
	for b.Len() < size {
		b.WriteString(fragments[rng.Intn(len(fragments))])
		fmt.Fprintf(&b, "// %d\n", rng.Intn(1<<20))
	}
	return []byte(b.String()[:size])
}

// writeBenchProjectTree materialises numFiles source files under root at
// varying depth, plus a handful of executable scripts and a symlink — the two
// entry kinds hashTreeEntry treats specially, and therefore the two that
// would go unmeasured if the fixture were regular files only.
//
// It also plants a .svelte-kit/ subtree (skipped via IgnoredBuildDirs) and
// some *.map files (skipped via the ignore matcher's default patterns), so
// the walk's skip paths are exercised rather than assumed away.
func writeBenchProjectTree(tb testing.TB, root string, numFiles int) (hashed int) {
	tb.Helper()

	pool := benchSourcePool(1 << 20)
	rng := rand.New(rand.NewSource(0xD00D1E))

	slice := func(n int) []byte {
		if n >= len(pool) {
			n = len(pool) - 1
		}
		off := rng.Intn(len(pool) - n)
		return pool[off : off+n]
	}

	write := func(rel string, content []byte, mode os.FileMode) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			tb.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, content, mode); err != nil {
			tb.Fatalf("write %s: %v", p, err)
		}
	}

	shapes := []string{
		"src/routes/%s/+page.svelte",
		"src/routes/%s/+page.server.ts",
		"src/routes/(app)/dashboard/%s/+page.svelte",
		"src/lib/components/ui/%s/index.ts",
		"src/lib/server/db/schema/%s.ts",
		"src/lib/utils/format/deep/%s.ts",
		"tests/e2e/%s.spec.ts",
		"static/data/%s.json",
		"messages/en/%s.json",
	}
	for i := 0; i < numFiles; i++ {
		rel := fmt.Sprintf(shapes[i%len(shapes)], fmt.Sprintf("mod%d", i))
		size := 800 + rng.Intn(5000)
		if i%50 == 0 {
			size = 120000
		}
		mode := os.FileMode(0o644)
		if i%97 == 0 {
			// An executable file: hashTreeEntry tags these "x", not "f".
			rel = fmt.Sprintf("scripts/tool%d.sh", i)
			mode = 0o755
		}
		write(rel, slice(size), mode)
		hashed++
	}

	// Symlinks are hashed by their target string rather than dereferenced.
	for i := 0; i < 5; i++ {
		link := filepath.Join(root, fmt.Sprintf("src/lib/link%d.ts", i))
		if err := os.Symlink(fmt.Sprintf("./components/ui/mod%d/index.ts", i), link); err != nil {
			tb.Fatalf("symlink: %v", err)
		}
		hashed++
	}

	// Skipped by IgnoredBuildDirs — the whole subtree is abandoned at the
	// directory, never walked into.
	for i := 0; i < 200; i++ {
		write(fmt.Sprintf(".svelte-kit/output/server/nodes/%d.js", i), slice(3000), 0o644)
		write(fmt.Sprintf("build/client/_app/immutable/chunk-%d.js", i), slice(3000), 0o644)
	}
	// Skipped per-file by ignoreutils.DefaultPatterns ("*.map"), inside a
	// directory that IS walked — the other skip path, and the one that costs
	// a Match call per entry.
	for i := 0; i < 50; i++ {
		write(fmt.Sprintf("static/_app/immutable/chunk-%d.js.map", i), slice(4000), 0o644)
		write(fmt.Sprintf("static/_app/immutable/chunk-%d.js", i), slice(9000), 0o644)
		hashed++ // only the .js sibling survives the matcher
	}

	return hashed
}

// BenchmarkComputeSourceTreeHash measures the remote-cache input hash over a
// synthetic project tree of ~1000 hashed files: an ignore-matcher-filtered
// walk, a sort, then an Lstat plus a full SHA-256 read of every surviving
// entry. This runs once per build before anything else can start, so it sits
// directly on the critical path of a cache probe.
func BenchmarkComputeSourceTreeHash(b *testing.B) {
	root := b.TempDir()
	want := writeBenchProjectTree(b, root, 1000)

	// Fixture floor: the hash must be a real 64-hex-character digest, and it
	// must be stable across calls. A walk that silently aborted early would
	// still return a digest — of a much smaller tree — and look fine.
	first, err := remotecacheutils.ComputeSourceTreeHash(root)
	if err != nil {
		b.Fatalf("ComputeSourceTreeHash: %v", err)
	}
	if len(first) != 64 {
		b.Fatalf("degenerate digest %q", first)
	}
	second, err := remotecacheutils.ComputeSourceTreeHash(root)
	if err != nil {
		b.Fatalf("ComputeSourceTreeHash (second call): %v", err)
	}
	if first != second {
		b.Fatalf("hash is not stable across calls: %s != %s", first, second)
	}

	// Independently measure what the walk should actually be reading, so the
	// MB/s figure below means something and a fixture that shrank would be
	// caught rather than quietly reported.
	var bytesOnDisk int64
	if err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if remotecacheutils.IgnoredBuildDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".map") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		bytesOnDisk += info.Size()
		return nil
	}); err != nil {
		b.Fatalf("measure fixture: %v", err)
	}
	if bytesOnDisk < 1<<20 {
		b.Fatalf("degenerate fixture: only %d bytes to hash under %s", bytesOnDisk, root)
	}
	b.Logf("fixture: ~%d hashed entries, %d bytes", want, bytesOnDisk)

	b.SetBytes(bytesOnDisk)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := remotecacheutils.ComputeSourceTreeHash(root)
		if err != nil {
			b.Fatalf("ComputeSourceTreeHash: %v", err)
		}
		if got != first {
			b.Fatalf("hash drifted: %s != %s", got, first)
		}
	}
}
