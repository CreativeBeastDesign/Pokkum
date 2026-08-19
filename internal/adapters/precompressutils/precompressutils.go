package precompressutils

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

	return filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
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
		return PrecompressFile(p, modTime, opts)
	})
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

	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("reading static file %q: %w", srcPath, err)
	}

	// Skip trivial files where compression adds header overhead
	if len(data) < 64 {
		return nil
	}

	origSize := len(data)

	// 1. Gzip (.gz)
	if opts.Gzip {
		gzPath := srcPath + ".gz"
		if isStale(srcInfo, gzPath) {
			buf := poolutils.GetByteBuffer()
			gw, err := gzip.NewWriterLevel(buf, gzip.BestCompression)
			if err == nil {
				_, _ = gw.Write(data)
				_ = gw.Close()
				if buf.Len() < origSize {
					if err := os.WriteFile(gzPath, buf.Bytes(), 0o644); err == nil {
						_ = os.Chtimes(gzPath, srcInfo.ModTime(), srcInfo.ModTime())
					}
				}
			}
			poolutils.PutByteBuffer(buf)
		}
	}

	// 2. Brotli (.br)
	if opts.Brotli {
		brPath := srcPath + ".br"
		if isStale(srcInfo, brPath) {
			buf := poolutils.GetByteBuffer()
			bw := brotli.NewWriterLevel(buf, brotli.BestCompression)
			_, _ = bw.Write(data)
			_ = bw.Close()
			if buf.Len() < origSize {
				if err := os.WriteFile(brPath, buf.Bytes(), 0o644); err == nil {
					_ = os.Chtimes(brPath, srcInfo.ModTime(), srcInfo.ModTime())
				}
			}
			poolutils.PutByteBuffer(buf)
		}
	}

	// 3. Zstandard (.zst)
	if opts.Zstd {
		zstPath := srcPath + ".zst"
		if isStale(srcInfo, zstPath) {
			buf := poolutils.GetByteBuffer()
			zw, err := zstd.NewWriter(buf, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
			if err == nil {
				_, _ = zw.Write(data)
				_ = zw.Close()
				if buf.Len() < origSize {
					if err := os.WriteFile(zstPath, buf.Bytes(), 0o644); err == nil {
						_ = os.Chtimes(zstPath, srcInfo.ModTime(), srcInfo.ModTime())
					}
				}
			}
			poolutils.PutByteBuffer(buf)
		}
	}

	return nil
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
