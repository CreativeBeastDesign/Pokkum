package precompressutils

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// PrecompressDirectory recursively traverses dir and generates sidecars
// (per opts) for all compressible static assets.
func PrecompressDirectory(dir string, modTime time.Time, opts PrecompressOptions) error {
	if dir == "" {
		return nil
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil
	}

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
// preserving modTime and only keeping sidecars that achieve positive compression savings.
func PrecompressFile(srcPath string, modTime time.Time, opts PrecompressOptions) error {
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
						_ = os.Chtimes(gzPath, modTime, modTime)
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
					_ = os.Chtimes(brPath, modTime, modTime)
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
						_ = os.Chtimes(zstPath, modTime, modTime)
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
