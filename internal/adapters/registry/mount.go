package registry

import (
	"net/http"
	"strings"
	"sync"
)

// mountOutcome classifies what actually happened to a single blob during one
// push: it was linked from another repository, skipped as already present, or
// its bytes went over the wire.
type mountOutcome int

const (
	// outcomeStreamed means the blob's bytes were uploaded to the target
	// repository. This is the outcome recorded when a cross-repository mount
	// was attempted and the registry declined it, because go-containerregistry
	// falls back to a normal upload in exactly that case.
	outcomeStreamed mountOutcome = iota

	// outcomeMounted means the registry accepted a cross-repository mount
	// (201 Created on the upload-initiation POST), so the blob was linked from
	// another repository and no bytes were transferred.
	outcomeMounted

	// outcomeAlreadyPresent means the blob was already in the target
	// repository, so go-containerregistry skipped it without either uploading
	// or mounting it.
	outcomeAlreadyPresent
)

// mountStats records the final outcome per blob digest for a single push.
//
// The zero value is ready to use: record allocates the map lazily while
// holding the lock, so a caller can simply take the address of a local
// mountStats.
//
// # Ownership
//
// One mountStats belongs to exactly one Push call and must never be shared
// between pushes. Adapter is documented as safe for concurrent use precisely
// because it holds no mutable state of its own; hanging a stats object off the
// Adapter would break that, and would also interleave the blob outcomes of
// concurrent pushes into one indistinguishable set.
//
// Within a single push the map is written from every layer-upload goroutine
// remote.Write spawns (up to remote.WithJobs of them), so every access — read
// and write alike — goes through mu. summary takes the same lock, but a
// meaningful snapshot additionally requires that the caller has already
// observed remote.Write return: only then are all of that push's requests
// guaranteed to have completed.
type mountStats struct {
	mu  sync.Mutex
	res map[string]mountOutcome
}

// record stores outcome as the current verdict for digest, replacing any
// earlier verdict for the same digest.
//
// Last-write-wins is the point of keying on the digest rather than
// incrementing counters. go-containerregistry retries a failed layer upload,
// and its retry re-runs the whole existence-check/mount/stream sequence for
// that same blob; counters would double-count every retried layer, whereas a
// map converges on whatever the last attempt actually did. The one nuance is
// that a layer which streamed successfully but failed afterwards (say, on the
// commit) and then got retried will be seen as already-present on the retry's
// existence check, and reported as such — the bytes did move, but they moved
// on a superseded attempt, and reporting the state the registry ended up in is
// the more useful of the two readings.
//
// A nil receiver and an empty digest are both no-ops, so callers never have to
// guard the call.
func (s *mountStats) record(digest string, outcome mountOutcome) {
	if s == nil || digest == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.res == nil {
		s.res = make(map[string]mountOutcome)
	}
	s.res[digest] = outcome
}

// summary returns how many distinct blob digests ended in each outcome.
//
// Counts are per digest, not per request: a blob whose upload was retried
// several times contributes exactly one to exactly one bucket.
//
// Call this only once the push whose blobs it describes has finished (i.e.
// after remote.Write/remote.WriteIndex has returned). The lock makes the read
// race-free at any moment, but it cannot make an early read complete.
func (s *mountStats) summary() (mounted, streamed, alreadyPresent int) {
	if s == nil {
		return 0, 0, 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.res {
		switch o {
		case outcomeMounted:
			mounted++
		case outcomeStreamed:
			streamed++
		case outcomeAlreadyPresent:
			alreadyPresent++
		}
	}
	return mounted, streamed, alreadyPresent
}

// mountObserver is a purely observational http.RoundTripper: it forwards every
// request to base untouched and classifies the registry's answer into stats on
// the way back. It never modifies, buffers, retries or short-circuits
// anything, and it never touches resp.Body — the body belongs to
// go-containerregistry, and reading so much as a byte of it here would corrupt
// the response for the real consumer.
//
// # Why sniff the transport at all
//
// go-containerregistry does not report per-blob push outcomes through any
// exported API, and the two obvious alternatives were both evaluated and
// rejected:
//
//   - remote.WithProgress reports byte counts only (v1.Update{Complete,
//     Total}). uploadOne calls incrProgress with the layer's full size on the
//     mounted path, on the already-present path and after a real stream alike
//     (pkg/v1/remote/write.go), so the three cases are literally
//     indistinguishable from the progress channel — a fully mounted push looks
//     exactly like a fully streamed one.
//
//   - pkg/logs.Progress does distinguish them ("mounted blob", "existing
//     blob", "pushed blob"), but those are package-level *log.Logger vars.
//     Redirecting them is process-global mutation: the output cannot be
//     attributed to a particular Push call, two concurrent pushes would
//     interleave into the same sink, and swapping the var while any other
//     goroutine may log through it is a data race that `go test -race` would
//     be right to flag. Parsing English log strings for structured data would
//     be a second, independent problem.
//
// Sniffing the wire has neither drawback: an observer is created per push, so
// its stats are call-scoped by construction, and the classification keys off
// the HTTP protocol itself, which the OCI distribution spec pins down far more
// firmly than any of go-containerregistry's internal log strings.
//
// # Sharing the base transport
//
// base is one of this package's package-level transports, never a freshly
// built one. Constructing a transport per push would discard the connection
// pool those vars exist to preserve (see defaultTransport), which would cost
// far more than the mount accounting saves. Wrapping is cheap precisely
// because mountObserver holds nothing but two pointers.
type mountObserver struct {
	base  http.RoundTripper
	stats *mountStats
}

var _ http.RoundTripper = (*mountObserver)(nil)

// RoundTrip implements http.RoundTripper.
func (o *mountObserver) RoundTrip(req *http.Request) (*http.Response, error) {
	base := o.base
	if base == nil {
		base = http.DefaultTransport
	}

	resp, err := base.RoundTrip(req)
	if err != nil || resp == nil {
		// Nothing to classify, and nothing to conclude: go-containerregistry
		// retries transport failures, and the retry is what will produce the
		// outcome worth recording.
		return resp, err
	}

	o.classify(req, resp.StatusCode)
	return resp, err
}

// classify records the outcome, if any, that the (request, status) pair
// implies. It reads only the request's method, path and query plus the status
// code — deliberately never the response body.
//
// The two shapes it recognises are the two requests uploadOne issues per layer
// (pkg/v1/remote/write.go):
//
//   - checkExistingBlob, "HEAD /v2/<repo>/blobs/<digest>", where 200 means the
//     blob is already in the target repository and the layer is skipped
//     outright (write.go:210-229, path built at :211; the skip is write.go:397-405).
//   - initiateUpload, "POST /v2/<repo>/blobs/uploads/?mount=<digest>&from=<repo>",
//     where 201 Created means the registry mounted the blob and anything else
//     means the client proceeds to stream it (write.go:237-288; the mount
//     params are set at :240-247 and only ever set together, and the
//     201-vs-202 split is decided at :277-287).
//
// Everything else on the wire — manifest HEADs and PUTs, token exchanges, the
// PATCH/PUT of the blob body itself — passes unrecorded. Matching on the
// path's structure rather than on a substring is what keeps the manifest HEAD
// that Push issues ("HEAD /v2/<repo>/manifests/<digest>", same digest-shaped
// final segment) from being misread as a blob existence check.
//
// # Known gap: layers that are never mount candidates
//
// outcomeStreamed is recorded only for a mount that was attempted and
// declined. A layer with no mount candidate at all — anything built locally
// rather than fetched from another repository — produces no mount attempt to
// observe: uploadOne only sets the mount/from pair for a *remote.MountableLayer
// (write.go:409-423), and initiateUpload only puts them in the query when both
// are non-empty (write.go:240-247). Such a layer is therefore invisible here,
// and a first-ever push of freshly built layers records nothing whatsoever.
//
// This is a limit of the wire, not an oversight. The param-less upload-start
// POST carries no digest anywhere in its URL, so there is nothing to key the
// map on; the digest only reappears on the commit PUT
// ("PUT <upload-location>?digest=<digest>", write.go:353-375), which is
// currently left unclassified.
//
// The consequence matters for whoever renders these numbers: "streamed" means
// "mount attempted and refused", NOT "bytes uploaded", and mounted=0
// streamed=0 alreadyPresent=0 is the normal, correct reading for a push of
// all-local layers. Report the counts in those terms, or extend classify to
// the commit PUT first.
func (o *mountObserver) classify(req *http.Request, status int) {
	if req == nil || req.URL == nil {
		return
	}

	switch req.Method {
	case http.MethodPost:
		if !isBlobUploadsPath(req.URL.Path) {
			return
		}
		q := req.URL.Query()
		mount, from := q.Get("mount"), q.Get("from")
		if mount == "" || from == "" {
			// A plain upload-start, including the mount-less retry
			// initiateUpload falls back to. Not a mount attempt; the streamed
			// verdict for this digest was already recorded by the attempt that
			// triggered the fallback.
			return
		}
		if status == http.StatusCreated {
			o.stats.record(mount, outcomeMounted)
			return
		}
		// 202 Accepted (upload begun), or an error status that sends
		// initiateUpload down its retry-without-mount path: either way the
		// bytes are about to go over the wire.
		o.stats.record(mount, outcomeStreamed)

	case http.MethodHead:
		if status != http.StatusOK {
			// 404 is the ordinary "not there yet" answer, and says nothing
			// about how the blob will subsequently arrive.
			return
		}
		digest, ok := blobDigestFromPath(req.URL.Path)
		if !ok {
			return
		}
		o.stats.record(digest, outcomeAlreadyPresent)
	}
}

// isBlobUploadsPath reports whether p is a blob-upload-initiation path,
// "/v2/<repo>/blobs/uploads/". The repository segment is variable in both
// length and content, so the test is on the final two segments, mirroring the
// path arithmetic go-containerregistry's own registry implementation uses to
// route these requests (pkg/registry/blobs.go).
func isBlobUploadsPath(p string) bool {
	elem := strings.Split(strings.Trim(p, "/"), "/")
	if len(elem) < 2 {
		return false
	}
	return elem[len(elem)-2] == "blobs" && elem[len(elem)-1] == "uploads"
}

// blobDigestFromPath returns the digest from a blob path,
// "/v2/<repo>/blobs/<digest>", reporting false for any other shape.
//
// The final segment must look like "<algorithm>:<encoded>". That check is
// deliberately weaker than v1.NewHash, which additionally rejects any
// algorithm other than sha256 and enforces its exact hex length: this is
// observability, so mis-parsing an exotic-but-valid digest into silence would
// be a worse failure than carrying an unexpected digest string through to a
// count.
func blobDigestFromPath(p string) (string, bool) {
	elem := strings.Split(strings.Trim(p, "/"), "/")
	if len(elem) < 2 {
		return "", false
	}
	if elem[len(elem)-2] != "blobs" {
		return "", false
	}
	digest := elem[len(elem)-1]
	algo, encoded, ok := strings.Cut(digest, ":")
	if !ok || algo == "" || encoded == "" {
		return "", false
	}
	return digest, true
}
