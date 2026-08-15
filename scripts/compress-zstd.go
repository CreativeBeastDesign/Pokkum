// Command compress-zstd is a tiny deterministic zstd compressor used by
// `make supervisor` to produce the go:embed-friendlier compressed representation
// of the pokkum-init supervisor binaries (see internal/adapters/supervisor).
//
// It is intentionally standalone (a main package file run via `go run`, not part
// of the pokkum CLI build) so that building the embedded supervisor assets never
// depends on an external zstd CLI tool, and so the compression parameters used at
// build time are guaranteed to match the vendored github.com/klauspost/compress/zstd
// library that the supervisor adapter uses to decompress at runtime.
//
// Usage: go run ./scripts/compress-zstd.go <src> <dst>
//
// The output is written with 0o644 permissions and is bit-for-bit deterministic
// for a given input: single-shot EncodeAll with zstd.SpeedBestCompression has no
// concurrency or timing nondeterminism, which is required for Pokkum's bit-for-bit
// reproducible release builds.
package main

import (
	"fmt"
	"os"

	"github.com/klauspost/compress/zstd"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintf(os.Stderr, "usage: %s <src> <dst>\n", os.Args[0])
		os.Exit(2)
	}
	src, dst := os.Args[1], os.Args[2]

	data, err := os.ReadFile(src)
	if err != nil {
		fmt.Fprintf(os.Stderr, "compress-zstd: reading %q: %v\n", src, err)
		os.Exit(1)
	}

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		fmt.Fprintf(os.Stderr, "compress-zstd: init encoder: %v\n", err)
		os.Exit(1)
	}
	compressed := enc.EncodeAll(data, nil)

	if err := os.WriteFile(dst, compressed, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "compress-zstd: writing %q: %v\n", dst, err)
		os.Exit(1)
	}
}
