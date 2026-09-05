package staticserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/embeddedbinaryutils"
	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// freshDecode decompresses an embedded asset through a decoder built solely for
// this call, deliberately bypassing both the memoisation table and the shared
// process-wide decoder singleton. It is the independent oracle these tests need:
// asserting the memoised bytes against themselves would prove nothing.
func freshDecode(t *testing.T, path string) []byte {
	t.Helper()
	compressed, err := binaries.ReadFile(path)
	if err != nil {
		t.Skipf("%s not embedded; run `make static-server` first", path)
	}
	data, err := embeddedbinaryutils.Decode(embeddedbinaryutils.NewDecoder(), compressed, errStaticServerCorrupt)
	if err != nil {
		t.Fatalf("fresh decode of %s: %v", path, err)
	}
	return data
}

// TestBinaryMemoisedBytesMatchFreshDecode is the correctness guard for the
// memoisation: what Binary hands out must be byte-identical to an independent
// decompression of the same embedded asset, for every supported platform. A
// memoised blob that is stale, truncated, or belongs to the other platform
// would ship straight into the image as PID 1.
func TestBinaryMemoisedBytesMatchFreshDecode(t *testing.T) {
	p := New(slog.Default())

	for _, tc := range []struct {
		plat ports.Platform
		path string
	}{
		{ports.LinuxAMD64, assetLinuxAMD64},
		{ports.LinuxARM64, assetLinuxARM64},
	} {
		t.Run(tc.plat.String(), func(t *testing.T) {
			want := freshDecode(t, tc.path)

			got, err := p.Binary(context.Background(), tc.plat)
			if err != nil {
				t.Fatalf("Binary(%s): %v", tc.plat, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("Binary(%s) returned %d memoised bytes that differ from a fresh %d-byte decode",
					tc.plat, len(got), len(want))
			}

			// A second call must be the same memoised array, and still equal
			// to the independent decode — so "memoised" cannot quietly mean
			// "memoised something else".
			again, err := p.Binary(context.Background(), tc.plat)
			if err != nil {
				t.Fatalf("second Binary(%s): %v", tc.plat, err)
			}
			if !bytes.Equal(again, want) {
				t.Errorf("second Binary(%s) no longer matches a fresh decode", tc.plat)
			}
			if len(got) > 0 && &got[0] != &again[0] {
				t.Errorf("Binary(%s) re-decompressed rather than returning the memoised blob", tc.plat)
			}
		})
	}
}

// TestBinaryDoesNotCrossPlatforms guards the memoisation key: amd64 and arm64
// are separate entries, so one platform's blob can never be served under the
// other's name.
func TestBinaryDoesNotCrossPlatforms(t *testing.T) {
	p := New(slog.Default())

	amd, err := p.Binary(context.Background(), ports.LinuxAMD64)
	if err != nil {
		if errors.Is(err, core.ErrStaticServerUnavailable) {
			t.Skip("static server binaries not embedded; run `make static-server` first")
		}
		t.Fatalf("Binary(LinuxAMD64): %v", err)
	}
	arm, err := p.Binary(context.Background(), ports.LinuxARM64)
	if err != nil {
		t.Fatalf("Binary(LinuxARM64): %v", err)
	}

	if bytes.Equal(amd, arm) {
		t.Fatal("amd64 and arm64 returned identical bytes — the two platforms share one memoisation entry")
	}
	if !bytes.Equal(amd, freshDecode(t, assetLinuxAMD64)) {
		t.Error("amd64 blob does not match a fresh decode of the amd64 asset")
	}
	if !bytes.Equal(arm, freshDecode(t, assetLinuxARM64)) {
		t.Error("arm64 blob does not match a fresh decode of the arm64 asset")
	}
}

// TestVersionMatchesFreshDecodeDigest pins Version to the SHA-256 of an
// independently decompressed amd64 blob, and to stability across calls. This is
// the "same digest before and after memoisation" property: the expected value
// is derived outside the memoised path, so it would still be right if the cache
// were wrong.
func TestVersionMatchesFreshDecodeDigest(t *testing.T) {
	p := New(slog.Default())

	want := fmt.Sprintf("%x", sha256.Sum256(freshDecode(t, assetLinuxAMD64)))

	for i := range 3 {
		got, err := p.Version(context.Background())
		if err != nil {
			t.Fatalf("call %d: Version(): %v", i, err)
		}
		if got != want {
			t.Fatalf("call %d: Version() = %q, want %q (SHA-256 of an independent decode of %s)",
				i, got, want, assetLinuxAMD64)
		}
	}

	// A second Provider instance must agree: the memoisation is process-wide,
	// not per-instance, and must not depend on which Provider asked first.
	other, err := New(nil).Version(context.Background())
	if err != nil {
		t.Fatalf("second Provider Version(): %v", err)
	}
	if other != want {
		t.Errorf("second Provider Version() = %q, want %q", other, want)
	}
}

// TestStaticServerBlobsCoverEverySupportedPlatform proves the memoisation table
// has no gap: every path Binary can produce is a table entry, so
// loadStaticServerBlob's uncached fallback is unreachable for real platforms. It
// asserts this by checking the table directly rather than by trusting the
// switch and the table to have been edited together.
func TestStaticServerBlobsCoverEverySupportedPlatform(t *testing.T) {
	for _, plat := range ports.SupportedPlatforms {
		var want string
		switch plat {
		case ports.LinuxAMD64:
			want = assetLinuxAMD64
		case ports.LinuxARM64:
			want = assetLinuxARM64
		default:
			t.Fatalf("platform %s is in ports.SupportedPlatforms but has no static server asset constant", plat)
		}
		if _, ok := staticServerBlobs[want]; !ok {
			t.Errorf("asset %s (for %s) has no memoisation entry; Binary would decompress it on every call", want, plat)
		}
	}
	if len(staticServerBlobs) != len(ports.SupportedPlatforms) {
		t.Errorf("memoisation table holds %d entries for %d supported platforms",
			len(staticServerBlobs), len(ports.SupportedPlatforms))
	}
}

// TestLoadStaticServerBlobUnknownPath covers the fallback branch: a path with no
// table entry is read uncached and reports the read failure rather than
// panicking on a nil map value.
func TestLoadStaticServerBlobUnknownPath(t *testing.T) {
	res := loadStaticServerBlob("bin/pokkum-static-linux-sparc64.zst")
	if res.readErr == nil {
		t.Fatal("loadStaticServerBlob on an unembedded path did not report a read error")
	}
	if res.data != nil {
		t.Error("loadStaticServerBlob returned data for an unembedded path")
	}
}

// TestBinaryConcurrentCallsShareOneBlob exercises the memoisation under
// concurrency (the point of `go test -race` here): every goroutine must get the
// same backing array and the same content, for both platforms at once.
func TestBinaryConcurrentCallsShareOneBlob(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency test in short mode")
	}

	p := New(slog.Default())
	if _, err := p.Binary(context.Background(), ports.LinuxAMD64); errors.Is(err, core.ErrStaticServerUnavailable) {
		t.Skip("static server binaries not embedded; run `make static-server` first")
	}

	const goroutines = 16
	var wg sync.WaitGroup
	type sample struct {
		plat ports.Platform
		addr *byte
		sum  [32]byte
	}
	samples := make(chan sample, goroutines*len(ports.SupportedPlatforms))

	for range goroutines {
		for _, plat := range ports.SupportedPlatforms {
			wg.Add(1)
			go func() {
				defer wg.Done()
				data, err := p.Binary(context.Background(), plat)
				if err != nil {
					t.Errorf("Binary(%s): %v", plat, err)
					return
				}
				if len(data) == 0 {
					t.Errorf("Binary(%s) returned empty data", plat)
					return
				}
				// Version() runs on the same memoised entry; call it here too
				// so the digest memo is exercised concurrently with the blob.
				if _, err := p.Version(context.Background()); err != nil {
					t.Errorf("Version(): %v", err)
					return
				}
				samples <- sample{plat: plat, addr: &data[0], sum: sha256.Sum256(data)}
			}()
		}
	}

	wg.Wait()
	close(samples)

	first := make(map[ports.Platform]sample)
	for s := range samples {
		prev, seen := first[s.plat]
		if !seen {
			first[s.plat] = s
			continue
		}
		if prev.addr != s.addr {
			t.Errorf("Binary(%s) handed out two different backing arrays across goroutines", s.plat)
		}
		if prev.sum != s.sum {
			t.Errorf("Binary(%s) handed out two different contents across goroutines", s.plat)
		}
	}
	if len(first) != len(ports.SupportedPlatforms) {
		t.Errorf("collected samples for %d platforms, want %d", len(first), len(ports.SupportedPlatforms))
	}
}
