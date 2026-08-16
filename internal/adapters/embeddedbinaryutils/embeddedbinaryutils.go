// Package embeddedbinaryutils holds the mechanical zstd decode logic shared
// by every adapter that go:embeds a platform-specific binary compressed at
// build time and decompresses it on-the-fly (internal/adapters/supervisor for
// pokkum-init, internal/adapters/staticserver for pokkum-static). Each such
// adapter still declares its own //go:embed directive (embed paths are
// resolved relative to the declaring file, so that part cannot be shared),
// its own corrupt/unavailable sentinel errors, and its own error message
// wording — only the byte-shuffling underneath is common.
package embeddedbinaryutils

import "github.com/klauspost/compress/zstd"

// IsUtilityPackage marks this as a non-adapter helper: it implements no
// Hexagonal port interface itself.
const IsUtilityPackage = true

// NewDecoder builds a zstd decoder suitable for a caller's package-level
// sync.OnceValue singleton. The klauspost zstd Decoder supports multiple
// concurrent stateless DecodeAll calls on one instance, so a single
// process-wide decoder is both concurrency-safe and avoids reconstructing a
// decoder per build; it is never closed, since stateless DecodeAll holds no
// per-call resources to release.
func NewDecoder() *zstd.Decoder {
	d, err := zstd.NewReader(nil)
	if err != nil {
		// NewReader(nil, ...) only fails if an option is invalid; none are set here.
		panic(err)
	}
	return d
}

// Decode decompresses a zstd-framed embedded binary blob with decoder,
// returning corrupt (a caller-supplied sentinel) for empty input rather than
// passing it to the decoder. A real embedded binary blob never compresses to
// zero bytes, and zstd's stateless DecodeAll silently succeeds on empty
// input — without this check a broken embed would come through as an empty,
// nil-error binary, violating the "never nil or empty on a nil error"
// contract every caller of this function is implementing.
func Decode(decoder *zstd.Decoder, compressed []byte, corrupt error) ([]byte, error) {
	if len(compressed) == 0 {
		return nil, corrupt
	}
	return decoder.DecodeAll(compressed, nil)
}
