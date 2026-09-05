package remotecacheutils_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/remotecacheutils"
)

// The remote cache could never hit. pokkum.lock is written twice during a
// build — SaveLockfile stamps updatedAt, RecordScanResult stamps a per-entry
// lastScannedAt — and both happen before ComputeInputHash. Because the tree
// hash walked every file in the project root, it covered a file this build had
// just rewritten with the current wall clock, so two builds of byte-identical
// source produced different composite input hashes.
//
// Nothing failed when this was broken, which is why it survived: a cache that
// never hits behaves exactly like a cache that correctly misses. The only
// observable is the hash itself, so that is what this test asserts.
func TestComputeSourceTreeHash_IgnoresLockfileMetadata(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "package.json"), `{"name":"app"}`)
	mustWrite(t, filepath.Join(dir, "src.js"), `export const a = 1`)

	lock := filepath.Join(dir, "pokkum.lock")
	lockWith := func(updatedAt, scannedAt string) string {
		return `{"version":1,"updatedAt":"` + updatedAt + `","bases":{` +
			`"distroless":{"ref":"gcr.io/distroless/cc-debian12:nonroot",` +
			`"digest":"sha256:1111111111111111111111111111111111111111111111111111111111111111",` +
			`"updatedAt":"` + updatedAt + `","lastScannedAt":"` + scannedAt + `"}}}`
	}

	mustWrite(t, lock, lockWith("2026-09-05T10:00:00Z", "2026-09-05T10:00:00Z"))
	first, err := remotecacheutils.ComputeSourceTreeHash(dir)
	if err != nil {
		t.Fatalf("ComputeSourceTreeHash: %v", err)
	}

	// Exactly what a real build does to this file between two runs: it
	// rewrites the timestamps and nothing else. No source file is touched.
	mustWrite(t, lock, lockWith("2026-09-06T23:59:59Z", "2026-09-06T23:59:59Z"))
	second, err := remotecacheutils.ComputeSourceTreeHash(dir)
	if err != nil {
		t.Fatalf("ComputeSourceTreeHash: %v", err)
	}

	if first != second {
		t.Fatalf("pokkum.lock's timestamps alone changed the source-tree hash, so the remote cache can never hit:\n  before: %s\n  after:  %s", first, second)
	}

	// The floor: prove the hash is not simply constant. A test that passes
	// because ComputeSourceTreeHash ignores everything would be worthless,
	// and that is the exact shape of failure this file exists to catch.
	mustWrite(t, filepath.Join(dir, "src.js"), `export const a = 2`)
	changed, err := remotecacheutils.ComputeSourceTreeHash(dir)
	if err != nil {
		t.Fatalf("ComputeSourceTreeHash: %v", err)
	}
	if changed == first {
		t.Fatalf("editing a real source file did not change the hash (%s) — the hash is not observing the tree at all, so the assertion above proves nothing", changed)
	}
}

// A base digest changed by hand in pokkum.lock must still invalidate the
// build, since that is the one part of the lockfile that determines image
// bytes. It does so through InputParams.BaseImageDigest rather than through
// the tree hash, so this asserts the tree hash is genuinely not the mechanism
// and the exclusion above cannot be blamed for a stale hit.
func TestComputeSourceTreeHash_LockfileDigestIsNotTheCacheMechanism(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "package.json"), `{"name":"app"}`)
	lock := filepath.Join(dir, "pokkum.lock")

	mustWrite(t, lock, `{"version":1,"bases":{"distroless":{"digest":"sha256:aaaa"}}}`)
	withA, err := remotecacheutils.ComputeSourceTreeHash(dir)
	if err != nil {
		t.Fatalf("ComputeSourceTreeHash: %v", err)
	}
	mustWrite(t, lock, `{"version":1,"bases":{"distroless":{"digest":"sha256:bbbb"}}}`)
	withB, err := remotecacheutils.ComputeSourceTreeHash(dir)
	if err != nil {
		t.Fatalf("ComputeSourceTreeHash: %v", err)
	}
	if withA != withB {
		t.Fatalf("the tree hash still observes pokkum.lock; it must not, or build timestamps re-enter the cache key:\n  %s\n  %s", withA, withB)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
