//go:build unix

package bunruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestResolver_CacheHitHashDoesNotSerializeAcrossPlatforms guards the mutex
// narrowing. Resolve used to hold r.mu across verifiedCacheHit, which
// SHA-256s the whole ~90MB cached binary, so the pipeline's per-platform
// fan-out serialized on that hash.
//
// A timing assertion would be flaky, so the hash is made *blocking* instead,
// deterministically: linux/amd64's cache entry is a FIFO, and os.Open on a
// FIFO with no writer blocks indefinitely — so the goroutine resolving
// linux/amd64 parks inside computeFileSHA256 and stays there until this test
// opens the write end. If the lock still covered that hash, the linux/arm64
// resolve (an ordinary, instantly-satisfiable cache hit) could not make
// progress and this test fails on its timeout.
func TestResolver_CacheHitHashDoesNotSerializeAcrossPlatforms(t *testing.T) {
	cacheDir := t.TempDir()

	// linux/amd64: a FIFO where the cached binary would be, with a sidecar
	// so the cache-hit path proceeds all the way into the hash.
	slug := strings.ReplaceAll(ports.LinuxAMD64.String(), "/", "_")
	blockingDir := filepath.Join(cacheDir, "1.2.2", string(ports.BunVariantStandard), slug)
	if err := os.MkdirAll(blockingDir, 0o700); err != nil {
		t.Fatalf("create blocking cache dir: %v", err)
	}
	fifoPath := filepath.Join(blockingDir, "bun")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Skipf("cannot create FIFO on this filesystem: %v", err)
	}
	if err := os.WriteFile(cacheDigestSidecarPath(fifoPath), []byte(strings.Repeat("a", 64)), 0o600); err != nil {
		t.Fatalf("write sidecar for FIFO: %v", err)
	}

	// linux/arm64: a normal, verified cache entry that must resolve promptly.
	_, armSHA := seedVerifiedCache(t, cacheDir, "1.2.2", ports.BunVariantStandard, ports.LinuxARM64, []byte("arm64 bun"))

	resolver := NewResolver(cacheDir, nil)

	blocked := make(chan struct{})
	blockedDone := make(chan struct{})
	go func() {
		defer close(blockedDone)
		close(blocked)
		// Blocks inside computeFileSHA256's os.Open of the FIFO until the
		// write end below is opened.
		_, _ = resolver.Resolve(context.Background(), memoRequest(ports.LinuxAMD64))
	}()
	<-blocked
	// Give the goroutine time to actually reach the blocking open. If it has
	// not, this test can only under-report (pass when it should have
	// blocked), never flake into a false failure.
	time.Sleep(250 * time.Millisecond)

	other := make(chan string, 1)
	otherErr := make(chan error, 1)
	go func() {
		res, err := resolver.Resolve(context.Background(), memoRequest(ports.LinuxARM64))
		if err != nil {
			otherErr <- err
			return
		}
		other <- res.SHA256
	}()

	select {
	case sha := <-other:
		if sha != armSHA {
			t.Errorf("linux/arm64 digest = %s, want %s", sha, armSHA)
		}
	case err := <-otherErr:
		t.Fatalf("linux/arm64 resolve failed: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("BUG: linux/arm64 cache hit was blocked by linux/amd64's in-progress hash — the resolver mutex is still held across verifiedCacheHit, serializing the per-platform fan-out on a ~90MB SHA-256")
	}

	// Unblock the parked goroutine so the test (and its TempDir cleanup) can
	// finish. Opening a FIFO for writing succeeds because a reader is parked
	// on the other end.
	go func() {
		f, err := os.OpenFile(fifoPath, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		_, _ = f.Write([]byte("unblock"))
		_ = f.Close()
	}()
	select {
	case <-blockedDone:
	case <-time.After(10 * time.Second):
		t.Error("the blocked linux/amd64 resolve never completed after the FIFO was unblocked")
	}
}
