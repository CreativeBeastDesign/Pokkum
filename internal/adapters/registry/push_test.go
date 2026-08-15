package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"

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

// --- cross-repo mount integration tests -------------------------------------
//
// The four tests below exercise Push's cross-repository blob-mount
// optimization end-to-end against newMountAwareTestRegistry
// (mount_test.go), on top of the harness fix pinned by
// TestMountAwareTestRegistry_RealWriteThenReadAgreeOnRepo (mount_test.go):
// without that fix, every test here would fail for a reason that has
// nothing to do with mounting — a real remote.Write-streamed blob (the
// config, and any layer that isn't mount-eligible) would simply vanish from
// the target repo on the very next read.
//
// Each test proves its outcome twice, independently:
//
//   - at the HTTP layer, via request counts (countMethod/cr, mirroring
//     TestPush_Idempotent above) and, for the cross-registry test, via a
//     transport-level spy that can see the full mount= / from= query string
//     countingRegistry deliberately drops;
//   - via Push's own "pushed image" structured log line
//     (mount_layers_mounted/_declined/_already_present, already emitted
//     unconditionally by push.go), captured through a JSON slog.Handler.
//
// Neither line of proof requires or adds a production-code hook: the log
// fields already exist for operators, and the HTTP-level counts observe
// exactly what a real registry operator would see on the wire.

// newLoggingAdapter returns an Adapter whose logger writes structured JSON to
// the returned buffer, so a test can recover the mounted/declined/already
// present blob counts push.go already logs on its "pushed image" line
// without needing any test-only accessor into mountStats.
func newLoggingAdapter() (*Adapter, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	return NewAdapter(logger), &buf
}

// pushedImageLogFields decodes every JSON log line in buf and returns the
// attributes of the LAST "pushed image" record found — last, because a test
// that reuses one Adapter for a seeding push plus the push under test only
// wants the second record's counts.
func pushedImageLogFields(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var last map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("decode log line %q: %v", line, err)
		}
		if rec["msg"] == "pushed image" {
			last = rec
		}
	}
	if last == nil {
		t.Fatalf("no %q log line found in captured output: %s", "pushed image", buf.String())
	}
	return last
}

// mountCount reads one of the mount_layers_* integer fields logged on the
// "pushed image" line. slog's JSON handler encodes Go ints as JSON numbers,
// which decode through encoding/json's default map[string]any handling as
// float64.
func mountCount(t *testing.T, fields map[string]any, key string) int {
	t.Helper()
	v, ok := fields[key]
	if !ok {
		t.Fatalf("log fields missing %q: %v", key, fields)
	}
	f, ok := v.(float64)
	if !ok {
		t.Fatalf("log field %q = %v (%T), want a number", key, v, v)
	}
	return int(f)
}

// drainLayer reads a layer's compressed body to completion, proving the
// registry can genuinely serve it end to end — not merely that it answered
// a HEAD/manifest request correctly.
func drainLayer(t *testing.T, l interface {
	Compressed() (io.ReadCloser, error)
}, i int) {
	t.Helper()
	rc, err := l.Compressed()
	if err != nil {
		t.Fatalf("layer %d Compressed: %v", i, err)
	}
	defer rc.Close()
	if _, err := io.Copy(io.Discard, rc); err != nil {
		t.Fatalf("layer %d: reading blob body back: %v", i, err)
	}
}

// TestPush_CrossRepoMount_ZeroEgress covers the success path: a base layer
// genuinely present under the source repo is mounted into the target repo
// (same registry host) with no bytes re-uploaded, while the freshly-built
// layer and regenerated config still stream normally alongside it.
func TestPush_CrossRepoMount_ZeroEgress(t *testing.T) {
	s, _, cr := newMountAwareTestRegistry(t)
	repoA := s.Repo("app/mount-source")
	repoB := s.Repo("app/mount-target")

	a, logBuf := newLoggingAdapter()

	baseImg := randomImage(t)
	if _, err := a.Push(t.Context(), ports.PushRequest{
		Repo:    repoA,
		Tags:    []string{"base"},
		Payload: ports.Payload{Image: baseImg},
	}); err != nil {
		t.Fatalf("seed push to source repo: %v", err)
	}

	// Pull the seeded image back through go-containerregistry's own
	// mounting wrapper, exactly as baseimage.Resolver does in production
	// (see resolver_test.go's
	// TestResolve_MirrorRef_LayersAreMountableFromMirrorRepo): every layer
	// comes back as a *remote.MountableLayer whose Reference names repoA,
	// which is what makes a cross-repo mount attempt possible at all.
	baseRef, err := name.ParseReference(repoA+":base", nameOptions(false)...)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	baseDesc, err := remote.Get(baseRef)
	if err != nil {
		t.Fatalf("remote.Get(repoA): %v", err)
	}
	mountableBase, err := baseDesc.Image()
	if err != nil {
		t.Fatalf("baseDesc.Image: %v", err)
	}
	baseLayers, err := mountableBase.Layers()
	if err != nil {
		t.Fatalf("mountableBase.Layers: %v", err)
	}
	for i, l := range baseLayers {
		if _, ok := l.(*remote.MountableLayer); !ok {
			t.Fatalf("base layer %d has type %T, want *remote.MountableLayer — test fixture is not exercising a real mount candidate", i, l)
		}
	}

	newLayer, err := random.Layer(256, types.OCILayer)
	if err != nil {
		t.Fatalf("random.Layer: %v", err)
	}
	composed, err := mutate.AppendLayers(mountableBase, newLayer)
	if err != nil {
		t.Fatalf("mutate.AppendLayers: %v", err)
	}
	composedDigest, err := composed.Digest()
	if err != nil {
		t.Fatalf("composed Digest: %v", err)
	}

	before := cr.len()
	res, err := a.Push(t.Context(), ports.PushRequest{
		Repo:    repoB,
		Tags:    []string{"v1"},
		Payload: ports.Payload{Image: composed},
	})
	if err != nil {
		t.Fatalf("Push composed image to target repo: %v", err)
	}
	if res.Digest != composedDigest {
		t.Errorf("res.Digest = %s, want %s", res.Digest, composedDigest)
	}
	reqs := cr.since(before)

	// The decisive HTTP-level proof: the composed image has exactly two
	// blobs that are new to repoB and are NOT mount candidates (the new
	// layer, and the config — mutate.AppendLayers regenerates the config to
	// add the new layer's DiffID, so it's new content too), plus the one
	// mount-candidate base layer. A genuine mount success means only those
	// two non-candidate blobs are ever streamed via PATCH; a silent fallback
	// to a full upload would make it three.
	if posts := countMethod(reqs, http.MethodPost, "/blobs/uploads/"); posts != 3 {
		t.Errorf("initiation POSTs to repoB = %d, want 3 (one per blob: base + new layer + config): %v", posts, reqs)
	}
	if patches := countMethod(reqs, http.MethodPatch, "/blobs/uploads/"); patches != 2 {
		t.Errorf("PATCH (blob stream) requests to repoB = %d, want 2 (new layer + config only — a 3rd would mean the base layer's bytes were re-uploaded instead of mounted): %v", patches, reqs)
	}
	if puts := countMethod(reqs, http.MethodPut, "/blobs/uploads/"); puts != 2 {
		t.Errorf("PUT (blob commit) requests to repoB = %d, want 2, matching the PATCH count: %v", puts, reqs)
	}

	fields := pushedImageLogFields(t, logBuf)
	if mounted := mountCount(t, fields, "mount_layers_mounted"); mounted != 1 {
		t.Errorf("mount_layers_mounted = %d, want 1", mounted)
	}
	if declined := mountCount(t, fields, "mount_layers_declined"); declined != 0 {
		t.Errorf("mount_layers_declined = %d, want 0", declined)
	}

	// Round-trip: the target repo must actually serve the full, correct
	// content — a "mount" that lied about linking the blob would otherwise
	// pass every request-count assertion above while leaving repoB unable to
	// actually serve the base layer.
	pulledRef, err := name.ParseReference(res.Ref)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", res.Ref, err)
	}
	pulled, err := remote.Image(pulledRef)
	if err != nil {
		t.Fatalf("remote.Image(repoB): %v", err)
	}
	gotDigest, err := pulled.Digest()
	if err != nil {
		t.Fatalf("pulled.Digest: %v", err)
	}
	if gotDigest != composedDigest {
		t.Errorf("pulled digest = %s, want %s", gotDigest, composedDigest)
	}
	pulledLayers, err := pulled.Layers()
	if err != nil {
		t.Fatalf("pulled.Layers: %v", err)
	}
	if len(pulledLayers) != 2 {
		t.Fatalf("pulled image has %d layers, want 2", len(pulledLayers))
	}
	for i, l := range pulledLayers {
		drainLayer(t, l, i)
	}
}

// TestPush_CrossRepoMount_FallbackOnAbsentBlob covers a layer that claims to
// be mountable from a source repo, but whose digest was never actually
// stored there on this harness — the registry must decline the mount and
// the client must fall back to a real upload, still succeeding.
func TestPush_CrossRepoMount_FallbackOnAbsentBlob(t *testing.T) {
	s, _, cr := newMountAwareTestRegistry(t)
	repoA := s.Repo("app/absent-source")
	repoB := s.Repo("app/absent-target")

	a, logBuf := newLoggingAdapter()

	// A layer that CLAIMS to be mountable from repoA, but whose digest was
	// never Put into repoA on this harness. Wrapping a freshly-built,
	// never-pushed layer in *remote.MountableLayer directly (rather than via
	// remote.Get, which would require the content to already exist
	// somewhere) is the most direct way to construct exactly that state.
	absentLayer, err := random.Layer(256, types.OCILayer)
	if err != nil {
		t.Fatalf("random.Layer: %v", err)
	}
	absentDigest, err := absentLayer.Digest()
	if err != nil {
		t.Fatalf("absentLayer.Digest: %v", err)
	}
	sourceRef, err := name.ParseReference(repoA+":never-pushed", nameOptions(false)...)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	mountableAbsent := &remote.MountableLayer{Layer: absentLayer, Reference: sourceRef}

	composed, err := mutate.AppendLayers(empty.Image, mountableAbsent)
	if err != nil {
		t.Fatalf("mutate.AppendLayers: %v", err)
	}
	composedDigest, err := composed.Digest()
	if err != nil {
		t.Fatalf("composed Digest: %v", err)
	}

	before := cr.len()
	res, err := a.Push(t.Context(), ports.PushRequest{
		Repo:    repoB,
		Tags:    []string{"v1"},
		Payload: ports.Payload{Image: composed},
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if res.Digest != composedDigest {
		t.Errorf("res.Digest = %s, want %s", res.Digest, composedDigest)
	}
	reqs := cr.since(before)

	// The mount was attempted (the layer's Reference made it a candidate)
	// and declined by the harness, since absentDigest was never genuinely
	// present under repoA — proof the layer actually streamed, not mounted.
	if patches := countMethod(reqs, http.MethodPatch, "/blobs/uploads/"); patches != 2 {
		t.Errorf("PATCH requests = %d, want 2 (the absent layer + config, both streamed since nothing mounted): %v", patches, reqs)
	}

	fields := pushedImageLogFields(t, logBuf)
	if mounted := mountCount(t, fields, "mount_layers_mounted"); mounted != 0 {
		t.Errorf("mount_layers_mounted = %d, want 0 (the source repo never had this digest)", mounted)
	}
	if declined := mountCount(t, fields, "mount_layers_declined"); declined != 1 {
		t.Errorf("mount_layers_declined = %d, want 1", declined)
	}

	// Round-trip proof the fallback stream actually landed the right bytes.
	pulledRef, err := name.ParseReference(res.Ref)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", res.Ref, err)
	}
	pulled, err := remote.Image(pulledRef)
	if err != nil {
		t.Fatalf("remote.Image: %v", err)
	}
	pulledLayers, err := pulled.Layers()
	if err != nil {
		t.Fatalf("pulled.Layers: %v", err)
	}
	if len(pulledLayers) != 1 {
		t.Fatalf("pulled image has %d layers, want 1", len(pulledLayers))
	}
	gotDigest, err := pulledLayers[0].Digest()
	if err != nil {
		t.Fatalf("pulled layer Digest: %v", err)
	}
	if gotDigest != absentDigest {
		t.Errorf("pulled layer digest = %s, want %s", gotDigest, absentDigest)
	}
	drainLayer(t, pulledLayers[0], 0)
}

// recordedURLRequest is one outbound client request observed by
// urlRecordingTransport: unlike countingRegistry's recordedRequest (which
// only keeps r.URL.Path on the SERVER side), this keeps the query string and
// destination host too — exactly what's needed to prove a mount POST's
// mount=/from= parameters and which registry host it was actually sent to.
type recordedURLRequest struct {
	Method string
	Host   string
	Path   string
	Query  url.Values
}

// urlRecordingTransport wraps a real http.RoundTripper and records every
// outbound request's method, destination host, path and query, so a test can
// prove a specific cross-repository mount attempt was genuinely sent to a
// specific registry host.
type urlRecordingTransport struct {
	http.RoundTripper
	mu   sync.Mutex
	reqs []recordedURLRequest
}

func (u *urlRecordingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	u.mu.Lock()
	u.reqs = append(u.reqs, recordedURLRequest{
		Method: r.Method,
		Host:   r.URL.Host,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
	})
	u.mu.Unlock()
	return u.RoundTripper.RoundTrip(r)
}

func (u *urlRecordingTransport) snapshot() []recordedURLRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]recordedURLRequest, len(u.reqs))
	copy(out, u.reqs)
	return out
}

// TestPush_CrossRepoMount_CrossRegistryRejected is the most important
// negative test: it proves the "zero-egress mount" feature degrades
// gracefully, rather than breaking, when the mount source and target are
// different registry hosts entirely.
//
// go-containerregistry's writer.uploadOne has no same-host precondition
// before attempting a mount (see registry_test.go's
// TestPush_DockerHubMountSuppression_IsRealAndAppliesToOurPushPath for the
// one narrow exception, Docker Hub, which does not apply to these loopback
// test hosts) — it will send the mount POST cross-host regardless, and it is
// the TARGET registry's job to recognize it has no idea what the claimed
// source repository contains and decline. This test sets up two entirely
// separate httptest servers (two independent newMountAwareTestRegistry
// instances, each with its own repoScopedBlobHandler and its own
// countingRegistry — server B has categorically no way to see server A's
// state), seeds the real blob only on server A, and pushes a composed image
// referencing it to server B. Proof this genuinely exercised the cross-host
// path, not just a same-host fallback that happens to look the same:
//
//   - server A's own request log (crA) is asserted empty for the whole
//     duration of the push to server B — the client only ever talks to the
//     registry it is pushing to, so if a request had somehow reached server A,
//     that would itself be the bug;
//   - a transport-level spy (urlRecordingTransport, swapped in for
//     defaultTransport exactly as TestRemoteOptions_AlwaysWiresPackageTransport
//     in registry_test.go does) captures the mount POST's actual destination
//     host and its mount=/from= query parameters, which countingRegistry's
//     path-only recordedRequest cannot show — confirming the request that hit
//     server B genuinely named server A's repository as the mount source.
func TestPush_CrossRepoMount_CrossRegistryRejected(t *testing.T) {
	sA, _, crA := newMountAwareTestRegistry(t)
	sB, _, crB := newMountAwareTestRegistry(t)
	repoA := sA.Repo("app/cross-source")
	repoB := sB.Repo("app/cross-target")

	a, logBuf := newLoggingAdapter()

	baseImg := randomImage(t)
	if _, err := a.Push(t.Context(), ports.PushRequest{
		Repo:    repoA,
		Tags:    []string{"base"},
		Payload: ports.Payload{Image: baseImg},
	}); err != nil {
		t.Fatalf("seed push to server A: %v", err)
	}

	baseRef, err := name.ParseReference(repoA+":base", nameOptions(false)...)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	baseDesc, err := remote.Get(baseRef)
	if err != nil {
		t.Fatalf("remote.Get(server A): %v", err)
	}
	mountableBase, err := baseDesc.Image()
	if err != nil {
		t.Fatalf("baseDesc.Image: %v", err)
	}
	baseLayers, err := mountableBase.Layers()
	if err != nil {
		t.Fatalf("mountableBase.Layers: %v", err)
	}
	if len(baseLayers) != 1 {
		t.Fatalf("test fixture has %d base layers, want 1", len(baseLayers))
	}
	baseLayerDigest, err := baseLayers[0].Digest()
	if err != nil {
		t.Fatalf("base layer Digest: %v", err)
	}

	newLayer, err := random.Layer(256, types.OCILayer)
	if err != nil {
		t.Fatalf("random.Layer: %v", err)
	}
	composed, err := mutate.AppendLayers(mountableBase, newLayer)
	if err != nil {
		t.Fatalf("mutate.AppendLayers: %v", err)
	}
	composedDigest, err := composed.Digest()
	if err != nil {
		t.Fatalf("composed Digest: %v", err)
	}

	spy := &urlRecordingTransport{RoundTripper: defaultTransport}
	origTransport := defaultTransport
	defaultTransport = spy
	t.Cleanup(func() { defaultTransport = origTransport })

	beforeA, beforeB := crA.len(), crB.len()
	res, err := a.Push(t.Context(), ports.PushRequest{
		Repo:    repoB,
		Tags:    []string{"v1"},
		Payload: ports.Payload{Image: composed},
	})
	if err != nil {
		t.Fatalf("Push composed image to server B: %v", err)
	}
	if res.Digest != composedDigest {
		t.Errorf("res.Digest = %s, want %s", res.Digest, composedDigest)
	}

	// Server A must never receive a mount-shaped request: mounting is a
	// target-registry-only operation (the target registry is the one that
	// would link the blob into itself; the source registry has no API for
	// participating in that beyond serving reads), so a POST reaching server
	// A here would mean this test is wired wrong, not that the feature works.
	//
	// Server A DOES legitimately see a GET for the base layer's blob: since
	// the mount is declined by server B, go-containerregistry falls back to
	// actually streaming the layer, and this layer's bytes are lazily backed
	// by wherever it was pulled from (server A) — a pulled *MountableLayer's
	// Compressed() reads through to its origin on demand, it does not cache
	// the whole blob in memory just because it was wrapped as mountable. That
	// GET is not incidental noise; it is itself independent proof the mount
	// did NOT succeed, since a successful mount would need no such read at
	// all (see TestPush_CrossRepoMount_ZeroEgress, where no such GET occurs).
	reqsA := crA.since(beforeA)
	for _, r := range reqsA {
		if r.Method == http.MethodPost {
			t.Errorf("server A observed a POST %+v during the push to server B — mounting only ever POSTs to the TARGET registry; a mount-shaped request reaching the source registry would be a bug", r)
		}
	}
	if gets := countMethod(reqsA, http.MethodGet, "/blobs/"+baseLayerDigest.String()); gets == 0 {
		t.Errorf("server A observed no GET for the base layer's blob (%s) during the push to server B — expected exactly this read as the direct consequence of the mount being declined and the layer streaming instead: %v", baseLayerDigest, reqsA)
	}

	// Confirm the mount POST that hit server B really did name repoA (on
	// server A) as its mount source — this is what makes "cross-host" a
	// checked fact about this test run rather than an assumption about how
	// go-containerregistry happens to behave.
	sourceRepo, err := name.NewRepository(repoA, nameOptions(false)...)
	if err != nil {
		t.Fatalf("NewRepository(repoA): %v", err)
	}
	wantFrom := sourceRepo.RepositoryStr()
	targetHost := sB.Host()

	var mountAttempted bool
	for _, rr := range spy.snapshot() {
		if rr.Method != http.MethodPost || rr.Host != targetHost || !strings.Contains(rr.Path, "/blobs/uploads/") {
			continue
		}
		if rr.Query.Get("mount") == baseLayerDigest.String() && rr.Query.Get("from") == wantFrom {
			mountAttempted = true
			break
		}
	}
	if !mountAttempted {
		t.Fatalf("no mount POST to server B (%s) with mount=%s&from=%s found among recorded requests: %+v",
			targetHost, baseLayerDigest, wantFrom, spy.snapshot())
	}

	// Server B's own mount-emulation handler has no knowledge of server A's
	// blobs (a completely separate repoScopedBlobHandler instance), so it
	// must decline and every blob — including the base layer — streams
	// normally.
	reqsB := crB.since(beforeB)
	if patches := countMethod(reqsB, http.MethodPatch, "/blobs/uploads/"); patches != 3 {
		t.Errorf("PATCH requests to server B = %d, want 3 (base layer + new layer + config, all streamed — nothing mountable cross-host): %v", patches, reqsB)
	}

	fields := pushedImageLogFields(t, logBuf)
	if mounted := mountCount(t, fields, "mount_layers_mounted"); mounted != 0 {
		t.Errorf("mount_layers_mounted = %d, want 0 (server B has no knowledge of server A's blobs)", mounted)
	}
	if declined := mountCount(t, fields, "mount_layers_declined"); declined != 1 {
		t.Errorf("mount_layers_declined = %d, want 1", declined)
	}

	// Round-trip against server B.
	pulledRef, err := name.ParseReference(res.Ref)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", res.Ref, err)
	}
	pulled, err := remote.Image(pulledRef)
	if err != nil {
		t.Fatalf("remote.Image(server B): %v", err)
	}
	gotDigest, err := pulled.Digest()
	if err != nil {
		t.Fatalf("pulled.Digest: %v", err)
	}
	if gotDigest != composedDigest {
		t.Errorf("pulled digest = %s, want %s", gotDigest, composedDigest)
	}
}

// TestPush_CrossRepoMount_RegistryRejectsMount simulates the registry
// explicitly rejecting a mount request (405, rather than the normal
// "digest not found, fall through to 202" answer tested by
// TestPush_CrossRepoMount_FallbackOnAbsentBlob), to exercise
// go-containerregistry's retryWithoutMount behavior distinctly:
// initiateUpload (pkg/v1/remote/write.go) treats ANY status other than
// 201/202 as a failure and, whenever a mount was attempted (from != ""),
// retries the exact same upload once more with no mount/from params at all —
// producing two initiation POSTs for that one blob instead of one.
func TestPush_CrossRepoMount_RegistryRejectsMount(t *testing.T) {
	baseImg := randomImage(t)
	baseImgLayers, err := baseImg.Layers()
	if err != nil {
		t.Fatalf("baseImg.Layers: %v", err)
	}
	if len(baseImgLayers) != 1 {
		t.Fatalf("test fixture has %d layers, want 1", len(baseImgLayers))
	}
	baseLayerDigest, err := baseImgLayers[0].Digest()
	if err != nil {
		t.Fatalf("base layer Digest: %v", err)
	}

	// The registry under test explicitly rejects any mount attempt for this
	// exact digest with 405 Method Not Allowed, rather than mountAwareHandler's
	// normal "not genuinely present, fall through" logic.
	s, _, cr := newMountRejectingTestRegistry(t, baseLayerDigest.String(), http.StatusMethodNotAllowed)
	repoA := s.Repo("app/reject-source")
	repoB := s.Repo("app/reject-target")

	a, logBuf := newLoggingAdapter()

	// Seeding the source repo never sets mount/from (a plain, non-Mountable
	// layer never does — see write.go's uploadOne), so mountRejectHandler's
	// digest match never fires for this push.
	if _, err := a.Push(t.Context(), ports.PushRequest{
		Repo:    repoA,
		Tags:    []string{"base"},
		Payload: ports.Payload{Image: baseImg},
	}); err != nil {
		t.Fatalf("seed push to source repo: %v", err)
	}

	baseRef, err := name.ParseReference(repoA+":base", nameOptions(false)...)
	if err != nil {
		t.Fatalf("ParseReference: %v", err)
	}
	baseDesc, err := remote.Get(baseRef)
	if err != nil {
		t.Fatalf("remote.Get: %v", err)
	}
	mountableBase, err := baseDesc.Image()
	if err != nil {
		t.Fatalf("baseDesc.Image: %v", err)
	}

	newLayer, err := random.Layer(256, types.OCILayer)
	if err != nil {
		t.Fatalf("random.Layer: %v", err)
	}
	composed, err := mutate.AppendLayers(mountableBase, newLayer)
	if err != nil {
		t.Fatalf("mutate.AppendLayers: %v", err)
	}
	composedDigest, err := composed.Digest()
	if err != nil {
		t.Fatalf("composed Digest: %v", err)
	}

	before := cr.len()
	res, err := a.Push(t.Context(), ports.PushRequest{
		Repo:    repoB,
		Tags:    []string{"v1"},
		Payload: ports.Payload{Image: composed},
	})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if res.Digest != composedDigest {
		t.Errorf("res.Digest = %s, want %s", res.Digest, composedDigest)
	}
	reqs := cr.since(before)

	// The rejected base layer alone accounts for TWO initiation POSTs (the
	// rejected mount attempt, then the mount-less retry), while the new
	// layer and config each need exactly one — the one signal that
	// distinguishes "registry actively rejected the mount" from "registry
	// reported the digest absent" (no retry ever happens in that case,
	// because the very first response already IS the normal 202 fallback).
	if posts := countMethod(reqs, http.MethodPost, "/blobs/uploads/"); posts != 4 {
		t.Errorf("initiation POSTs to repoB = %d, want 4 (1 each for the new layer and config, 2 for the rejected-then-retried base layer): %v", posts, reqs)
	}
	// Despite the retry, every blob must still land exactly once: 3 PATCH
	// streams and 3 PUT commits, not 4 of either — proving nothing was
	// double-uploaded by the retry.
	if patches := countMethod(reqs, http.MethodPatch, "/blobs/uploads/"); patches != 3 {
		t.Errorf("PATCH requests = %d, want 3: %v", patches, reqs)
	}
	if puts := countMethod(reqs, http.MethodPut, "/blobs/uploads/"); puts != 3 {
		t.Errorf("blob-commit PUT requests = %d, want 3: %v", puts, reqs)
	}
	if manifestPuts := countMethod(reqs, http.MethodPut, "/manifests/"); manifestPuts != 1 {
		t.Errorf("manifest PUT requests = %d, want 1 (not double-committed): %v", manifestPuts, reqs)
	}

	fields := pushedImageLogFields(t, logBuf)
	if mounted := mountCount(t, fields, "mount_layers_mounted"); mounted != 0 {
		t.Errorf("mount_layers_mounted = %d, want 0", mounted)
	}
	if declined := mountCount(t, fields, "mount_layers_declined"); declined != 1 {
		t.Errorf("mount_layers_declined = %d, want 1 (deduped to the one digest, despite two POSTs for it)", declined)
	}

	pulledRef, err := name.ParseReference(res.Ref)
	if err != nil {
		t.Fatalf("ParseReference(%q): %v", res.Ref, err)
	}
	pulled, err := remote.Image(pulledRef)
	if err != nil {
		t.Fatalf("remote.Image: %v", err)
	}
	gotDigest, err := pulled.Digest()
	if err != nil {
		t.Fatalf("pulled.Digest: %v", err)
	}
	if gotDigest != composedDigest {
		t.Errorf("pulled digest = %s, want %s", gotDigest, composedDigest)
	}
	pulledLayers, err := pulled.Layers()
	if err != nil {
		t.Fatalf("pulled.Layers: %v", err)
	}
	if len(pulledLayers) != 2 {
		t.Fatalf("pulled image has %d layers, want 2", len(pulledLayers))
	}
	for i, l := range pulledLayers {
		drainLayer(t, l, i)
	}
}
