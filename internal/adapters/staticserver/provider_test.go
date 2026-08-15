package staticserver

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
	p := New(slog.Default())
	data, err := p.Binary(context.Background(), ports.LinuxAMD64)
	if err != nil {
		if errors.Is(err, core.ErrStaticServerUnavailable) {
			t.Skip("static server binary not embedded; run `make static-server` first")
		}
		t.Fatalf("Binary(LinuxAMD64): %v", err)
	}
	if len(data) == 0 {
		t.Error("Binary(LinuxAMD64) returned empty slice")
	}
	if !bytes.HasPrefix(data, []byte("\x7fELF")) {
		t.Errorf("Binary(LinuxAMD64) does not start with ELF magic; got %v", data[:4])
	}
	if len(data) > 0 {
		orig := data[0]
		data[0] = ^data[0]
		data2, err := p.Binary(context.Background(), ports.LinuxAMD64)
		if err != nil {
			t.Fatalf("second Binary(LinuxAMD64): %v", err)
		}
		if data2[0] != orig {
			t.Error("mutation of returned slice affected a subsequent call")
		}
	}
}

func TestBinaryUnsupportedPlatform(t *testing.T) {
	p := New(slog.Default())
	unsupported := ports.Platform{OS: "darwin", Arch: "amd64"}
	_, err := p.Binary(context.Background(), unsupported)
	if !errors.Is(err, core.ErrUnsupportedPlatform) {
		t.Errorf("Binary(unsupported): expected ErrUnsupportedPlatform, got %v", err)
	}
}

func TestBinaryMissingErrorMessage(t *testing.T) {
	p := New(slog.Default())
	data, err := p.Binary(context.Background(), ports.LinuxAMD64)
	if err == nil {
		if len(data) > 0 {
			t.Skip("static server binaries are embedded; skipping missing-binary error test")
		}
	}
	if !errors.Is(err, core.ErrStaticServerUnavailable) {
		t.Fatalf("expected ErrStaticServerUnavailable, got %v", err)
	}
	errMsg := err.Error()
	if !bytes.Contains([]byte(errMsg), []byte("make static-server")) {
		t.Errorf("error message should mention 'make static-server'; got: %v", errMsg)
	}
}

func TestVersionMatchesBinaryHash(t *testing.T) {
	p := New(slog.Default())
	version, err := p.Version(context.Background())
	if err != nil {
		t.Fatalf("Version(): %v", err)
	}
	if version == "" {
		_, binErr := p.Binary(context.Background(), ports.LinuxAMD64)
		if binErr == nil {
			t.Error("Version() empty but binary embedded")
		}
		return // absent is fine
	}
	if len(version) != 64 {
		t.Errorf("Version() = %d chars, want 64 (SHA256 hex)", len(version))
	}
	data, binErr := p.Binary(context.Background(), ports.LinuxAMD64)
	if binErr != nil {
		if errors.Is(binErr, core.ErrStaticServerUnavailable) {
			t.Skip("binary not embedded, cannot verify version hash")
		}
		t.Fatalf("Binary: %v", binErr)
	}
	if want := fmt.Sprintf("%x", sha256.Sum256(data)); version != want {
		t.Errorf("Version() = %q, want %q", version, want)
	}
}

func TestNewWithNilLogger(t *testing.T) {
	p := New(nil)
	if p == nil {
		t.Error("New(nil) returned nil")
	}
}

func TestBinaryConcurrency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency test in short mode")
	}
	p := New(slog.Default())
	results := make(chan error, len(ports.SupportedPlatforms))
	for _, plat := range ports.SupportedPlatforms {
		go func(platform ports.Platform) {
			data, err := p.Binary(context.Background(), platform)
			if err != nil && !errors.Is(err, core.ErrStaticServerUnavailable) {
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
	for range ports.SupportedPlatforms {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
}

// TestDecodeStaticServerRoundTrip pins the zstd round-trip invariant shared with
// the supervisor: compress with the same settings `make static-server` uses and
// decode through decodeStaticServer to reproduce the raw bytes exactly.
func TestDecodeStaticServerRoundTrip(t *testing.T) {
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
		t.Fatalf("test payload did not compress")
	}
	got, err := decodeStaticServer(compressed)
	if err != nil {
		t.Fatalf("decodeStaticServer: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("round-trip mismatch: got %d bytes, want %d", len(got), len(raw))
	}
	if len(got) > 0 {
		got[0] = ^got[0]
		again, err := decodeStaticServer(compressed)
		if err != nil {
			t.Fatalf("second decodeStaticServer: %v", err)
		}
		if again[0] == got[0] {
			t.Error("decoded slice is not owner-owned; mutation leaked across decodes")
		}
	}
}

// TestDecodeStaticServerCorrupt verifies garbage (a present-but-unusable embedded
// blob) surfaces as a decoder error.
func TestDecodeStaticServerCorrupt(t *testing.T) {
	for _, bad := range [][]byte{
		[]byte("this is definitely not a zstd frame"),
		{},
		{0x28, 0xb5, 0x2f, 0xfd, 0x00, 0x00},
	} {
		if _, err := decodeStaticServer(bad); err == nil {
			t.Errorf("decodeStaticServer(%d bytes) succeeded on corrupt input; want error", len(bad))
		}
	}
}
