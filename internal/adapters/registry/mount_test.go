package registry

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	regharness "github.com/CreativeBeastDesign/pokkum/pkg/registry"
)

// --- repo-scoped in-memory blob storage -------------------------------------
//
// go-containerregistry's own registry.NewInMemoryBlobHandler (pkg/registry's
// unexported memHandler, see blobs.go) stores blobs keyed by digest ALONE. It
// deliberately ignores which repo a blob was requested/stored under, so it
// cannot distinguish "digest exists under repo A" from "digest exists under
// repo B" — every digest looks present everywhere. That makes it useless for
// testing cross-repo blob mounting (POST .../blobs/uploads/?mount=&from=):
// a naive test would see the mount candidate as "already there" regardless
// of which repo it actually asks about, so the test would pass without ever
// exercising real mount-vs-no-mount logic.
//
// repoScopedBlobHandler fixes that by keying storage on (repo, digest), so
// this harness can genuinely gate mount emulation on repo-scoped presence.
type repoDigestKey struct {
	repo   string
	digest string
}

type repoScopedBlobHandler struct {
	mu    sync.Mutex
	blobs map[repoDigestKey][]byte
}

func newRepoScopedBlobHandler() *repoScopedBlobHandler {
	return &repoScopedBlobHandler{blobs: map[repoDigestKey][]byte{}}
}

// normalizeBlobRepo undoes a path-depth quirk in upstream's own repo
// computation (pkg/registry/blobs.go, blobs.handle):
//
//	repo := req.URL.Host + path.Join(elem[1:len(elem)-2]...)
//
// elem is every path.Split segment of the request URL, and that slice
// expression trims exactly the LAST TWO segments before joining the rest
// back into a repo string — which only produces the real repository name
// when the request's final two segments are "blobs/<digest>" (every read:
// GET/HEAD, and the mount-initiation POST, whose path ends
// ".../blobs/uploads/" with no id yet).
//
// The chunked-upload PATCH/PUT requests (streamBlob/commitBlob in
// pkg/v1/remote/write.go) hit ".../blobs/uploads/<id>" instead — one segment
// deeper — so trimming the same last two segments leaves "blobs" itself
// inside the joined string. A real push's blob body therefore lands under
// "<repo>/blobs" while every read of that same content asks for it under
// plain "<repo>", so a genuine remote.Write-driven push would appear to
// vanish on the very next remote.Head/Get/mount-check against the same repo.
//
// Trimming a trailing "/blobs" segment here is a no-op for the already-correct
// shapes (GET/HEAD/initiation-POST never end with the extra "blobs" segment,
// since path.Join has already dropped it there), so this normalizes both
// shapes onto the one true repo string without needing to know which code
// path a given call came from.
func normalizeBlobRepo(repo string) string {
	return strings.TrimSuffix(repo, "/blobs")
}

// Get implements registry.BlobHandler.
func (h *repoScopedBlobHandler) Get(_ context.Context, repo string, hash v1.Hash) (io.ReadCloser, error) {
	repo = normalizeBlobRepo(repo)
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.blobs[repoDigestKey{repo, hash.String()}]
	if !ok {
		return nil, registry.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// Stat implements registry.BlobStatHandler.
func (h *repoScopedBlobHandler) Stat(_ context.Context, repo string, hash v1.Hash) (int64, error) {
	repo = normalizeBlobRepo(repo)
	h.mu.Lock()
	defer h.mu.Unlock()
	b, ok := h.blobs[repoDigestKey{repo, hash.String()}]
	if !ok {
		return 0, registry.ErrNotFound
	}
	return int64(len(b)), nil
}

// Put implements registry.BlobPutHandler. The signature (io.ReadCloser, not
// io.Reader) matches the vendored interface exactly; see
// pkg/registry/blobs.go's BlobPutHandler in the go-containerregistry module.
func (h *repoScopedBlobHandler) Put(_ context.Context, repo string, hash v1.Hash, rc io.ReadCloser) error {
	repo = normalizeBlobRepo(repo)
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.blobs[repoDigestKey{repo, hash.String()}] = b
	return nil
}

var (
	_ registry.BlobHandler     = (*repoScopedBlobHandler)(nil)
	_ registry.BlobStatHandler = (*repoScopedBlobHandler)(nil)
	_ registry.BlobPutHandler  = (*repoScopedBlobHandler)(nil)
)

// --- mount-emulation HTTP wrapper --------------------------------------------

// parseUploadsRepo returns the repo name for a blob-upload-initiation path
// of the form "/v2/<repo>/blobs/uploads/", or ok=false for anything else.
// This mirrors the path math in go-containerregistry's blobs.go isBlob/handle
// (elem[len(elem)-3]=="blobs", elem[len(elem)-2]=="uploads"), but only needs
// to recognize the exact upload-initiation shape the mount wrapper cares
// about.
func parseUploadsRepo(urlPath string) (repo string, ok bool) {
	const prefix = "/v2/"
	const suffix = "/blobs/uploads/"
	if !strings.HasPrefix(urlPath, prefix) || !strings.HasSuffix(urlPath, suffix) {
		return "", false
	}
	repo = strings.TrimSuffix(strings.TrimPrefix(urlPath, prefix), suffix)
	if repo == "" {
		return "", false
	}
	return repo, true
}

// mountAwareHandler intercepts cross-repo blob-mount initiation requests
// (POST .../v2/<targetRepo>/blobs/uploads/?mount=<digest>&from=<sourceRepo>)
// and emulates real-registry mount semantics against bh.
//
// go-containerregistry's in-memory registry.New() handler has no concept of
// "mount" at all: its POST handler (blobs.go, case http.MethodPost) only
// understands a "digest" query param for monolithic upload, or no params at
// all to begin a normal chunked upload. A "mount"+"from" pair is simply
// ignored, so without this wrapper every mount attempt would silently fall
// through to a normal 202-Accepted upload-start — never exercising the
// fast-path the client (remote.writer.initiateUpload, pkg/v1/remote/write.go)
// is actually trying to use, and never proving that mounting is genuinely
// gated on the digest existing under the *source* repo.
//
// Behavior mirrors real registries and what the go-containerregistry client
// expects from initiateUpload:
//   - digest genuinely present under the source repo: copy it into the
//     target repo and respond 201 Created with Docker-Content-Digest (and,
//     for fidelity with the real distribution spec, a Location header
//     pointing at the now-mounted blob) — the client treats any 201 here as
//     "mounted, nothing more to do" and does not parse Location.
//   - digest absent from the source repo: do NOT intercept; let the
//     underlying handler proceed with its normal upload-start flow (202
//     Accepted + Location + Range), so the client streams the blob as usual.
//   - anything else (wrong method, wrong path shape, no mount/from params):
//     do NOT intercept.
func mountAwareHandler(bh *repoScopedBlobHandler, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		targetRepo, ok := parseUploadsRepo(r.URL.Path)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		mount := r.URL.Query().Get("mount")
		from := r.URL.Query().Get("from")
		if mount == "" || from == "" {
			next.ServeHTTP(w, r)
			return
		}
		hash, err := v1.NewHash(mount)
		if err != nil {
			// Malformed digest: let the normal handler produce the
			// appropriate error response.
			next.ServeHTTP(w, r)
			return
		}

		ctx := r.Context()
		if _, err := bh.Stat(ctx, from, hash); err != nil {
			// Not genuinely present under the source repo — do not fake a
			// mount. Fall through to the real upload-start flow.
			next.ServeHTTP(w, r)
			return
		}

		rc, err := bh.Get(ctx, from, hash)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		// Essential: without actually writing the blob under the target
		// repo, a later manifest PUT referencing this digest (or any
		// remote.Image/round-trip read against the target repo) would fail
		// to find it, even though we just told the client it was mounted.
		if err := bh.Put(ctx, targetRepo, hash, rc); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Docker-Content-Digest", hash.String())
		w.Header().Set("Location", "/v2/"+targetRepo+"/blobs/"+hash.String())
		w.WriteHeader(http.StatusCreated)
	})
}

// --- test harness wiring -----------------------------------------------------

// newMountAwareTestRegistry starts an in-memory OCI registry (pkg/registry)
// backed by a repoScopedBlobHandler and fronted by mountAwareHandler, so
// cross-repo mount tests can exercise genuine repo-scoped digest gating
// instead of the always-present semantics of go-containerregistry's own
// digest-only in-memory blob store.
//
// It also records every request the server sees (mirroring countingRegistry
// in registry_test.go), so a follow-up zero-egress-style test can assert
// exactly which requests a successful mount avoided.
func newMountAwareTestRegistry(t *testing.T) (*regharness.Server, *repoScopedBlobHandler, *countingRegistry) {
	t.Helper()
	bh := newRepoScopedBlobHandler()
	cr := &countingRegistry{}

	s, err := regharness.NewServer(
		regharness.WithBlobHandler(bh),
		regharness.WithHandlerWrapper(func(next http.Handler) http.Handler {
			cr.inner = mountAwareHandler(bh, next)
			return cr
		}),
	)
	if err != nil {
		t.Fatalf("regharness.NewServer: %v", err)
	}
	t.Cleanup(s.Close)
	return s, bh, cr
}

// mountRejectHandler forces a mount POST for one specific digest to fail with
// status instead of following the normal accept/decline logic, so a test can
// exercise go-containerregistry's retryWithoutMount path (write.go's
// initiateUpload: on any status other than 201/202 when a mount was
// attempted, it retries the same upload once more with no mount/from params
// at all) distinctly from the "digest genuinely absent from the source repo"
// case, which never produces a rejection status — mountAwareHandler simply
// declines to intercept and the registry's normal handler answers 202
// directly, with no retry involved.
//
// It must run BEFORE mountAwareHandler in the chain: the retry's second POST
// carries no mount/from params, so it will not match rejectDigest here and
// falls through unchanged to mountAwareHandler (a no-op pass-through for a
// param-less request) and then the real registry handler.
func mountRejectHandler(rejectDigest string, status int, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if _, ok := parseUploadsRepo(r.URL.Path); ok {
				q := r.URL.Query()
				if q.Get("mount") == rejectDigest && q.Get("from") != "" {
					http.Error(w, `{"errors":[{"code":"DENIED","message":"mount rejected by test"}]}`, status)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// newMountRejectingTestRegistry is newMountAwareTestRegistry plus
// mountRejectHandler in front of it, so a mount attempt for rejectDigest
// answers status (e.g. 405 or 400) instead of mountAwareHandler's normal
// accept-if-genuinely-present/decline-otherwise logic. Every other digest
// still goes through the real mount-emulation path unchanged.
func newMountRejectingTestRegistry(t *testing.T, rejectDigest string, status int) (*regharness.Server, *repoScopedBlobHandler, *countingRegistry) {
	t.Helper()
	bh := newRepoScopedBlobHandler()
	cr := &countingRegistry{}

	s, err := regharness.NewServer(
		regharness.WithBlobHandler(bh),
		regharness.WithHandlerWrapper(func(next http.Handler) http.Handler {
			cr.inner = mountRejectHandler(rejectDigest, status, mountAwareHandler(bh, next))
			return cr
		}),
	)
	if err != nil {
		t.Fatalf("regharness.NewServer: %v", err)
	}
	t.Cleanup(s.Close)
	return s, bh, cr
}

// --- harness bug regression: write-then-read must agree on repo -----------
//
// This pins the fix in normalizeBlobRepo above with the most direct proof
// available: drive a REAL remote.Write-issued chunked upload (POST-init,
// PATCH-stream, PUT-commit — exactly what a.Push's non-mount layers use)
// through this harness, then read the same content back with remote.Head and
// remote.Image against the SAME repo. Before the fix, the PATCH/PUT pair
// stored the blob under "<repo>/blobs" (see normalizeBlobRepo's doc comment
// for the exact path-depth math) while remote.Head/remote.Image asked for it
// under plain "<repo>", so this exact sequence returned BLOB_UNKNOWN /
// MANIFEST_UNKNOWN on a harness with the bug still present — every one of the
// four cross-repo-mount tests below depends on this working first, since
// three of them push freshly-built (non-mountable) layers that only ever
// travel this same chunked-upload path.
func TestMountAwareTestRegistry_RealWriteThenReadAgreeOnRepo(t *testing.T) {
	s, _, _ := newMountAwareTestRegistry(t)
	repo := s.Repo("app/write-then-read")

	img := randomImage(t)
	wantDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}

	ref, err := name.NewTag(repo+":v1", nameOptions(false)...)
	if err != nil {
		t.Fatalf("name.NewTag: %v", err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatalf("remote.Write: %v (this is exactly the failure mode the repoScopedBlobHandler key-normalization "+
			"fix exists to prevent: a real push storing blobs under a different key than reads look them up under)", err)
	}

	desc, err := remote.Head(ref)
	if err != nil {
		t.Fatalf("remote.Head after remote.Write against the same repo: %v", err)
	}
	if desc.Digest != wantDigest {
		t.Errorf("remote.Head digest = %s, want %s", desc.Digest, wantDigest)
	}

	pulled, err := remote.Image(ref)
	if err != nil {
		t.Fatalf("remote.Image after remote.Write against the same repo: %v", err)
	}
	gotDigest, err := pulled.Digest()
	if err != nil {
		t.Fatalf("pulled.Digest: %v", err)
	}
	if gotDigest != wantDigest {
		t.Errorf("pulled digest = %s, want %s", gotDigest, wantDigest)
	}

	// Read the layer body back too: normalizeBlobRepo must agree for every
	// blob a chunked upload writes, not merely for whichever blob happens to
	// answer HEAD/manifest requests correctly.
	layers, err := pulled.Layers()
	if err != nil {
		t.Fatalf("Layers: %v", err)
	}
	for i, l := range layers {
		rc, err := l.Compressed()
		if err != nil {
			t.Fatalf("layer %d Compressed: %v", i, err)
		}
		_, err = io.Copy(io.Discard, rc)
		rc.Close()
		if err != nil {
			t.Fatalf("layer %d: reading blob body back: %v", i, err)
		}
	}
}

// --- harness smoke test -------------------------------------------------
//
// This does not test the push feature itself (that's a separate follow-up
// task) — it only proves the harness genuinely gates on repo-scoped digest
// presence rather than always answering 201, which is the whole point of
// building repoScopedBlobHandler instead of reusing go-containerregistry's
// digest-only in-memory blob store.
func TestMountAwareTestRegistry_Harness(t *testing.T) {
	s, bh, _ := newMountAwareTestRegistry(t)

	content := []byte("mount-me-if-you-can")
	hash, _, err := v1.SHA256(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("v1.SHA256: %v", err)
	}
	// Seed the blob directly under "app/source", exactly as if a prior push
	// had already written it there.
	if err := bh.Put(context.Background(), "app/source", hash, io.NopCloser(bytes.NewReader(content))); err != nil {
		t.Fatalf("seed Put: %v", err)
	}

	t.Run("mount succeeds when digest exists under the source repo", func(t *testing.T) {
		url := s.URL + "/v2/app/mount-target/blobs/uploads/?mount=" + hash.String() + "&from=app/source"
		resp, err := http.Post(url, "", nil)
		if err != nil {
			t.Fatalf("POST mount: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status = %d, want %d (mount fast-path)", resp.StatusCode, http.StatusCreated)
		}
		if got := resp.Header.Get("Docker-Content-Digest"); got != hash.String() {
			t.Errorf("Docker-Content-Digest = %q, want %q", got, hash.String())
		}

		// The essential assertion: the blob must now be genuinely readable
		// from the TARGET repo, not just reported as mounted.
		size, err := bh.Stat(context.Background(), "app/mount-target", hash)
		if err != nil {
			t.Fatalf("target repo Stat after mount: %v", err)
		}
		if size != int64(len(content)) {
			t.Errorf("target repo blob size = %d, want %d", size, len(content))
		}
	})

	t.Run("mount falls through to normal upload-start when digest is absent from the source repo", func(t *testing.T) {
		neverPushed := "sha256:" + strings.Repeat("0", 64)
		url := s.URL + "/v2/app/other-target/blobs/uploads/?mount=" + neverPushed + "&from=app/never-populated"
		resp, err := http.Post(url, "", nil)
		if err != nil {
			t.Fatalf("POST mount: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("status = %d, want %d (normal upload-start fallback, proving the digest was NOT treated as present)", resp.StatusCode, http.StatusAccepted)
		}
		if loc := resp.Header.Get("Location"); loc == "" {
			t.Errorf("Location header empty; normal upload-start flow did not run")
		}

		missingHash, err := v1.NewHash(neverPushed)
		if err != nil {
			t.Fatalf("v1.NewHash: %v", err)
		}
		if _, err := bh.Stat(context.Background(), "app/other-target", missingHash); err == nil {
			t.Errorf("target repo has the blob after a failed mount; nothing should have been written")
		}
	})

	t.Run("digest present under an unrelated repo is not mistaken for presence under the requested source repo", func(t *testing.T) {
		// Same digest as the first subtest, seeded under "app/source" — but
		// this mount asks for it "from" a different, never-populated repo.
		// A digest-only blob store (like go-containerregistry's own
		// in-memory handler) would incorrectly say this digest is present
		// everywhere; repoScopedBlobHandler must not.
		url := s.URL + "/v2/app/yet-another-target/blobs/uploads/?mount=" + hash.String() + "&from=app/unrelated-repo"
		resp, err := http.Post(url, "", nil)
		if err != nil {
			t.Fatalf("POST mount: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("status = %d, want %d (must not mount a digest that isn't actually present under the requested source repo)", resp.StatusCode, http.StatusAccepted)
		}
	})
}

// --- mountObserver classification tests --------------------------------
//
// The tests below exercise mountObserver.RoundTrip / classify in complete
// isolation from any real registry: they hand it a fabricated
// http.RoundTripper that answers with whatever *http.Response the test case
// needs, and assert on mountStats.summary() afterwards. No network, no
// regharness server — the point is to pin the classification logic itself,
// independent of whether go-containerregistry's own retry/mount machinery
// happens to produce those exact request shapes in practice (that is what
// TestMountAwareTestRegistry_Harness and the push_test.go suite already
// cover end-to-end).

// fakeRoundTripper is a minimal, test-only http.RoundTripper: fn decides the
// response (or error) for every request, and calls counts how many times
// RoundTrip was invoked, guarded by mu so the "many goroutines" race test
// below is legitimately race-free rather than just lucky.
type fakeRoundTripper struct {
	mu    sync.Mutex
	calls int
	fn    func(call int, req *http.Request) (*http.Response, error)
}

func (f *fakeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	call := f.calls
	f.calls++
	f.mu.Unlock()
	return f.fn(call, req)
}

// fakeResponse builds a minimal *http.Response with the given status and
// body, suitable for handing back from a fakeRoundTripper. Header is
// non-nil because some callers (go-containerregistry's own transport code,
// were this ever exercised through it) assume a non-nil header map; a bare
// mountObserver test does not strictly need it, but a nil map is an easy
// footgun to avoid here for free.
func fakeResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// newMountPOST builds the exact request shape initiateUpload sends for a
// cross-repo blob mount attempt: "POST /v2/<targetRepo>/blobs/uploads/
// ?mount=<digest>&from=<sourceRepo>" (write.go:237-247).
func newMountPOST(t *testing.T, targetRepo, digest, sourceRepo string) *http.Request {
	t.Helper()
	u := "https://registry.example/v2/" + targetRepo + "/blobs/uploads/?mount=" + digest + "&from=" + sourceRepo
	req, err := http.NewRequest(http.MethodPost, u, nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	return req
}

// newBlobHEAD builds the exact request shape checkExistingBlob sends:
// "HEAD /v2/<repo>/blobs/<digest>" (write.go:210-229).
func newBlobHEAD(t *testing.T, repo, digest string) *http.Request {
	t.Helper()
	u := "https://registry.example/v2/" + repo + "/blobs/" + digest
	req, err := http.NewRequest(http.MethodHead, u, nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	return req
}

// TestMountObserver_MountAccepted_201_RecordsMounted covers case 1: a mount
// POST answered 201 Created must be reported as mounted.
func TestMountObserver_MountAccepted_201_RecordsMounted(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	base := &fakeRoundTripper{fn: func(call int, req *http.Request) (*http.Response, error) {
		return fakeResponse(http.StatusCreated, ""), nil
	}}
	o := &mountObserver{base: base, stats: &mountStats{}}

	resp, err := o.RoundTrip(newMountPOST(t, "app/target", digest, "app/source"))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()

	mounted, streamed, present := o.stats.summary()
	if mounted != 1 || streamed != 0 || present != 0 {
		t.Errorf("summary() = (mounted=%d, streamed=%d, present=%d), want (1, 0, 0)", mounted, streamed, present)
	}
}

// TestMountObserver_MountDeclined_202_RecordsStreamed covers case 2: a mount
// POST answered 202 Accepted (the registry declined the mount and the client
// is about to stream the blob instead) must be reported as streamed, per
// mount.go's classify doc comment.
func TestMountObserver_MountDeclined_202_RecordsStreamed(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	base := &fakeRoundTripper{fn: func(call int, req *http.Request) (*http.Response, error) {
		return fakeResponse(http.StatusAccepted, ""), nil
	}}
	o := &mountObserver{base: base, stats: &mountStats{}}

	resp, err := o.RoundTrip(newMountPOST(t, "app/target", digest, "app/source"))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()

	mounted, streamed, present := o.stats.summary()
	if mounted != 0 || streamed != 1 || present != 0 {
		t.Errorf("summary() = (mounted=%d, streamed=%d, present=%d), want (0, 1, 0)", mounted, streamed, present)
	}
}

// TestMountObserver_BlobHEAD_200_RecordsAlreadyPresent covers case 3: a blob
// existence-check HEAD answered 200 must be reported as already-present.
func TestMountObserver_BlobHEAD_200_RecordsAlreadyPresent(t *testing.T) {
	digest := "sha256:" + strings.Repeat("c", 64)
	base := &fakeRoundTripper{fn: func(call int, req *http.Request) (*http.Response, error) {
		return fakeResponse(http.StatusOK, ""), nil
	}}
	o := &mountObserver{base: base, stats: &mountStats{}}

	resp, err := o.RoundTrip(newBlobHEAD(t, "app/target", digest))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	resp.Body.Close()

	mounted, streamed, present := o.stats.summary()
	if mounted != 0 || streamed != 0 || present != 1 {
		t.Errorf("summary() = (mounted=%d, streamed=%d, present=%d), want (0, 0, 1)", mounted, streamed, present)
	}
}

// TestMountObserver_RetryDedup_LastWriteWins covers case 4: mountStats.record
// is documented as last-write-wins, keyed on digest, precisely so a retried
// mount POST for the same blob converges on a single verdict instead of
// double-counting. This test drives the same digest through two mount POSTs
// with different outcomes, in both orders, and asserts the final summary
// reflects only the second attempt.
func TestMountObserver_RetryDedup_LastWriteWins(t *testing.T) {
	t.Run("declined-then-mounted ends up mounted, not double-counted", func(t *testing.T) {
		digest := "sha256:" + strings.Repeat("d", 64)
		responses := []*http.Response{
			fakeResponse(http.StatusInternalServerError, ""), // first attempt: registry errors, go-containerregistry would retry
			fakeResponse(http.StatusCreated, ""),             // retry: mount succeeds
		}
		base := &fakeRoundTripper{fn: func(call int, req *http.Request) (*http.Response, error) {
			return responses[call], nil
		}}
		o := &mountObserver{base: base, stats: &mountStats{}}

		for i := 0; i < 2; i++ {
			resp, err := o.RoundTrip(newMountPOST(t, "app/target", digest, "app/source"))
			if err != nil {
				t.Fatalf("RoundTrip call %d: %v", i, err)
			}
			resp.Body.Close()
		}

		mounted, streamed, present := o.stats.summary()
		if mounted != 1 || streamed != 0 || present != 0 {
			t.Errorf("summary() = (mounted=%d, streamed=%d, present=%d), want (1, 0, 0) — last write (201) must win, not accumulate as 1 mounted + 1 declined", mounted, streamed, present)
		}
	})

	t.Run("mounted-then-declined ends up declined, confirming genuine last-write-wins", func(t *testing.T) {
		digest := "sha256:" + strings.Repeat("e", 64)
		responses := []*http.Response{
			fakeResponse(http.StatusCreated, ""),  // first attempt: mount succeeds
			fakeResponse(http.StatusAccepted, ""), // a second POST for the same digest is later declined
		}
		base := &fakeRoundTripper{fn: func(call int, req *http.Request) (*http.Response, error) {
			return responses[call], nil
		}}
		o := &mountObserver{base: base, stats: &mountStats{}}

		for i := 0; i < 2; i++ {
			resp, err := o.RoundTrip(newMountPOST(t, "app/target", digest, "app/source"))
			if err != nil {
				t.Fatalf("RoundTrip call %d: %v", i, err)
			}
			resp.Body.Close()
		}

		mounted, streamed, present := o.stats.summary()
		if mounted != 0 || streamed != 1 || present != 0 {
			t.Errorf("summary() = (mounted=%d, streamed=%d, present=%d), want (0, 1, 0) — the LAST write (202) must win, proving this is genuinely last-write-wins and not first-write-wins or sticky-mounted", mounted, streamed, present)
		}
	})
}

// TestMountObserver_DoesNotConsumeResponseBody covers case 5: mountObserver
// is documented as never touching resp.Body — reading so much as a byte of
// it here would corrupt the response for go-containerregistry's real
// consumer downstream. This test proves the body is still fully and
// correctly readable by the caller after RoundTrip returns.
func TestMountObserver_DoesNotConsumeResponseBody(t *testing.T) {
	const wantBody = `{"errors":[{"code":"MOUNT_INVALID"}]}`
	digest := "sha256:" + strings.Repeat("f", 64)
	base := &fakeRoundTripper{fn: func(call int, req *http.Request) (*http.Response, error) {
		return fakeResponse(http.StatusAccepted, wantBody), nil
	}}
	o := &mountObserver{base: base, stats: &mountStats{}}

	resp, err := o.RoundTrip(newMountPOST(t, "app/target", digest, "app/source"))
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading resp.Body after RoundTrip: %v", err)
	}
	if string(got) != wantBody {
		t.Errorf("resp.Body after RoundTrip = %q, want %q (observer must not consume/alter the body)", got, wantBody)
	}
}

// TestMountObserver_ConcurrentRoundTrips_RaceFree covers case 6: many
// goroutines firing RoundTrip through the SAME mountObserver concurrently,
// for distinct digests, must produce a summary consistent with every
// individual outcome and must not trip `go test -race`. mountStats' own doc
// comment says every access goes through its mutex specifically so this
// holds; this test is what actually exercises that under -race rather than
// just trusting the comment.
func TestMountObserver_ConcurrentRoundTrips_RaceFree(t *testing.T) {
	const n = 200

	base := &fakeRoundTripper{fn: func(call int, req *http.Request) (*http.Response, error) {
		// Alternate outcomes deterministically by request shape so the
		// expected summary is computable up front, rather than merely
		// "doesn't crash."
		q := req.URL.Query()
		digest := q.Get("mount")
		// Parse errors must surface as a transport error rather than being
		// swallowed into idx == 0: silently defaulting would send every
		// request down the 201 branch and turn a broken fixture into a
		// summary mismatch with no explanation.
		idx, err := strconv.Atoi(strings.TrimPrefix(digest, "sha256:concurrent"))
		if err != nil {
			return nil, fmt.Errorf("test fixture: unparseable mount digest %q: %w", digest, err)
		}
		switch idx % 2 {
		case 0:
			return fakeResponse(http.StatusCreated, ""), nil
		default:
			return fakeResponse(http.StatusAccepted, ""), nil
		}
	}}
	o := &mountObserver{base: base, stats: &mountStats{}}

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			digest := fmt.Sprintf("sha256:concurrent%064d", i)
			req := newMountPOST(t, "app/target", digest, "app/source")
			resp, err := o.RoundTrip(req)
			if err != nil {
				t.Errorf("RoundTrip(%d): %v", i, err)
				return
			}
			resp.Body.Close()
		}(i)
	}
	wg.Wait()

	mounted, streamed, present := o.stats.summary()
	if present != 0 {
		t.Errorf("present = %d, want 0", present)
	}
	if mounted+streamed != n {
		t.Errorf("mounted+streamed = %d, want %d (one outcome per distinct digest, none lost or duplicated)", mounted+streamed, n)
	}
	// n is even, so exactly half of the digests (idx%2==0) landed on 201.
	wantMounted := n / 2
	if mounted != wantMounted {
		t.Errorf("mounted = %d, want %d", mounted, wantMounted)
	}
	if streamed != n-wantMounted {
		t.Errorf("streamed = %d, want %d", streamed, n-wantMounted)
	}
}

// --- remoteConfig.Jobs wiring tests --------------------------------------
//
// remoteOptions must append remote.WithJobs(cfg.Jobs) only when Jobs > 0:
// remote.WithJobs itself panics/errors on a non-positive value if applied
// (see the Jobs field doc on remoteConfig in registry.go), so Jobs: 0 must
// never reach it. The two tests below prove both branches actually work
// end-to-end against a real remote.Write/remote.Head call, rather than just
// inspecting the []remote.Option slice — the interesting bug this guards
// against is a push failing at runtime because WithJobs(0) slipped through.

// TestRemoteOptions_JobsZero_OmittedAndWriteSucceeds proves Jobs: 0 does not
// cause remote.Write to fail — i.e. remote.WithJobs(0) was correctly omitted
// from the option set rather than passed through and rejected.
func TestRemoteOptions_JobsZero_OmittedAndWriteSucceeds(t *testing.T) {
	s, _ := newTestRegistry(t)
	repo := registryRepo(t, s, "app/jobs-zero")

	opts, err := remoteOptions(t.Context(), remoteConfig{Jobs: 0})
	if err != nil {
		t.Fatalf("remoteOptions: %v", err)
	}

	ref, err := name.NewTag(repo+":v1", nameOptions(false)...)
	if err != nil {
		t.Fatalf("name.NewTag: %v", err)
	}

	img := randomImage(t)
	if err := remote.Write(ref, img, opts...); err != nil {
		t.Fatalf("remote.Write with Jobs=0: %v", err)
	}

	wantDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	desc, err := remote.Head(ref, opts...)
	if err != nil {
		t.Fatalf("remote.Head after write: %v", err)
	}
	if desc.Digest != wantDigest {
		t.Errorf("pushed digest = %s, want %s", desc.Digest, wantDigest)
	}
}

// TestRemoteOptions_JobsPositive_AppliedAndWriteSucceeds proves a positive
// Jobs value doesn't break a real push either. It cannot directly introspect
// that remote.WithJobs(4) was the option actually applied — remote.Option is
// an opaque functional option — so a successful real write/round-trip is the
// available proof that passing it through didn't regress anything.
func TestRemoteOptions_JobsPositive_AppliedAndWriteSucceeds(t *testing.T) {
	s, _ := newTestRegistry(t)
	repo := registryRepo(t, s, "app/jobs-positive")

	opts, err := remoteOptions(t.Context(), remoteConfig{Jobs: 4})
	if err != nil {
		t.Fatalf("remoteOptions: %v", err)
	}

	ref, err := name.NewTag(repo+":v1", nameOptions(false)...)
	if err != nil {
		t.Fatalf("name.NewTag: %v", err)
	}

	img := randomImage(t)
	if err := remote.Write(ref, img, opts...); err != nil {
		t.Fatalf("remote.Write with Jobs=4: %v", err)
	}

	wantDigest, err := img.Digest()
	if err != nil {
		t.Fatalf("Digest: %v", err)
	}
	desc, err := remote.Head(ref, opts...)
	if err != nil {
		t.Fatalf("remote.Head after write: %v", err)
	}
	if desc.Digest != wantDigest {
		t.Errorf("pushed digest = %s, want %s", desc.Digest, wantDigest)
	}
}
