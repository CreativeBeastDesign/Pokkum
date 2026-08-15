package supervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestBinaryPresentAMD64(t *testing.T) {
	// This test requires the binaries to be embedded. If they are absent,
	// skip with a clear message.
	p := New(slog.Default())
	data, err := p.Binary(context.Background(), ports.LinuxAMD64)

	if err != nil {
		if errors.Is(err, core.ErrSupervisorUnavailable) {
			t.Skip("supervisor binary not embedded; run `make supervisor` first")
		}
		t.Fatalf("Binary(LinuxAMD64): %v", err)
	}

	// Verify the binary is non-empty and starts with ELF magic
	if len(data) == 0 {
		t.Error("Binary(LinuxAMD64) returned empty slice")
	}
	if !bytes.HasPrefix(data, []byte("\x7fELF")) {
		t.Errorf("Binary(LinuxAMD64) does not start with ELF magic; got %v", data[:4])
	}

	// Verify defensive copy: mutating the returned slice must not affect
	// a subsequent call.
	if len(data) > 0 {
		origByte := data[0]
		data[0] = ^data[0] // Flip all bits
		data2, err := p.Binary(context.Background(), ports.LinuxAMD64)
		if err != nil {
			t.Fatalf("second Binary(LinuxAMD64) after mutation: %v", err)
		}
		if data2[0] != origByte {
			t.Errorf("mutation of returned slice affected subsequent call; expected %v, got %v",
				origByte, data2[0])
		}
	}
}

func TestBinaryPresentARM64(t *testing.T) {
	p := New(slog.Default())
	data, err := p.Binary(context.Background(), ports.LinuxARM64)

	if err != nil {
		if errors.Is(err, core.ErrSupervisorUnavailable) {
			t.Skip("supervisor binary not embedded; run `make supervisor` first")
		}
		t.Fatalf("Binary(LinuxARM64): %v", err)
	}

	if len(data) == 0 {
		t.Error("Binary(LinuxARM64) returned empty slice")
	}
	if !bytes.HasPrefix(data, []byte("\x7fELF")) {
		t.Errorf("Binary(LinuxARM64) does not start with ELF magic; got %v", data[:4])
	}

	// Verify defensive copy
	if len(data) > 0 {
		origByte := data[0]
		data[0] = ^data[0]
		data2, err := p.Binary(context.Background(), ports.LinuxARM64)
		if err != nil {
			t.Fatalf("second Binary(LinuxARM64) after mutation: %v", err)
		}
		if data2[0] != origByte {
			t.Errorf("mutation of returned slice affected subsequent call; expected %v, got %v",
				origByte, data2[0])
		}
	}
}

func TestBinaryUnsupportedPlatform(t *testing.T) {
	p := New(slog.Default())

	// Test with an unsupported platform
	unsupported := ports.Platform{OS: "darwin", Arch: "amd64"}
	_, err := p.Binary(context.Background(), unsupported)

	if !errors.Is(err, core.ErrUnsupportedPlatform) {
		t.Errorf("Binary(unsupported): expected ErrUnsupportedPlatform, got %v", err)
	}

	// Error message should mention the platform
	errMsg := err.Error()
	if !bytes.Contains([]byte(errMsg), []byte("darwin")) {
		t.Errorf("error message should mention platform; got: %v", errMsg)
	}
}

func TestBinaryMissingErrorMessage(t *testing.T) {
	p := New(slog.Default())

	// Call Binary on a valid platform. If binaries are present, skip this test.
	// If binaries are absent, verify the error message is helpful.
	data, err := p.Binary(context.Background(), ports.LinuxAMD64)

	if err == nil {
		// Binaries are embedded; test cannot run in this state
		if len(data) > 0 {
			t.Skip("supervisor binaries are embedded; skipping missing-binary error test")
		}
	}

	if !errors.Is(err, core.ErrSupervisorUnavailable) {
		t.Fatalf("expected ErrSupervisorUnavailable, got %v", err)
	}

	// Error message MUST mention "make supervisor" to be helpful to users
	errMsg := err.Error()
	if !bytes.Contains([]byte(errMsg), []byte("make supervisor")) {
		t.Errorf("error message should mention 'make supervisor'; got: %v", errMsg)
	}
}

func TestVersionPresent(t *testing.T) {
	p := New(slog.Default())
	version, err := p.Version(context.Background())

	if err != nil {
		t.Fatalf("Version(): %v", err)
	}

	// Version may be empty in development builds (per the interface doc),
	// but if the binary is embedded it should be a hex string.
	if version == "" {
		// Get the binary to check if it's embedded
		_, binErr := p.Binary(context.Background(), ports.LinuxAMD64)
		if binErr == nil {
			t.Error("Version() returned empty string but binary is embedded")
		}
		// Binary not embedded is fine; version is optional
		return
	}

	// If non-empty, verify it looks like a SHA256 hex digest
	if len(version) != 64 {
		t.Errorf("Version() returned %d characters; expected 64 (SHA256 hex)", len(version))
	}

	// All characters should be hex digits
	for _, ch := range version {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			t.Errorf("Version() contains non-hex character %q", ch)
		}
	}

	// Verify it matches the SHA256 of the amd64 binary
	data, binErr := p.Binary(context.Background(), ports.LinuxAMD64)
	if binErr != nil {
		if errors.Is(binErr, core.ErrSupervisorUnavailable) {
			t.Skip("binary not embedded, cannot verify version hash")
		}
		t.Fatalf("Binary(LinuxAMD64): %v", binErr)
	}

	expectedHash := fmt.Sprintf("%x", sha256.Sum256(data))
	if version != expectedHash {
		t.Errorf("Version() = %q, expected %q", version, expectedHash)
	}
}

func TestVersionAbsent(t *testing.T) {
	p := New(slog.Default())

	// Try to get the version. If binaries are not embedded, Version should
	// return empty string and nil error (per the interface doc).
	version, err := p.Version(context.Background())

	if err != nil {
		t.Fatalf("Version() should not error when binary is absent, but got: %v", err)
	}

	// If binary is absent, version should be empty
	_, binErr := p.Binary(context.Background(), ports.LinuxAMD64)
	if errors.Is(binErr, core.ErrSupervisorUnavailable) {
		if version != "" {
			t.Errorf("Version() should return empty string when binary is absent, but got %q", version)
		}
	}
}

func TestNewWithNilLogger(t *testing.T) {
	// New(nil) should use slog.Default() internally
	p := New(nil)
	if p == nil {
		t.Error("New(nil) returned nil")
	}

	// Should still be functional
	_, err := p.Binary(context.Background(), ports.LinuxAMD64)
	if err != nil {
		if !errors.Is(err, core.ErrSupervisorUnavailable) {
			t.Fatalf("unexpected error: %v", err)
		}
		// Missing binary is ok, provider is functional
	}
}

func TestBinaryConcurrency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency test in short mode")
	}

	p := New(slog.Default())

	// Try to call Binary concurrently for both platforms
	results := make(chan error, 2)
	for _, plat := range ports.SupportedPlatforms {
		go func(platform ports.Platform) {
			data, err := p.Binary(context.Background(), platform)
			if err != nil && !errors.Is(err, core.ErrSupervisorUnavailable) {
				results <- fmt.Errorf("Binary(%s): %w", platform, err)
				return
			}
			if err == nil && len(data) == 0 {
				results <- fmt.Errorf("Binary(%s) returned empty data", platform)
				return
			}
			results <- nil
		}(plat)
	}

	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
}

// TestDecodeSupervisorRoundTrip guards the core invariant of compressed embedding:
// zstd-compressing a pokkum-init-style blob (ELF-prefixed) with the same encoder
// settings `make supervisor` uses, then decoding it through decodeSupervisor,
// must reproduce the original bytes exactly. This is what guarantees Binary()
// returns the bit-identical raw binary the packager writes to /pokkum/init even
// though the embedded representation is compressed.
func TestDecodeSupervisorRoundTrip(t *testing.T) {
	// A realistic supervisor payload: several KB of ELF-prefixed, mostly
	// repetitive bytes (code sections compress well, like a real binary).
	raw := make([]byte, 0, 32*1024)
	raw = append(raw, []byte("\x7fELF\x02\x01\x01")...)
	for i := 0; i < 4*1024; i++ {
		raw = append(raw, byte(i%251))
	}

	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	compressed := enc.EncodeAll(raw, nil)

	if len(compressed) >= len(raw) {
		t.Fatalf("test payload did not compress (compressed=%d raw=%d); fixture too incompressible", len(compressed), len(raw))
	}

	got, err := decodeSupervisor(compressed)
	if err != nil {
		t.Fatalf("decodeSupervisor: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("round-trip mismatch: got %d bytes, want %d", len(got), len(raw))
	}

	// The decoded slice must be owner-owned: mutating it must not affect a
	// subsequent decode (the fresh allocation each DecodeAll produces).
	if len(got) > 0 {
		got[0] = ^got[0]
		again, err := decodeSupervisor(compressed)
		if err != nil {
			t.Fatalf("second decodeSupervisor: %v", err)
		}
		if again[0] == got[0] {
			t.Error("mutation of decoded slice affected a subsequent decode; output is not owner-owned")
		}
	}
}

// TestDecodeSupervisorCorrupt verifies that garbage (a present-but-unusable
// embedded blob) surfaces as a decoder error rather than a panic or silent
// success. This is the decode-seam counterpart of Binary() wrapping the failure
// as errSupervisorCorrupt.
func TestDecodeSupervisorCorrupt(t *testing.T) {
	for _, bad := range [][]byte{
		[]byte("this is definitely not a zstd frame"),
		{},
		{0x28, 0xb5, 0x2f, 0xfd, 0x00, 0x00}, // valid zstd magic, garbage payload
	} {
		if _, err := decodeSupervisor(bad); err == nil {
			t.Errorf("decodeSupervisor(%d bytes) succeeded on corrupt input; want error", len(bad))
		}
	}
}

// TestBinaryRealAssetsDecompress verifies that when the generated .zst assets are
// embedded (i.e. `make supervisor` has been run), Binary returns valid raw ELF for
// every supported platform — this is the end-to-end confirmation that the embedded
// compressed representation decompresses cleanly through the public seam. When the
// assets are absent (fresh checkout), the test skips exactly like the other
// binary-presence tests.
func TestBinaryRealAssetsDecompress(t *testing.T) {
	p := New(slog.Default())
	for _, plat := range ports.SupportedPlatforms {
		data, err := p.Binary(context.Background(), plat)
		if err != nil {
			if errors.Is(err, core.ErrSupervisorUnavailable) {
				t.Skipf("supervisor binary not embedded for %s; run `make supervisor` first", plat)
			}
			t.Fatalf("Binary(%s): %v", plat, err)
		}
		if len(data) == 0 {
			t.Fatalf("Binary(%s) returned empty slice", plat)
		}
		if !bytes.HasPrefix(data, []byte("\x7fELF")) {
			t.Errorf("Binary(%s): decompressed bytes do not start with ELF magic; got %v", plat, data[:4])
		}
	}
}
