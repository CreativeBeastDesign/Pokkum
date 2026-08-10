package registry

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/CreativeBeastDesign/pokkum/internal/core"
	"github.com/CreativeBeastDesign/pokkum/internal/ports"
)

func TestPush_EmptyRepo(t *testing.T) {
	a := NewAdapter(nil)
	_, err := a.Push(context.Background(), ports.PushRequest{
		Payload: ports.Payload{Image: randomImage(t)},
	})
	if !errors.Is(err, core.ErrNoDockerRepo) {
		t.Fatalf("err = %v, want core.ErrNoDockerRepo", err)
	}
}

func TestPush_ZeroPayload(t *testing.T) {
	a := NewAdapter(nil)
	_, err := a.Push(context.Background(), ports.PushRequest{Repo: "example.com/app"})
	if !errors.Is(err, core.ErrPushFailed) {
		t.Fatalf("err = %v, want core.ErrPushFailed", err)
	}
}

func TestPush_SingleImage_RoundTrip(t *testing.T) {
	s, _ := newTestRegistry(t)
	repo := registryRepo(t, s, "app/single")
	img := randomImage(t)
	wantDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	a := NewAdapter(nil)
	res, err := a.Push(context.Background(), ports.PushRequest{
		Repo:    repo,
		Tags:    []string{"v1", "latest"},
		Payload: ports.Payload{Image: img},
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if res.Digest != wantDigest {
		t.Errorf("Digest = %s, want %s", res.Digest, wantDigest)
	}
	wantRef := repo + "@" + wantDigest.String()
	if res.Ref != wantRef {
		t.Errorf("Ref = %q, want %q", res.Ref, wantRef)
	}
	if len(res.Tags) != 2 {
		t.Errorf("Tags = %v, want 2 entries", res.Tags)
	}

	// Pull back by the returned digest reference and confirm it matches.
	ref, err := name.ParseReference(res.Ref)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", res.Ref, err)
	}
	pulled, err := remote.Image(ref)
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

	// Both requested tags must resolve to the same manifest.
	for _, tag := range []string{"v1", "latest"} {
		tref, err := name.ParseReference(repo + ":" + tag)
		if err != nil {
			t.Fatalf("ParseReference(%q): %v", tag, err)
		}
		desc, err := remote.Head(tref)
		if err != nil {
			t.Fatalf("remote.Head(%q): %v", tag, err)
		}
		if desc.Digest != wantDigest {
			t.Errorf("tag %q resolves to %s, want %s", tag, desc.Digest, wantDigest)
		}
	}
}

func TestPush_Index_RoundTrip(t *testing.T) {
	s, _ := newTestRegistry(t)
	repo := registryRepo(t, s, "app/multi")
	idx := indexWithPlatforms(t, []ports.Platform{ports.LinuxAMD64, ports.LinuxARM64})
	wantDigest, err := idx.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	a := NewAdapter(nil)
	res, err := a.Push(context.Background(), ports.PushRequest{
		Repo:    repo,
		Tags:    []string{"v1"},
		Payload: ports.Payload{Index: idx},
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if res.Digest != wantDigest {
		t.Errorf("Digest = %s, want %s", res.Digest, wantDigest)
	}

	ref, err := name.ParseReference(res.Ref)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	desc, err := remote.Get(ref)
	if err != nil {
		t.Fatalf("remote.Get: %v", err)
	}
	pulledIdx, err := desc.ImageIndex()
	if err != nil {
		t.Fatalf("ImageIndex: %v", err)
	}
	gotDigest, err := pulledIdx.Digest()
	if err != nil {
		t.Fatalf("pulled index Digest: %v", err)
	}
	if gotDigest != wantDigest {
		t.Errorf("pulled index digest = %s, want %s", gotDigest, wantDigest)
	}
	im, err := pulledIdx.IndexManifest()
	if err != nil {
		t.Fatalf("IndexManifest: %v", err)
	}
	if len(im.Manifests) != 2 {
		t.Errorf("index has %d manifests, want 2", len(im.Manifests))
	}
}

// TestPush_Idempotent proves the second push of identical content performs no
// upload at all: it counts every PUT/POST the test registry saw during each
// push and asserts the second round is zero of both, while still returning
// the same digest.
func TestPush_Idempotent(t *testing.T) {
	s, cr := newTestRegistry(t)
	repo := registryRepo(t, s, "app/idempotent")
	img := randomImage(t)
	wantDigest, _ := img.Digest()

	a := NewAdapter(nil)
	req := ports.PushRequest{
		Repo:    repo,
		Tags:    []string{"v1"},
		Payload: ports.Payload{Image: img},
	}

	before1 := cr.len()
	res1, err := a.Push(context.Background(), req)
	if err != nil {
		t.Fatalf("first Push: %v", err)
	}
	first := cr.since(before1)
	firstPUTs := countMethod(first, http.MethodPut, "")
	firstPOSTs := countMethod(first, http.MethodPost, "")
	if firstPUTs == 0 && firstPOSTs == 0 {
		t.Fatalf("first push recorded no PUT/POST at all — test fixture is broken, first push must upload something")
	}

	before2 := cr.len()
	res2, err := a.Push(context.Background(), req)
	if err != nil {
		t.Fatalf("second Push: %v", err)
	}
	second := cr.since(before2)
	secondPUTs := countMethod(second, http.MethodPut, "")
	secondPOSTs := countMethod(second, http.MethodPost, "")

	if secondPUTs != 0 || secondPOSTs != 0 {
		t.Errorf("second push made %d PUT and %d POST requests, want 0 of each (requests: %v)", secondPUTs, secondPOSTs, second)
	}
	secondHEADs := countMethod(second, http.MethodHead, "")
	if secondHEADs == 0 {
		t.Errorf("second push made no HEAD request; the idempotency check itself did not run")
	}

	if res2.Digest != wantDigest || res1.Digest != wantDigest {
		t.Errorf("digests = %s, %s, want both %s", res1.Digest, res2.Digest, wantDigest)
	}
}

// --- digestExists classification --------------------------------------------

func TestDigestExists_NotFound(t *testing.T) {
	s, _ := newTestRegistry(t)
	repo, err := name.NewRepository(registryRepo(t, s, "app/missing"))
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	digestRef := repo.Digest("sha256:" + strings.Repeat("0", 64))

	a := NewAdapter(nil)
	exists, err := a.digestExists(digestRef, []remote.Option{remote.WithContext(context.Background())})
	if err != nil {
		t.Fatalf("digestExists: %v", err)
	}
	if exists {
		t.Errorf("exists = true, want false for a digest the registry has never seen")
	}
}

func TestDigestExists_Unauthorized(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(s.Close)

	repo, err := name.NewRepository(strings.TrimPrefix(s.URL, "http://") + "/app/x")
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	digestRef := repo.Digest("sha256:" + strings.Repeat("1", 64))

	a := NewAdapter(nil)
	_, err = a.digestExists(digestRef, []remote.Option{remote.WithContext(context.Background())})
	if !errors.Is(err, core.ErrRegistryAuth) {
		t.Fatalf("err = %v, want core.ErrRegistryAuth", err)
	}
}

func TestDigestExists_Forbidden(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(s.Close)

	repo, err := name.NewRepository(strings.TrimPrefix(s.URL, "http://") + "/app/x")
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	digestRef := repo.Digest("sha256:" + strings.Repeat("1", 64))

	a := NewAdapter(nil)
	_, err = a.digestExists(digestRef, []remote.Option{remote.WithContext(context.Background())})
	if !errors.Is(err, core.ErrRegistryAuth) {
		t.Fatalf("err = %v, want core.ErrRegistryAuth", err)
	}
}

func TestDigestExists_NetworkError(t *testing.T) {
	// Reserve a loopback address and close it immediately, so connecting to
	// it fails fast with "connection refused" rather than a registry
	// response of any kind — this is what a DNS failure, a firewalled host,
	// or a registry that is simply down looks like to the classifier.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	repo, err := name.NewRepository(addr+"/app/x", name.Insecure)
	if err != nil {
		t.Fatalf("NewRepository: %v", err)
	}
	digestRef := repo.Digest("sha256:" + strings.Repeat("2", 64))

	a := NewAdapter(nil)
	_, err = a.digestExists(digestRef, []remote.Option{remote.WithContext(context.Background())})
	if err == nil {
		t.Fatal("digestExists: want error for an unreachable host, got nil")
	}
	if errors.Is(err, core.ErrRegistryAuth) {
		t.Errorf("err = %v, misclassified a network error as ErrRegistryAuth", err)
	}
	if !errors.Is(err, core.ErrPushFailed) {
		t.Errorf("err = %v, want core.ErrPushFailed", err)
	}
}
