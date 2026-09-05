package main

import (
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
)

// Baseline benchmarks for the startup attestation walk.
//
// verifyAttestation runs once per container start, before the child process is
// exec'd, so its cost is pure added startup latency for every single container
// Pokkum produces. Today walkAttestRoot reads each file whole
// (root.ReadFile) and hashes it with sha256.Sum256, single-threaded, one file
// at a time — these numbers are the "before" for the planned streaming +
// parallel-hashing change.
//
// The fixture is a synthetic /app-shaped tree: the same six attestation roots
// the real image has, nested the way a built SvelteKit image nests them
// (client/_app/immutable/..., node_modules/<pkg>/dist/..., vendor/@sveltejs/...),
// with a handful of multi-MB files among many small ones so the mix of
// per-file overhead versus raw hashing throughput is realistic.

// benchAttestSpec describes one synthetic tree size.
type benchAttestSpec struct {
	name     string
	files    int   // total regular files across all roots
	bigFiles int   // how many of those are multi-MB
	bigSize  int64 // size of each of those
	total    int64 // approximate total tree size in bytes
}

// benchAttestFixture is a built tree plus the numbers describing it.
type benchAttestFixture struct {
	appDir string
	roots  []string
	files  int
	bytes  int64
}

// benchHashName produces a stable, hash-looking basename, mirroring the shape
// of SvelteKit's content-hashed build output.
func benchHashName(prefix string, i int) string {
	return fmt.Sprintf("%s-%08x", prefix, uint32(i)*2654435761)
}

// benchAttestRelPaths returns n distinct paths relative to the /app dir,
// distributed across the six attestation roots in roughly the proportions a
// real layered image has (node_modules and client dominate the file count).
func benchAttestRelPaths(n int) []string {
	shapes := []struct {
		share int // percent of the total file count
		fn    func(i int) string
	}{
		{8, func(i int) string {
			return fmt.Sprintf("server/chunks/%s.js", benchHashName("srv", i))
		}},
		{34, func(i int) string {
			dirs := [...]string{"chunks", "entry", "nodes", "assets"}
			return fmt.Sprintf("client/_app/immutable/%s/%s.js", dirs[i%len(dirs)], benchHashName("chunk", i))
		}},
		{8, func(i int) string {
			return fmt.Sprintf("prerendered/blog/%d/post-%d.html", 2024+i%3, i)
		}},
		{5, func(i int) string {
			return fmt.Sprintf("vendor/@sveltejs/kit/src/runtime/%d/mod-%d.js", i%9, i)
		}},
		{1, func(i int) string {
			return fmt.Sprintf("native/addon-%d.node", i)
		}},
	}

	paths := make([]string, 0, n)
	assigned := 0
	for _, s := range shapes {
		count := n * s.share / 100
		for i := 0; i < count; i++ {
			paths = append(paths, s.fn(i))
		}
		assigned += count
	}
	// Everything left over goes to node_modules — the deepest, widest root.
	for i := 0; i < n-assigned; i++ {
		paths = append(paths, fmt.Sprintf(
			"node_modules/pkg-%03d/dist/%s/%s.js", i%120, [...]string{"esm", "cjs", "types"}[i%3], benchHashName("m", i)))
	}
	return paths
}

// benchWriteSizedFile writes exactly size bytes to path, using filler for the
// bulk and an 8-byte seed prefix so no two files share content (a tree of
// identical files would be an unrealistically friendly page-cache workload).
func benchWriteSizedFile(b *testing.B, path string, size int64, filler []byte, seed uint64) {
	b.Helper()
	f, err := os.Create(path)
	if err != nil {
		b.Fatalf("create %s: %v", path, err)
	}
	var prefix [8]byte
	binary.LittleEndian.PutUint64(prefix[:], seed)
	if _, err := f.Write(prefix[:]); err != nil {
		_ = f.Close()
		b.Fatalf("write %s: %v", path, err)
	}
	remaining := size - int64(len(prefix))
	for remaining > 0 {
		chunk := int64(len(filler))
		if remaining < chunk {
			chunk = remaining
		}
		if _, err := f.Write(filler[:chunk]); err != nil {
			_ = f.Close()
			b.Fatalf("write %s: %v", path, err)
		}
		remaining -= chunk
	}
	if err := f.Close(); err != nil {
		b.Fatalf("close %s: %v", path, err)
	}
}

// buildBenchAttestTree materialises spec under base and returns the fixture.
// It does NOT touch the attestation globals; the sub-benchmark does that, so
// the (expensive) tree build happens exactly once per size.
func buildBenchAttestTree(b *testing.B, base string, spec benchAttestSpec) benchAttestFixture {
	b.Helper()

	appDir := filepath.Join(base, "app")
	filler := make([]byte, 1<<20)
	rng := rand.New(rand.NewPCG(0x706f6b6b, 0x756d0001)) // fixed seed: identical trees run to run
	for i := range filler {
		filler[i] = byte(rng.Uint32())
	}

	rels := benchAttestRelPaths(spec.files)
	smallCount := int64(len(rels) - spec.bigFiles)
	smallTotal := spec.total - int64(spec.bigFiles)*spec.bigSize
	meanSmall := smallTotal / smallCount
	if meanSmall < 64 {
		meanSmall = 64
	}
	// Spread the multi-MB files evenly through the list rather than clustering
	// them in one root.
	bigStride := len(rels) / (spec.bigFiles + 1)

	made := make(map[string]bool, 512)
	var total int64
	for i, rel := range rels {
		p := filepath.Join(appDir, filepath.FromSlash(rel))
		dir := filepath.Dir(p)
		if !made[dir] {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				b.Fatalf("mkdir %s: %v", dir, err)
			}
			made[dir] = true
		}
		var size int64
		if bigStride > 0 && i > 0 && i%bigStride == 0 && i/bigStride <= spec.bigFiles {
			size = spec.bigSize
		} else {
			// Uniform in [mean/2, 3*mean/2) — mean preserved, width realistic.
			size = meanSmall/2 + rng.Int64N(meanSmall)
		}
		benchWriteSizedFile(b, p, size, filler, uint64(i))
		total += size
	}

	roots := make([]string, 0, len(attestRoots))
	for _, r := range []string{"server", "client", "prerendered", "vendor", "node_modules", "native"} {
		roots = append(roots, filepath.Join(appDir, r))
	}
	return benchAttestFixture{appDir: appDir, roots: roots, files: len(rels), bytes: total}
}

// useBenchAttestFixture points the attestation globals at fx for the duration
// of the calling (sub-)benchmark iteration and restores them afterwards.
func useBenchAttestFixture(b *testing.B, fx benchAttestFixture) {
	b.Helper()
	oldDir, oldRoots := attestAppDir, attestRoots
	attestAppDir = fx.appDir
	attestRoots = fx.roots
	b.Cleanup(func() {
		attestAppDir = oldDir
		attestRoots = oldRoots
	})
}

// BenchmarkVerifyAttestation measures the full per-container-start attestation
// cost: stat /app, walk every root, read + SHA-256 every regular file, sort the
// records and fold them into the root digest, then compare. This is the whole
// gate, not a slice of it.
func BenchmarkVerifyAttestation(b *testing.B) {
	specs := []benchAttestSpec{
		{name: "1000files_10MB", files: 1000, bigFiles: 3, bigSize: 2 << 20, total: 10 << 20},
		{name: "10000files_100MB", files: 10000, bigFiles: 10, bigSize: 4 << 20, total: 100 << 20},
	}

	log := discardLogger()
	for _, spec := range specs {
		// Build outside b.Run: the sub-benchmark body is re-entered for every
		// N the framework tries, and rebuilding a 100 MB tree each time would
		// dwarf (and distort) the measurement.
		fx := buildBenchAttestTree(b, b.TempDir(), spec)

		b.Run(spec.name, func(b *testing.B) {
			useBenchAttestFixture(b, fx)

			// The expected digest is the tree's own digest, so every iteration
			// runs the full walk AND the success comparison — the path a
			// healthy container actually takes.
			records, err := walkAttestTree()
			if err != nil {
				b.Fatalf("walkAttestTree: %v", err)
			}
			if len(records) != fx.files {
				b.Fatalf("fixture walk saw %d files, want %d", len(records), fx.files)
			}
			expected := attestRootDigest(records)

			b.SetBytes(fx.bytes)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := verifyAttestation(log, expected); err != nil {
					b.Fatalf("verifyAttestation: %v", err)
				}
			}
		})
	}
}
