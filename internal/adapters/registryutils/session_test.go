package registryutils_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/registryutils"
)

// pingCountingTransport counts the `GET /v2/` API-version pings that
// go-containerregistry issues once per fetcher/writer it builds. That ping —
// plus, against a real registry, the 401 challenge and token request that
// follow it — is the per-operation cost a shared Session exists to eliminate,
// so counting it is the most direct measurement of whether sharing worked.
type pingCountingTransport struct {
	base  http.RoundTripper
	pings atomic.Int64
	all   atomic.Int64
}

func (t *pingCountingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.all.Add(1)
	if req.URL.Path == "/v2/" || req.URL.Path == "/v2" {
		t.pings.Add(1)
	}
	return t.base.RoundTrip(req)
}

func newTestRegistry(t *testing.T) (repo name.Repository, tr *pingCountingTransport) {
	t.Helper()
	s := httptest.NewServer(registry.New())
	t.Cleanup(s.Close)

	host := strings.TrimPrefix(s.URL, "http://")
	r, err := name.NewRepository(host+"/session/test", name.WeakValidation)
	if err != nil {
		t.Fatalf("parse repository: %v", err)
	}
	return r, &pingCountingTransport{base: http.DefaultTransport}
}

// TestSession_SharesOneAuthSessionAcrossOperations is the measurement behind
// the change: the number of authenticated sessions a run of registry
// operations costs stops scaling with the number of operations.
//
// It compares two arms doing the identical work. The control arm uses the
// package-level remote.Get/Head/Write/Tag functions, each of which builds a
// fresh Puller/Pusher and therefore a fresh fetcher — an authn.Resolve, a
// `GET /v2/` ping, and (against a real registry) a 401 challenge and a token
// request. The shared arm runs everything through one Session.
//
// The assertion is on the *shape*, not on a magic constant: the control's
// ping count must grow with the number of repetitions, while the shared
// session's must not grow at all.
func TestSession_SharesOneAuthSessionAcrossOperations(t *testing.T) {
	ctx := context.Background()

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}

	// perCall runs reps rounds of (write, 3 tags, 4 gets, 4 heads) against a
	// fresh registry using the package-level convenience functions.
	perCall := func(reps int) int64 {
		repo, tr := newTestRegistry(t)
		opts := []remote.Option{remote.WithContext(ctx), remote.WithTransport(tr)}
		for r := 0; r < reps; r++ {
			if err := remote.Write(repo.Tag("a"), img, opts...); err != nil {
				t.Fatalf("control write: %v", err)
			}
			for _, tag := range []string{"b", "c", "d"} {
				if err := remote.Tag(repo.Tag(tag), img, opts...); err != nil {
					t.Fatalf("control tag %s: %v", tag, err)
				}
			}
			for i := 0; i < 4; i++ {
				if _, err := remote.Get(repo.Tag("a"), opts...); err != nil {
					t.Fatalf("control get: %v", err)
				}
				if _, err := remote.Head(repo.Tag("a"), opts...); err != nil {
					t.Fatalf("control head: %v", err)
				}
			}
		}
		return tr.pings.Load()
	}

	// shared runs the identical sequence through one Session.
	shared := func(reps int) int64 {
		repo, tr := newTestRegistry(t)
		sess := registryutils.NewSession(remote.WithTransport(tr))
		for r := 0; r < reps; r++ {
			if err := sess.Push(ctx, repo.Tag("a"), img); err != nil {
				t.Fatalf("session push: %v", err)
			}
			for _, tag := range []string{"b", "c", "d"} {
				if err := sess.Put(ctx, repo.Tag(tag), img); err != nil {
					t.Fatalf("session put %s: %v", tag, err)
				}
			}
			for i := 0; i < 4; i++ {
				if _, err := sess.Get(ctx, repo.Tag("a")); err != nil {
					t.Fatalf("session get: %v", err)
				}
				if _, err := sess.Head(ctx, repo.Tag("a")); err != nil {
					t.Fatalf("session head: %v", err)
				}
			}
		}
		return tr.pings.Load()
	}

	control1, control4 := perCall(1), perCall(4)
	shared1, shared4 := shared(1), shared(4)

	t.Logf("/v2/ pings — per-call: %d (1 round) -> %d (4 rounds); shared session: %d -> %d",
		control1, control4, shared1, shared4)

	if control1 == 0 {
		t.Fatal("the control arm issued no /v2/ pings at all; the fixture is not measuring authentication setup")
	}
	if control4 <= control1 {
		t.Fatalf("control arm did not scale: %d pings for 1 round vs %d for 4; the fixture cannot distinguish per-call from shared sessions", control1, control4)
	}
	if shared4 != shared1 {
		t.Fatalf("BUG: the shared session issued %d pings for 1 round but %d for 4 — something is rebuilding the fetcher or writer per operation instead of once",
			shared1, shared4)
	}
	if shared4 >= control4 {
		t.Fatalf("BUG: the shared session cost %d pings vs %d for the per-call control over the same work: no saving at all", shared4, control4)
	}
}

// TestSession_GetIsNotContentCached is the safety half of the change above.
//
// A shared Puller memoises the *fetcher* — the resolved authenticator and the
// configured http.Client — and nothing else. If it also memoised manifests,
// every "re-pull and compare the digest before verifying" check in this
// codebase would silently become a no-op: baseimage's VerifyBaseImage compares
// the digest it re-pulls against the one Resolve returned precisely so that a
// floating tag which moved in between is caught, and a cached manifest would
// hand back the pre-move digest and report a match.
//
// So: move the tag under a live session and assert the session observes the
// move.
func TestSession_GetIsNotContentCached(t *testing.T) {
	ctx := context.Background()
	repo, tr := newTestRegistry(t)
	sess := registryutils.NewSession(remote.WithTransport(tr))

	imgA, err := random.Image(128, 1)
	if err != nil {
		t.Fatalf("random.Image A: %v", err)
	}
	imgB, err := random.Image(256, 2)
	if err != nil {
		t.Fatalf("random.Image B: %v", err)
	}
	digestA, err := imgA.Digest()
	if err != nil {
		t.Fatalf("digest A: %v", err)
	}
	digestB, err := imgB.Digest()
	if err != nil {
		t.Fatalf("digest B: %v", err)
	}
	if digestA == digestB {
		t.Fatal("fixture is degenerate: both images have the same digest")
	}

	tag := repo.Tag("floating")
	if err := sess.Push(ctx, tag, imgA); err != nil {
		t.Fatalf("push A: %v", err)
	}

	gotA, err := sess.Get(ctx, tag)
	if err != nil {
		t.Fatalf("get before move: %v", err)
	}
	if gotA.Digest != digestA {
		t.Fatalf("get before move: %s != %s", gotA.Digest, digestA)
	}
	headA, err := sess.Head(ctx, tag)
	if err != nil {
		t.Fatalf("head before move: %v", err)
	}
	if headA.Digest != digestA {
		t.Fatalf("head before move: %s != %s", headA.Digest, digestA)
	}

	// The tag moves out from under the session, exactly as a floating
	// upstream tag can between a resolve and its verification.
	if err := sess.Push(ctx, tag, imgB); err != nil {
		t.Fatalf("push B: %v", err)
	}

	gotB, err := sess.Get(ctx, tag)
	if err != nil {
		t.Fatalf("get after move: %v", err)
	}
	if gotB.Digest != digestB {
		t.Fatalf("BUG: the shared session returned the pre-move digest %s after the tag moved to %s — "+
			"a Puller is caching manifest content, which silently defeats every re-pull-and-compare check in this codebase",
			gotB.Digest, digestB)
	}
	headB, err := sess.Head(ctx, tag)
	if err != nil {
		t.Fatalf("head after move: %v", err)
	}
	if headB.Digest != digestB {
		t.Fatalf("BUG: the shared session's Head returned the pre-move digest %s after the tag moved to %s", headB.Digest, digestB)
	}
}

// TestSession_ConcurrentUse drives one Session from many goroutines at once.
// The Puller and Pusher are built under a sync.Once and go-containerregistry's
// own reader/writer maps are sync.Maps, but a Session is shared across a
// build's concurrent stages, so the claim needs a -race witness rather than an
// argument.
func TestSession_ConcurrentUse(t *testing.T) {
	ctx := context.Background()
	repo, tr := newTestRegistry(t)
	sess := registryutils.NewSession(remote.WithTransport(tr))

	img, err := random.Image(128, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	want, err := img.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if err := sess.Push(ctx, repo.Tag("base"), img); err != nil {
		t.Fatalf("seed push: %v", err)
	}

	const workers = 12
	errs := make([]error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			d, err := sess.Get(ctx, repo.Tag("base"))
			if err != nil {
				errs[i] = err
				return
			}
			if d.Digest != want {
				errs[i] = context.DeadlineExceeded // placeholder; checked below
			}
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
	}
}
