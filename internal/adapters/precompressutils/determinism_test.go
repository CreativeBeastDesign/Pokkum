package precompressutils

// White-box guards for the two properties the parallel/pooled rewrite must not
// break. Both are about BYTES, not speed: sidecars are packaged into the
// client and prerendered layers, so a single differing byte moves a layer
// digest, the image manifest, and every golden that pins them.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
)

var determinismEpoch = time.Unix(1700000000, 0).UTC()

// determinismPayloads returns text payloads shaped like real bundle output, in
// a spread of sizes. Incompressible or trivially compressible content would
// make the comparisons below pass for the wrong reason.
func determinismPayloads(n int) [][]byte {
	fragments := []string{
		"function n(e,t){return e&&e.__esModule?e:{default:e}}",
		"const r=Object.freeze({__proto__:null,get default(){return o}});",
		"export{a as default,b as hydrate,c as mount};",
		".btn{display:inline-flex;align-items:center;gap:.5rem}",
		"await import(\"./chunks/entry.client.js\").then(m=>m.start(a,b));",
	}
	rng := rand.New(rand.NewSource(0xBEEF))
	out := make([][]byte, 0, n)
	for i := 0; i < n; i++ {
		size := 1024 + rng.Intn(96*1024)
		var b strings.Builder
		b.Grow(size + 128)
		for b.Len() < size {
			b.WriteString(fragments[rng.Intn(len(fragments))])
			fmt.Fprintf(&b, "//#%d\n", rng.Intn(1<<20))
		}
		out = append(out, []byte(b.String()[:size]))
	}
	return out
}

// TestPooledCompressorsMatchFreshWriters is the guard on the sync.Pool
// introduction: a pooled, Reset writer must emit byte-for-byte what a
// freshly constructed writer at the same level emits. If any encoder carried
// state across a Reset, every sidecar produced after the first would differ
// from what the pre-pool code produced and the client layer's digest would
// move.
func TestPooledCompressorsMatchFreshWriters(t *testing.T) {
	payloads := determinismPayloads(12)

	for i, data := range payloads {
		// gzip
		var fresh bytes.Buffer
		gwFresh, err := gzip.NewWriterLevel(&fresh, gzip.BestCompression)
		if err != nil {
			t.Fatalf("gzip.NewWriterLevel: %v", err)
		}
		if _, err := gwFresh.Write(data); err != nil {
			t.Fatalf("gzip write: %v", err)
		}
		if err := gwFresh.Close(); err != nil {
			t.Fatalf("gzip close: %v", err)
		}

		var pooled bytes.Buffer
		gw := getGzipWriter(&pooled)
		if _, err := gw.Write(data); err != nil {
			t.Fatalf("pooled gzip write: %v", err)
		}
		if err := gw.Close(); err != nil {
			t.Fatalf("pooled gzip close: %v", err)
		}
		putGzipWriter(gw)
		if !bytes.Equal(fresh.Bytes(), pooled.Bytes()) {
			t.Errorf("payload %d: pooled gzip output differs from fresh (%d vs %d bytes)", i, fresh.Len(), pooled.Len())
		}

		// brotli
		fresh.Reset()
		bwFresh := brotli.NewWriterLevel(&fresh, brotli.BestCompression)
		if _, err := bwFresh.Write(data); err != nil {
			t.Fatalf("brotli write: %v", err)
		}
		if err := bwFresh.Close(); err != nil {
			t.Fatalf("brotli close: %v", err)
		}

		pooled.Reset()
		bw := getBrotliWriter(&pooled)
		if _, err := bw.Write(data); err != nil {
			t.Fatalf("pooled brotli write: %v", err)
		}
		if err := bw.Close(); err != nil {
			t.Fatalf("pooled brotli close: %v", err)
		}
		putBrotliWriter(bw)
		if !bytes.Equal(fresh.Bytes(), pooled.Bytes()) {
			t.Errorf("payload %d: pooled brotli output differs from fresh (%d vs %d bytes)", i, fresh.Len(), pooled.Len())
		}

		// zstd
		fresh.Reset()
		zwFresh, err := zstd.NewWriter(&fresh, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
		if err != nil {
			t.Fatalf("zstd.NewWriter: %v", err)
		}
		if _, err := zwFresh.Write(data); err != nil {
			t.Fatalf("zstd write: %v", err)
		}
		if err := zwFresh.Close(); err != nil {
			t.Fatalf("zstd close: %v", err)
		}

		pooled.Reset()
		zw, err := getZstdWriter(&pooled)
		if err != nil {
			t.Fatalf("pooled zstd writer: %v", err)
		}
		if _, err := zw.Write(data); err != nil {
			t.Fatalf("pooled zstd write: %v", err)
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("pooled zstd close: %v", err)
		}
		putZstdWriter(zw)
		if !bytes.Equal(fresh.Bytes(), pooled.Bytes()) {
			t.Errorf("payload %d: pooled zstd output differs from fresh (%d vs %d bytes)", i, fresh.Len(), pooled.Len())
		}
	}
}

// writeDeterminismTree materialises a small asset tree and returns the
// slash-relative paths of every file in it.
func writeDeterminismTree(t *testing.T, root string) []string {
	t.Helper()
	payloads := determinismPayloads(24)
	exts := []string{".js", ".mjs", ".css", ".json", ".svg", ".html"}
	var rels []string
	for i, data := range payloads {
		rel := fmt.Sprintf("_app/immutable/chunks/nested-%d/chunk-%d%s", i%4, i, exts[i%len(exts)])
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		rels = append(rels, rel)
	}
	// A file below the 64-byte floor, which must produce no sidecar at all.
	tiny := filepath.Join(root, "tiny.js")
	if err := os.WriteFile(tiny, []byte("var a=1;"), 0o644); err != nil {
		t.Fatalf("write tiny: %v", err)
	}
	rels = append(rels, "tiny.js")
	return rels
}

// sidecarDigests hashes every sidecar under root, keyed by its slash-relative
// path, so two trees can be compared byte for byte in one map comparison.
func sidecarDigests(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".gz", ".br", ".zst":
		default:
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// TestPrecompressOutputIsWorkerCountInvariant is the guard that protects the
// goldens from the worker pool: the same tree compressed with one worker and
// with many must produce identical sidecar bytes, file for file.
func TestPrecompressOutputIsWorkerCountInvariant(t *testing.T) {
	opts := PrecompressOptions{Gzip: true, Brotli: true, Zstd: true}

	roots := map[int]string{}
	digests := map[int]map[string]string{}

	for _, workers := range []int{1, 2, 8} {
		root := filepath.Join(t.TempDir(), "client")
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("mkdir root: %v", err)
		}
		rels := writeDeterminismTree(t, root)
		paths := make([]string, 0, len(rels))
		for _, rel := range rels {
			p := filepath.Join(root, filepath.FromSlash(rel))
			if IsCompressible(p) {
				paths = append(paths, p)
			}
		}
		sort.Strings(paths)

		if err := precompressPathsN(paths, determinismEpoch, opts, workers); err != nil {
			t.Fatalf("precompressPathsN(workers=%d): %v", workers, err)
		}
		roots[workers] = root
		digests[workers] = sidecarDigests(t, root)
		if len(digests[workers]) == 0 {
			t.Fatalf("workers=%d produced no sidecars; the fixture proves nothing", workers)
		}
	}

	ref := digests[1]
	t.Logf("reference run (1 worker) produced %d sidecars", len(ref))
	for _, workers := range []int{2, 8} {
		got := digests[workers]
		if len(got) != len(ref) {
			t.Fatalf("workers=%d produced %d sidecars, 1 worker produced %d", workers, len(got), len(ref))
		}
		for rel, want := range ref {
			if got[rel] != want {
				t.Errorf("workers=%d: %s digest %s, want %s", workers, rel, got[rel], want)
			}
		}
	}

	// The below-floor file must have been skipped in every run.
	for workers, d := range digests {
		for _, ext := range []string{".gz", ".br", ".zst"} {
			if _, ok := d["tiny.js"+ext]; ok {
				t.Errorf("workers=%d: compressed a file below the 64-byte floor", workers)
			}
		}
	}
}

// TestPrecompressFileSkipsReadWhenEverySidecarIsFresh proves the reordering
// that moved the staleness check ahead of os.ReadFile actually took effect:
// with fresh sidecars in place, a source file that has been made UNREADABLE is
// still processed without error, which is only possible if it was never read.
func TestPrecompressFileSkipsReadWhenEverySidecarIsFresh(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: chmod 0 does not make a file unreadable")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "app.js")
	payload := determinismPayloads(1)[0]
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	opts := PrecompressOptions{Gzip: true, Brotli: true}
	if err := PrecompressFile(src, determinismEpoch, opts); err != nil {
		t.Fatalf("warm PrecompressFile: %v", err)
	}
	for _, ext := range []string{".gz", ".br"} {
		if _, err := os.Stat(src + ext); err != nil {
			t.Fatalf("warm run produced no %s sidecar: %v", ext, err)
		}
	}

	if err := os.Chmod(src, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(src, 0o644) })

	if err := PrecompressFile(src, determinismEpoch, opts); err != nil {
		t.Fatalf("already-fresh PrecompressFile read the source (it must not): %v", err)
	}
}
