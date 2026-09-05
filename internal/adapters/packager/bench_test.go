package packager

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/layercacheutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/pruneutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// benchJSPool returns a deterministic block of JavaScript-shaped text. Slices
// taken from it compress roughly the way real vendored bundle output does
// (~3-4x under gzip). Content shape is load-bearing for these benchmarks:
// zero-filled fixtures would compress ~1000:1 and make the compressor look
// free, while crypto-random ones would compress not at all and make it look
// uniformly expensive. gzip's window is 32 KB, so drawing every file from one
// shared pool does not create unrealistic cross-file redundancy.
func benchJSPool(size int) []byte {
	fragments := []string{
		"'use strict';Object.defineProperty(exports,'__esModule',{value:true});",
		"function _interopDefault(e){return e&&e.__esModule?e:{default:e}}",
		"const {resolve,join,dirname,relative}=require('node:path');",
		"module.exports=function normalize(input,options){if(!input)return'';",
		"export{default as createClient,type ClientOptions}from './client.js';",
		"exports.parse=function(str,opt){var obj={};var len=str.length;",
		"if(typeof process!=='undefined'&&process.env.NODE_ENV!=='production'){",
		"class Emitter{constructor(){this._events=Object.create(null)}on(k,f){}}",
		"const RE_TOKEN=/\\{\\{\\s*([a-zA-Z0-9_.$]+)\\s*\\}\\}/g;",
		"return new Promise((resolve,reject)=>{setTimeout(resolve,delay)});",
	}
	rng := rand.New(rand.NewSource(0xBEEF01))
	var b strings.Builder
	b.Grow(size + 512)
	for b.Len() < size {
		b.WriteString(fragments[rng.Intn(len(fragments))])
		fmt.Fprintf(&b, "//%d\n", rng.Intn(1<<22))
	}
	return []byte(b.String()[:size])
}

// benchTreeStats records what a generated fixture actually contains, so a
// benchmark can assert its fixture rather than trust it.
type benchTreeStats struct {
	Files int
	Junk  int
	Bytes int64
}

// writeBenchVendorTree materialises a stand-in for a vendored node_modules
// tree under root: numFiles files totalling roughly totalBytes, nested four to
// six directories deep, with a realistic mix of runtime .js/.json, a few
// multi-megabyte native/bundle blobs, and the README/LICENSE/*.d.ts/*.map
// cruft that pruneutils strips.
func writeBenchVendorTree(tb testing.TB, root string, numFiles int, totalBytes int64) benchTreeStats {
	tb.Helper()

	pool := benchJSPool(8 << 20)
	rng := rand.New(rand.NewSource(0x1DEA55))

	slice := func(n int64) []byte {
		if n >= int64(len(pool)) {
			n = int64(len(pool)) - 1
		}
		off := rng.Intn(len(pool) - int(n))
		return pool[off : off+int(n)]
	}

	var stats benchTreeStats
	write := func(rel string, content []byte, junk bool) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			tb.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, content, 0o644); err != nil {
			tb.Fatalf("write %s: %v", p, err)
		}
		stats.Files++
		stats.Bytes += int64(len(content))
		if junk {
			stats.Junk++
		}
	}

	// A few multi-MB files stand in for prebuilt native bindings and the very
	// large single bundles real trees carry (esbuild, rolldown, sharp, ...).
	bigCount, bigSize := 2, int64(1<<20)
	if numFiles >= 2000 {
		bigCount, bigSize = 5, int64(4<<20)
	}
	remaining := totalBytes - int64(bigCount)*bigSize
	smallCount := numFiles - bigCount
	if smallCount < 1 || remaining < int64(smallCount) {
		tb.Fatalf("bad fixture parameters: %d files / %d bytes", numFiles, totalBytes)
	}
	avg := remaining / int64(smallCount)

	// Package directories are the unit of nesting. Each contributes a fixed
	// set of files (some junk) plus a variable number of chunks deeper in the
	// tree, so depth ranges from 2 to 6 segments.
	for pkg := 0; stats.Files < smallCount; pkg++ {
		var dir string
		switch pkg % 3 {
		case 0:
			dir = fmt.Sprintf("@scope%d/pkg-%d", pkg%5, pkg)
		default:
			dir = fmt.Sprintf("pkg-%d", pkg)
		}

		size := func() int64 {
			return avg/2 + rng.Int63n(avg+1)
		}

		fixed := []struct {
			rel  string
			junk bool
		}{
			{dir + "/package.json", false},
			{dir + "/index.js", false},
			{dir + "/dist/index.mjs", false},
			{dir + "/README.md", true},
			{dir + "/LICENSE", true},
			{dir + "/dist/index.d.ts", true},
			{dir + "/dist/index.js.map", true},
		}
		for _, f := range fixed {
			if stats.Files >= smallCount {
				break
			}
			write(f.rel, slice(size()), f.junk)
		}

		// 1-6 extra runtime chunks at depth 4-6, which is what pushes the
		// junk ratio down to a realistic ~30% and the depth up to 6.
		extra := 1 + rng.Intn(6)
		for i := 0; i < extra && stats.Files < smallCount; i++ {
			rel := fmt.Sprintf("%s/dist/esm/internal/chunk-%d.js", dir, i)
			if i%3 == 0 {
				rel = fmt.Sprintf("%s/dist/cjs/lib/util-%d.js", dir, i)
			}
			write(rel, slice(size()), false)
		}
	}

	for i := 0; i < bigCount; i++ {
		write(fmt.Sprintf("pkg-native-%d/dist/native/binding-linux-x64.node", i), slice(bigSize), false)
	}

	return stats
}

// BenchmarkBuildDirectoryTreeLayerWithPruning measures the vendor-layer hot
// path end to end: a WalkDir over the tree, a pruneutils.IsJunk decision per
// entry, a full SHA-256 of every surviving file for the startup-attestation
// records, then tar assembly and single-pass compression.
//
// Two sizes, because the interesting question for any optimisation here is
// how the cost scales with file count versus with bytes: ~500 files / ~5 MB is
// a modest dependency tree, ~5000 files / ~50 MB is a realistic vendored
// node_modules for a production SvelteKit app.
func BenchmarkBuildDirectoryTreeLayerWithPruning(b *testing.B) {
	cases := []struct {
		name  string
		files int
		bytes int64
	}{
		{"small_500files_5MB", 500, 5 << 20},
		{"large_5000files_50MB", 5000, 50 << 20},
	}

	for _, tc := range cases {
		for _, compression := range []ports.CompressionAlgorithm{ports.CompressionGzip, ports.CompressionZstd} {
			b.Run(tc.name+"/"+string(compression), func(b *testing.B) {
				dir := b.TempDir()
				stats := writeBenchVendorTree(b, dir, tc.files, tc.bytes)

				// Fixture floor. A tree with no junk would leave the pruning
				// branch unmeasured; a tree where everything is junk would
				// leave the tar/compress path unmeasured. Both would still
				// report a healthy ns/op.
				if stats.Junk == 0 || stats.Junk == stats.Files {
					b.Fatalf("degenerate fixture: %d/%d files judged junk", stats.Junk, stats.Files)
				}
				b.Logf("fixture: %d files, %d junk, %d bytes on disk", stats.Files, stats.Junk, stats.Bytes)

				opts := pruneutils.PruneOptions{}
				b.SetBytes(stats.Bytes)
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					ctx, cleanup := NewBuildContext(context.Background())
					layer, pruned, records, err := BuildDirectoryTreeLayerWithPruning(
						ctx, ports.LinuxAMD64, dir, "/app/vendor", buildEpoch, compression, opts)
					if err != nil {
						cleanup()
						b.Fatalf("BuildDirectoryTreeLayerWithPruning: %v", err)
					}

					b.StopTimer()
					if layer == nil {
						b.Fatal("nil layer")
					}
					if pruned.FilesPruned != stats.Junk {
						b.Fatalf("pruned %d files, fixture says %d are junk", pruned.FilesPruned, stats.Junk)
					}
					if len(records) != stats.Files-stats.Junk {
						b.Fatalf("got %d attestation records for %d kept files", len(records), stats.Files-stats.Junk)
					}
					// The layer's temp file is deleted here, with the timer
					// stopped, so teardown never lands in the measurement.
					cleanup()
					b.StartTimer()
				}
			})
		}
	}
}

// BenchmarkBuildCustomFileLayer measures the single-file immutable-binary
// layer path — the one the Bun runtime goes through — on both sides of the
// on-disk layer cache.
//
// The fixture is 90 MB, matching the real Bun executable, because the whole
// point of the cache-hit path is that it avoids compressing a blob that
// large; a smaller stand-in would understate the gap between the two paths,
// which is the number this baseline exists to record.
//
// POKKUM_CACHE_DIR is pointed at a temp directory for the whole benchmark, so
// nothing here touches (or is perturbed by) the developer's real
// ~/Library/Caches/pokkum/layers.
func BenchmarkBuildCustomFileLayer(b *testing.B) {
	const binarySize = 90 << 20

	// A compiled runtime is not compressible text: filling it with JS-shaped
	// bytes would make gzip nearly free and both paths meaningless.
	content := make([]byte, binarySize)
	rng := rand.New(rand.NewSource(0xB0071E))
	if _, err := rng.Read(content); err != nil {
		b.Fatalf("generate binary content: %v", err)
	}

	srcDir := b.TempDir()
	srcPath := filepath.Join(srcDir, "bun")
	if err := os.WriteFile(srcPath, content, 0o755); err != nil {
		b.Fatalf("write source binary: %v", err)
	}

	// Production passes req.BunRuntime.SHA256 here: a digest the Bun resolver
	// computed and verified moments earlier. The benchmark does the same, so it
	// measures the layer build rather than a re-hash of 90 MB that no real
	// build performs. (The cache_hit fixture floor below still derives the key
	// from the file itself, which cross-checks that this value is the right one.)
	contentSHA := layercacheutils.ComputeBytesSHA256(content)

	cacheRoot := b.TempDir()
	b.Setenv("POKKUM_CACHE_DIR", cacheRoot)
	cacheDir := layercacheutils.ResolveCacheDir()
	if !strings.HasPrefix(cacheDir, cacheRoot) {
		b.Fatalf("cache dir %q escaped the benchmark temp root %q", cacheDir, cacheRoot)
	}

	b.Run("cache_miss", func(b *testing.B) {
		b.SetBytes(binarySize)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			ctx, cleanup := NewBuildContext(context.Background())
			layer, err := BuildCustomFileLayer(
				ctx, ports.LinuxAMD64, "/usr/local/bin/bun", srcPath, contentSHA, pinnedImmutableBinaryEpoch, ports.CompressionGzip)
			if err != nil {
				cleanup()
				b.Fatalf("BuildCustomFileLayer: %v", err)
			}

			b.StopTimer()
			if layer == nil {
				b.Fatal("nil layer")
			}
			// Emptying the cache is what makes the NEXT iteration a miss.
			// Without it, iteration 2 onwards would silently measure the hit
			// path while this sub-benchmark claimed to measure the miss.
			if err := os.RemoveAll(cacheDir); err != nil {
				b.Fatalf("clear layer cache: %v", err)
			}
			cleanup()
			b.StartTimer()
		}
	})

	b.Run("cache_hit", func(b *testing.B) {
		// Warm the cache once, outside the measurement.
		warmCtx, warmCleanup := NewBuildContext(context.Background())
		if _, err := BuildCustomFileLayer(
			warmCtx, ports.LinuxAMD64, "/usr/local/bin/bun", srcPath, contentSHA, pinnedImmutableBinaryEpoch, ports.CompressionGzip); err != nil {
			warmCleanup()
			b.Fatalf("warm BuildCustomFileLayer: %v", err)
		}
		warmCleanup()

		// Fixture floor: prove the cache entry the hit path depends on is
		// actually on disk. If it were not, every iteration below would take
		// the miss path and this sub-benchmark would report a number for a
		// code path it never executed.
		hash, err := layercacheutils.ComputeFileSHA256(srcPath)
		if err != nil {
			b.Fatalf("ComputeFileSHA256: %v", err)
		}
		key := layercacheutils.ComputeKey("/usr/local/bin/bun", hash, ports.LinuxAMD64, ports.CompressionGzip)
		if _, ok := layercacheutils.Get(cacheDir, key, ports.CompressionGzip); !ok {
			b.Fatalf("cache was not warmed: no entry for key %s in %s", key, cacheDir)
		}

		b.SetBytes(binarySize)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			layer, err := BuildCustomFileLayer(
				context.Background(), ports.LinuxAMD64, "/usr/local/bin/bun", srcPath, contentSHA, pinnedImmutableBinaryEpoch, ports.CompressionGzip)
			if err != nil {
				b.Fatalf("BuildCustomFileLayer: %v", err)
			}
			if layer == nil {
				b.Fatal("nil layer")
			}
		}
	})
}
