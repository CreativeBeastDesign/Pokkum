package remotecacheutils_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/remotecacheutils"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// countingRegistry records every request the registry serves, so a test can
// assert on how much network work a single Cacher.Check actually costs.
type countingRegistry struct {
	inner http.Handler

	mu       sync.Mutex
	requests []string // "<METHOD> <path>"
	pings    int
}

func (c *countingRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.requests = append(c.requests, r.Method+" "+r.URL.Path)
	if r.URL.Path == "/v2/" {
		c.pings++
	}
	c.mu.Unlock()
	c.inner.ServeHTTP(w, r)
}

// since returns the requests recorded after mark, and the current mark.
func (c *countingRegistry) since(mark int) ([]string, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]string(nil), c.requests[mark:]...)
	return out, len(c.requests)
}

func (c *countingRegistry) pingCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pings
}

// TestCacher_Check_DoesNotRefetchTheSameManifest pins the request shape of a
// verified cache hit that reconciles a release tag.
//
// Check used to fetch the very same manifest up to three times over: once to
// probe the cache tag (discarding everything but the digest), once more inside
// ReconcileTags for a descriptor it had just thrown away, and once per
// verification. Each of those also opened its own authenticated session.
//
// The two assertions here are the two halves of the fix:
//
//   - exactly one GET of a manifest under this repo that is not the signature
//     tag — the cache-tag probe. A second one means the descriptor is being
//     re-fetched instead of threaded through.
//   - a small, constant number of /v2/ pings for the whole Check, rather than
//     one per phase.
func TestCacher_Check_DoesNotRefetchTheSameManifest(t *testing.T) {
	counter := &countingRegistry{inner: registry.New()}
	s := httptest.NewServer(counter)
	t.Cleanup(s.Close)

	host := strings.TrimPrefix(s.URL, "http://")
	repo := host + "/acme/requests"

	privPEM, pubPEM := generateTestKey(t)

	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("img.Digest: %v", err)
	}

	inputHash := strings.Repeat("c", 64)
	cacheRef, err := name.NewTag(repo+":"+remotecacheutils.CacheTag(inputHash), name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(cache tag): %v", err)
	}
	if err := remote.Write(cacheRef, img); err != nil {
		t.Fatalf("push cache entry: %v", err)
	}
	pushTestCosignSignature(t, repo, digest, privPEM, false)

	// Everything up to here is fixture setup; only what Check itself does is
	// measured.
	_, mark := counter.since(0)
	pingsBefore := counter.pingCount()

	c := remotecacheutils.New(remotecacheutils.WithCosignSigner(cosign.NewSigner(nil)))
	res, err := c.Check(context.Background(), ports.RemoteCacheRequest{
		Repo:      repo,
		InputHash: inputHash,
		Tags:      []string{"v1.0.0", "latest", "stable"},
		Verify: ports.RemoteCacheVerifyOptions{
			VerifySignature: true,
			VerifyMode:      ports.CacheVerifyStaticKey,
			PublicKeyPEM:    pubPEM,
		},
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !res.Hit || !res.Verified {
		t.Fatalf("expected a verified hit, got Hit=%v Verified=%v", res.Hit, res.Verified)
	}

	reqs, _ := counter.since(mark)
	pings := counter.pingCount() - pingsBefore

	// The signature tag is a different manifest and is legitimately fetched;
	// everything else under /manifests/ is the cached image's own manifest.
	sigFragment := "-" + digest.Hex + ".sig"
	imageManifestGETs := 0
	for _, r := range reqs {
		if strings.HasPrefix(r, "GET ") && strings.Contains(r, "/manifests/") && !strings.Contains(r, sigFragment) {
			imageManifestGETs++
		}
	}
	t.Logf("Check issued %d requests, %d /v2/ pings, %d GETs of the cached image manifest; requests: %v",
		len(reqs), pings, imageManifestGETs, reqs)

	if imageManifestGETs != 1 {
		t.Fatalf("Check fetched the cached image manifest %d times; want exactly 1 "+
			"(the cache-tag probe, whose descriptor must be threaded into reconciliation rather than re-fetched by digest)",
			imageManifestGETs)
	}

	// One puller session and one pusher session. Previously each of the probe,
	// the verification fetch and the reconcile opened its own.
	if pings > 2 {
		t.Fatalf("Check issued %d /v2/ pings; want at most 2 (one puller, one pusher) — the phases are not sharing one session", pings)
	}

	// The reconciliation must still have happened, and must point every
	// requested tag at the digest that was actually verified.
	for _, tag := range []string{"v1.0.0", "latest", "stable"} {
		ref, err := name.NewTag(repo+":"+tag, name.WeakValidation)
		if err != nil {
			t.Fatal(err)
		}
		desc, err := remote.Get(ref)
		if err != nil {
			t.Fatalf("release tag %s was not created: %v", tag, err)
		}
		if desc.Digest != digest {
			t.Fatalf("release tag %s points at %s, want the verified digest %s", tag, desc.Digest, digest)
		}
	}
}
