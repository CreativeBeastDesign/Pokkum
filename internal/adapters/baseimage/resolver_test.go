package baseimage

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"

	"github.com/CreativeBeastDesign/pokkum/internal/adapters/cosign"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/lockfileutils"
	"github.com/CreativeBeastDesign/pokkum/internal/adapters/sigstore"
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

func TestResolve_LockfileResolutionAndSave(t *testing.T) {
	s, _ := newTestRegistry(t)
	ref := pushIndex(t, s, "app/locktest:v1", []ports.Platform{ports.LinuxAMD64})

	tmpDir := t.TempDir()
	lockPath := strings.Replace(ref, "/", "_", -1) + ".lock"
	lockPath = strings.Replace(lockPath, ":", "_", -1)
	lockPath = strings.Replace(lockPath, ".", "_", -1)
	lockPath = filepath.Join(tmpDir, lockPath)

	r := NewResolver(nil)
	ctx := context.Background()
	req := ports.BaseImageRequest{
		Preset:       ports.BaseImageCustom,
		Ref:          ref,
		Platforms:    []ports.Platform{ports.LinuxAMD64},
		LockfilePath: lockPath,
	}

	res, err := r.Resolve(ctx, req)
	if err != nil {
		t.Fatalf("first Resolve with lockfile: %v", err)
	}

	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Fatalf("expected lockfile to be saved at %s", lockPath)
	}

	// Resolve again using lockfile
	res2, err := r.Resolve(ctx, req)
	if err != nil {
		t.Fatalf("second Resolve using lockfile: %v", err)
	}

	if res2.Digest != res.Digest {
		t.Errorf("expected digest %s, got %s", res.Digest, res2.Digest)
	}
}

// TestResolve_CustomBaseLockKeyCollision guards a lockfile-keying gap found
// while adding --base's custom-reference support: lockKey for
// BaseImageCustom is always the literal string "custom" (resolver.go's
// lockKey = string(req.Preset)), regardless of what the actual custom Ref
// is — unlike every other preset, where lockKey already uniquely identifies
// one specific image. Two different custom bases sharing one project's
// pokkum.lock therefore share that single "custom" slot.
//
// Before the fix, this was worse than a mere cache-slot eviction: resolving
// custom base B after custom base A had already been locked would find A's
// entry under the "custom" key (lockKey lookup does not consider what Ref
// was actually requested), trust its entry.Ref and entry.PinnedRef, and
// silently resolve and return A's image — a different image than the one B
// actually requested — even though B's own Ref was passed through
// correctly. This test resolves A, then resolves B against the same
// lockfile and asserts the result is actually B's digest, not A's.
func TestResolve_CustomBaseLockKeyCollision(t *testing.T) {
	s, _ := newTestRegistry(t)
	refA := pushImage(t, s, "app/custom-a:v1", ports.LinuxAMD64)
	refB := pushImage(t, s, "app/custom-b:v1", ports.LinuxAMD64)

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "pokkum.lock")

	r := NewResolver(nil)
	ctx := context.Background()

	reqA := ports.BaseImageRequest{
		Preset:       ports.BaseImageCustom,
		Ref:          refA,
		Platforms:    []ports.Platform{ports.LinuxAMD64},
		Insecure:     true,
		LockfilePath: lockPath,
	}
	resA, err := r.Resolve(ctx, reqA)
	if err != nil {
		t.Fatalf("Resolve custom base A: %v", err)
	}

	reqB := ports.BaseImageRequest{
		Preset:       ports.BaseImageCustom,
		Ref:          refB,
		Platforms:    []ports.Platform{ports.LinuxAMD64},
		Insecure:     true,
		LockfilePath: lockPath,
	}
	resB, err := r.Resolve(ctx, reqB)
	if err != nil {
		t.Fatalf("Resolve custom base B (after A was already locked under the shared \"custom\" key): %v", err)
	}

	if resA.Digest == resB.Digest {
		t.Fatalf("custom base A and B resolved to the same digest %s; the test fixtures are supposed to differ", resA.Digest)
	}
	if resB.Ref != refB {
		t.Errorf("resolved base B's Ref = %q, want %q (the requested ref, not A's locked pinned ref)", resB.Ref, refB)
	}
	if resB.UpstreamRef != refB {
		t.Errorf("resolved base B's UpstreamRef = %q, want %q; got a value that suggests A's stale lock entry (keyed only by preset \"custom\") was trusted for a different reference", resB.UpstreamRef, refB)
	}

	// Each custom reference now owns its own lockfile slot, so B's resolve no
	// longer evicts A's pin. That the slots are independently *usable* is
	// covered by TestResolve_TwoCustomRefs_EachKeepsItsOwnLockSlot; what this
	// test still exists to catch is B silently coming back as A's content.
	lf, err := lockfileutils.LoadLockfile(lockPath)
	if err != nil {
		t.Fatalf("LoadLockfile: %v", err)
	}
	entryB, ok := lf.Bases[lockKeyFor(ports.BaseImageCustom, refB)]
	if !ok {
		t.Fatalf("expected a locked entry for B after resolving it; keys: %v", lockfileKeys(lf))
	}
	if entryB.Ref != refB {
		t.Errorf("B's locked entry.Ref = %q, want %q", entryB.Ref, refB)
	}
	entryA, ok := lf.Bases[lockKeyFor(ports.BaseImageCustom, refA)]
	if !ok {
		t.Fatalf("A's pin was evicted by B's resolve; keys: %v", lockfileKeys(lf))
	}
	if entryA.Ref != refA {
		t.Errorf("A's locked entry.Ref = %q, want %q", entryA.Ref, refA)
	}
}

func TestResolve_OfflineMode(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "empty.lock")

	r := NewResolver(nil)
	ctx := context.Background()
	req := ports.BaseImageRequest{
		Preset:       ports.BaseImageDistroless,
		Platforms:    []ports.Platform{ports.LinuxAMD64},
		LockfilePath: lockPath,
		Offline:      true,
	}

	_, err := r.Resolve(ctx, req)
	if !errors.Is(err, core.ErrInvalidBaseImage) {
		t.Fatalf("offline mode without lock entry: expected core.ErrInvalidBaseImage, got %v", err)
	}
}

func TestResolve_BaseImageCosignSignatureVerification(t *testing.T) {
	s, _ := newTestRegistry(t)
	ref := pushImage(t, s, "app/unsigned:v1", ports.LinuxAMD64)

	r := NewResolver(nil,
		WithCosignSigner(cosign.NewSigner(nil)),
		WithKeylessVerifier(sigstore.NewVerifier(nil)),
	)
	ctx := context.Background()

	// 1. Unsigned image ref in test registry should fail when VerifySignature is true
	reqUnsigned := ports.BaseImageRequest{
		Preset:          ports.BaseImageCustom,
		Ref:             ref,
		Platforms:       []ports.Platform{ports.LinuxAMD64},
		Insecure:        true,
		VerifySignature: true,
	}

	_, err := r.Resolve(ctx, reqUnsigned)
	if !errors.Is(err, core.ErrBaseSignatureInvalid) {
		t.Fatalf("expected core.ErrBaseSignatureInvalid for unsigned image in registry, got: %v", err)
	}

	// 2. Unsigned image when VerifySignature is false should succeed
	reqNoVerify := ports.BaseImageRequest{
		Preset:          ports.BaseImageCustom,
		Ref:             ref,
		Platforms:       []ports.Platform{ports.LinuxAMD64},
		Insecure:        true,
		VerifySignature: false,
	}
	res, err := r.Resolve(ctx, reqNoVerify)
	if err != nil {
		t.Fatalf("expected successful resolution when VerifySignature is false, got: %v", err)
	}
	if res.Digest.String() == "" {
		t.Error("expected non-empty digest for resolved image")
	}
}

// genECKeyPairPEM generates an ephemeral P-256 key pair for signature tests,
// so verification correctness is proven independently of whatever the
// package's embedded DefaultBaseImagePublicKeyPEM happens to be.
func genECKeyPairPEM(t *testing.T) (privPEM, pubPEM []byte) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}
	privPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	pubPEM = pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	return privPEM, pubPEM
}

// pushCosignSignature signs digest with privPEM using a real cosign.Signer
// and pushes the resulting Simple Signing payload as an OCI image tagged
// "<repo>:<alg>-<hex>.sig", exactly as verifyBaseImageSignature expects to
// find it — a real signature artifact, not a stub.
func pushCosignSignature(t *testing.T, s *httptest.Server, repo string, digest v1.Hash, privPEM []byte, corrupt bool) {
	t.Helper()

	signer := cosign.NewSigner(nil)
	bundle, err := signer.Sign(context.Background(), ports.CosignSignRequest{
		Repo:   repo,
		Digest: digest,
		KeyPEM: privPEM,
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	b64Sig := bundle.Base64Signature
	if corrupt {
		// Flip the signature's first byte after decoding so the bundle is
		// still valid base64 (and gets past the decode step) but fails
		// cryptographic verification.
		raw, err := base64.StdEncoding.DecodeString(b64Sig)
		if err != nil {
			t.Fatalf("decode signature for corruption: %v", err)
		}
		raw[0] ^= 0xFF
		b64Sig = base64.StdEncoding.EncodeToString(raw)
	}

	layer := static.NewLayer(bundle.PayloadBytes, "application/vnd.dev.cosign.simplesigning.v1+json")
	sigImg, err := mutate.Append(empty.Image, mutate.Addendum{
		Layer:       layer,
		Annotations: map[string]string{"dev.cosignproject.cosign/signature": b64Sig},
	})
	if err != nil {
		t.Fatalf("mutate.Append: %v", err)
	}

	sigRef := registryRef(t, s, repo[strings.Index(repo, "/")+1:]+":"+digest.Algorithm+"-"+digest.Hex+".sig")
	tag, err := name.NewTag(sigRef, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(%q): %v", sigRef, err)
	}
	if err := remote.Write(tag, sigImg); err != nil {
		t.Fatalf("Write signature: %v", err)
	}
}

func TestResolve_BaseImageCosignSignatureVerification_RealSignature(t *testing.T) {
	s, _ := newTestRegistry(t)
	ref := pushImage(t, s, "app/signed:v1", ports.LinuxAMD64)
	privPEM, pubPEM := genECKeyPairPEM(t)

	r := NewResolver(nil,
		WithCosignSigner(cosign.NewSigner(nil)),
		WithKeylessVerifier(sigstore.NewVerifier(nil)),
	)
	ctx := context.Background()

	// Resolve once without verification to learn the pushed digest and repo
	// string exactly as the resolver computes them.
	pre, err := r.Resolve(ctx, ports.BaseImageRequest{
		Preset:    ports.BaseImageCustom,
		Ref:       ref,
		Platforms: []ports.Platform{ports.LinuxAMD64},
		Insecure:  true,
	})
	if err != nil {
		t.Fatalf("pre-resolve: %v", err)
	}
	parsedRef, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	repo := parsedRef.Context().Name()

	t.Run("genuinely signed image passes verification", func(t *testing.T) {
		pushCosignSignature(t, s, repo, pre.Digest, privPEM, false)
		t.Setenv("POKKUM_BASE_IMAGE_PUBKEY", string(pubPEM))

		res, err := r.Resolve(ctx, ports.BaseImageRequest{
			Preset:          ports.BaseImageCustom,
			Ref:             ref,
			Platforms:       []ports.Platform{ports.LinuxAMD64},
			Insecure:        true,
			VerifySignature: true,
		})
		if err != nil {
			t.Fatalf("expected genuinely-signed image to verify, got: %v", err)
		}
		if res.Digest != pre.Digest {
			t.Errorf("digest mismatch: got %s, want %s", res.Digest, pre.Digest)
		}
	})

	t.Run("tampered signature fails verification", func(t *testing.T) {
		ref2 := pushImage(t, s, "app/tampered:v1", ports.LinuxAMD64)
		pre2, err := r.Resolve(ctx, ports.BaseImageRequest{
			Preset: ports.BaseImageCustom, Ref: ref2,
			Platforms: []ports.Platform{ports.LinuxAMD64}, Insecure: true,
		})
		if err != nil {
			t.Fatalf("pre-resolve: %v", err)
		}
		pushCosignSignature(t, s, "app/tampered", pre2.Digest, privPEM, true)
		t.Setenv("POKKUM_BASE_IMAGE_PUBKEY", string(pubPEM))

		_, err = r.Resolve(ctx, ports.BaseImageRequest{
			Preset:          ports.BaseImageCustom,
			Ref:             ref2,
			Platforms:       []ports.Platform{ports.LinuxAMD64},
			Insecure:        true,
			VerifySignature: true,
		})
		if !errors.Is(err, core.ErrBaseSignatureInvalid) {
			t.Fatalf("expected core.ErrBaseSignatureInvalid for tampered signature, got: %v", err)
		}
	})

	t.Run("wrong public key fails verification", func(t *testing.T) {
		ref3 := pushImage(t, s, "app/wrongkey:v1", ports.LinuxAMD64)
		pre3, err := r.Resolve(ctx, ports.BaseImageRequest{
			Preset: ports.BaseImageCustom, Ref: ref3,
			Platforms: []ports.Platform{ports.LinuxAMD64}, Insecure: true,
		})
		if err != nil {
			t.Fatalf("pre-resolve: %v", err)
		}
		pushCosignSignature(t, s, "app/wrongkey", pre3.Digest, privPEM, false)
		_, otherPub := genECKeyPairPEM(t)
		t.Setenv("POKKUM_BASE_IMAGE_PUBKEY", string(otherPub))

		_, err = r.Resolve(ctx, ports.BaseImageRequest{
			Preset:          ports.BaseImageCustom,
			Ref:             ref3,
			Platforms:       []ports.Platform{ports.LinuxAMD64},
			Insecure:        true,
			VerifySignature: true,
		})
		if !errors.Is(err, core.ErrBaseSignatureInvalid) {
			t.Fatalf("expected core.ErrBaseSignatureInvalid for wrong public key, got: %v", err)
		}
	})
}

// TestResolve_BaseImageStaticKey_NoKeyConfigured_FailsClosed proves that a
// genuinely, validly signed static-key base image is refused — not silently
// accepted, and not silently reported as unverified — when
// POKKUM_BASE_IMAGE_PUBKEY is unset. This is the regression guard for the
// deleted DefaultBaseImagePublicKeyPEM fallback (Roadmap.md item 2h): a
// shared, unattributed placeholder public key used to stand in here, but
// nothing ever signed with its (nonexistent) private half. The setup is
// otherwise identical to the "genuinely signed image passes verification"
// case in TestResolve_BaseImageCosignSignatureVerification_RealSignature —
// a real key pair, a real signature pushed to a real in-process registry —
// so the only variable under test is whether a key was configured.
func TestResolve_BaseImageStaticKey_NoKeyConfigured_FailsClosed(t *testing.T) {
	s, _ := newTestRegistry(t)
	ref := pushImage(t, s, "app/signed-nokey:v1", ports.LinuxAMD64)
	privPEM, _ := genECKeyPairPEM(t)

	r := NewResolver(nil,
		WithCosignSigner(cosign.NewSigner(nil)),
		WithKeylessVerifier(sigstore.NewVerifier(nil)),
	)
	ctx := context.Background()

	pre, err := r.Resolve(ctx, ports.BaseImageRequest{
		Preset:    ports.BaseImageCustom,
		Ref:       ref,
		Platforms: []ports.Platform{ports.LinuxAMD64},
		Insecure:  true,
	})
	if err != nil {
		t.Fatalf("pre-resolve: %v", err)
	}
	parsedRef, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	repo := parsedRef.Context().Name()

	pushCosignSignature(t, s, repo, pre.Digest, privPEM, false)

	// Deliberately NOT setting POKKUM_BASE_IMAGE_PUBKEY — the "no key
	// configured" case under test. Explicitly cleared (rather than merely
	// relying on it never having been set) so this test is isolated
	// regardless of run order relative to sibling tests in this file that do
	// set it.
	t.Setenv("POKKUM_BASE_IMAGE_PUBKEY", "")

	_, err = r.Resolve(ctx, ports.BaseImageRequest{
		Preset:          ports.BaseImageCustom,
		Ref:             ref,
		Platforms:       []ports.Platform{ports.LinuxAMD64},
		Insecure:        true,
		VerifySignature: true,
	})
	if err == nil {
		t.Fatal("expected Resolve to refuse a genuinely-signed base image when POKKUM_BASE_IMAGE_PUBKEY is not configured")
	}
	if !errors.Is(err, core.ErrBaseSignatureInvalid) {
		t.Fatalf("err = %v, want core.ErrBaseSignatureInvalid", err)
	}
	if !strings.Contains(err.Error(), "no key is configured") {
		t.Errorf("error should distinguish 'no key configured' from a signature that was checked and found invalid, got: %v", err)
	}
	if !strings.Contains(err.Error(), "POKKUM_BASE_IMAGE_PUBKEY") {
		t.Errorf("error should name the env var to set, got: %v", err)
	}
}

// --- VerifyBaseImage tests -------------------------------------------------
//
// These exercise the split verification call path introduced so signature
// verification can run as its own pipeline stage (internal/core/pipeline.go),
// concurrently with Compiler.Prepare, instead of inline inside Resolve.
// Resolve itself is called with VerifySignature:false in every test below,
// exactly as internal/core/pipeline.go now calls it.

// TestVerifyBaseImage_UsesResolvedRef_NoSecondNetworkCall is the regression
// guard for a real bug caught during review of this feature: an earlier draft
// of VerifyBaseImage re-derived ref/upstreamRef by re-reading the lockfile,
// which — because Resolve already writes a new lockfile entry before
// returning — silently picked a different (but digest-equivalent) ref string
// on the very first resolve for a preset, missing the r.pull memoization
// cache and triggering a second, redundant pull of the base manifest.
//
// The fixed VerifyBaseImage takes resolved.Ref/resolved.UpstreamRef straight
// through, so its own signature-verification call must add exactly one new
// manifest GET (fetching the Cosign signature tag, which is not something
// r.pull's cache covers and is expected on every call not yet proven by the
// verifications cache) — never a second GET for the base image manifest
// itself.
func TestVerifyBaseImage_UsesResolvedRef_NoSecondNetworkCall(t *testing.T) {
	s, cr := newTestRegistry(t)
	ref := pushImage(t, s, "app/verify-once:v1", ports.LinuxAMD64)
	privPEM, pubPEM := genECKeyPairPEM(t)

	r := NewResolver(nil,
		WithCosignSigner(cosign.NewSigner(nil)),
		WithKeylessVerifier(sigstore.NewVerifier(nil)),
	)
	ctx := context.Background()

	req := ports.BaseImageRequest{
		Preset:          ports.BaseImageCustom,
		Ref:             ref,
		Platforms:       []ports.Platform{ports.LinuxAMD64},
		Insecure:        true,
		VerifySignature: false,
	}
	resolved, err := r.Resolve(ctx, req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	afterResolve := cr.manifestGETs.Load()
	if afterResolve == 0 {
		t.Fatalf("Resolve made no manifest GETs; test fixture is broken")
	}

	// Push a genuine signature for the resolved digest so VerifyBaseImage's
	// own fetch (of the ".sig" tag, a different reference from the base
	// image's own tag/digest and therefore never covered by r.pull's cache)
	// succeeds cleanly, isolating the assertion to "did it also re-pull the
	// base manifest".
	parsedRef, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	pushCosignSignature(t, s, parsedRef.Context().Name(), resolved.Digest, privPEM, false)
	t.Setenv("POKKUM_BASE_IMAGE_PUBKEY", string(pubPEM))

	if err := r.VerifyBaseImage(ctx, resolved, req); err != nil {
		t.Fatalf("VerifyBaseImage: %v", err)
	}
	afterVerify := cr.manifestGETs.Load()

	if got, want := afterVerify-afterResolve, int64(1); got != want {
		t.Errorf("manifest GETs added by VerifyBaseImage = %d, want exactly %d "+
			"(the signature tag fetch only — a second GET here would mean the base "+
			"image manifest was re-pulled under a ref string that missed the pull cache)",
			got, want)
	}
}

// TestVerifyBaseImage_DigestMismatch_FailsClosed proves VerifyBaseImage fails
// closed rather than verifying a manifest other than the one Resolve actually
// returned, e.g. because a floating tag moved between the two calls. Since
// r.pull is memoized by ref, the cleanest way to force a genuine mismatch
// without fighting the cache is to hand VerifyBaseImage a BaseImage whose
// Digest field was tampered with after a real Resolve — VerifyBaseImage must
// compare it against what its own (cache-hit) pull actually returns and
// refuse to proceed.
func TestVerifyBaseImage_DigestMismatch_FailsClosed(t *testing.T) {
	s, _ := newTestRegistry(t)
	ref := pushImage(t, s, "app/verify-mismatch:v1", ports.LinuxAMD64)

	r := NewResolver(nil,
		WithCosignSigner(cosign.NewSigner(nil)),
		WithKeylessVerifier(sigstore.NewVerifier(nil)),
	)
	ctx := context.Background()
	req := ports.BaseImageRequest{
		Preset:    ports.BaseImageCustom,
		Ref:       ref,
		Platforms: []ports.Platform{ports.LinuxAMD64},
		Insecure:  true,
	}
	resolved, err := r.Resolve(ctx, req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	tampered := *resolved
	tampered.Digest = v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("f", 64)}

	err = r.VerifyBaseImage(ctx, &tampered, req)
	if err == nil {
		t.Fatal("expected VerifyBaseImage to fail closed on a digest mismatch")
	}
	if !errors.Is(err, core.ErrBaseSignatureInvalid) {
		t.Fatalf("err = %v, want core.ErrBaseSignatureInvalid", err)
	}
	if !strings.Contains(err.Error(), "digest changed between resolve") {
		t.Errorf("error should explain the digest-changed reason, got: %v", err)
	}
}

// TestVerifyBaseImage_KeylessAndStaticKey drives both signature-verification
// dispatch paths through the split Resolve(VerifySignature:false) +
// VerifyBaseImage call path, reusing the same fixtures as
// TestResolve_BaseImageCosignSignatureVerification_RealSignature (static key)
// and TestResolve_KeylessMode_ClaimsMismatchFailsClosed (keyless). The
// underlying verifyBaseImage/runVerification dispatch logic is unchanged by
// this feature — only how it gets called is new — so these mirror what those
// tests already assert about Resolve, just observed through VerifyBaseImage
// instead.
func TestVerifyBaseImage_KeylessAndStaticKey(t *testing.T) {
	t.Run("static key: genuinely signed image passes", func(t *testing.T) {
		s, _ := newTestRegistry(t)
		ref := pushImage(t, s, "app/verify-static-ok:v1", ports.LinuxAMD64)
		privPEM, pubPEM := genECKeyPairPEM(t)

		r := NewResolver(nil,
			WithCosignSigner(cosign.NewSigner(nil)),
			WithKeylessVerifier(sigstore.NewVerifier(nil)),
		)
		ctx := context.Background()
		req := ports.BaseImageRequest{
			Preset:          ports.BaseImageCustom,
			Ref:             ref,
			Platforms:       []ports.Platform{ports.LinuxAMD64},
			Insecure:        true,
			VerifySignature: false,
		}
		resolved, err := r.Resolve(ctx, req)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}

		parsedRef, err := name.ParseReference(ref, name.WeakValidation)
		if err != nil {
			t.Fatalf("ParseReference: %v", err)
		}
		pushCosignSignature(t, s, parsedRef.Context().Name(), resolved.Digest, privPEM, false)
		t.Setenv("POKKUM_BASE_IMAGE_PUBKEY", string(pubPEM))

		if err := r.VerifyBaseImage(ctx, resolved, req); err != nil {
			t.Fatalf("expected genuinely-signed image to verify via VerifyBaseImage, got: %v", err)
		}
	})

	t.Run("static key: tampered signature fails closed", func(t *testing.T) {
		s, _ := newTestRegistry(t)
		ref := pushImage(t, s, "app/verify-static-bad:v1", ports.LinuxAMD64)
		privPEM, pubPEM := genECKeyPairPEM(t)

		r := NewResolver(nil,
			WithCosignSigner(cosign.NewSigner(nil)),
			WithKeylessVerifier(sigstore.NewVerifier(nil)),
		)
		ctx := context.Background()
		req := ports.BaseImageRequest{
			Preset:          ports.BaseImageCustom,
			Ref:             ref,
			Platforms:       []ports.Platform{ports.LinuxAMD64},
			Insecure:        true,
			VerifySignature: false,
		}
		resolved, err := r.Resolve(ctx, req)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}

		parsedRef, err := name.ParseReference(ref, name.WeakValidation)
		if err != nil {
			t.Fatalf("ParseReference: %v", err)
		}
		pushCosignSignature(t, s, parsedRef.Context().Name(), resolved.Digest, privPEM, true)
		t.Setenv("POKKUM_BASE_IMAGE_PUBKEY", string(pubPEM))

		err = r.VerifyBaseImage(ctx, resolved, req)
		if !errors.Is(err, core.ErrBaseSignatureInvalid) {
			t.Fatalf("err = %v, want core.ErrBaseSignatureInvalid", err)
		}
	})

	t.Run("keyless: claims mismatch fails closed", func(t *testing.T) {
		s, _ := newTestRegistry(t)
		ref := pushImage(t, s, "app/verify-keyless-mismatch:v1", ports.LinuxAMD64)

		r := NewResolver(nil,
			WithCosignSigner(cosign.NewSigner(nil)),
			WithKeylessVerifier(sigstore.NewVerifier(nil)),
		)
		ctx := context.Background()
		req := ports.BaseImageRequest{
			Preset:          ports.BaseImageCustom,
			Ref:             ref,
			Platforms:       []ports.Platform{ports.LinuxAMD64},
			Insecure:        true,
			VerifySignature: false,
			VerifyMode:      ports.BaseImageVerifyKeyless,
			KeylessIdentity: ports.KeylessIdentity{
				Issuer: ports.DistrolessKeylessIssuer,
				SAN:    ports.DistrolessKeylessSAN,
			},
		}
		resolved, err := r.Resolve(ctx, req)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}

		parsedRef, err := name.ParseReference(ref, name.WeakValidation)
		if err != nil {
			t.Fatalf("ParseReference: %v", err)
		}
		pushKeylessSignatureFixture(t, s, parsedRef.Context().Name(), resolved.Digest, "../sigstore/testdata/distroless-nonroot")

		err = r.VerifyBaseImage(ctx, resolved, req)
		if err == nil {
			t.Fatal("expected VerifyBaseImage to fail: a genuine distroless signature was attached to an unrelated image")
		}
		if !errors.Is(err, core.ErrBaseSignatureInvalid) {
			t.Fatalf("err = %v, want core.ErrBaseSignatureInvalid", err)
		}
		if !strings.Contains(err.Error(), "is a signature for repository") {
			t.Fatalf("error does not indicate a claims (repository/digest) mismatch, got: %v", err)
		}
	})
}

func TestResolve_CustomRegistryConfig(t *testing.T) {
	t.Run("invalid registry config path returns ErrRegistryAuth", func(t *testing.T) {
		r := NewResolver(nil, nil)
		_, err := r.Resolve(t.Context(), ports.BaseImageRequest{
			Preset:             ports.BaseImageDistroless,
			Platforms:          []ports.Platform{ports.LinuxAMD64},
			RegistryConfigPath: "/nonexistent/path/config.json",
		})
		if err == nil || !errors.Is(err, core.ErrRegistryAuth) {
			t.Fatalf("expected ErrRegistryAuth for non-existent config path, got %v", err)
		}
	})

	t.Run("valid custom config.json resolves base image successfully", func(t *testing.T) {
		s, _ := newTestRegistry(t)
		ref := pushImage(t, s, "test/base-custom-auth:latest", ports.LinuxAMD64)

		r := NewResolver(nil, nil)
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config.json")
		host := strings.TrimPrefix(s.URL, "http://")
		configContent := `{"auths": {"` + host + `": {"auth": "dXNlcjpwYXNz"}}} `
		if err := os.WriteFile(configPath, []byte(configContent), 0600); err != nil {
			t.Fatalf("failed to write custom config.json: %v", err)
		}

		res, err := r.Resolve(t.Context(), ports.BaseImageRequest{
			Preset:             ports.BaseImageCustom,
			Ref:                ref,
			Platforms:          []ports.Platform{ports.LinuxAMD64},
			Insecure:           true,
			RegistryConfigPath: configPath,
		})
		if err != nil {
			t.Fatalf("Resolve with custom registry config failed: %v", err)
		}
		if res.Digest.String() == "" {
			t.Error("expected non-empty digest for resolved base image")
		}
	})
}

// --- M6b: resolver dispatch-logic tests, especially the anti-downgrade
// guarantee ---------------------------------------------------------------
//
// The tests below do not re-test cryptographic correctness (that is M3's and
// M6a's job, for internal/adapters/cosign and internal/adapters/sigstore
// respectively). They pin down verifyBaseImage's dispatch logic: which
// verification path runs for a given request, and that it never silently
// substitutes a weaker one.

// TestResolve_KeylessMode_RejectsStaticKeySignature is the anti-downgrade
// test: the single most important test in this milestone.
//
// An operator who explicitly asks for keyless verification (VerifyMode:
// BaseImageVerifyKeyless) must never have that request silently satisfied by
// a static-key signature, even when one is genuinely present and
// cryptographically valid, and even though the custom preset defaults to
// static-key verification. verifyKeylessSignature must refuse to run at all
// when no layer carries the keyless (certificate + bundle) annotations —
// there is deliberately no fallback to the weaker static-key path for a
// given Resolve call, because falling back would let whoever controls the
// signature tag choose which control actually runs.
func TestResolve_KeylessMode_RejectsStaticKeySignature(t *testing.T) {
	s, _ := newTestRegistry(t)
	ref := pushImage(t, s, "app/static-only:v1", ports.LinuxAMD64)
	privPEM, _ := genECKeyPairPEM(t)

	r := NewResolver(nil,
		WithCosignSigner(cosign.NewSigner(nil)),
		WithKeylessVerifier(sigstore.NewVerifier(nil)),
	)
	ctx := context.Background()

	pre, err := r.Resolve(ctx, ports.BaseImageRequest{
		Preset:    ports.BaseImageCustom,
		Ref:       ref,
		Platforms: []ports.Platform{ports.LinuxAMD64},
		Insecure:  true,
	})
	if err != nil {
		t.Fatalf("pre-resolve: %v", err)
	}
	parsedRef, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	repo := parsedRef.Context().Name()

	// A genuine, valid static-key signature — and nothing else — is present
	// on the signature tag.
	pushCosignSignature(t, s, repo, pre.Digest, privPEM, false)

	// Force keyless verification explicitly (the custom preset's own default
	// is static-key, so VerifyMode must be set to simulate "operator
	// explicitly asked for keyless"). The identity's value must not matter:
	// resolution has to fail before identity is ever checked, since there is
	// no keyless material to check it against.
	_, err = r.Resolve(ctx, ports.BaseImageRequest{
		Preset:          ports.BaseImageCustom,
		Ref:             ref,
		Platforms:       []ports.Platform{ports.LinuxAMD64},
		Insecure:        true,
		VerifySignature: true,
		VerifyMode:      ports.BaseImageVerifyKeyless,
		KeylessIdentity: ports.KeylessIdentity{Issuer: "irrelevant", SAN: "irrelevant"},
	})
	if err == nil {
		t.Fatal("expected keyless verification to fail against an image carrying only a static-key signature, got nil error")
	}
	if !errors.Is(err, core.ErrBaseSignatureInvalid) {
		t.Fatalf("err = %v, want core.ErrBaseSignatureInvalid", err)
	}
	// Confirm the failure is specifically "no keyless material found", not
	// some other unrelated failure — otherwise this test could pass for the
	// wrong reason without actually proving the anti-downgrade guarantee.
	if !strings.Contains(err.Error(), "carries no keyless signature material") {
		t.Fatalf("error does not indicate the expected no-keyless-material failure, got: %v", err)
	}
}

// TestResolve_KeylessMode_PubkeyConflictGuard proves that setting
// POKKUM_BASE_IMAGE_PUBKEY against a preset that verifies keyless by default
// fails loudly instead of being silently ignored or silently downgrading to
// static-key verification.
func TestResolve_KeylessMode_PubkeyConflictGuard(t *testing.T) {
	s, _ := newTestRegistry(t)
	// Any image works: the guard fires before any signature material is
	// fetched, so nothing needs to be pushed to the ".sig" tag.
	ref := pushImage(t, s, "app/pubkey-conflict:v1", ports.LinuxAMD64)

	t.Setenv("POKKUM_BASE_IMAGE_PUBKEY", "irrelevant-value")

	r := NewResolver(nil,
		WithCosignSigner(cosign.NewSigner(nil)),
		WithKeylessVerifier(sigstore.NewVerifier(nil)),
	)
	// ports.BaseImageDistroless has DefaultVerifyMode() == keyless.
	// BaseImageRequest.Ref overriding a preset's default ref while keeping
	// the preset's semantics is exactly what effectiveRef supports, so this
	// resolves the locally pushed image under the distroless preset's
	// keyless-by-default policy.
	_, err := r.Resolve(context.Background(), ports.BaseImageRequest{
		Preset:          ports.BaseImageDistroless,
		Ref:             ref,
		Platforms:       []ports.Platform{ports.LinuxAMD64},
		Insecure:        true,
		VerifySignature: true,
		VerifyMode:      ports.BaseImageVerifyAuto,
	})
	if err == nil {
		t.Fatal("expected resolve to fail when POKKUM_BASE_IMAGE_PUBKEY is set for a keyless-by-default preset")
	}
	if !errors.Is(err, core.ErrBaseSignatureInvalid) {
		t.Fatalf("err = %v, want core.ErrBaseSignatureInvalid", err)
	}
	if !strings.Contains(err.Error(), "POKKUM_BASE_IMAGE_PUBKEY") {
		t.Errorf("error should name the conflicting env var POKKUM_BASE_IMAGE_PUBKEY, got: %v", err)
	}
	if !strings.Contains(err.Error(), "--base-verify-mode=static-key") {
		t.Errorf("error should point at --base-verify-mode=static-key as the way to express the intent, got: %v", err)
	}
}

// pushKeylessSignatureFixture pushes a "<repo>:<alg>-<hex>.sig" tag manifest
// for repo@digest whose layer blob and annotations are copied verbatim from
// a real, captured keyless Sigstore signature fixture (see
// internal/adapters/sigstore/testdata/distroless-nonroot/README.md), rather
// than freshly generated material. This lets a test attach a genuine,
// cryptographically valid keyless signature to an image the signature was
// never actually issued for — exactly what
// TestResolve_KeylessMode_ClaimsMismatchFailsClosed needs in order to prove
// the payload-claims re-check is load-bearing and not just decoration.
func pushKeylessSignatureFixture(t *testing.T, s *httptest.Server, repo string, digest v1.Hash, fixtureDir string) {
	t.Helper()

	payloadBytes, err := os.ReadFile(filepath.Join(fixtureDir, "payload.json"))
	if err != nil {
		t.Fatalf("read payload.json: %v", err)
	}
	sigBytes, err := os.ReadFile(filepath.Join(fixtureDir, "signature.bin"))
	if err != nil {
		t.Fatalf("read signature.bin: %v", err)
	}
	certPEM, err := os.ReadFile(filepath.Join(fixtureDir, "certificate.pem"))
	if err != nil {
		t.Fatalf("read certificate.pem: %v", err)
	}
	chainPEM, err := os.ReadFile(filepath.Join(fixtureDir, "chain.pem"))
	if err != nil {
		t.Fatalf("read chain.pem: %v", err)
	}
	bundleJSON, err := os.ReadFile(filepath.Join(fixtureDir, "bundle.json"))
	if err != nil {
		t.Fatalf("read bundle.json: %v", err)
	}

	layer := static.NewLayer(payloadBytes, "application/vnd.dev.cosign.simplesigning.v1+json")
	sigImg, err := mutate.Append(empty.Image, mutate.Addendum{
		Layer: layer,
		Annotations: map[string]string{
			sigstore.CosignSignatureAnnotation:   base64.StdEncoding.EncodeToString(sigBytes),
			sigstore.CosignCertificateAnnotation: string(certPEM),
			sigstore.CosignChainAnnotation:       string(chainPEM),
			sigstore.CosignBundleAnnotation:      string(bundleJSON),
		},
	})
	if err != nil {
		t.Fatalf("mutate.Append: %v", err)
	}

	sigRef := registryRef(t, s, repo[strings.Index(repo, "/")+1:]+":"+digest.Algorithm+"-"+digest.Hex+".sig")
	tag, err := name.NewTag(sigRef, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(%q): %v", sigRef, err)
	}
	if err := remote.Write(tag, sigImg); err != nil {
		t.Fatalf("Write signature: %v", err)
	}
}

// TestResolve_KeylessMode_ClaimsMismatchFailsClosed proves that genuine
// Sigstore cryptographic success alone is not sufficient: the resolver must
// also confirm the signed payload actually names the image being resolved.
//
// It attaches a real, captured distroless keyless signature — verbatim
// payload, signature bytes, certificate and Rekor bundle — to a completely
// unrelated locally pushed image, then resolves with the real distroless
// identity (Issuer/SAN). Fulcio identity matching succeeds (the certificate
// really was issued to that identity) and full Sigstore cryptographic
// verification succeeds (chain, SCT, Rekor SET all check out against the
// embedded public-good trust root, fully offline). The resolve must still
// fail, because checkSimpleSigningClaims notices the payload's
// docker-reference names gcr.io/distroless/cc-debian12, not the local image.
func TestResolve_KeylessMode_ClaimsMismatchFailsClosed(t *testing.T) {
	s, _ := newTestRegistry(t)
	ref := pushImage(t, s, "app/claims-mismatch:v1", ports.LinuxAMD64)

	r := NewResolver(nil,
		WithCosignSigner(cosign.NewSigner(nil)),
		WithKeylessVerifier(sigstore.NewVerifier(nil)),
	)
	ctx := context.Background()

	pre, err := r.Resolve(ctx, ports.BaseImageRequest{
		Preset:    ports.BaseImageCustom,
		Ref:       ref,
		Platforms: []ports.Platform{ports.LinuxAMD64},
		Insecure:  true,
	})
	if err != nil {
		t.Fatalf("pre-resolve: %v", err)
	}
	parsedRef, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	repo := parsedRef.Context().Name()

	pushKeylessSignatureFixture(t, s, repo, pre.Digest, "../sigstore/testdata/distroless-nonroot")

	_, err = r.Resolve(ctx, ports.BaseImageRequest{
		Preset:          ports.BaseImageCustom,
		Ref:             ref,
		Platforms:       []ports.Platform{ports.LinuxAMD64},
		Insecure:        true,
		VerifySignature: true,
		VerifyMode:      ports.BaseImageVerifyKeyless,
		KeylessIdentity: ports.KeylessIdentity{
			Issuer: ports.DistrolessKeylessIssuer,
			SAN:    ports.DistrolessKeylessSAN,
		},
	})
	if err == nil {
		t.Fatal("expected resolve to fail: a genuine distroless signature was attached to an unrelated image")
	}
	if !errors.Is(err, core.ErrBaseSignatureInvalid) {
		t.Fatalf("err = %v, want core.ErrBaseSignatureInvalid", err)
	}
	if !strings.Contains(err.Error(), "is a signature for repository") {
		t.Fatalf("error does not indicate a claims (repository/digest) mismatch, got: %v", err)
	}
}

// TestResolve_KeylessMode_EmptyIdentityRefused proves that keyless
// verification refuses to run against an unconstrained identity rather than
// treating a zero-value KeylessIdentity as "match anything". The custom
// preset has no default keyless identity, so leaving
// BaseImageRequest.KeylessIdentity empty must fail before any signature
// material is even fetched.
func TestResolve_KeylessMode_EmptyIdentityRefused(t *testing.T) {
	s, _ := newTestRegistry(t)
	ref := pushImage(t, s, "app/empty-identity:v1", ports.LinuxAMD64)

	r := NewResolver(nil,
		WithCosignSigner(cosign.NewSigner(nil)),
		WithKeylessVerifier(sigstore.NewVerifier(nil)),
	)
	_, err := r.Resolve(context.Background(), ports.BaseImageRequest{
		Preset:          ports.BaseImageCustom,
		Ref:             ref,
		Platforms:       []ports.Platform{ports.LinuxAMD64},
		Insecure:        true,
		VerifySignature: true,
		VerifyMode:      ports.BaseImageVerifyKeyless,
		// KeylessIdentity deliberately left at its zero value.
	})
	if err == nil {
		t.Fatal("expected resolve to fail for an unconstrained (empty) keyless identity")
	}
	if !errors.Is(err, core.ErrBaseSignatureInvalid) {
		t.Fatalf("err = %v, want core.ErrBaseSignatureInvalid", err)
	}
	if !strings.Contains(err.Error(), "unconstrained identity") {
		t.Errorf("error should mention refusing an unconstrained identity, got: %v", err)
	}
}

// --- Escrow Mirroring Tests -----------------------------------------------

func TestResolve_EscrowMirror_Success_Index(t *testing.T) {
	sUpstream, _ := newTestRegistry(t)
	sMirror, _ := newTestRegistry(t)

	ref := pushIndex(t, sUpstream, "upstream/base:v1", []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64})

	parsedRef, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", ref, err)
	}
	desc, err := remote.Get(parsedRef)
	if err != nil {
		t.Fatalf("remote.Get: %v", err)
	}

	privPEM, _ := genECKeyPairPEM(t)
	pushCosignSignature(t, sUpstream, parsedRef.Context().Name(), desc.Digest, privPEM, false)

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "pokkum.lock")

	mirrorRepo := registryRef(t, sMirror, "mirror/base")
	r := NewResolver(nil)
	ctx := context.Background()

	req := ports.BaseImageRequest{
		Preset:         ports.BaseImageCustom,
		Ref:            ref,
		Platforms:      []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64},
		LockfilePath:   lockPath,
		UpdateBase:     true,
		MirrorRegistry: mirrorRepo,
	}

	res, err := r.Resolve(ctx, req)
	if err != nil {
		t.Fatalf("Resolve with escrow mirror failed: %v", err)
	}

	expectedMirrorRef := fmt.Sprintf("%s:sha256-%s", mirrorRepo, res.Digest.Hex)

	// Check that lockfile recorded mirror_ref
	lf, err := lockfileutils.LoadLockfile(lockPath)
	if err != nil {
		t.Fatalf("LoadLockfile: %v", err)
	}
	entry, ok := lf.Bases[lockKeyFor(ports.BaseImageCustom, ref)]
	if !ok {
		t.Fatalf("lock entry not found for custom ref %s; keys: %v", ref, lockfileKeys(lf))
	}
	if entry.MirrorRef != expectedMirrorRef {
		t.Fatalf("lock entry MirrorRef = %q, want %q", entry.MirrorRef, expectedMirrorRef)
	}

	// Verify the mirrored index actually exists on mirror registry
	mTag, err := name.NewTag(expectedMirrorRef, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag: %v", err)
	}
	mDesc, err := remote.Get(mTag)
	if err != nil {
		t.Fatalf("remote.Get on mirror target %s: %v", expectedMirrorRef, err)
	}
	if mDesc.Digest != res.Digest {
		t.Fatalf("mirror digest = %s, want %s", mDesc.Digest, res.Digest)
	}

	// Verify the Cosign signature tag was mirrored as well
	sigTagRef := fmt.Sprintf("%s:sha256-%s.sig", mirrorRepo, res.Digest.Hex)
	mSigTag, err := name.NewTag(sigTagRef, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag for sig: %v", err)
	}
	if _, err := remote.Get(mSigTag); err != nil {
		t.Fatalf("remote.Get for mirrored signature %s: %v", sigTagRef, err)
	}

	// Now resolve using the lockfile to prove that it pulls from mirror
	r2 := NewResolver(nil)
	res2, err := r2.Resolve(ctx, ports.BaseImageRequest{
		Preset:       ports.BaseImageCustom,
		Ref:          ref,
		Platforms:    []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64},
		LockfilePath: lockPath,
	})
	if err != nil {
		t.Fatalf("Resolve using lockfile with mirror_ref failed: %v", err)
	}
	if res2.Digest != res.Digest {
		t.Fatalf("res2.Digest = %s, want %s", res2.Digest, res.Digest)
	}
	if res2.Ref != expectedMirrorRef {
		t.Fatalf("res2.Ref = %s, want %s (mirror ref)", res2.Ref, expectedMirrorRef)
	}
}

func TestResolve_EscrowMirror_Success_SingleImage(t *testing.T) {
	sUpstream, _ := newTestRegistry(t)
	sMirror, _ := newTestRegistry(t)

	ref := pushImage(t, sUpstream, "upstream/single:v1", ports.LinuxAMD64)
	mirrorRepo := registryRef(t, sMirror, "mirror/single")

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "pokkum.lock")

	r := NewResolver(nil)
	ctx := context.Background()

	res, err := r.Resolve(ctx, ports.BaseImageRequest{
		Preset:         ports.BaseImageCustom,
		Ref:            ref,
		Platforms:      []ports.Platform{ports.LinuxAMD64},
		LockfilePath:   lockPath,
		UpdateBase:     true,
		MirrorRegistry: mirrorRepo,
	})
	if err != nil {
		t.Fatalf("Resolve with single image escrow failed: %v", err)
	}

	expectedMirrorRef := fmt.Sprintf("%s:sha256-%s", mirrorRepo, res.Digest.Hex)
	lf, err := lockfileutils.LoadLockfile(lockPath)
	if err != nil {
		t.Fatalf("LoadLockfile: %v", err)
	}
	entry, ok := lf.Bases[lockKeyFor(ports.BaseImageCustom, ref)]
	if !ok || entry.MirrorRef != expectedMirrorRef {
		t.Fatalf("expected MirrorRef = %q, got %+v (keys: %v)", expectedMirrorRef, entry, lockfileKeys(lf))
	}

	mTag, err := name.NewTag(expectedMirrorRef, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag: %v", err)
	}
	if _, err := remote.Get(mTag); err != nil {
		t.Fatalf("remote.Get on mirror target %s: %v", expectedMirrorRef, err)
	}
}

func TestResolve_EscrowMirror_WriteFailure_Image_FailsClosed(t *testing.T) {
	sUpstream, _ := newTestRegistry(t)
	ref := pushImage(t, sUpstream, "upstream/fail:v1", ports.LinuxAMD64)

	// Unreachable mirror target on loopback port 1
	unreachableMirror := "127.0.0.1:1/mirror/unreachable"

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "pokkum.lock")

	r := NewResolver(nil)
	ctx := context.Background()

	_, err := r.Resolve(ctx, ports.BaseImageRequest{
		Preset:         ports.BaseImageCustom,
		Ref:            ref,
		Platforms:      []ports.Platform{ports.LinuxAMD64},
		LockfilePath:   lockPath,
		UpdateBase:     true,
		MirrorRegistry: unreachableMirror,
	})
	if err == nil {
		t.Fatal("expected Resolve to fail closed when mirror write fails")
	}
	if !errors.Is(err, core.ErrPushFailed) {
		t.Fatalf("expected error wrapping core.ErrPushFailed, got: %v", err)
	}

	// Verify pokkum.lock was NOT created with a phantom mirror
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("expected lockfile not to be created after failed mirror write")
	}
}

func TestResolve_EscrowMirror_WriteFailure_Signature_FailsClosed(t *testing.T) {
	sUpstream, _ := newTestRegistry(t)
	ref := pushImage(t, sUpstream, "upstream/sigfail:v1", ports.LinuxAMD64)

	parsedRef, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	desc, err := remote.Get(parsedRef)
	if err != nil {
		t.Fatalf("remote.Get: %v", err)
	}
	privPEM, _ := genECKeyPairPEM(t)
	pushCosignSignature(t, sUpstream, parsedRef.Context().Name(), desc.Digest, privPEM, false)

	// Mirror server that accepts base image write but rejects signature writes (.sig) with HTTP 500
	reg := registry.New()
	mirrorHandler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, ".sig") {
			http.Error(w, "signature mirror store failure", http.StatusInternalServerError)
			return
		}
		reg.ServeHTTP(w, req)
	})
	sMirror := httptest.NewServer(mirrorHandler)
	t.Cleanup(sMirror.Close)

	mirrorRepo := registryRef(t, sMirror, "mirror/sigfail")
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "pokkum.lock")

	r := NewResolver(nil)
	ctx := context.Background()

	_, err = r.Resolve(ctx, ports.BaseImageRequest{
		Preset:         ports.BaseImageCustom,
		Ref:            ref,
		Platforms:      []ports.Platform{ports.LinuxAMD64},
		LockfilePath:   lockPath,
		UpdateBase:     true,
		MirrorRegistry: mirrorRepo,
	})
	if err == nil {
		t.Fatal("expected Resolve to fail closed when signature mirror write fails")
	}
	if !errors.Is(err, core.ErrPushFailed) {
		t.Fatalf("expected error wrapping core.ErrPushFailed, got: %v", err)
	}

	// Verify pokkum.lock was NOT created
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("expected lockfile not to be created after failed signature mirror write")
	}
}

func TestResolve_EscrowMirror_FallbackToPinnedWhenMirrorUnreachable(t *testing.T) {
	sUpstream, _ := newTestRegistry(t)
	ref := pushImage(t, sUpstream, "upstream/fallback:v1", ports.LinuxAMD64)

	parsedRef, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	desc, err := remote.Get(parsedRef)
	if err != nil {
		t.Fatalf("remote.Get: %v", err)
	}

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "pokkum.lock")

	// Create a lockfile with an unreachable mirror ref, but a valid pinned ref
	lf := &ports.PokkumLockfile{
		Version:   lockfileutils.LockfileSchemaVersion,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Bases: map[string]ports.BaseLockEntry{
			string(ports.BaseImageCustom): {
				Ref:       ref,
				Digest:    desc.Digest.String(),
				PinnedRef: parsedRef.Context().Name() + "@" + desc.Digest.String(),
				MirrorRef: "127.0.0.1:1/mirror/down:sha256-" + desc.Digest.Hex,
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			},
		},
	}
	if err := lockfileutils.SaveLockfile(lockPath, lf); err != nil {
		t.Fatalf("SaveLockfile: %v", err)
	}

	r := NewResolver(nil)
	ctx := context.Background()

	res, err := r.Resolve(ctx, ports.BaseImageRequest{
		Preset:       ports.BaseImageCustom,
		Ref:          ref,
		Platforms:    []ports.Platform{ports.LinuxAMD64},
		LockfilePath: lockPath,
	})
	if err != nil {
		t.Fatalf("expected Resolve to fall back to PinnedRef when MirrorRef fails, got err: %v", err)
	}
	if res.Digest != desc.Digest {
		t.Fatalf("expected digest %s, got %s", desc.Digest, res.Digest)
	}
	if !strings.Contains(res.Ref, parsedRef.Context().Name()) {
		t.Fatalf("expected fallback to pinned ref, got ref: %s", res.Ref)
	}
}

func TestResolve_EscrowMirror_OfflineUsesMirrorWhenUpstreamUnreachable(t *testing.T) {
	sMirror, _ := newTestRegistry(t)
	mirrorRefTag := pushImage(t, sMirror, "mirror/isolated:sha256-test", ports.LinuxAMD64)

	mParsed, err := name.ParseReference(mirrorRefTag, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	desc, err := remote.Get(mParsed)
	if err != nil {
		t.Fatalf("remote.Get: %v", err)
	}

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "pokkum.lock")

	// PinnedRef points to an unreachable upstream server, MirrorRef points to reachable mirror
	lf := &ports.PokkumLockfile{
		Version:   lockfileutils.LockfileSchemaVersion,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Bases: map[string]ports.BaseLockEntry{
			string(ports.BaseImageCustom): {
				Ref:       "upstream.example.invalid/base:v1",
				Digest:    desc.Digest.String(),
				PinnedRef: "127.0.0.1:1/upstream/base@" + desc.Digest.String(),
				MirrorRef: mirrorRefTag,
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			},
		},
	}
	if err := lockfileutils.SaveLockfile(lockPath, lf); err != nil {
		t.Fatalf("SaveLockfile: %v", err)
	}

	r := NewResolver(nil)
	ctx := context.Background()

	res, err := r.Resolve(ctx, ports.BaseImageRequest{
		Preset:       ports.BaseImageCustom,
		Ref:          "upstream.example.invalid/base:v1",
		Platforms:    []ports.Platform{ports.LinuxAMD64},
		LockfilePath: lockPath,
	})
	if err != nil {
		t.Fatalf("expected Resolve to succeed using MirrorRef even when upstream PinnedRef is dead: %v", err)
	}
	if res.Digest != desc.Digest {
		t.Fatalf("expected digest %s, got %s", desc.Digest, res.Digest)
	}
	if res.Ref != mirrorRefTag {
		t.Fatalf("expected resolved ref %s, got %s", mirrorRefTag, res.Ref)
	}
}

// TestResolve_EscrowMirror_VerifySignature_SucceedsAgainstUpstreamRepo used
// to be TestResolve_EscrowMirror_VerifySignature_DockerReferenceMismatch,
// which empirically CONFIRMED (2026-08-14) a real bug: static-key Cosign
// verification checked the signature payload's embedded docker-reference
// against the repo bytes were actually fetched from, which — because
// mirror-resolve-first makes Resolve prefer entry.MirrorRef whenever it's
// reachable, not just as an outage fallback — was routinely the escrow
// mirror's repo name, not the upstream repo the signature was actually made
// for. That failed ordinary, successful resolution any time both
// --mirror-registry and VerifySignature were in play together (the default
// combination for the distroless/chainguard presets).
//
// Fixed by threading a second value, upstreamRef (sourced from the
// lockfile's entry.Ref, which is always the true upstream reference), through
// Resolve -> verifyBaseImage -> runVerification -> verifyStaticKeySignature /
// verifyKeylessSignature, so the docker-reference claims check always runs
// against the upstream repo while byte/tag fetching stays keyed on wherever
// the image actually lives (the mirror, when reachable).
//
// This test mirrors a genuinely-signed base image exactly as the real
// escrow write path does (image + its .sig tag, unmodified), then resolves
// with VerifySignature:true against a lockfile whose MirrorRef points at
// that mirror, and now asserts success with the security-relevant fields
// checked explicitly — not just "no error".
func TestResolve_EscrowMirror_VerifySignature_SucceedsAgainstUpstreamRepo(t *testing.T) {
	sUpstream, _ := newTestRegistry(t)
	sMirror, _ := newTestRegistry(t)

	upstreamRef := pushImage(t, sUpstream, "upstream/signed-base:v1", ports.LinuxAMD64)
	upstreamParsed, err := name.ParseReference(upstreamRef, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	upstreamDesc, err := remote.Get(upstreamParsed)
	if err != nil {
		t.Fatalf("remote.Get upstream: %v", err)
	}
	upstreamRepo := upstreamParsed.Context().Name()

	// Sign for the UPSTREAM repo name — exactly what a real signer does; it
	// has no concept of any mirror this image might later be copied to.
	privPEM, pubPEM := genECKeyPairPEM(t)
	pushCosignSignature(t, sUpstream, upstreamRepo, upstreamDesc.Digest, privPEM, false)

	// Mirror both the image and its .sig tag unmodified, exactly as
	// Resolve's real escrow-write path does (internal/adapters/baseimage/resolver.go,
	// the MirrorRegistry block: copies the image, then separately copies
	// "<repo>:<alg>-<hex>.sig" if present).
	mirrorRepo := registryRef(t, sMirror, "mirror/signed-base")
	mirrorImageRef := fmt.Sprintf("%s:sha256-%s", mirrorRepo, upstreamDesc.Digest.Hex)
	img, err := upstreamDesc.Image()
	if err != nil {
		t.Fatalf("upstreamDesc.Image: %v", err)
	}
	mirrorImageTag, err := name.NewTag(mirrorImageRef, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(mirror image): %v", err)
	}
	if err := remote.Write(mirrorImageTag, img); err != nil {
		t.Fatalf("mirror image write: %v", err)
	}

	sigTag := upstreamDesc.Digest.Algorithm + "-" + upstreamDesc.Digest.Hex + ".sig"
	upstreamSigRef, _ := name.ParseReference(upstreamRepo+":"+sigTag, name.WeakValidation)
	sigDesc, err := remote.Get(upstreamSigRef)
	if err != nil {
		t.Fatalf("remote.Get upstream sig: %v", err)
	}
	sigImg, err := sigDesc.Image()
	if err != nil {
		t.Fatalf("sigDesc.Image: %v", err)
	}
	mirrorSigTag, err := name.NewTag(mirrorRepo+":"+sigTag, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(mirror sig): %v", err)
	}
	if err := remote.Write(mirrorSigTag, sigImg); err != nil {
		t.Fatalf("mirror sig write: %v", err)
	}

	// Lockfile as it would look after a real `pokkum base update --mirror-registry=...`:
	// both PinnedRef (upstream) and MirrorRef are reachable and valid.
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "pokkum.lock")
	lf := &ports.PokkumLockfile{
		Version:   lockfileutils.LockfileSchemaVersion,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Bases: map[string]ports.BaseLockEntry{
			string(ports.BaseImageCustom): {
				Ref:       upstreamRef,
				Digest:    upstreamDesc.Digest.String(),
				PinnedRef: upstreamRepo + "@" + upstreamDesc.Digest.String(),
				MirrorRef: mirrorImageRef,
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			},
		},
	}
	if err := lockfileutils.SaveLockfile(lockPath, lf); err != nil {
		t.Fatalf("SaveLockfile: %v", err)
	}

	t.Setenv("POKKUM_BASE_IMAGE_PUBKEY", string(pubPEM))

	r := NewResolver(nil,
		WithCosignSigner(cosign.NewSigner(nil)),
		WithKeylessVerifier(sigstore.NewVerifier(nil)),
	)
	res, resolveErr := r.Resolve(context.Background(), ports.BaseImageRequest{
		Preset:          ports.BaseImageCustom,
		Ref:             upstreamRef,
		Platforms:       []ports.Platform{ports.LinuxAMD64},
		LockfilePath:    lockPath,
		VerifySignature: true,
	})

	// Fixed: claims are now checked against upstreamRepo (from entry.Ref),
	// not the mirror-derived repo the bytes were actually fetched from, so
	// this resolves cleanly even though the mirror was preferred.
	if resolveErr != nil {
		t.Fatalf("expected verification to succeed against the mirrored image once claims are checked against the upstream repo (entry.Ref), got: %v", resolveErr)
	}
	if res.Digest != upstreamDesc.Digest {
		t.Fatalf("res.Digest = %s, want %s (the digest of the actually-signed upstream image)", res.Digest, upstreamDesc.Digest)
	}
	if res.Ref != mirrorImageRef {
		t.Fatalf("res.Ref = %s, want %s (mirror is still preferred when reachable)", res.Ref, mirrorImageRef)
	}
	wantPinnedRef := mirrorRepo + "@" + upstreamDesc.Digest.String()
	if res.PinnedRef != wantPinnedRef {
		t.Fatalf("res.PinnedRef = %s, want %s", res.PinnedRef, wantPinnedRef)
	}
}

// TestResolve_EscrowMirror_VerifySignature_TamperedMirrorDigestFailsClosed
// proves the fix above didn't create a fail-open: checking the
// docker-reference claim against the upstream repo instead of the mirror
// must not weaken digest integrity. It constructs a genuine upstream image +
// signature (digest DA), then has the mirror serve *different* content
// (digest DB) while replaying the genuine, unmodified upstream signature
// payload — which still claims DA — under the .sig tag name the resolver
// will look for once it discovers the pulled digest is DB.
//
// Originally (before the escrow-mirror digest-pin enforcement fix,
// docs/archive/Roadmap.md item 2h) this was caught by cosign.Signer.Verify's own
// independent DockerManifestDigest comparison, deep inside signature
// verification — proving the repo-name claim matched (so the fix under test
// at the time wasn't accidentally what failed here) while digest integrity
// still held. That check is still there and would still catch this. But
// Resolve now compares entry.Digest against what the escrow mirror actually
// served immediately after the mirror pull, before ever attempting
// signature verification — a strictly earlier and more direct check for
// exactly this class of attack (see
// TestResolve_EscrowMirror_DigestSubstitution_DifferentLegitimatelySignedImageFailsClosed,
// which covers the case this fix specifically closed: a mirror substituting
// a *different, independently and genuinely signed* image, which the old
// per-signature DockerManifestDigest check alone could not catch). This test
// now asserts on that earlier failure.
func TestResolve_EscrowMirror_VerifySignature_TamperedMirrorDigestFailsClosed(t *testing.T) {
	sUpstream, _ := newTestRegistry(t)
	sMirror, _ := newTestRegistry(t)

	// Genuine upstream image + genuine static-key signature over it (digest DA).
	upstreamRef := pushImage(t, sUpstream, "upstream/tamper:v1", ports.LinuxAMD64)
	upstreamParsed, err := name.ParseReference(upstreamRef, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	upstreamDesc, err := remote.Get(upstreamParsed)
	if err != nil {
		t.Fatalf("remote.Get upstream: %v", err)
	}
	upstreamRepo := upstreamParsed.Context().Name()

	privPEM, pubPEM := genECKeyPairPEM(t)
	pushCosignSignature(t, sUpstream, upstreamRepo, upstreamDesc.Digest, privPEM, false)

	// Fetch the genuine signature image bytes verbatim, so they can be
	// replayed unmodified under a different tag below — the payload still
	// claims digest DA (the real, signed digest), unmodified.
	sigTag := upstreamDesc.Digest.Algorithm + "-" + upstreamDesc.Digest.Hex + ".sig"
	upstreamSigRef, err := name.ParseReference(upstreamRepo+":"+sigTag, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference(sig): %v", err)
	}
	sigDesc, err := remote.Get(upstreamSigRef)
	if err != nil {
		t.Fatalf("remote.Get upstream sig: %v", err)
	}
	sigImg, err := sigDesc.Image()
	if err != nil {
		t.Fatalf("sigDesc.Image: %v", err)
	}

	// Mirror serves TAMPERED bytes: a different random image (digest DB != DA).
	mirrorImageRef := pushImage(t, sMirror, "mirror/tamper:v1", ports.LinuxAMD64)
	mirrorParsed, err := name.ParseReference(mirrorImageRef, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference(mirror): %v", err)
	}
	mirrorDesc, err := remote.Get(mirrorParsed)
	if err != nil {
		t.Fatalf("remote.Get mirror: %v", err)
	}
	if mirrorDesc.Digest == upstreamDesc.Digest {
		t.Fatalf("test setup bug: mirror image accidentally has the same digest as upstream")
	}
	mirrorRepo := mirrorParsed.Context().Name()

	// Attacker replays the genuine upstream signature — unmodified, still
	// claiming DA — under the .sig tag name the resolver will actually look
	// for once it discovers the pulled digest is DB.
	mirrorSigRefStr := mirrorRepo + ":" + mirrorDesc.Digest.Algorithm + "-" + mirrorDesc.Digest.Hex + ".sig"
	mirrorSigTag, err := name.NewTag(mirrorSigRefStr, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(mirror sig): %v", err)
	}
	if err := remote.Write(mirrorSigTag, sigImg); err != nil {
		t.Fatalf("mirror sig write: %v", err)
	}

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "pokkum.lock")
	lf := &ports.PokkumLockfile{
		Version:   lockfileutils.LockfileSchemaVersion,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Bases: map[string]ports.BaseLockEntry{
			string(ports.BaseImageCustom): {
				Ref:       upstreamRef,
				Digest:    upstreamDesc.Digest.String(),
				PinnedRef: upstreamRepo + "@" + upstreamDesc.Digest.String(),
				MirrorRef: mirrorImageRef,
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			},
		},
	}
	if err := lockfileutils.SaveLockfile(lockPath, lf); err != nil {
		t.Fatalf("SaveLockfile: %v", err)
	}

	t.Setenv("POKKUM_BASE_IMAGE_PUBKEY", string(pubPEM))

	r := NewResolver(nil,
		WithCosignSigner(cosign.NewSigner(nil)),
		WithKeylessVerifier(sigstore.NewVerifier(nil)),
	)
	_, resolveErr := r.Resolve(context.Background(), ports.BaseImageRequest{
		Preset:          ports.BaseImageCustom,
		Ref:             upstreamRef,
		Platforms:       []ports.Platform{ports.LinuxAMD64},
		LockfilePath:    lockPath,
		VerifySignature: true,
	})

	// If digest integrity were NOT independently enforced, and if the
	// repo-name claim match introduced by the earlier fix were what silently
	// permitted this, Resolve would incorrectly succeed against tampered
	// mirror content. It must fail closed on digest integrity specifically —
	// now caught even earlier than signature verification, by the
	// escrow-mirror digest-pin check comparing the mirror's served digest
	// (DB) against pokkum.lock's locked digest (DA) immediately after the
	// mirror pull.
	if resolveErr == nil {
		t.Fatal("expected Resolve to fail closed: the mirror served different bytes than what was actually signed upstream")
	}
	if !errors.Is(resolveErr, core.ErrBaseSignatureInvalid) {
		t.Fatalf("err = %v, want core.ErrBaseSignatureInvalid", resolveErr)
	}
	if !strings.Contains(resolveErr.Error(), "escrow mirror") || !strings.Contains(resolveErr.Error(), "substituted") {
		t.Fatalf("expected failure to name the escrow-mirror digest substitution specifically, got: %v", resolveErr)
	}
	if !strings.Contains(resolveErr.Error(), upstreamDesc.Digest.String()) || !strings.Contains(resolveErr.Error(), mirrorDesc.Digest.String()) {
		t.Fatalf("expected failure to name both the locked digest (%s) and the digest actually served (%s), got: %v", upstreamDesc.Digest, mirrorDesc.Digest, resolveErr)
	}
}

// TestResolve_EscrowMirror_DigestSubstitution_DifferentLegitimatelySignedImageFailsClosed
// proves the escrow-mirror digest-substitution attack described in
// docs/archive/Roadmap.md item 2h/Lessons.md: an attacker with push access to the
// project's own escrow mirror retargets the mutable "sha256-<hex>" tag a
// locked entry.MirrorRef names to a DIFFERENT image — one that is itself
// entirely genuine and validly signed by the real upstream signer, e.g. an
// older release still carrying known CVEs — under the SAME upstream
// repository name. Unlike TestResolve_EscrowMirror_VerifySignature_TamperedMirrorDigestFailsClosed
// (which replays digest A's genuine signature against different bytes B, so
// the docker-manifest-digest claims check alone is what fails), this test's
// signature is completely genuine for whatever the mirror actually serves:
// real cert-free static-key signature, correct docker-reference, correct
// docker-manifest-digest for the SERVED digest. Every signature-verification
// check passes. The only thing that can catch this is comparing the served
// digest against the digest pokkum.lock actually locked this preset to
// (entry.Digest) — which, before the fix under test, was written but never
// read back for comparison anywhere in this file.
func TestResolve_EscrowMirror_DigestSubstitution_DifferentLegitimatelySignedImageFailsClosed(t *testing.T) {
	sUpstream, _ := newTestRegistry(t)
	sMirror, _ := newTestRegistry(t)

	// Two distinct, real upstream releases under the SAME repository name —
	// exactly the shape of two different tags/digests of e.g.
	// gcr.io/distroless/cc-debian12 over time. Both are genuinely signed by
	// the same real upstream signer.
	privPEM, pubPEM := genECKeyPairPEM(t)

	upstreamRefA := pushImage(t, sUpstream, "upstream/substitution:v1", ports.LinuxAMD64)
	parsedA, err := name.ParseReference(upstreamRefA, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference(A): %v", err)
	}
	descA, err := remote.Get(parsedA)
	if err != nil {
		t.Fatalf("remote.Get(A): %v", err)
	}
	upstreamRepo := parsedA.Context().Name()
	pushCosignSignature(t, sUpstream, upstreamRepo, descA.Digest, privPEM, false)

	upstreamRefB := pushImage(t, sUpstream, "upstream/substitution:v2", ports.LinuxAMD64)
	parsedB, err := name.ParseReference(upstreamRefB, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference(B): %v", err)
	}
	descB, err := remote.Get(parsedB)
	if err != nil {
		t.Fatalf("remote.Get(B): %v", err)
	}
	if descB.Digest == descA.Digest {
		t.Fatalf("test setup bug: A and B accidentally share a digest")
	}
	pushCosignSignature(t, sUpstream, upstreamRepo, descB.Digest, privPEM, false)

	// Legitimate escrow mirroring of A: image + its real .sig tag, unmodified
	// — exactly what Resolve's own MirrorRegistry block does.
	mirrorRepo := registryRef(t, sMirror, "mirror/substitution")
	mirrorTagStr := fmt.Sprintf("%s:sha256-%s", mirrorRepo, descA.Digest.Hex)
	mirrorTag, err := name.NewTag(mirrorTagStr, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(mirror A): %v", err)
	}
	imgA, err := descA.Image()
	if err != nil {
		t.Fatalf("descA.Image: %v", err)
	}
	if err := remote.Write(mirrorTag, imgA); err != nil {
		t.Fatalf("mirror write A: %v", err)
	}

	// ATTACK: retarget the SAME mirror tag to serve image B instead — the
	// mutable-tag substitution the fix must catch.
	imgB, err := descB.Image()
	if err != nil {
		t.Fatalf("descB.Image: %v", err)
	}
	if err := remote.Write(mirrorTag, imgB); err != nil {
		t.Fatalf("mirror retarget to B: %v", err)
	}

	// Mirror B's genuine signature under the tag name the resolver will
	// actually look for once it discovers the served digest is B's — an
	// attacker with push access to the mirror can trivially also mirror B's
	// real, already-public signature from upstream.
	sigTagB := descB.Digest.Algorithm + "-" + descB.Digest.Hex + ".sig"
	upstreamSigRefB, err := name.ParseReference(upstreamRepo+":"+sigTagB, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference(sig B): %v", err)
	}
	sigDescB, err := remote.Get(upstreamSigRefB)
	if err != nil {
		t.Fatalf("remote.Get(sig B): %v", err)
	}
	sigImgB, err := sigDescB.Image()
	if err != nil {
		t.Fatalf("sigDescB.Image: %v", err)
	}
	mirrorSigTagB, err := name.NewTag(mirrorRepo+":"+sigTagB, name.WeakValidation)
	if err != nil {
		t.Fatalf("NewTag(mirror sig B): %v", err)
	}
	if err := remote.Write(mirrorSigTagB, sigImgB); err != nil {
		t.Fatalf("mirror write sig B: %v", err)
	}

	// Lockfile as it would look after a real `pokkum base update
	// --mirror-registry=...` for image A: entry.Digest is locked to A,
	// exactly what the fix must enforce.
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "pokkum.lock")
	lf := &ports.PokkumLockfile{
		Version:   lockfileutils.LockfileSchemaVersion,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Bases: map[string]ports.BaseLockEntry{
			string(ports.BaseImageCustom): {
				Ref:       upstreamRefA,
				Digest:    descA.Digest.String(),
				PinnedRef: upstreamRepo + "@" + descA.Digest.String(),
				MirrorRef: mirrorTagStr,
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			},
		},
	}
	if err := lockfileutils.SaveLockfile(lockPath, lf); err != nil {
		t.Fatalf("SaveLockfile: %v", err)
	}

	t.Setenv("POKKUM_BASE_IMAGE_PUBKEY", string(pubPEM))

	r := NewResolver(nil,
		WithCosignSigner(cosign.NewSigner(nil)),
		WithKeylessVerifier(sigstore.NewVerifier(nil)),
	)
	res, resolveErr := r.Resolve(context.Background(), ports.BaseImageRequest{
		Preset:          ports.BaseImageCustom,
		Ref:             upstreamRefA,
		Platforms:       []ports.Platform{ports.LinuxAMD64},
		LockfilePath:    lockPath,
		VerifySignature: true,
	})

	// Every signature check would pass here — B's signature is completely
	// genuine for B. Only the digest-vs-lockfile check can catch this.
	if resolveErr == nil {
		t.Fatalf("expected Resolve to fail closed: the escrow mirror served a different, legitimately-signed image (digest %s) than what pokkum.lock locked this preset to (digest %s), but Resolve returned success with digest %s",
			descB.Digest, descA.Digest, res.Digest)
	}
	if !errors.Is(resolveErr, core.ErrBaseSignatureInvalid) {
		t.Fatalf("err = %v, want core.ErrBaseSignatureInvalid", resolveErr)
	}
	if !strings.Contains(resolveErr.Error(), descA.Digest.String()) || !strings.Contains(resolveErr.Error(), descB.Digest.String()) {
		t.Errorf("error should name both the locked digest (%s) and the digest actually served (%s) so an operator can diagnose the substitution, got: %v", descA.Digest, descB.Digest, resolveErr)
	}
}

func TestResolve_UpdateBase_DoesNotPreserveStaleMirrorRefForNewDigest(t *testing.T) {
	sUpstream, _ := newTestRegistry(t)
	ref := pushImage(t, sUpstream, "upstream/stale:v2", ports.LinuxAMD64)

	parsedRef, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	desc, err := remote.Get(parsedRef)
	if err != nil {
		t.Fatalf("remote.Get: %v", err)
	}

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "pokkum.lock")

	oldDigest := "sha256:" + strings.Repeat("0", 64)
	oldMirrorRef := "ghcr.io/myorg/base-mirror:sha256-" + strings.Repeat("0", 64)

	lf := &ports.PokkumLockfile{
		Version:   lockfileutils.LockfileSchemaVersion,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Bases: map[string]ports.BaseLockEntry{
			string(ports.BaseImageCustom): {
				Ref:       ref,
				Digest:    oldDigest,
				PinnedRef: parsedRef.Context().Name() + "@" + oldDigest,
				MirrorRef: oldMirrorRef,
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			},
		},
	}
	if err := lockfileutils.SaveLockfile(lockPath, lf); err != nil {
		t.Fatalf("SaveLockfile: %v", err)
	}

	r := NewResolver(nil)
	ctx := context.Background()

	// Update base WITHOUT --mirror-registry
	res, err := r.Resolve(ctx, ports.BaseImageRequest{
		Preset:       ports.BaseImageCustom,
		Ref:          ref,
		Platforms:    []ports.Platform{ports.LinuxAMD64},
		LockfilePath: lockPath,
		UpdateBase:   true,
	})
	if err != nil {
		t.Fatalf("Resolve update: %v", err)
	}
	if res.Digest == desc.Digest && desc.Digest.String() == oldDigest {
		t.Fatal("test setup error: new digest matches old digest")
	}

	lfUpdated, err := lockfileutils.LoadLockfile(lockPath)
	if err != nil {
		t.Fatalf("LoadLockfile: %v", err)
	}
	entry, ok := lfUpdated.Bases[lockKeyFor(ports.BaseImageCustom, ref)]
	if !ok {
		t.Fatalf("lock entry missing for custom ref %s; keys: %v", ref, lockfileKeys(lfUpdated))
	}
	if entry.Digest != desc.Digest.String() {
		t.Fatalf("expected updated digest %s, got %s", desc.Digest, entry.Digest)
	}
	if entry.MirrorRef != "" {
		t.Fatalf("expected stale MirrorRef to be cleared on digest update without mirror-registry, got: %q", entry.MirrorRef)
	}
}

func TestResolver_RecordScanResult(t *testing.T) {
	s := httptest.NewServer(registry.New())
	t.Cleanup(s.Close)

	ref := pushImage(t, s, "app/distroless:latest", ports.LinuxAMD64)
	parsed, err := name.ParseReference(ref, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	desc, err := remote.Get(parsed)
	if err != nil {
		t.Fatalf("remote.Get: %v", err)
	}

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "pokkum.lock")

	lf := &ports.PokkumLockfile{
		Version:   lockfileutils.LockfileSchemaVersion,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Bases: map[string]ports.BaseLockEntry{
			string(ports.BaseImageCustom): {
				Ref:       ref,
				Digest:    desc.Digest.String(),
				PinnedRef: ref,
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			},
		},
	}
	if err := lockfileutils.SaveLockfile(lockPath, lf); err != nil {
		t.Fatalf("SaveLockfile: %v", err)
	}

	r := NewResolver(nil)
	scan := ports.ScanResult{
		Target:           ref,
		Passed:           false,
		MaxSeverityFound: ports.SeverityCritical,
		Vulnerabilities: []ports.Vulnerability{
			{ID: "CVE-2026-0001", Severity: ports.SeverityCritical, Package: "libssl3"},
			{ID: "CVE-2026-0002", Severity: ports.SeverityHigh, Package: "libc6"},
		},
		ToolchainAdvisories: []ports.Vulnerability{
			{ID: "GHSA-bun-1.1.0", Severity: ports.SeverityHigh, Package: "bun"},
		},
	}

	err = r.RecordScanResult(context.Background(), lockPath, ports.BaseImageCustom, ref, scan)
	if err != nil {
		t.Fatalf("RecordScanResult: %v", err)
	}

	updatedLF, err := lockfileutils.LoadLockfile(lockPath)
	if err != nil {
		t.Fatalf("LoadLockfile: %v", err)
	}

	// The lockfile above was seeded with a legacy bare "custom" entry, so
	// RecordScanResult migrates it: the findings land in the per-reference slot.
	entry, ok := lockfileutils.GetLockedBase(updatedLF, lockKeyFor(ports.BaseImageCustom, ref))
	if !ok {
		t.Fatalf("missing locked base entry under per-reference key %q; lockfile keys: %v",
			lockKeyFor(ports.BaseImageCustom, ref), lockfileKeys(updatedLF))
	}

	if entry.LastScannedAt == "" {
		t.Errorf("expected LastScannedAt to be populated")
	}
	if entry.VulnerabilitiesCount != 3 {
		t.Errorf("VulnerabilitiesCount = %d, want 3", entry.VulnerabilitiesCount)
	}
	if entry.MaxSeverity != string(ports.SeverityCritical) {
		t.Errorf("MaxSeverity = %s, want critical", entry.MaxSeverity)
	}

	// Verify Resolve reads back the scan audit fields from the lockfile
	resolved, err := r.Resolve(context.Background(), ports.BaseImageRequest{
		Preset:       ports.BaseImageCustom,
		Ref:          ref,
		LockfilePath: lockPath,
		Offline:      true,
		Platforms:    []ports.Platform{ports.LinuxAMD64},
	})
	if err != nil {
		t.Fatalf("Resolve offline: %v", err)
	}
	if resolved.LastScannedAt != entry.LastScannedAt {
		t.Errorf("resolved.LastScannedAt = %q, want %q", resolved.LastScannedAt, entry.LastScannedAt)
	}
	if resolved.VulnerabilitiesCount != 3 {
		t.Errorf("resolved.VulnerabilitiesCount = %d, want 3", resolved.VulnerabilitiesCount)
	}
	if resolved.MaxSeverity != string(ports.SeverityCritical) {
		t.Errorf("resolved.MaxSeverity = %q, want %q", resolved.MaxSeverity, string(ports.SeverityCritical))
	}
}

// TestResolve_MirrorRef_LayersAreMountableFromMirrorRepo pins a property the
// planned cross-repo-blob-mount optimization for registry pushes depends on:
// when Resolve serves an image from a lockfile's entry.MirrorRef (the
// "Base Image Escrow / Mirroring" feature, `pokkum base update
// --mirror-registry=<repo>`), the v1.Image it returns must still be the
// go-containerregistry "mountable" image remote.Get produces — i.e. every
// layer must be a *remote.MountableLayer whose Reference names the MIRROR
// repository, not the upstream one.
//
// This matters because remote.Write attempts a cross-repository blob mount
// (skip re-uploading bytes it can prove the target registry already has) only
// when pushing a *remote.MountableLayer, and it mounts FROM whatever
// repository that layer's Reference names. A mirror set up via
// --mirror-registry is typically same-host as the eventual push target
// (that's the point of mirroring — escrow the base close to where builds
// push), so the mount source has to be the mirror, not upstream, for the
// optimization to ever actually fire. If Resolve's mirror-preference path
// (resolver.go's `if entry.MirrorRef != ""` block) ever rebuilt or unwrapped
// the image instead of returning exactly what r.pull cached from
// remote.Get(...).Image(), this property would silently break and every
// mirrored build would fall back to full layer re-uploads without anyone
// noticing (no test currently observes the Reference on the returned image,
// only that resolution succeeds and Ref/Digest look right).
//
// Setup deliberately points PinnedRef at an unreachable upstream host — the
// same fixture shape as TestResolve_EscrowMirror_OfflineUsesMirrorWhenUpstreamUnreachable
// — so that the only place the returned image's bytes could have come from is
// the mirror pull.
func TestResolve_MirrorRef_LayersAreMountableFromMirrorRepo(t *testing.T) {
	sMirror, _ := newTestRegistry(t)
	mirrorRef := pushImage(t, sMirror, "mirror/mountable:v1", ports.LinuxAMD64)

	mParsed, err := name.ParseReference(mirrorRef, name.WeakValidation)
	if err != nil {
		t.Fatalf("ParseReference(mirror): %v", err)
	}
	desc, err := remote.Get(mParsed)
	if err != nil {
		t.Fatalf("remote.Get(mirror): %v", err)
	}
	wantMirrorRepoName := mParsed.Context().Name()

	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, "pokkum.lock")
	lf := &ports.PokkumLockfile{
		Version:   lockfileutils.LockfileSchemaVersion,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Bases: map[string]ports.BaseLockEntry{
			string(ports.BaseImageCustom): {
				// Upstream/pinned both point at an unreachable host: if the
				// resolver ever fell back off the mirror path, Resolve would
				// fail outright rather than silently return upstream-sourced
				// layers, so a passing test here really does prove the mirror
				// path produced the returned image.
				Ref:       "upstream.example.invalid/mountable:v1",
				Digest:    desc.Digest.String(),
				PinnedRef: "127.0.0.1:1/upstream/mountable@" + desc.Digest.String(),
				MirrorRef: mirrorRef,
				UpdatedAt: time.Now().UTC().Format(time.RFC3339),
			},
		},
	}
	if err := lockfileutils.SaveLockfile(lockPath, lf); err != nil {
		t.Fatalf("SaveLockfile: %v", err)
	}

	r := NewResolver(nil)
	res, err := r.Resolve(context.Background(), ports.BaseImageRequest{
		Preset:       ports.BaseImageCustom,
		Ref:          "upstream.example.invalid/mountable:v1",
		Platforms:    []ports.Platform{ports.LinuxAMD64},
		LockfilePath: lockPath,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Ref != mirrorRef {
		t.Fatalf("res.Ref = %s, want %s (mirror should have been preferred)", res.Ref, mirrorRef)
	}

	img, ok := res.Images[ports.LinuxAMD64]
	if !ok {
		t.Fatalf("Images missing linux/amd64")
	}

	layers, err := img.Layers()
	if err != nil {
		t.Fatalf("Layers: %v", err)
	}
	if len(layers) == 0 {
		t.Fatalf("test fixture produced an image with no layers")
	}
	for i, l := range layers {
		ml, ok := l.(*remote.MountableLayer)
		if !ok {
			t.Fatalf("layer %d has type %T, want *remote.MountableLayer — Resolve's mirror path is not preserving "+
				"go-containerregistry's mountable-image wrapping, so cross-repo blob mount can never fire for a "+
				"mirrored base image", i, l)
		}
		if got := ml.Reference.Context().Name(); got != wantMirrorRepoName {
			t.Errorf("layer %d MountableLayer.Reference repo = %q, want %q (the mirror repo) — a cross-repo mount "+
				"attempt for this layer would target the wrong source repository", i, got, wantMirrorRepoName)
		}
	}
}

// TestVerifyBaseImage_NoVerifierInjected_FailsClosed proves the resolver
// refuses rather than accepts when the verifier its verify mode needs was
// never injected.
//
// This is the regression guard for removing the constructor defaults. Both
// verifiers used to be defaulted inside NewResolver by constructing
// cosign.NewSigner(log) / sigstore.NewVerifier(log) directly — the
// adapter-imports-adapter edges internal/architecture_test.go used to
// allowlist. With the defaults gone, "no verifier" is reachable, and the one
// unacceptable outcome is that it silently reads as "nothing to verify, carry
// on": that is the same fail-open shape a149b28 removed from the placeholder
// trust anchor and 812662e removed from the cache verify-mode default arm.
//
// Each subtest is otherwise byte-identical in setup to its passing sibling
// elsewhere in this file — a real key pair, a real signature pushed to a real
// in-process registry, a real key configured in the environment — so the ONLY
// variable is whether the resolver was handed something able to verify. That
// matters: without a genuinely verifiable image, a refusal proves nothing,
// because an unsigned image would be refused anyway.
func TestVerifyBaseImage_NoVerifierInjected_FailsClosed(t *testing.T) {
	t.Run("static-key mode without a CosignSigner", func(t *testing.T) {
		s, _ := newTestRegistry(t)
		ref := pushImage(t, s, "app/no-signer-injected:v1", ports.LinuxAMD64)
		privPEM, pubPEM := genECKeyPairPEM(t)

		// Deliberately NO options: this is the composition-root wiring gap
		// under test.
		r := NewResolver(nil)
		ctx := context.Background()

		req := ports.BaseImageRequest{
			Preset:    ports.BaseImageCustom,
			Ref:       ref,
			Platforms: []ports.Platform{ports.LinuxAMD64},
			Insecure:  true,
		}
		resolved, err := r.Resolve(ctx, req)
		if err != nil {
			t.Fatalf("pre-resolve: %v", err)
		}
		parsedRef, err := name.ParseReference(ref, name.WeakValidation)
		if err != nil {
			t.Fatalf("ParseReference: %v", err)
		}
		pushCosignSignature(t, s, parsedRef.Context().Name(), resolved.Digest, privPEM, false)

		// A real, correct key IS configured, so "no key" cannot be the reason
		// for the refusal.
		t.Setenv("POKKUM_BASE_IMAGE_PUBKEY", string(pubPEM))

		verifyReq := req
		verifyReq.VerifySignature = true
		verifyReq.VerifyMode = ports.BaseImageVerifyStaticKey
		err = r.VerifyBaseImage(ctx, resolved, verifyReq)
		if err == nil {
			t.Fatal("VerifyBaseImage accepted a base image with no ports.CosignSigner injected; static-key verification must fail closed")
		}
		if !errors.Is(err, core.ErrBaseSignatureInvalid) {
			t.Fatalf("err = %v, want core.ErrBaseSignatureInvalid", err)
		}
		if !strings.Contains(err.Error(), "ports.CosignSigner") || !strings.Contains(err.Error(), "WithCosignSigner") {
			t.Errorf("error should name the missing dependency and its injection point, got: %v", err)
		}
	})

	t.Run("keyless mode without a KeylessVerifier", func(t *testing.T) {
		s, _ := newTestRegistry(t)
		ref := pushImage(t, s, "app/no-keyless-injected:v1", ports.LinuxAMD64)

		r := NewResolver(nil)
		ctx := context.Background()

		req := ports.BaseImageRequest{
			Preset:    ports.BaseImageCustom,
			Ref:       ref,
			Platforms: []ports.Platform{ports.LinuxAMD64},
			Insecure:  true,
		}
		resolved, err := r.Resolve(ctx, req)
		if err != nil {
			t.Fatalf("pre-resolve: %v", err)
		}
		parsedRef, err := name.ParseReference(ref, name.WeakValidation)
		if err != nil {
			t.Fatalf("ParseReference: %v", err)
		}
		// Real captured Fulcio/Rekor material, so the refusal cannot be
		// explained by absent keyless material either.
		pushKeylessSignatureFixture(t, s, parsedRef.Context().Name(), resolved.Digest, "../sigstore/testdata/distroless-nonroot")

		verifyReq := req
		verifyReq.VerifySignature = true
		verifyReq.VerifyMode = ports.BaseImageVerifyKeyless
		verifyReq.KeylessIdentity = ports.KeylessIdentity{
			Issuer: ports.DistrolessKeylessIssuer,
			SAN:    ports.DistrolessKeylessSAN,
		}
		err = r.VerifyBaseImage(ctx, resolved, verifyReq)
		if err == nil {
			t.Fatal("VerifyBaseImage accepted a base image with no ports.KeylessVerifier injected; keyless verification must fail closed")
		}
		if !errors.Is(err, core.ErrBaseSignatureInvalid) {
			t.Fatalf("err = %v, want core.ErrBaseSignatureInvalid", err)
		}
		if !strings.Contains(err.Error(), "ports.KeylessVerifier") || !strings.Contains(err.Error(), "WithKeylessVerifier") {
			t.Errorf("error should name the missing dependency and its injection point, got: %v", err)
		}
	})

	t.Run("un-injected resolver still resolves when not verifying", func(t *testing.T) {
		// The other half of the contract, and the reason these are options
		// rather than required constructor parameters: a resolver that never
		// verifies (e.g. `pokkum base check`) legitimately needs no verifier,
		// and must not be broken by the refusals above.
		s, _ := newTestRegistry(t)
		ref := pushImage(t, s, "app/no-verifiers-resolve-ok:v1", ports.LinuxAMD64)

		r := NewResolver(nil)
		resolved, err := r.Resolve(context.Background(), ports.BaseImageRequest{
			Preset:    ports.BaseImageCustom,
			Ref:       ref,
			Platforms: []ports.Platform{ports.LinuxAMD64},
			Insecure:  true,
		})
		if err != nil {
			t.Fatalf("Resolve without verifiers: %v", err)
		}
		if resolved.Digest.Hex == "" {
			t.Fatal("Resolve returned no digest")
		}
	})
}
