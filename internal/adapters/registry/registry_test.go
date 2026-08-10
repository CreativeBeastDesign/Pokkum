package registry

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"

	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

// TestMain isolates every test in this package from the host's real Docker
// credential configuration, exactly as internal/adapters/baseimage does (see
// that package's resolver_test.go for the original diagnosis). Adapter.Push
// and Adapter.AttachSBOM always authenticate with authn.DefaultKeychain, per
// the ports.Registry contract, so private/mirrored registries work in
// production. DefaultKeychain reads $DOCKER_CONFIG/config.json, and on a
// machine configured with a credential store ("credsStore": "desktop",
// "osxkeychain", etc.) that means shelling out to a credential-helper binary
// that blocks indefinitely when Docker Desktop is not running — it was this
// exact hang, undiagnosed, that cost W6 a full debugging cycle. Pointing
// DOCKER_CONFIG at an empty directory makes DefaultKeychain fall back to
// anonymous auth instead, which is what the in-memory test registry expects,
// and keeps this suite hermetic and fast.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pokkum-registry-dockerconfig")
	if err != nil {
		fmt.Fprintln(os.Stderr, "registry: TestMain: MkdirTemp:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	os.Setenv("DOCKER_CONFIG", dir)
	os.Exit(m.Run())
}

// --- test fixtures ---------------------------------------------------------

// recordedRequest is one HTTP request observed by a countingRegistry.
type recordedRequest struct {
	Method string
	Path   string
}

// countingRegistry wraps the in-memory registry handler (pkg/registry) and
// records every request it sees, so tests can prove that a skipped push
// really did skip the network round trips — not just that it returned the
// right digest.
type countingRegistry struct {
	inner http.Handler

	mu   sync.Mutex
	reqs []recordedRequest
}

func (c *countingRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.reqs = append(c.reqs, recordedRequest{Method: r.Method, Path: r.URL.Path})
	c.mu.Unlock()
	c.inner.ServeHTTP(w, r)
}

// since returns every request recorded after index i (0 means "from the
// start"), so a test can snapshot len(reqs) before an operation and inspect
// only what that operation did.
func (c *countingRegistry) since(i int) []recordedRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]recordedRequest, len(c.reqs)-i)
	copy(out, c.reqs[i:])
	return out
}

func (c *countingRegistry) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.reqs)
}

// countMethod counts recorded requests (from the whole history) with the
// given HTTP method against a path containing substr.
func countMethod(reqs []recordedRequest, method, substr string) int {
	n := 0
	for _, r := range reqs {
		if r.Method == method && strings.Contains(r.Path, substr) {
			n++
		}
	}
	return n
}

// newTestRegistry starts an in-memory OCI registry (pkg/registry) behind
// httptest, so every test in this file runs with no real network access.
func newTestRegistry(t *testing.T) (*httptest.Server, *countingRegistry) {
	t.Helper()
	cr := &countingRegistry{inner: registry.New()}
	s := httptest.NewServer(cr)
	t.Cleanup(s.Close)
	return s, cr
}

// registryRepo builds "host:port/repo", which go-containerregistry resolves
// as plain HTTP automatically because the host is loopback.
func registryRepo(t *testing.T, s *httptest.Server, repo string) string {
	t.Helper()
	host := strings.TrimPrefix(s.URL, "http://")
	return host + "/" + repo
}

// randomImage returns a small random single-layer image, good enough for any
// test that only cares that a digest round-trips.
func randomImage(t *testing.T) v1.Image {
	t.Helper()
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	return img
}

// imageWithPlatform returns a small random image whose config file declares
// the given OS/architecture, so platform-selection assertions have something
// real to check against.
func imageWithPlatform(t *testing.T, osName, arch string) v1.Image {
	t.Helper()
	img := randomImage(t)
	cf, err := img.ConfigFile()
	if err != nil {
		t.Fatalf("ConfigFile: %v", err)
	}
	cf = cf.DeepCopy()
	cf.OS = osName
	cf.Architecture = arch
	cf.Variant = ""
	out, err := mutate.ConfigFile(img, cf)
	if err != nil {
		t.Fatalf("mutate.ConfigFile: %v", err)
	}
	return out
}

// indexWithPlatforms builds an in-memory multi-platform index with one
// random child per platform. It does not touch the network.
func indexWithPlatforms(t *testing.T, platforms []ports.Platform) v1.ImageIndex {
	t.Helper()
	var addenda []mutate.IndexAddendum
	for _, p := range platforms {
		img := imageWithPlatform(t, p.OS, p.Arch)
		addenda = append(addenda, mutate.IndexAddendum{
			Add: img,
			Descriptor: v1.Descriptor{
				Platform: &v1.Platform{OS: p.OS, Architecture: p.Arch},
			},
		})
	}
	return mutate.AppendManifests(empty.Index, addenda...)
}

// indexWithAttestationPlaceholder builds a two-child index: one real platform
// image plus a buildkit-style "unknown/unknown" attestation manifest, so
// tests can confirm the placeholder is skipped rather than treated as a
// selectable (or reportable-as-skipped) platform.
func indexWithAttestationPlaceholder(t *testing.T, p ports.Platform) v1.ImageIndex {
	t.Helper()
	img := imageWithPlatform(t, p.OS, p.Arch)
	attestation := randomImage(t)
	idx := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{
			Add:        img,
			Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: p.OS, Architecture: p.Arch}},
		},
		mutate.IndexAddendum{
			Add:        attestation,
			Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "unknown", Architecture: "unknown"}},
		},
	)
	return idx
}
