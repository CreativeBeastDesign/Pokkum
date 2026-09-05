package sigstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// readWrongKeysRoot loads testdata/trusted-root-wrong-keys.json — a structurally
// valid copy of the public-good root with the Rekor log key swapped for an
// unrelated P-256 key. See TestVerify_WrongTrustedRootFailsClosed for its
// provenance. It is the second, genuinely different trust root these cache
// tests need: "different root" has to mean a root that produces a different
// verdict on the same material, not merely different bytes.
func readWrongKeysRoot(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "trusted-root-wrong-keys.json"))
	if err != nil {
		t.Fatalf("read testdata/trusted-root-wrong-keys.json: %v", err)
	}
	return b
}

// TestTrustedRootCache_KeyedOnContent is the structural half of the cache-key
// guard: the memoisation key must be the SHA-256 of the trusted-root bytes, so
// identical bytes reuse an entry and different bytes never can.
func TestTrustedRootCache_KeyedOnContent(t *testing.T) {
	good := DefaultTrustedRootJSON()
	wrong := readWrongKeysRoot(t)

	v := NewVerifier(nil)

	// Same bytes twice: one entry, reused.
	first := v.loadTrustedRoot(good)
	second := v.loadTrustedRoot(good)
	if first != second {
		t.Error("loadTrustedRoot re-parsed an identical trusted root instead of reusing the memoised entry")
	}
	if first.parseErr != nil || first.verifierErr != nil {
		t.Fatalf("embedded trusted root did not load: parseErr=%v verifierErr=%v", first.parseErr, first.verifierErr)
	}

	// A *different* root must never be served the first one's entry. This is
	// the property that keeps the cache incapable of changing a verdict.
	other := v.loadTrustedRoot(wrong)
	if other == first {
		t.Fatal("two different trusted roots shared one cache entry — the key is not content-derived")
	}
	if other.root == first.root {
		t.Error("two different trusted roots share one parsed *root.TrustedRoot")
	}
	if other.verifier == first.verifier {
		t.Error("two different trusted roots share one *verify.Verifier")
	}

	// And they are filed under their own content digests, not under a path,
	// a supplied/not-supplied flag, or a single default slot.
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.roots) != 2 {
		t.Errorf("cache holds %d entries after two distinct roots, want 2", len(v.roots))
	}
	if _, ok := v.roots[digestOf(good)]; !ok {
		t.Error("embedded root is not filed under digestOf(its bytes)")
	}
	if _, ok := v.roots[digestOf(wrong)]; !ok {
		t.Error("wrong-keys root is not filed under digestOf(its bytes)")
	}
}

// TestVerify_TwoTrustedRootsOnOneVerifierKeepSeparateVerdicts is the behavioural
// half, and the one that actually matters: the same *Verifier*, reused across
// two different trust roots, must produce each root's own verdict. A cache keyed
// on anything coarser than content would let the first root's parsed material
// decide the second root's verification — which is the only way memoising a
// verification *input* could ever become a fail-open.
//
// Both orderings are exercised, because a cache that leaks only warm-to-cold
// (or only cold-to-warm) would pass a single-ordering test.
func TestVerify_TwoTrustedRootsOnOneVerifierKeepSeparateVerdicts(t *testing.T) {
	good := DefaultTrustedRootJSON()
	wrong := readWrongKeysRoot(t)

	verifyWith := func(t *testing.T, v *Verifier, rootJSON []byte) error {
		t.Helper()
		req := loadFixture(t)
		req.TrustedRootJSON = rootJSON
		_, err := v.Verify(context.Background(), req)
		return err
	}

	t.Run("good root first, then wrong root", func(t *testing.T) {
		v := NewVerifier(nil)
		if err := verifyWith(t, v, good); err != nil {
			t.Fatalf("Verify with the real embedded trust root failed: %v", err)
		}
		err := verifyWith(t, v, wrong)
		if err == nil {
			t.Fatal("Verify accepted the fixture against the substituted-key trust root after a good root had been cached — the cache leaked a verdict")
		}
		if !errors.Is(err, ErrTlogInvalid) {
			t.Fatalf("Verify error = %v, want one wrapping ErrTlogInvalid", err)
		}
	})

	t.Run("wrong root first, then good root", func(t *testing.T) {
		v := NewVerifier(nil)
		err := verifyWith(t, v, wrong)
		if err == nil {
			t.Fatal("Verify accepted the fixture against the substituted-key trust root")
		}
		if !errors.Is(err, ErrTlogInvalid) {
			t.Fatalf("Verify error = %v, want one wrapping ErrTlogInvalid", err)
		}
		// The negative result must not poison the good root either.
		if err := verifyWith(t, v, good); err != nil {
			t.Fatalf("Verify with the real embedded trust root failed after a bad root had been cached: %v", err)
		}
	})

	t.Run("repeated on one verifier, verdicts stay stable", func(t *testing.T) {
		v := NewVerifier(nil)
		for i := range 4 {
			if err := verifyWith(t, v, good); err != nil {
				t.Fatalf("iteration %d: good root failed: %v", i, err)
			}
			if err := verifyWith(t, v, wrong); !errors.Is(err, ErrTlogInvalid) {
				t.Fatalf("iteration %d: wrong root error = %v, want ErrTlogInvalid", i, err)
			}
		}
	})
}

// TestVerify_CorruptTrustedRootRejectedWithWarmCache proves a malformed trust
// root is still rejected once the cache holds a good entry — i.e. a cache miss
// on damaged input cannot fall back to whatever was cached before, and a cached
// *failure* keeps failing rather than decaying into a pass on a second look.
func TestVerify_CorruptTrustedRootRejectedWithWarmCache(t *testing.T) {
	good := DefaultTrustedRootJSON()

	corruptions := map[string][]byte{
		"truncated to half":       good[:len(good)/2],
		"truncated to one byte":   good[:1],
		"not JSON at all":         []byte("this is not a trusted root"),
		"empty JSON object":       []byte("{}"),
		"valid JSON, wrong shape": []byte(`{"mediaType":"application/vnd.dev.sigstore.trustedroot+json;version=0.1"}`),
	}

	for name, bad := range corruptions {
		t.Run(name, func(t *testing.T) {
			v := NewVerifier(nil)

			// Warm the cache with a root that verifies.
			warm := loadFixture(t)
			warm.TrustedRootJSON = good
			if _, err := v.Verify(context.Background(), warm); err != nil {
				t.Fatalf("warming Verify with the embedded trust root failed: %v", err)
			}

			req := loadFixture(t)
			req.TrustedRootJSON = bad

			// Twice: the first call populates the entry, the second reads the
			// memoised failure back. Both must reject.
			for i := range 2 {
				_, err := v.Verify(context.Background(), req)
				if err == nil {
					t.Fatalf("call %d: Verify accepted a corrupt trusted root", i)
				}
				if !errors.Is(err, ErrMalformedMaterial) && !errors.Is(err, ErrChainInvalid) && !errors.Is(err, ErrTlogInvalid) {
					t.Fatalf("call %d: Verify error = %v, want one of ErrMalformedMaterial/ErrChainInvalid/ErrTlogInvalid", i, err)
				}
			}
		})
	}
}

// TestLoadTrustedRoot_EmptyIsNotCached covers the one input with no content to
// key on. It must fail to parse and must not occupy an entry — a slot under the
// digest of nothing is a key that means nothing.
func TestLoadTrustedRoot_EmptyIsNotCached(t *testing.T) {
	v := NewVerifier(nil)

	for _, empty := range [][]byte{nil, {}} {
		entry := v.loadTrustedRoot(empty)
		if entry.parseErr == nil {
			t.Error("loadTrustedRoot accepted empty trusted-root bytes")
		}
		if entry.root != nil || entry.verifier != nil {
			t.Error("loadTrustedRoot returned material for empty trusted-root bytes")
		}
	}

	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.roots) != 0 {
		t.Errorf("cache holds %d entries after only empty input, want 0", len(v.roots))
	}
}

// TestVerify_ConcurrentAcrossTrustedRoots runs the good and the substituted-key
// root concurrently through one Verifier. Under -race this covers the cache's
// locking; regardless of -race it re-asserts that concurrency cannot cross a
// verdict from one root onto the other.
func TestVerify_ConcurrentAcrossTrustedRoots(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency test in short mode")
	}

	good := DefaultTrustedRootJSON()
	wrong := readWrongKeysRoot(t)
	base := loadFixture(t)

	v := NewVerifier(nil)

	const iterations = 8
	var wg sync.WaitGroup
	errs := make(chan error, iterations*2)

	for range iterations {
		wg.Add(2)
		go func() {
			defer wg.Done()
			req := base
			req.TrustedRootJSON = good
			if _, err := v.Verify(context.Background(), req); err != nil {
				errs <- err
			}
		}()
		go func() {
			defer wg.Done()
			req := base
			req.TrustedRootJSON = wrong
			if _, err := v.Verify(context.Background(), req); !errors.Is(err, ErrTlogInvalid) {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Verify produced the wrong verdict: %v", err)
	}
}
