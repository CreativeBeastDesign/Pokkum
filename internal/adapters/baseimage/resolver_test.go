package baseimage

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
// credential configuration. Resolver always authenticates with
// authn.DefaultKeychain (per the port contract, so private/mirrored bases
// work in production); DefaultKeychain reads $DOCKER_CONFIG/config.json, and
// on a machine configured with a credential store ("credsStore": "desktop",
// "osxkeychain", etc.) that means shelling out to a credential-helper binary
// that can block on OS-level auth prompts or a Docker Desktop socket that
// isn't there in CI. Pointing DOCKER_CONFIG at an empty directory makes
// DefaultKeychain fall back to anonymous auth instead, which is exactly what
// the in-memory test registry expects — and keeps this suite hermetic.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "pokkum-baseimage-dockerconfig")
	if err != nil {
		fmt.Fprintln(os.Stderr, "baseimage: TestMain: MkdirTemp:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	os.Setenv("DOCKER_CONFIG", dir)
	os.Exit(m.Run())
}

// --- test fixtures -----------------------------------------------------

// countingRegistry wraps the in-memory registry handler and counts GET
// requests to manifest endpoints, so tests can assert the resolver's cache
// actually avoids a second network round-trip.
type countingRegistry struct {
	inner        http.Handler
	manifestGETs atomic.Int64
}

func (c *countingRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodGet && strings.Contains(req.URL.Path, "/manifests/") {
		c.manifestGETs.Add(1)
	}
	c.inner.ServeHTTP(w, req)
}

// newTestRegistry starts an in-memory OCI registry (pkg/registry) behind
// httptest, so every test in this file runs with no network access and no
// Docker daemon.
func newTestRegistry(t *testing.T) (*httptest.Server, *countingRegistry) {
	t.Helper()
	cr := &countingRegistry{inner: registry.New()}
	s := httptest.NewServer(cr)
	t.Cleanup(s.Close)
	return s, cr
}

// registryRef builds "host:port/repo:tag" against the test server, which
// go-containerregistry resolves as plain HTTP automatically because the host
// is loopback.
func registryRef(t *testing.T, s *httptest.Server, repoTag string) string {
	t.Helper()
	host := strings.TrimPrefix(s.URL, "http://")
	return host + "/" + repoTag
}

// imageWithPlatform returns a small random image whose config file declares
// the given OS/architecture, so index-child and single-image platform
// assertions have something real to check against.
func imageWithPlatform(t *testing.T, osName, arch string) v1.Image {
	t.Helper()
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
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

// pushIndex builds and pushes a multi-platform index with one child per
// requested platform pair, tags it, and returns the tag string.
func pushIndex(t *testing.T, s *httptest.Server, repoTag string, platforms []ports.Platform) string {
	t.Helper()
	ref := registryRef(t, s, repoTag)

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
	idx := mutate.AppendManifests(empty.Index, addenda...)

	tag, err := name.NewTag(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(%q): %v", ref, err)
	}
	if err := remote.WriteIndex(tag, idx); err != nil {
		t.Fatalf("WriteIndex: %v", err)
	}
	return ref
}

// pushImage pushes a single-platform (non-index) image and returns the tag
// string.
func pushImage(t *testing.T, s *httptest.Server, repoTag string, p ports.Platform) string {
	t.Helper()
	ref := registryRef(t, s, repoTag)
	img := imageWithPlatform(t, p.OS, p.Arch)

	tag, err := name.NewTag(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(%q): %v", ref, err)
	}
	if err := remote.Write(tag, img); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return ref
}

// --- tests ---------------------------------------------------------------

func TestResolve_IndexChildSelection(t *testing.T) {
	s, cr := newTestRegistry(t)
	ref := pushIndex(t, s, "app/base:v1", []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64})

	r := NewResolver(nil)
	got, err := r.Resolve(context.Background(), ports.BaseImageRequest{
		Preset:    ports.BaseImageCustom,
		Ref:       ref,
		Platforms: []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !got.IsIndex {
		t.Errorf("IsIndex = false, want true")
	}
	if got.Digest.String() == "" {
		t.Errorf("Digest is empty")
	}
	if !strings.Contains(got.PinnedRef, "@sha256:") {
		t.Errorf("PinnedRef = %q, want a digest-form reference", got.PinnedRef)
	}
	for _, p := range []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64} {
		img, ok := got.Images[p]
		if !ok {
			t.Fatalf("Images missing entry for %s", p)
		}
		cf, err := img.ConfigFile()
		if err != nil {
			t.Fatalf("ConfigFile for %s: %v", p, err)
		}
		if cf.OS != p.OS || cf.Architecture != p.Arch {
			t.Errorf("platform %s: got config os/arch %s/%s", p, cf.OS, cf.Architecture)
		}
	}
	if len(got.Images) != 2 {
		t.Errorf("Images has %d entries, want exactly 2", len(got.Images))
	}

	if n := cr.manifestGETs.Load(); n == 0 {
		t.Errorf("expected at least one manifest GET against the test registry")
	}
}

func TestResolve_IndexMissingPlatform(t *testing.T) {
	s, _ := newTestRegistry(t)
	// Index only has amd64.
	ref := pushIndex(t, s, "app/amd64only:v1", []ports.Platform{ports.LinuxAMD64})

	r := NewResolver(nil)
	_, err := r.Resolve(context.Background(), ports.BaseImageRequest{
		Preset:    ports.BaseImageCustom,
		Ref:       ref,
		Platforms: []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64},
	})
	if !errors.Is(err, core.ErrBaseImageIncompatible) {
		t.Fatalf("err = %v, want core.ErrBaseImageIncompatible", err)
	}
}

func TestResolve_SingleImageMatchingPlatform(t *testing.T) {
	s, _ := newTestRegistry(t)
	ref := pushImage(t, s, "app/single:v1", ports.LinuxAMD64)

	r := NewResolver(nil)
	got, err := r.Resolve(context.Background(), ports.BaseImageRequest{
		Preset:    ports.BaseImageCustom,
		Ref:       ref,
		Platforms: []ports.Platform{ports.LinuxAMD64},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.IsIndex {
		t.Errorf("IsIndex = true, want false for a single-platform image")
	}
	if _, ok := got.Images[ports.LinuxAMD64]; !ok {
		t.Fatalf("Images missing linux/amd64")
	}
}

func TestResolve_SingleImageArchMismatch(t *testing.T) {
	s, _ := newTestRegistry(t)
	// Image is amd64 only; ask for arm64.
	ref := pushImage(t, s, "app/single-amd64:v1", ports.LinuxAMD64)

	r := NewResolver(nil)
	_, err := r.Resolve(context.Background(), ports.BaseImageRequest{
		Preset:    ports.BaseImageCustom,
		Ref:       ref,
		Platforms: []ports.Platform{ports.LinuxARM64},
	})
	if !errors.Is(err, core.ErrBaseImageIncompatible) {
		t.Fatalf("err = %v, want core.ErrBaseImageIncompatible", err)
	}
	if err == nil || !strings.Contains(err.Error(), ref) {
		t.Errorf("error should name the offending ref, got: %v", err)
	}
}

func TestResolve_DigestPinning(t *testing.T) {
	s, _ := newTestRegistry(t)
	ref := pushIndex(t, s, "app/pin:v1", []ports.Platform{ports.LinuxAMD64})

	r := NewResolver(nil)
	got, err := r.Resolve(context.Background(), ports.BaseImageRequest{
		Preset:    ports.BaseImageCustom,
		Ref:       ref,
		Platforms: []ports.Platform{ports.LinuxAMD64},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	wantPrefix := registryRef(t, s, "app/pin") + "@sha256:"
	if !strings.HasPrefix(got.PinnedRef, wantPrefix) {
		t.Errorf("PinnedRef = %q, want prefix %q", got.PinnedRef, wantPrefix)
	}
	if got.Ref != ref {
		t.Errorf("Ref = %q, want %q (the tag as requested)", got.Ref, ref)
	}
}

func TestResolve_CachesRepeatedResolution(t *testing.T) {
	s, cr := newTestRegistry(t)
	ref := pushIndex(t, s, "app/cache:v1", []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64})

	r := NewResolver(nil)
	ctx := context.Background()
	req := ports.BaseImageRequest{
		Preset:    ports.BaseImageCustom,
		Ref:       ref,
		Platforms: []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64},
	}
	if _, err := r.Resolve(ctx, req); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	first := cr.manifestGETs.Load()
	if first == 0 {
		t.Fatalf("first Resolve made no manifest GETs; test fixture is broken")
	}

	// A second call for the exact same ref and platform set must not add any
	// further manifest GETs: the top-level pull is cached by (ref, insecure),
	// and each platform's child selection is cached by (ref, insecure,
	// platform), so nothing here has a reason to touch the network again.
	if _, err := r.Resolve(ctx, req); err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	second := cr.manifestGETs.Load()

	if second != first {
		t.Errorf("manifest GETs grew from %d to %d on an identical repeated Resolve; caching is not working", first, second)
	}
}

// TestResolve_CachePerPlatform proves the cache is keyed by (ref, platform)
// rather than by ref alone: resolving a platform seen before must not touch
// the network, but resolving a new platform on an already-seen ref must.
func TestResolve_CachePerPlatform(t *testing.T) {
	s, cr := newTestRegistry(t)
	ref := pushIndex(t, s, "app/perplatform:v1", []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64})

	r := NewResolver(nil)
	ctx := context.Background()

	if _, err := r.Resolve(ctx, ports.BaseImageRequest{
		Preset: ports.BaseImageCustom, Ref: ref, Platforms: []ports.Platform{ports.LinuxAMD64},
	}); err != nil {
		t.Fatalf("resolve amd64: %v", err)
	}
	afterAMD64 := cr.manifestGETs.Load()

	if _, err := r.Resolve(ctx, ports.BaseImageRequest{
		Preset: ports.BaseImageCustom, Ref: ref, Platforms: []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64},
	}); err != nil {
		t.Fatalf("resolve amd64+arm64: %v", err)
	}
	afterBoth := cr.manifestGETs.Load()

	if afterBoth <= afterAMD64 {
		t.Errorf("adding a never-before-requested platform (arm64) made no new manifest GET (%d -> %d); cache should be per-platform, not per-ref only", afterAMD64, afterBoth)
	}
}

func TestResolve_IncompatibleBase_DistrolessStatic(t *testing.T) {
	r := NewResolver(nil)
	_, err := r.Resolve(context.Background(), ports.BaseImageRequest{
		Preset:    ports.BaseImageCustom,
		Ref:       "gcr.io/distroless/static-debian12:nonroot",
		Platforms: []ports.Platform{ports.LinuxAMD64},
	})
	if !errors.Is(err, core.ErrBaseImageIncompatible) {
		t.Fatalf("err = %v, want core.ErrBaseImageIncompatible", err)
	}
	if !strings.Contains(err.Error(), "libc") {
		t.Errorf("error should explain the dynamic-linking reason, got: %v", err)
	}
}

func TestResolve_IncompatibleBase_Scratch(t *testing.T) {
	r := NewResolver(nil)
	for _, ref := range []string{"scratch", "docker.io/library/scratch:latest", "myregistry.example/team/scratch@sha256:" + strings.Repeat("a", 64)} {
		_, err := r.Resolve(context.Background(), ports.BaseImageRequest{
			Preset:    ports.BaseImageCustom,
			Ref:       ref,
			Platforms: []ports.Platform{ports.LinuxAMD64},
		})
		if !errors.Is(err, core.ErrBaseImageIncompatible) {
			t.Errorf("ref %q: err = %v, want core.ErrBaseImageIncompatible", ref, err)
		}
	}
}

func TestResolve_IncompatibleBase_DoesNotFlagCompatibleRefs(t *testing.T) {
	// These must NOT trip the static-base heuristic: a real network pull
	// would be attempted (and fail, since there's no listener), which is
	// exactly the point — proves the check doesn't over-match.
	for _, ref := range []string{
		"gcr.io/distroless/cc-debian12:nonroot",
		"cgr.dev/chainguard/glibc-dynamic:latest",
		"127.0.0.1:1/scratchpad/app:v1", // contains "scratch" as a substring of a longer segment, must not match
	} {
		if reason, bad := staticBaseReason(ref); bad {
			t.Errorf("ref %q flagged incompatible (%s), want not flagged", ref, reason)
		}
	}
}

func TestResolve_InvalidPreset(t *testing.T) {
	r := NewResolver(nil)
	_, err := r.Resolve(context.Background(), ports.BaseImageRequest{
		Preset:    ports.BaseImagePreset("bogus"),
		Ref:       "example.com/app:v1",
		Platforms: []ports.Platform{ports.LinuxAMD64},
	})
	if !errors.Is(err, core.ErrInvalidBaseImage) {
		t.Fatalf("err = %v, want core.ErrInvalidBaseImage", err)
	}
}

func TestResolve_UnparseableRef(t *testing.T) {
	r := NewResolver(nil)
	_, err := r.Resolve(context.Background(), ports.BaseImageRequest{
		Preset:    ports.BaseImageCustom,
		Ref:       "  not a valid ref :: at all  ",
		Platforms: []ports.Platform{ports.LinuxAMD64},
	})
	if !errors.Is(err, core.ErrInvalidBaseImage) {
		t.Fatalf("err = %v, want core.ErrInvalidBaseImage", err)
	}
}

func TestResolve_NoPlatforms(t *testing.T) {
	r := NewResolver(nil)
	_, err := r.Resolve(context.Background(), ports.BaseImageRequest{
		Preset: ports.BaseImageDistroless,
		Ref:    "example.com/app:v1",
	})
	if !errors.Is(err, core.ErrInvalidBaseImage) {
		t.Fatalf("err = %v, want core.ErrInvalidBaseImage", err)
	}
}

func TestEffectiveRef(t *testing.T) {
	// effectiveRef contains the empty-Ref fallback logic Resolve relies on.
	// It is exercised directly (no network) so the default-ref-per-preset
	// contract from ports.BaseImagePreset.DefaultRef is covered without
	// depending on gcr.io/cgr.dev being reachable.
	cases := []struct {
		name    string
		req     ports.BaseImageRequest
		wantRef string
		wantErr error
	}{
		{
			name:    "distroless default",
			req:     ports.BaseImageRequest{Preset: ports.BaseImageDistroless},
			wantRef: ports.DistrolessBaseRef,
		},
		{
			name:    "chainguard default",
			req:     ports.BaseImageRequest{Preset: ports.BaseImageChainguard},
			wantRef: ports.ChainguardBaseRef,
		},
		{
			name:    "explicit ref overrides preset default",
			req:     ports.BaseImageRequest{Preset: ports.BaseImageDistroless, Ref: "example.com/pinned@sha256:" + strings.Repeat("b", 64)},
			wantRef: "example.com/pinned@sha256:" + strings.Repeat("b", 64),
		},
		{
			name:    "custom preset with no ref is invalid",
			req:     ports.BaseImageRequest{Preset: ports.BaseImageCustom},
			wantErr: core.ErrInvalidBaseImage,
		},
		{
			name:    "unknown preset is invalid",
			req:     ports.BaseImageRequest{Preset: ports.BaseImagePreset("nonsense")},
			wantErr: core.ErrInvalidBaseImage,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := effectiveRef(tc.req)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantRef {
				t.Errorf("ref = %q, want %q", got, tc.wantRef)
			}
		})
	}
}
