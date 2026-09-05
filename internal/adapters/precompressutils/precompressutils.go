package precompressutils

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/poolutils"
)

// IsUtilityPackage marks this as a reusable utility, not a port adapter.
const IsUtilityPackage = true

// CompressibleExtensions contains static asset extensions that benefit from pre-compression.
var CompressibleExtensions = map[string]bool{
	".js":   true,
	".mjs":  true,
	".cjs":  true,
	".css":  true,
	".html": true,
	".htm":  true,
	".json": true,
	".svg":  true,
	".xml":  true,
	".txt":  true,
	".wasm": true,
	".ttf":  true,
	".otf":  true,
	".eot":  true,
	".map":  true,
}

// IsCompressible reports whether a filename has a compressible extension.
func IsCompressible(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	return CompressibleExtensions[ext]
}

// PrecompressOptions selects which sidecar formats PrecompressDirectory and
// PrecompressFile generate. The zero value generates nothing — callers must
// opt in to each format they want.
//
// Not every format pays off for every serving path: the layered strategy's
// runtime (@sveltejs/adapter-node's bundled sirv server) only ever negotiates
// gzip/brotli, so generating .zst sidecars there is wasted build time and
// wasted layer bytes. Only pokkum-static (--strategy=static) negotiates zstd.
type PrecompressOptions struct {
	Gzip   bool
	Brotli bool
	Zstd   bool
}

// dirLocks serialises PrecompressDirectory per directory.
//
// This is a work-deduplication measure, not the race fix — a distinction worth
// stating precisely, because getting it wrong is how the first attempt at this
// fix stopped short. A multi-platform build fans out over platforms and every
// platform packages from the same tree, so without the lock all of them compress
// the same files simultaneously on the first pass. With it, one does the work and
// the rest find the sidecars already fresh.
//
// Together with correct freshness detection it is nevertheless what closes the
// race, and the argument is worth spelling out because it is not obvious.
//
// Every platform precompresses before it tars. The first platform takes the lock
// and writes every sidecar while all the others are still blocked on it, so no tar
// walk has begun. Each subsequent platform then acquires the lock, finds the
// sidecars already fresh, writes nothing, and only then tars. Writes therefore
// happen strictly before any walk starts, which is the property the race needed.
//
// That safety rests on freshness being correct — see PrecompressFile, where
// pinning sidecar mtimes to the build epoch had made every sidecar permanently
// stale, so every platform rewrote everything and the window was maximal.
//
// Writing sidecars atomically (temp file plus rename) was tried instead and is
// worse here: os.CreateTemp places the temporary file in the very directory the
// packager walks, so a concurrent walk either fails its lstat when the file is
// renamed away or packages a .tmp-* file into the image. A test caught exactly
// that.
var dirLocks sync.Map // cleaned absolute path -> *sync.Mutex

func lockForDir(dir string) *sync.Mutex {
	key := dir
	if abs, err := filepath.Abs(dir); err == nil {
		key = filepath.Clean(abs)
	}
	actual, _ := dirLocks.LoadOrStore(key, &sync.Mutex{})
	return actual.(*sync.Mutex)
}

// PrecompressDirectory recursively traverses dir and generates sidecars
// (per opts) for all compressible static assets.
//
// The walk collects candidate paths first and compresses them through a
// bounded worker pool, rather than compressing inline in the WalkDir callback
// as it used to. Brotli at BestCompression runs at roughly 1 MB/s on one core,
// so the old shape left every other core idle for the whole cold pass; the
// files are independent (each writes only its own srcPath+".gz"/".br"/".zst")
// so there is nothing to order between them.
//
// The parallelism cannot influence the bytes written: a sidecar's contents are
// a pure function of its source file and the compression level, both of which
// are untouched here. TestPrecompressOutputIsWorkerCountInvariant pins that,
// because these bytes are packaged into client/prerendered layers and reach
// the image digest.
//
// Safe for concurrent use with the same dir: calls are serialised per directory,
// and a second caller finds the sidecars already fresh and rewrites nothing.
func PrecompressDirectory(dir string, modTime time.Time, opts PrecompressOptions) error {
	if dir == "" {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil
	}

	mu := lockForDir(dir)
	mu.Lock()
	defer mu.Unlock()

	paths := make([]string, 0, 256)
	if err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		// Skip existing compressed archives and sidecars
		ext := strings.ToLower(filepath.Ext(p))
		if ext == ".gz" || ext == ".br" || ext == ".zst" {
			return nil
		}
		if !IsCompressible(p) {
			return nil
		}
		paths = append(paths, p)
		return nil
	}); err != nil {
		return err
	}

	return precompressPaths(paths, modTime, opts)
}

// precompressPaths compresses paths through min(GOMAXPROCS, len(paths))
// workers and returns the error belonging to the LOWEST-indexed path that
// failed.
//
// Lowest index, not first-to-fail, is deliberate: paths arrives in WalkDir's
// lexical order, so this reproduces exactly the error the old inline walk
// would have surfaced, instead of making the reported error depend on which
// goroutine happened to lose first. The one behavioural difference from the
// old shape is that the remaining files are still compressed before the error
// is returned rather than the walk aborting on it — an error here is a stat or
// read failure on an individual asset, and the caller (packager.Build) treats
// the whole step as a warning either way.
func precompressPaths(paths []string, modTime time.Time, opts PrecompressOptions) error {
	workers := runtime.GOMAXPROCS(0)
	if workers > len(paths) {
		workers = len(paths)
	}
	return precompressPathsN(paths, modTime, opts, workers)
}

// precompressPathsN is precompressPaths with the worker count supplied rather
// than derived, which is what lets a test run the identical work at one worker
// and at many and diff the resulting bytes.
func precompressPathsN(paths []string, modTime time.Time, opts PrecompressOptions, workers int) error {
	if len(paths) == 0 {
		return nil
	}
	if workers < 1 {
		workers = 1
	}

	if workers == 1 {
		for _, p := range paths {
			if err := PrecompressFile(p, modTime, opts); err != nil {
				return err
			}
		}
		return nil
	}

	errs := make([]error, len(paths))
	var next atomic.Int64
	var wg sync.WaitGroup
	// Nothing fallible sits between the dispatch loop and Wait: every worker's
	// only exit is the index check, and per-file errors are recorded in errs
	// rather than returned, so no path can leak a goroutine.
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				idx := int(next.Add(1) - 1)
				if idx >= len(paths) {
					return
				}
				errs[idx] = PrecompressFile(paths[idx], modTime, opts)
			}
		}()
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// PrecompressFile generates sidecars (per opts) for srcPath if compressible,
// keeping only those that achieve positive compression savings.
//
// A sidecar's on-disk mtime is set to its SOURCE file's mtime, not to modTime.
// That looks like it weakens reproducibility and does not: the only ModTime that
// reaches a tar header is the pinned value the packager passes to writeTar (see
// packager/layer.go), so an on-disk mtime never influences image bytes.
//
// Pinning sidecars to the build epoch actively broke freshness. isStale compares
// the sidecar's mtime against the source's, and a build's source files are
// written *now* while the epoch is derived from the last commit — so every
// sidecar was permanently "older than its source" and every platform in a
// multi-platform build re-ran brotli at BestCompression over the entire tree.
// Using the source's own mtime makes the comparison meaningful: freshly written
// sidecars are fresh, while a source overwritten in place afterwards is newer
// than its sidecar and correctly regenerates it — which is the guard isStale was
// written for in the first place.
func PrecompressFile(srcPath string, modTime time.Time, opts PrecompressOptions) error {
	// modTime is retained in the signature for callers and future use; sidecar
	// timestamps deliberately come from the source file instead, per the doc above.
	_ = modTime
	if !IsCompressible(srcPath) {
		return nil
	}

	srcInfo, err := os.Stat(srcPath)
	if err != nil {
		return fmt.Errorf("stat static file %q: %w", srcPath, err)
	}

	// Skip trivial files where compression adds header overhead. Decided from
	// the stat, before the read, for the same reason as the staleness check
	// below.
	if srcInfo.Size() < 64 {
		return nil
	}

	// Freshness is settled BEFORE the source is read. Every platform after the
	// first in a multi-platform build, and every incremental rebuild, lands on
	// the path where all sidecars are already fresh — and the old ordering
	// os.ReadFile'd the entire tree into memory there before discovering it had
	// nothing to write.
	gzPath, brPath, zstPath := srcPath+".gz", srcPath+".br", srcPath+".zst"
	needGzip := opts.Gzip && isStale(srcInfo, gzPath)
	needBrotli := opts.Brotli && isStale(srcInfo, brPath)
	needZstd := opts.Zstd && isStale(srcInfo, zstPath)
	if !needGzip && !needBrotli && !needZstd {
		return nil
	}

	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("reading static file %q: %w", srcPath, err)
	}

	// Re-checked against the bytes actually read: the stat above is a separate
	// syscall, so a file truncated in between would otherwise reach the
	// compressors below.
	if len(data) < 64 {
		return nil
	}

	origSize := len(data)

	// 1. Gzip (.gz)
	if needGzip {
		writeSidecar(gzPath, srcInfo, origSize, func(w io.Writer) error {
			gw := getGzipWriter(w)
			defer putGzipWriter(gw)
			_, _ = gw.Write(data)
			return gw.Close()
		})
	}

	// 2. Brotli (.br)
	if needBrotli {
		writeSidecar(brPath, srcInfo, origSize, func(w io.Writer) error {
			bw := getBrotliWriter(w)
			defer putBrotliWriter(bw)
			_, _ = bw.Write(data)
			return bw.Close()
		})
	}

	// 3. Zstandard (.zst)
	if needZstd {
		writeSidecar(zstPath, srcInfo, origSize, func(w io.Writer) error {
			zw, err := getZstdWriter(w)
			if err != nil {
				return err
			}
			defer putZstdWriter(zw)
			_, _ = zw.Write(data)
			return zw.Close()
		})
	}

	return nil
}

// writeSidecar runs compress into a pooled buffer and writes the result to
// sidecarPath only when it actually came out smaller than the source, matching
// the original inline behaviour exactly: a compressor that errors, or output
// that is not smaller, leaves no sidecar behind and is not an error — the file
// simply ships uncompressed.
func writeSidecar(sidecarPath string, srcInfo os.FileInfo, origSize int, compress func(io.Writer) error) {
	buf := poolutils.GetByteBuffer()
	defer poolutils.PutByteBuffer(buf)

	if err := compress(buf); err != nil {
		return
	}
	if buf.Len() >= origSize {
		return
	}
	if err := os.WriteFile(sidecarPath, buf.Bytes(), 0o644); err == nil {
		_ = os.Chtimes(sidecarPath, srcInfo.ModTime(), srcInfo.ModTime())
	}
}

// Compressor pools.
//
// A brotli encoder at BestCompression allocates a multi-megabyte window per
// writer, and the old code minted one per file per format: the cold-path
// benchmark measured 16.3 GB allocated to produce sidecars for a 10 MB tree.
// Reusing encoders through sync.Pool removes essentially all of that.
//
// Every pooled writer is constructed with exactly the level the inline code
// used and is Reset onto its new destination before use, so the bytes it
// produces are identical to a freshly constructed writer's. That is not a
// cosmetic claim — sidecars are packaged into client/prerendered layers and
// reach the image digest — and TestPooledCompressorsMatchFreshWriters pins it
// by diffing pooled output against fresh-writer output byte for byte.
var (
	gzipWriterPool   sync.Pool
	brotliWriterPool sync.Pool
	zstdWriterPool   sync.Pool
)

func getGzipWriter(w io.Writer) *gzip.Writer {
	if v := gzipWriterPool.Get(); v != nil {
		gw := v.(*gzip.Writer)
		gw.Reset(w)
		return gw
	}
	// BestCompression never returns an error from NewWriterLevel; the only
	// error case is an out-of-range level, and this one is a constant.
	gw, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		panic(fmt.Sprintf("precompressutils: gzip.NewWriterLevel(BestCompression): %v", err))
	}
	return gw
}

func putGzipWriter(gw *gzip.Writer) {
	gw.Reset(io.Discard)
	gzipWriterPool.Put(gw)
}

func getBrotliWriter(w io.Writer) *brotli.Writer {
	if v := brotliWriterPool.Get(); v != nil {
		bw := v.(*brotli.Writer)
		bw.Reset(w)
		return bw
	}
	return brotli.NewWriterLevel(w, brotli.BestCompression)
}

func putBrotliWriter(bw *brotli.Writer) {
	bw.Reset(io.Discard)
	brotliWriterPool.Put(bw)
}

// getZstdWriter returns an encoder configured exactly as the inline code
// configured it — SpeedBestCompression and nothing else. In particular the
// encoder concurrency is left at the library default, unchanged: it is an
// input to the bytes this produces, and those bytes are layer content.
func getZstdWriter(w io.Writer) (*zstd.Encoder, error) {
	if v := zstdWriterPool.Get(); v != nil {
		zw := v.(*zstd.Encoder)
		zw.Reset(w)
		return zw, nil
	}
	// No WithEncoderConcurrency here, unlike packager/layer.go, and that
	// asymmetry is deliberate rather than an oversight.
	//
	// The question it raises is a real one: .zst sidecars are packaged into
	// the static strategy's layers, so if their bytes varied with the
	// encoder's concurrency they would vary with GOMAXPROCS, and an image
	// built on an 8-core laptop would not match one built on a 2-core CI
	// runner. That would break the bit-for-bit reproducibility invariant
	// silently, in a way no single-machine test could ever catch.
	//
	// Measured 2026-09-05, rather than reasoned about: compressing 6 MiB of
	// bundle-shaped input at SpeedBestCompression with concurrency 1, 2, 4,
	// 8, 16 and the library default produced six byte-identical outputs
	// (872218 bytes, identical SHA-256). klauspost/compress leaves
	// concurrentBlocks off by default, so the extra encoder states are used
	// for pipelining, never to split a stream into independently-compressed
	// blocks — concurrency changes throughput and memory, not output.
	//
	// layer.go pins concurrency to 1 purely to stop each writer allocating
	// GOMAXPROCS encoder states it cannot use. Here the writers are pooled
	// and reused across the whole tree, so that cost is already amortised,
	// and pinning would additionally forgo whatever pipelining the encoder
	// does get. Leave it alone; the reproducibility concern is answered.
	return zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
}

func putZstdWriter(zw *zstd.Encoder) {
	zw.Reset(io.Discard)
	zstdWriterPool.Put(zw)
}

// isStale reports whether the sidecar at sidecarPath needs to be (re)generated
// because it is missing or no longer reflects the current contents of the
// source file described by srcInfo.
//
// Existence alone is not a valid freshness signal: a sidecar written for an
// earlier version of the source file survives untouched if the source is
// later overwritten in place (e.g. an incremental rebuild that reuses the
// output directory without a full clean), leaving compressed clients served
// stale, mismatched bytes while uncompressed clients see the new content.
// Comparing mtimes closes that gap: a source file that changed after its
// sidecar was written is, by definition, newer than that sidecar.
func isStale(srcInfo os.FileInfo, sidecarPath string) bool {
	sidecarInfo, err := os.Stat(sidecarPath)
	if err != nil {
		// Missing (or unreadable) sidecar: nothing to reuse.
		return true
	}
	return sidecarInfo.ModTime().Before(srcInfo.ModTime())
}
