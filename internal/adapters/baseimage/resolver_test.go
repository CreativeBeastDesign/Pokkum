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

	r := NewResolver(nil)
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

	r := NewResolver(nil)
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

	r := NewResolver(nil)
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

	r := NewResolver(nil)
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

	r := NewResolver(nil)
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

	r := NewResolver(nil)
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
	entry, ok := lockfileutils.GetLockedBase(lf, string(ports.BaseImageCustom))
	if !ok {
		t.Fatalf("lock entry not found for custom preset")
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
	entry, ok := lockfileutils.GetLockedBase(lf, string(ports.BaseImageCustom))
	if !ok || entry.MirrorRef != expectedMirrorRef {
		t.Fatalf("expected MirrorRef = %q, got %+v", expectedMirrorRef, entry)
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
	mirrorHandler := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.Contains(req.URL.Path, ".sig") {
			http.Error(w, "signature mirror store failure", http.StatusInternalServerError)
			return
		}
		registry.New().ServeHTTP(w, req)
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
	entry, ok := lockfileutils.GetLockedBase(lfUpdated, string(ports.BaseImageCustom))
	if !ok {
		t.Fatalf("lock entry missing")
	}
	if entry.Digest != desc.Digest.String() {
		t.Fatalf("expected updated digest %s, got %s", desc.Digest, entry.Digest)
	}
	if entry.MirrorRef != "" {
		t.Fatalf("expected stale MirrorRef to be cleared on digest update without mirror-registry, got: %q", entry.MirrorRef)
	}
}
