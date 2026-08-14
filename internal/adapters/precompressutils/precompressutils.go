package precompressutils

import (
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/andybalholm/brotli"
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

// PrecompressDirectory recursively traverses dir and generates .gz, .br, and .zst
// precompressed sidecars for all compressible static assets.
func PrecompressDirectory(dir string, modTime time.Time) error {
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
		return PrecompressFile(p, modTime)
	})
}

// PrecompressFile generates .gz, .br, and .zst sidecars for srcPath if compressible,
// preserving modTime and only keeping sidecars that achieve positive compression savings.
func PrecompressFile(srcPath string, modTime time.Time) error {
	if !IsCompressible(srcPath) {
		return nil
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
	gzPath := srcPath + ".gz"
	if _, err := os.Stat(gzPath); os.IsNotExist(err) {
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

	// 2. Brotli (.br)
	brPath := srcPath + ".br"
	if _, err := os.Stat(brPath); os.IsNotExist(err) {
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

	// 3. Zstandard (.zst)
	zstPath := srcPath + ".zst"
	if _, err := os.Stat(zstPath); os.IsNotExist(err) {
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

	return nil
}
