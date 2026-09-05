package registryutils

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
)

// recordingKeychain answers every target with an authenticator whose username
// is the target's full string, and records how many times it was asked. That
// makes both properties under test directly observable: *which* credential
// came back, and whether the underlying keychain was consulted at all.
type recordingKeychain struct {
	mu    sync.Mutex
	calls map[string]int
}

func newRecordingKeychain() *recordingKeychain {
	return &recordingKeychain{calls: map[string]int{}}
}

func (r *recordingKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	r.mu.Lock()
	r.calls[target.String()]++
	r.mu.Unlock()
	return authn.FromConfig(authn.AuthConfig{
		Username: target.String(),
		Password: "secret-for-" + target.String(),
	}), nil
}

func (r *recordingKeychain) count(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[key]
}

func (r *recordingKeychain) total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, v := range r.calls {
		n += v
	}
	return n
}

func mustRepo(t *testing.T, s string) name.Repository {
	t.Helper()
	repo, err := name.NewRepository(s, name.WeakValidation)
	if err != nil {
		t.Fatalf("parse repository %q: %v", s, err)
	}
	return repo
}

// TestMemoKeychain_NeverCrossesTargets is the security guard on the credential
// memo.
//
// Memoising credential resolution is only acceptable if a cached entry can
// never be handed to a different target than the one it was resolved for.
// authn.DefaultKeychain looks up target.String() before falling back to
// target.RegistryStr(), so credentials can legitimately be scoped to a single
// repository — which means the memo has to key on the full resource string,
// not the registry host. Keying on the host would let one repository's
// credential be presented to another repository, and (worse) one registry's to
// another registry.
func TestMemoKeychain_NeverCrossesTargets(t *testing.T) {
	inner := newRecordingKeychain()
	memo := newMemoKeychain(inner)

	targets := []string{
		"registry.example.com/team-a/app",
		"registry.example.com/team-b/app",
		"other-registry.example.com/team-a/app",
		"ghcr.io/acme/service",
	}

	// Two passes: the first populates the memo, the second must be served
	// from it — and must still give every target its own credential.
	for pass := 0; pass < 2; pass++ {
		for _, target := range targets {
			auth, err := memo.Resolve(mustRepo(t, target))
			if err != nil {
				t.Fatalf("pass %d: resolve %s: %v", pass, target, err)
			}
			cfg, err := auth.Authorization()
			if err != nil {
				t.Fatalf("pass %d: authorization %s: %v", pass, target, err)
			}
			if cfg.Username != target {
				t.Fatalf("pass %d: BUG: target %q was handed the credential belonging to %q — the memo is returning one target's credentials for another",
					pass, target, cfg.Username)
			}
			if cfg.Password != "secret-for-"+target {
				t.Fatalf("pass %d: BUG: target %q was handed the secret belonging to %q", pass, target, cfg.Password)
			}
		}
	}

	// And the memo actually memoised: one inner resolution per distinct
	// target, not one per call.
	for _, target := range targets {
		if got := inner.count(target); got != 1 {
			t.Errorf("target %q resolved %d times through the inner keychain; want exactly 1", target, got)
		}
	}
	if got, want := inner.total(), len(targets); got != want {
		t.Errorf("inner keychain consulted %d times in total; want %d (one per distinct target)", got, want)
	}
}

// TestMemoKeychain_ConcurrentResolveIsCorrect drives the memo from many
// goroutines across several targets at once. A memo guarded by the wrong lock
// discipline could hand back a half-written map entry — i.e. another target's
// credential — under contention, which is exactly the failure this type must
// not have.
func TestMemoKeychain_ConcurrentResolveIsCorrect(t *testing.T) {
	inner := newRecordingKeychain()
	memo := newMemoKeychain(inner)

	targets := []string{
		"a.example.com/one",
		"a.example.com/two",
		"b.example.com/one",
		"c.example.com/deep/nested/repo",
	}

	const perTarget = 25
	errs := make(chan error, len(targets)*perTarget)
	var wg sync.WaitGroup
	for _, target := range targets {
		for i := 0; i < perTarget; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				auth, err := memo.ResolveContext(context.Background(), mustRepo(t, target))
				if err != nil {
					errs <- err
					return
				}
				cfg, err := auth.Authorization()
				if err != nil {
					errs <- err
					return
				}
				if cfg.Username != target {
					errs <- fmt.Errorf("target %q got credential for %q", target, cfg.Username)
				}
			}()
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent resolve: %v", err)
	}
}

// erroringKeychain always fails, so the memo's error handling is observable.
type erroringKeychain struct {
	mu    sync.Mutex
	calls int
	fail  bool
}

func (e *erroringKeychain) Resolve(target authn.Resource) (authn.Authenticator, error) {
	e.mu.Lock()
	e.calls++
	fail := e.fail
	e.mu.Unlock()
	if fail {
		return nil, fmt.Errorf("helper unavailable")
	}
	return authn.FromConfig(authn.AuthConfig{Username: target.String()}), nil
}

// TestMemoKeychain_DoesNotCacheErrors pins that a transient credential failure
// is not memoised. A credential helper that is briefly unavailable (Docker
// Desktop still starting, an expired SSO session being refreshed) must not
// poison every subsequent registry operation in the build.
func TestMemoKeychain_DoesNotCacheErrors(t *testing.T) {
	inner := &erroringKeychain{fail: true}
	memo := newMemoKeychain(inner)
	target := mustRepo(t, "registry.example.com/app")

	if _, err := memo.Resolve(target); err == nil {
		t.Fatal("expected the inner keychain's error to surface, got nil")
	}

	inner.mu.Lock()
	inner.fail = false
	inner.mu.Unlock()

	auth, err := memo.Resolve(target)
	if err != nil {
		t.Fatalf("BUG: the memo cached a transient failure and kept returning it: %v", err)
	}
	cfg, err := auth.Authorization()
	if err != nil {
		t.Fatalf("authorization: %v", err)
	}
	if cfg.Username != target.String() {
		t.Fatalf("got credential for %q, want %q", cfg.Username, target.String())
	}
}
