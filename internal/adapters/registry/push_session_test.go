package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// pingCountingRegistry wraps the in-memory registry and counts the `GET /v2/`
// API-version pings each authenticated session costs.
type pingCountingRegistry struct {
	inner http.Handler

	mu    sync.Mutex
	pings int
}

func (p *pingCountingRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v2/" {
		p.mu.Lock()
		p.pings++
		p.mu.Unlock()
	}
	p.inner.ServeHTTP(w, r)
}

func (p *pingCountingRegistry) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pings
}

// TestPush_TagReconciliationSharesOneSession pins that the cost of the
// idempotent-skip path stops scaling with the number of tags.
//
// That path issues a HEAD and possibly a PUT per requested tag. Each of those
// used to go through a package-level remote.Head/remote.Tag call, which builds
// a fresh Puller/Pusher — an authn.Resolve, a /v2/ ping, and against a real
// registry a 401 challenge and a token request — per tag. A push requesting
// eight tags therefore paid for roughly nine authenticated sessions to move
// eight pointers.
//
// The assertion is on shape rather than an exact number: reconciling eight
// tags must not cost measurably more sessions than reconciling two.
func TestPush_TagReconciliationSharesOneSession(t *testing.T) {
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}

	// reconcile pushes img once, then pushes it again under len(tags) tags —
	// the second Push takes the "digest already present" branch and runs
	// reconcileTags. It returns the pings the second Push alone cost.
	reconcile := func(tags []string) int {
		counter := &pingCountingRegistry{inner: registry.New()}
		s := httptest.NewServer(counter)
		t.Cleanup(s.Close)

		repo := strings.TrimPrefix(s.URL, "http://") + "/acme/reconcile"
		a := NewAdapter(nil)
		ctx := context.Background()

		if _, err := a.Push(ctx, ports.PushRequest{
			Repo:     repo,
			Payload:  ports.Payload{Image: img},
			Tags:     []string{"seed"},
			Insecure: true,
		}); err != nil {
			t.Fatalf("seed push: %v", err)
		}

		before := counter.count()
		if _, err := a.Push(ctx, ports.PushRequest{
			Repo:     repo,
			Payload:  ports.Payload{Image: img},
			Tags:     tags,
			Insecure: true,
		}); err != nil {
			t.Fatalf("reconcile push: %v", err)
		}
		return counter.count() - before
	}

	few := reconcile([]string{"a", "b"})
	many := reconcile([]string{"a", "b", "c", "d", "e", "f", "g", "h"})

	t.Logf("/v2/ pings during the reconcile push: %d for 2 tags, %d for 8 tags", few, many)

	if few == 0 {
		t.Fatal("the reconcile push issued no /v2/ pings at all; the fixture is not measuring authentication setup")
	}
	if many != few {
		t.Fatalf("BUG: reconciling 8 tags cost %d authenticated sessions vs %d for 2 tags — "+
			"the per-tag HEAD/PUT pair is opening its own session instead of sharing the push's",
			many, few)
	}
}
