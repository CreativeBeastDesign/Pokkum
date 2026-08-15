package registry

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
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

func TestRegistry_CustomRegistryConfig(t *testing.T) {
	t.Run("invalid config path returns ErrRegistryAuth", func(t *testing.T) {
		a := NewAdapter(nil)
		img := randomImage(t)
		_, err := a.Push(t.Context(), ports.PushRequest{
			Repo:               "localhost:5000/test/app",
			Payload:            ports.Payload{Image: img},
			RegistryConfigPath: "/nonexistent/path/config.json",
		})
		if err == nil || !errors.Is(err, core.ErrRegistryAuth) {
			t.Fatalf("expected ErrRegistryAuth for non-existent config path, got %v", err)
		}
	})

	t.Run("valid custom config.json resolves successfully", func(t *testing.T) {
		s, _ := newTestRegistry(t)
		host := strings.TrimPrefix(s.URL, "http://")
		a := NewAdapter(nil)
		img := randomImage(t)

		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.json")
		configContent := `{"auths": {"` + host + `": {"auth": "dXNlcjpwYXNz"}}} `
		if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
			t.Fatalf("failed to write custom config.json: %v", err)
		}

		res, err := a.Push(t.Context(), ports.PushRequest{
			Repo:               host + "/test/custom-auth",
			Payload:            ports.Payload{Image: img},
			Insecure:           true,
			RegistryConfigPath: configPath,
		})
		if err != nil {
			t.Fatalf("Push with custom registry config failed: %v", err)
		}
		if res.Ref == "" {
			t.Fatal("expected non-empty ref from Push")
		}
	})
}

// TestPush_DockerHubMountSuppression_IsRealAndAppliesToOurPushPath documents
// and pins a load-bearing quirk of go-containerregistry that the planned
// cross-repo-blob-mount optimization for Push (Roadmap.md "Registry Push
// Optimizations") must not be surprised by: when a.Push's target repo is
// Docker Hub, go-containerregistry deliberately never attempts a cross-repo
// blob mount for an inherited *remote.MountableLayer whose source registry
// isn't also Docker Hub — even though such a layer is otherwise perfectly
// mountable (see resolver_test.go's
// TestResolve_MirrorRef_LayersAreMountableFromMirrorRepo for proof that
// Resolve does hand back genuinely mountable layers with an intact source
// Reference).
//
// Source: github.com/google/go-containerregistry/pkg/v1/remote/write.go,
// writer.uploadOne (v0.21.9, lines 409-422):
//
//	if ml, ok := l.(*MountableLayer); ok {
//	    ...
//	    from = ml.Reference.Context().RepositoryStr()
//	    origin = ml.Reference.Context().RegistryStr()
//
//	    // This keeps breaking with DockerHub.
//	    // https://github.com/google/go-containerregistry/issues/1741
//	    if w.repo.RegistryStr() == name.DefaultRegistry && origin != w.repo.RegistryStr() {
//	        from = ""
//	        origin = ""
//	    }
//	}
//
// initiateUpload (same file, ~line 240) only sends the `?mount=<digest>
// &from=<repo>` query parameters when `from != ""`; clearing from/origin
// here means uploadOne falls straight through to a normal, full blob
// upload — not a mount attempt that is expected to fail. This is
// intentional upstream behavior working around a real DockerHub API bug
// (see the linked issue), not something Pokkum needs to work around itself.
//
// Consequence for Pokkum once cross-repo mounting lands in a.Push: any build
// whose base image comes from a registry other than Docker Hub — true for
// both stock presets (gcr.io/distroless, cgr.dev/chainguard) and for any
// --mirror-registry escrow target that isn't itself on Docker Hub — and is
// pushed to a Docker Hub repo will show zero mounted layers for those
// inherited layers. That is expected, not a regression, and telemetry or
// alerting built on a "layers mounted" metric must not treat it as one.
//
// write.go's writer type and uploadOne method are unexported, so this
// package cannot invoke the real code path as a unit without a live (or
// heavily faked) Docker Hub endpoint. Instead, this test reconstructs the
// exact boolean condition above using the same exported building blocks
// a.Push itself uses to build its target repository (name.NewRepository via
// this package's nameOptions, RegistryStr, name.DefaultRegistry) and proves
// it evaluates to "suppressed" for the realistic Pokkum shapes described
// above, and to "not suppressed" for the cases that must keep mounting
// (mirror and push target on the same host; push target not Docker Hub at
// all). If go-containerregistry ever changes this condition, this test's
// documented source-line reference goes stale in a way a reviewer bumping
// the dependency can actually notice and re-verify.
func TestPush_DockerHubMountSuppression_IsRealAndAppliesToOurPushPath(t *testing.T) {
	// wouldSuppressMount reproduces, condition-for-condition, the check
	// write.go's writer.uploadOne applies before deciding whether to send a
	// MountableLayer's Reference as the mount source of a blob-mount
	// request. pushRepo is the repository a.Push is pushing to (exactly what
	// push.go builds via name.NewRepository(req.Repo, nameOptions(...)...));
	// sourceRegistry is the registry a candidate layer's
	// MountableLayer.Reference names (e.g. "gcr.io" for a stock distroless
	// base, or the --mirror-registry host for an escrowed one).
	wouldSuppressMount := func(pushRepo name.Repository, sourceRegistry string) bool {
		return pushRepo.RegistryStr() == name.DefaultRegistry && sourceRegistry != pushRepo.RegistryStr()
	}

	cases := []struct {
		name           string
		pushRepo       string // as Pokkum's ports.PushRequest.Repo would look
		sourceRegistry string
		wantSuppressed bool
	}{
		{
			name:           "push to Docker Hub, base image from gcr.io distroless",
			pushRepo:       "docker.io/someorg/app",
			sourceRegistry: "gcr.io",
			wantSuppressed: true,
		},
		{
			name:           "push to Docker Hub, base escrow-mirrored to a non-Docker-Hub host",
			pushRepo:       "index.docker.io/someorg/app",
			sourceRegistry: "ghcr.io",
			wantSuppressed: true,
		},
		{
			name:           "push to Docker Hub, base ALSO on Docker Hub",
			pushRepo:       "docker.io/someorg/app",
			sourceRegistry: "index.docker.io",
			wantSuppressed: false,
		},
		{
			name:           "push to GHCR, base from gcr.io distroless — not Docker Hub, mount stays live",
			pushRepo:       "ghcr.io/someorg/app",
			sourceRegistry: "gcr.io",
			wantSuppressed: false,
		},
		{
			name:           "push to a self-hosted registry same-host as the escrow mirror",
			pushRepo:       "registry.internal.example.com/someorg/app",
			sourceRegistry: "registry.internal.example.com",
			wantSuppressed: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, err := name.NewRepository(tc.pushRepo, nameOptions(false)...)
			if err != nil {
				t.Fatalf("name.NewRepository(%q): %v", tc.pushRepo, err)
			}
			got := wouldSuppressMount(repo, tc.sourceRegistry)
			if got != tc.wantSuppressed {
				t.Errorf("wouldSuppressMount(%q, %q) = %v, want %v", tc.pushRepo, tc.sourceRegistry, got, tc.wantSuppressed)
			}
		})
	}

	// Docker Hub can be named two ways ("docker.io" and "index.docker.io"),
	// and go-containerregistry's name.NewRepository normalizes both to
	// name.DefaultRegistry ("index.docker.io") — which is exactly why both
	// docker.io-named cases above hit the suppression branch. Pin that
	// normalization explicitly, since the whole test's premise depends on
	// it holding.
	for _, host := range []string{"docker.io", "index.docker.io"} {
		repo, err := name.NewRepository(host+"/someorg/app", nameOptions(false)...)
		if err != nil {
			t.Fatalf("name.NewRepository(%q): %v", host, err)
		}
		if repo.RegistryStr() != name.DefaultRegistry {
			t.Errorf("NewRepository(%q).RegistryStr() = %q, want %q (name.DefaultRegistry)", host, repo.RegistryStr(), name.DefaultRegistry)
		}
	}
}

// --- defaultTransport / insecureTransport regression coverage --------------
//
// The three tests below guard the change that replaced a single
// insecureTransport &http.Transport{} literal with defaultTransport and
// insecureTransport, both built by cloning remote.DefaultTransport, and made
// remoteOptions set remote.WithTransport(...) unconditionally instead of only
// on the insecure path. Together they prove:
//
//  1. the clone actually carries over the tuning remote.DefaultTransport
//     exists to provide (TestTransports_PreserveRemoteDefaultTransportTuning);
//  2. the secure path's explicit transport doesn't silently break ordinary
//     pushes (TestPush_SingleImage_RoundTrip in push_test.go already proves
//     this — see that test's use of defaultTransport via the non-Insecure
//     path — and TestPush_InsecureSelfSignedTLS_RoundTrip below proves the
//     insecure path's TLS handling specifically, which nothing previously
//     exercised); and
//  3. remoteOptions really does wire its own transport into every request on
//     both paths, rather than one of them quietly falling through to
//     remote's own package-level default (TestRemoteOptions_AlwaysWiresPackageTransport).

// TestTransports_PreserveRemoteDefaultTransportTuning asserts that
// defaultTransport and insecureTransport really are clones of
// remote.DefaultTransport — carrying over proxy support, HTTP/2, and the
// enlarged per-host idle connection pool — rather than bare &http.Transport{}
// literals that would silently drop all of it back to net/http's defaults
// (no proxy honored, HTTP/1.1 only implied by an unset TLS config, 2 idle
// conns per host instead of 50).
func TestTransports_PreserveRemoteDefaultTransportTuning(t *testing.T) {
	dt, ok := defaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("defaultTransport is %T, want *http.Transport", defaultTransport)
	}
	it, ok := insecureTransport.(*http.Transport)
	if !ok {
		t.Fatalf("insecureTransport is %T, want *http.Transport", insecureTransport)
	}

	// remote.DefaultTransport's own configured value — read directly rather
	// than hardcoded, so this test fails loudly (not silently drifts) if
	// go-containerregistry ever changes its tuning and the clone picks that
	// change up automatically.
	want, ok := remote.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("remote.DefaultTransport is %T, want *http.Transport", remote.DefaultTransport)
	}

	for _, tc := range []struct {
		name string
		tr   *http.Transport
	}{
		{"defaultTransport", dt},
		{"insecureTransport", it},
	} {
		if tc.tr.ForceAttemptHTTP2 != want.ForceAttemptHTTP2 {
			t.Errorf("%s.ForceAttemptHTTP2 = %v, want %v (matching remote.DefaultTransport)", tc.name, tc.tr.ForceAttemptHTTP2, want.ForceAttemptHTTP2)
		}
		if tc.tr.MaxIdleConnsPerHost != want.MaxIdleConnsPerHost {
			t.Errorf("%s.MaxIdleConnsPerHost = %d, want %d (matching remote.DefaultTransport)", tc.name, tc.tr.MaxIdleConnsPerHost, want.MaxIdleConnsPerHost)
		}
		if tc.tr.Proxy == nil {
			t.Errorf("%s.Proxy = nil, want a non-nil proxy func (proxy support silently dropped)", tc.name)
		} else if reflect.ValueOf(tc.tr.Proxy).Pointer() != reflect.ValueOf(want.Proxy).Pointer() {
			// Go only allows comparing func values to nil, not to each other,
			// so this compares the underlying function pointers via reflect —
			// the standard idiom for "is this literally the same function"
			// when nil-only comparison isn't precise enough. want.Proxy is
			// remote.DefaultTransport's http.ProxyFromEnvironment.
			t.Errorf("%s.Proxy is not the same function as remote.DefaultTransport.Proxy", tc.name)
		}
		if tc.tr.DialContext == nil {
			t.Errorf("%s.DialContext = nil, want remote.DefaultTransport's 30s-timeout dialer", tc.name)
		}
	}

	// The clone must not share remote.DefaultTransport's own *http.Transport
	// instance — cloning exists specifically so the two paths' TLSClientConfig
	// overrides don't collide with each other or with upstream's shared value.
	if dt == want || it == want {
		t.Fatal("defaultTransport or insecureTransport point at the same *http.Transport instance as remote.DefaultTransport; Clone() did not happen")
	}
	if dt == it {
		t.Fatal("defaultTransport and insecureTransport point at the same *http.Transport instance")
	}

	// dt.TLSClientConfig cannot be asserted nil: net/http's Transport.Clone()
	// itself calls t.nextProtoOnce.Do(t.onceSetNextProtoDefaults) on the
	// *source* transport before copying it (see net/http/transport.go,
	// Transport.Clone), and with ForceAttemptHTTP2 set that lazily allocates
	// a TLSClientConfig (NextProtos: ["h2","http/1.1"]) on remote.DefaultTransport
	// itself the very first time anything clones it — including the package
	// init that builds defaultTransport in the first place. So
	// defaultTransport.TLSClientConfig is non-nil from the moment the
	// package loads, before any request is ever sent; "nil" is not a
	// reachable state to assert here. (This is also, incidentally, what
	// registry.go's own comment on Clone() means by "resets the internal
	// HTTP/2 transport that http.Transport caches on first use" — it's this
	// exact mechanism, triggered a layer up.) What actually matters —
	// verification is never skipped on the secure path — is asserted
	// directly instead.
	if dt.TLSClientConfig != nil && dt.TLSClientConfig.InsecureSkipVerify {
		t.Errorf("defaultTransport.TLSClientConfig.InsecureSkipVerify = true, want false (secure path must verify certificates)")
	}
	if it.TLSClientConfig == nil || !it.TLSClientConfig.InsecureSkipVerify {
		t.Errorf("insecureTransport.TLSClientConfig = %+v, want InsecureSkipVerify: true", it.TLSClientConfig)
	}
}

// TestPush_InsecureSelfSignedTLS_RoundTrip exercises the one push path no
// other test in this package touches: Insecure: true against a registry that
// still speaks TLS, just with a certificate that doesn't verify (the
// self-signed-TLS-over-https case documented on insecureTransport in
// registry.go). name.Insecure only ever adds "http" as a *fallback* scheme —
// go-containerregistry's Ping always tries "https" first (see
// remote/transport/ping.go) — so this request goes out over real TLS via
// insecureTransport, and only succeeds if InsecureSkipVerify actually made it
// onto that transport's TLSClientConfig. Before the transport carried
// InsecureSkipVerify at all (or if a future change dropped it), this exact
// request would fail with a certificate verification error instead of
// falling back cleanly, because httptest.NewTLSServer's listener doesn't
// speak plain HTTP for the fallback attempt to land on.
func TestPush_InsecureSelfSignedTLS_RoundTrip(t *testing.T) {
	cr := &countingRegistry{inner: registry.New()}
	s := httptest.NewTLSServer(cr)
	t.Cleanup(s.Close)

	host := strings.TrimPrefix(s.URL, "https://")
	repo := host + "/app/insecure-tls"

	img := randomImage(t)
	wantDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	a := NewAdapter(nil)
	res, err := a.Push(t.Context(), ports.PushRequest{
		Repo:     repo,
		Tags:     []string{"v1"},
		Payload:  ports.Payload{Image: img},
		Insecure: true,
	})
	if err != nil {
		t.Fatalf("Push against self-signed-TLS registry with Insecure=true: %v", err)
	}
	if res.Digest != wantDigest {
		t.Errorf("Digest = %s, want %s", res.Digest, wantDigest)
	}
	if res.Ref != repo+"@"+wantDigest.String() {
		t.Errorf("Ref = %q, want %q", res.Ref, repo+"@"+wantDigest.String())
	}
	if cr.len() == 0 {
		t.Fatal("no requests observed by the in-memory registry; push did not reach it at all")
	}

	// Pull the pushed content back over the same insecure/self-signed TLS
	// path to prove the round trip is genuine, not just a successful-looking
	// error path.
	ref, err := name.ParseReference(res.Ref, nameOptions(true)...)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", res.Ref, err)
	}
	pulled, err := remote.Image(ref, remote.WithTransport(insecureTransport))
	if err != nil {
		t.Fatalf("remote.Image: %v", err)
	}
	gotDigest, err := pulled.Digest()
	if err != nil {
		t.Fatalf("pulled.Digest: %v", err)
	}
	if gotDigest != wantDigest {
		t.Errorf("pulled digest = %s, want %s", gotDigest, wantDigest)
	}
}

// spyTransport wraps a real http.RoundTripper and counts how many requests
// pass through it, so a test can prove a *specific* transport instance — not
// just "some" transport — actually carried a request.
type spyTransport struct {
	http.RoundTripper
	calls atomic.Int32
}

func (s *spyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.calls.Add(1)
	return s.RoundTripper.RoundTrip(r)
}

// TestRemoteOptions_AlwaysWiresPackageTransport proves remoteOptions puts
// defaultTransport (insecure=false) or insecureTransport (insecure=true) on
// the wire for real, on both paths — closing the gap opened by making
// remote.WithTransport unconditional. Before that change, the secure path
// left the transport unset and remote fell through to its own
// remote.DefaultTransport; a bug that made remoteOptions build rt correctly
// but forget to append remote.WithTransport(rt) (or append the wrong
// variable) would still pass every push-succeeds test in this package,
// because remote's fallback default is functionally equivalent to an
// untouched defaultTransport. Swapping the package transport for a
// request-counting spy is the only way to observe which concrete transport a
// request actually used.
func TestRemoteOptions_AlwaysWiresPackageTransport(t *testing.T) {
	for _, tc := range []struct {
		name     string
		insecure bool
	}{
		{"secure path wires defaultTransport", false},
		{"insecure path wires insecureTransport", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestRegistry(t)
			repo := registryRepo(t, s, "app/spy")

			spy := &spyTransport{RoundTripper: &http.Transport{}}

			// Tests in this package run sequentially (none call t.Parallel),
			// so swapping the package-level var for the duration of this
			// subtest and restoring it in Cleanup is safe.
			if tc.insecure {
				orig := insecureTransport
				insecureTransport = spy
				t.Cleanup(func() { insecureTransport = orig })
			} else {
				orig := defaultTransport
				defaultTransport = spy
				t.Cleanup(func() { defaultTransport = orig })
			}

			opts, err := remoteOptions(t.Context(), remoteConfig{Insecure: tc.insecure})
			if err != nil {
				t.Fatalf("remoteOptions: %v", err)
			}

			ref, err := name.NewRepository(repo, nameOptions(tc.insecure)...)
			if err != nil {
				t.Fatalf("NewRepository(%q): %v", repo, err)
			}

			// Any real network call exercises whichever transport opts
			// carries; a HEAD against a tag that doesn't exist yet is the
			// cheapest one, and its 404 is expected, not a failure.
			if _, err := remote.Head(ref.Tag("missing"), opts...); err != nil && !isNotFound(err) {
				t.Fatalf("remote.Head: unexpected error: %v", err)
			}

			if got := spy.calls.Load(); got == 0 {
				t.Fatalf("spy transport recorded 0 requests: remoteOptions(insecure=%v) did not wire the package transport into the request", tc.insecure)
			}
		})
	}
}
