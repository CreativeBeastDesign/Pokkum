package bunruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// seedVerifiedCache writes content plus a matching digest sidecar at exactly
// the path Resolve computes for (version, variant, platform), i.e. the state
// a previous, genuinely-verified download would have left behind. Returns
// the binary path and its SHA256.
//
// Writing the cache directly (rather than driving a fake release server) is
// deliberate: these tests are about the cache-hit path only, and every
// request below sets Offline so that a *missed* cache hit fails loudly with
// ErrHermeticViolation instead of quietly reaching for the network.
func seedVerifiedCache(t *testing.T, cacheDir, version string, variant ports.BunVariant, platform ports.Platform, content []byte) (string, string) {
	t.Helper()
	slug := strings.ReplaceAll(platform.String(), "/", "_")
	dir := filepath.Join(cacheDir, version, string(variant), slug)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("seed cache dir: %v", err)
	}
	binaryPath := filepath.Join(dir, "bun")
	if err := os.WriteFile(binaryPath, content, 0o700); err != nil {
		t.Fatalf("seed cached binary: %v", err)
	}
	sum := sha256.Sum256(content)
	sha := hex.EncodeToString(sum[:])
	if err := os.WriteFile(cacheDigestSidecarPath(binaryPath), []byte(sha), 0o600); err != nil {
		t.Fatalf("seed digest sidecar: %v", err)
	}
	return binaryPath, sha
}

func memoRequest(platform ports.Platform) ports.BunResolverRequest {
	return ports.BunResolverRequest{
		Platform:        platform,
		Version:         "1.2.2",
		Variant:         ports.BunVariantStandard,
		SourceDateEpoch: time.Unix(1700000000, 0),
		Offline:         true,
	}
}

// bumpMTime forces path's modification time to be distinctly newer, so the
// same-length rewrite cases below cannot pass or fail on filesystem
// timestamp granularity.
func bumpMTime(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	newTime := info.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(path, newTime, newTime); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// TestResolver_Memo_ReVerifiesWhenCachedFileChanges is the core guard on the
// process-scoped verified-digest memo: a memo keyed on the path alone would
// return the first call's digest forever. The cached binary is legitimately
// replaced between two Resolve calls (as a concurrent pokkum process
// re-downloading the same version would), and the second Resolve must report
// the NEW digest — which it can only do by re-hashing.
//
// The same-size subtest is the sharper half: there, size is unchanged and
// only the mtime (and, for the replace-via-rename case, the inode) differs,
// so it fails if the key drops mtime/identity and keeps only path+size.
func TestResolver_Memo_ReVerifiesWhenCachedFileChanges(t *testing.T) {
	for _, tc := range []struct {
		name      string
		first     []byte
		second    []byte
		sameSized bool
	}{
		{
			name:   "different size",
			first:  []byte("#!/bin/sh\necho 'bun one'"),
			second: []byte("#!/bin/sh\necho 'bun two, and rather longer than the first'"),
		},
		{
			name:      "same size",
			first:     []byte("#!/bin/sh\necho 'bun AAAA'"),
			second:    []byte("#!/bin/sh\necho 'bun BBBB'"),
			sameSized: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.sameSized && len(tc.first) != len(tc.second) {
				t.Fatalf("test bug: contents differ in length (%d vs %d)", len(tc.first), len(tc.second))
			}
			cacheDir := t.TempDir()
			binaryPath, firstSHA := seedVerifiedCache(t, cacheDir, "1.2.2", ports.BunVariantStandard, ports.LinuxAMD64, tc.first)
			resolver := NewResolver(cacheDir, nil)
			req := memoRequest(ports.LinuxAMD64)

			res1, err := resolver.Resolve(context.Background(), req)
			if err != nil {
				t.Fatalf("first resolve: %v", err)
			}
			if res1.SHA256 != firstSHA {
				t.Fatalf("first resolve digest = %s, want %s", res1.SHA256, firstSHA)
			}

			// Replace the cached binary and its sidecar, exactly as a fresh
			// verified download would.
			if _, secondSHA := seedVerifiedCache(t, cacheDir, "1.2.2", ports.BunVariantStandard, ports.LinuxAMD64, tc.second); true {
				bumpMTime(t, binaryPath)
				bumpMTime(t, cacheDigestSidecarPath(binaryPath))

				res2, err := resolver.Resolve(context.Background(), req)
				if err != nil {
					t.Fatalf("second resolve: %v", err)
				}
				if res2.SHA256 == firstSHA {
					t.Fatalf("BUG: memo returned the stale digest %s after the cached file changed; the second Resolve did not re-hash", firstSHA)
				}
				if res2.SHA256 != secondSHA {
					t.Fatalf("second resolve digest = %s, want %s", res2.SHA256, secondSHA)
				}
				if res2.Size != int64(len(tc.second)) {
					t.Errorf("second resolve size = %d, want %d", res2.Size, len(tc.second))
				}
			}
		})
	}
}

// TestResolver_Memo_TamperedBinaryStillRejected is the security half: the
// memo must not turn a corrupted cache entry into a hit. The binary is
// overwritten with attacker bytes of the identical length while the sidecar
// (the record of what was actually verified) is left alone, so the only
// honest answer is "cache miss" — and with Offline set, a cache miss must
// fail closed rather than return anything at all.
func TestResolver_Memo_TamperedBinaryStillRejected(t *testing.T) {
	real := []byte("#!/bin/sh\necho 'the real, verified bun'")
	tampered := []byte("#!/bin/sh\necho 'attacker-swapped bun!!'")
	if len(real) != len(tampered) {
		t.Fatalf("test bug: contents differ in length (%d vs %d)", len(real), len(tampered))
	}

	cacheDir := t.TempDir()
	binaryPath, realSHA := seedVerifiedCache(t, cacheDir, "1.2.2", ports.BunVariantStandard, ports.LinuxAMD64, real)
	resolver := NewResolver(cacheDir, nil)
	req := memoRequest(ports.LinuxAMD64)

	res, err := resolver.Resolve(context.Background(), req)
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if res.SHA256 != realSHA {
		t.Fatalf("first resolve digest = %s, want %s", res.SHA256, realSHA)
	}

	// Tamper in place, leaving the sidecar (and the length) untouched.
	if err := os.WriteFile(binaryPath, tampered, 0o700); err != nil {
		t.Fatalf("simulate tampering: %v", err)
	}
	bumpMTime(t, binaryPath)

	res2, err := resolver.Resolve(context.Background(), req)
	if err == nil {
		t.Fatalf("BUG: tampered cache entry was accepted as a memo hit and returned %s (%s)", res2.SHA256, res2.BinaryPath)
	}
	if !errors.Is(err, core.ErrHermeticViolation) {
		t.Errorf("expected the tampered entry to be a cache miss failing closed under Offline, got: %v", err)
	}

	// A failed verification must never itself be memoised: repeating the
	// call must repeat the full check and keep failing, not settle into a
	// remembered answer.
	if res3, err := resolver.Resolve(context.Background(), req); err == nil {
		t.Fatalf("BUG: a failed verification was memoised — the repeat resolve returned %s (%s)", res3.SHA256, res3.BinaryPath)
	}
}

// TestResolver_Memo_DoesNotCrossRequests guards the "never answer for the
// wrong version/variant/platform" constraint on the memo key. Two platforms
// are seeded with different content in the same cache; resolving both from
// one Resolver must yield each platform's own digest.
func TestResolver_Memo_DoesNotCrossRequests(t *testing.T) {
	cacheDir := t.TempDir()
	_, amdSHA := seedVerifiedCache(t, cacheDir, "1.2.2", ports.BunVariantStandard, ports.LinuxAMD64, []byte("amd64 bun"))
	_, armSHA := seedVerifiedCache(t, cacheDir, "1.2.2", ports.BunVariantStandard, ports.LinuxARM64, []byte("arm64 bun, different"))
	if amdSHA == armSHA {
		t.Fatal("test bug: seeded identical content for both platforms")
	}
	resolver := NewResolver(cacheDir, nil)

	for range 2 { // second pass exercises the memo, not the cold path
		amd, err := resolver.Resolve(context.Background(), memoRequest(ports.LinuxAMD64))
		if err != nil {
			t.Fatalf("resolve amd64: %v", err)
		}
		arm, err := resolver.Resolve(context.Background(), memoRequest(ports.LinuxARM64))
		if err != nil {
			t.Fatalf("resolve arm64: %v", err)
		}
		if amd.SHA256 != amdSHA {
			t.Errorf("amd64 digest = %s, want %s", amd.SHA256, amdSHA)
		}
		if arm.SHA256 != armSHA {
			t.Errorf("arm64 digest = %s, want %s", arm.SHA256, armSHA)
		}
		if amd.BinaryPath == arm.BinaryPath {
			t.Errorf("both platforms resolved to the same path %s", amd.BinaryPath)
		}
	}
}

// TestResolver_Memo_ConcurrentResolvesAreRaceFree runs the fan-out shape the
// pipeline actually uses — several concurrent Resolve calls across two
// platforms against one Resolver — so `go test -race` sees the memo map
// under concurrent read and write. Without the dedicated verifiedMu around
// r.verified this fails with a race report.
func TestResolver_Memo_ConcurrentResolvesAreRaceFree(t *testing.T) {
	cacheDir := t.TempDir()
	_, amdSHA := seedVerifiedCache(t, cacheDir, "1.2.2", ports.BunVariantStandard, ports.LinuxAMD64, []byte("amd64 bun"))
	_, armSHA := seedVerifiedCache(t, cacheDir, "1.2.2", ports.BunVariantStandard, ports.LinuxARM64, []byte("arm64 bun, different"))
	resolver := NewResolver(cacheDir, nil)

	want := map[ports.Platform]string{ports.LinuxAMD64: amdSHA, ports.LinuxARM64: armSHA}
	platforms := []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64}

	var wg sync.WaitGroup
	errCh := make(chan error, 64)
	for i := range 32 {
		platform := platforms[i%len(platforms)]
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := resolver.Resolve(context.Background(), memoRequest(platform))
			if err != nil {
				errCh <- err
				return
			}
			if res.SHA256 != want[platform] {
				errCh <- errors.New("wrong digest for " + platform.String() + ": " + res.SHA256)
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

// TestNewResolver_DefaultHTTPClientHasTimeouts asserts on the client
// NewResolver actually constructs, not on a comment: before this, a nil
// httpClient fell through to http.DefaultClient, which has no Timeout and no
// transport phase timeouts, so a stalled connection hung the ~90MB release
// download forever.
func TestNewResolver_DefaultHTTPClientHasTimeouts(t *testing.T) {
	r := NewResolver(t.TempDir(), nil)

	if r.HTTPClient == http.DefaultClient {
		t.Fatal("BUG: resolver fell back to http.DefaultClient, which has no timeout")
	}
	// Generous enough that a real 90MB archive over a slow link cannot be
	// aborted by it (30m ~= 50KB/s), but still finite.
	if r.HTTPClient.Timeout < 15*time.Minute {
		t.Errorf("client Timeout = %s, want a finite backstop of at least 15m for a ~90MB download", r.HTTPClient.Timeout)
	}
	transport, ok := r.HTTPClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client transport is %T, want *http.Transport with phase timeouts", r.HTTPClient.Transport)
	}
	if transport.ResponseHeaderTimeout <= 0 {
		t.Error("transport ResponseHeaderTimeout is unset: a connection that accepts but never responds would stall until the total timeout")
	}
	if transport.TLSHandshakeTimeout <= 0 {
		t.Error("transport TLSHandshakeTimeout is unset")
	}
	if transport.DialContext == nil {
		t.Error("transport DialContext is unset: no dial timeout is configured")
	}

	// An explicitly supplied client is still honoured untouched.
	custom := &http.Client{}
	if got := NewResolver(t.TempDir(), custom); got.HTTPClient != custom {
		t.Error("explicitly supplied HTTP client was replaced")
	}
}

// TestResolver_StalledResponse_IsAbortedByResponseHeaderTimeout proves the
// phase timeout works end-to-end against a server that accepts the
// connection and then never sends a response header — the exact stall shape
// http.DefaultClient could not escape.
func TestResolver_StalledResponse_IsAbortedByResponseHeaderTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		server.Close()
	})

	// total is deliberately long here so that only ResponseHeaderTimeout can
	// end this request: if it were the flat timeout doing the work, the
	// assertion below would not be about the phase timeout at all.
	resolver := &Resolver{
		CacheDir: t.TempDir(),
		HTTPClient: newHTTPClient(httpTimeouts{
			dial:           2 * time.Second,
			tlsHandshake:   2 * time.Second,
			responseHeader: 200 * time.Millisecond,
			total:          5 * time.Minute,
		}),
	}

	done := make(chan error, 1)
	go func() {
		_, err := resolver.fetchSmall(context.Background(), server.URL)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the stalled fetch to fail, got nil error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("BUG: stalled fetch was never aborted — ResponseHeaderTimeout is not in effect on the resolver's client")
	}
}
