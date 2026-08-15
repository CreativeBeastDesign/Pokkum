# Lessons

Post-mortems for bugs caught during self-review or debugging, with the
preventative rule each one produced. Newest entries first.

---

## 2026-08-15 — The 4-step verification suite does not run `golangci-lint`, so a CI-breaking `errcheck` finding survived every "green" report

**Where:** `internal/adapters/registry/mount_test.go`,
`TestMountObserver_ConcurrentRoundTrips_RaceFree` (caught during the final
adversarial review gate, after the feature had already been reported as
verified).

**What happened:** The concurrency test derived each fake response's outcome
from the request's digest via `fmt.Sscanf(..., "%d", &idx)`, discarding the
returned error. `gofmt`, `go vet`, `go build` and `go test ./internal/...
-race` all pass on that line, so every step of `CLAUDE.md` §5's verification
suite reported green — but `.golangci.yml` enables `errcheck`, its
`_test\.go$` exclusion covers only `gosec`/`staticcheck`/`revive`, and
`.github/workflows/ci.yml` runs `golangci-lint run ./...` on every push. The
change would have failed CI on the first run.

**Root cause:** `fmt.Sscanf`'s error was ignored because the happy path was
"obviously" fine — the digests are generated two lines away by the same test.
That reasoning is correct about behavior and irrelevant to the lint gate,
which is what actually blocks the merge.

**Faulty assumption:** that "the CLAUDE.md verification suite is green" is the
same claim as "this change is mergeable." It isn't: the suite covers
formatting, vet, build and tests, and deliberately says nothing about the
linters CI additionally enforces. `HEAD~3` (`chore: fix lint findings…`)
exists precisely because this gap has been walked into before.

**Fix:** Replaced `fmt.Sscanf` with `strconv.Atoi` and returned a transport
error on a parse failure, so an unparseable fixture surfaces as a named
`RoundTrip` error instead of silently defaulting `idx` to 0 (which would have
sent every request down the 201 branch and produced an unexplained summary
mismatch). `golangci-lint run ./...` is now clean repo-wide.

**Preventative rule:** Run `make lint` (or `golangci-lint run ./...`) as a
fifth step alongside `CLAUDE.md` §5's four, before declaring any code change
complete — especially for new `_test.go` files, which people assume are
lint-exempt and which this repo's config only partially exempts. Never report
"verification suite passed" as a proxy for "CI will pass" when CI runs gates
the suite does not.

---

## 2026-08-15 — Found the same bare-`&http.Transport{}` anti-pattern in 3 places; deliberately fixed only 1 to keep diff scoped

**Where:** `internal/adapters/registry/registry.go` (fixed), `internal/adapters/baseimage/resolver.go:92` (not fixed), `internal/adapters/remotecacheutils/remotecacheutils.go:432, 725, 766` (not fixed).

**What happened:** While fixing HTTP/2 negotiation on the insecure-TLS path in `registry.go`, a search for `&http.Transport{` literals revealed the same pattern — a bare struct literal instead of cloning from `remote.DefaultTransport` — in three other locations in the codebase.

**Why not fixed:** `resolver.go`'s `insecureTransport` and `remotecacheutils.go`'s three inline `remote.WithTransport(&http.Transport{...})` calls were identified but deliberately left unmodified to keep this task's scope tight. The `registry.go` change (which is on the critical push path) was the priority; the other two modules (base image resolution and cache/pull operations) are separate concerns with different risk profiles.

**Preventative rule:** When a code search discovers the same anti-pattern in multiple places, do not assume "finding one means fixing them all" or vice versa. Be explicit in the code review / task plan about which instances are in-scope and why, so a future maintainer (yourself in 6 months) does not think "we fixed this" means "it's fixed everywhere."

---

## 2026-08-15 — Upstream's own repo-path math splits "reads" and "chunked-upload writes" into different key shapes; a repo-scoped test double must normalize both onto one key

**Where:** `internal/adapters/registry/mount_test.go`, `repoScopedBlobHandler`
(the in-memory `(repo, digest)`-keyed blob store backing
`newMountAwareTestRegistry`, used by `push_test.go`'s cross-repo-mount
integration tests).

**What happened:** A prior task flagged, but did not fix, that a real
`remote.Write`-driven push against this harness would store a blob under a
different key than any subsequent read of that same blob would look it up
under — meaning every non-mounted layer (freshly built layers, and the image
config, which is *always* a plain blob) would appear to vanish (`BLOB_UNKNOWN`)
on the very next `remote.Head`/`remote.Image` call. This task's job depended
on that being fixed first, since three of the four planned integration tests
push at least one non-mountable blob.

**Root cause:** go-containerregistry's own in-memory registry
(`pkg/registry/blobs.go`, `blobs.handle`) computes the repo string once per
request as `req.URL.Host + path.Join(elem[1:len(elem)-2]...)` — trimming
exactly the *last two* path segments before rejoining the rest. That produces
the correct repo only when a request's final two segments are
`blobs/<digest-or-"uploads">`, which holds for every read (`GET`/`HEAD`) and
for the mount-initiation POST (`.../blobs/uploads/`, no id yet). The chunked
upload's `PATCH`/`PUT` requests (`streamBlob`/`commitBlob` in
`pkg/v1/remote/write.go`) instead hit `.../blobs/uploads/<id>` — one segment
deeper — so trimming the same "last two" leaves the literal segment `"blobs"`
inside the joined repo string. Every real streamed blob therefore lands under
`"<repo>/blobs"` while every read asks for `"<repo>"`.

**Faulty assumption (in the harness, not this task):** that a single `repo`
string received by a `BlobHandler` implementation is already normalized and
safe to use as a map key verbatim, regardless of which HTTP verb produced it.
It isn't — upstream's *own* path arithmetic is verb-shape-dependent, which is
easy to miss because `isBlob()` (the *routing* predicate, same file) correctly
handles both shapes; only the separate `repo :=` line does not.

**Fix:** Added `normalizeBlobRepo(repo string) string { return
strings.TrimSuffix(repo, "/blobs") }`, applied at the top of
`repoScopedBlobHandler`'s `Get`/`Stat`/`Put`. This is a no-op for the
already-correct shapes (they never end in the literal segment `"blobs"`), so
it unifies both call shapes onto the one true repo key without needing to
know which code path a given call came from. Verified with a regression test
(`TestMountAwareTestRegistry_RealWriteThenReadAgreeOnRepo`) that fails with
`BLOB_UNKNOWN` when the normalization is reverted, and passes with it in
place.

**Preventative rule:** When a test double receives a value that a *third
party's* routing code derived from a URL path via positional slicing (not a
documented, stable API), do not trust that the same logical value comes out
identically shaped across every HTTP verb that routes through it. Grep the
real implementation for every place the value is computed/reused, not just
the one call site the bug report points at — and write the round-trip
regression test (`write` via the real client path, then `read` via the real
client path, against the same identifier) before writing any test that
*depends* on that round trip working, since it is the cheapest possible proof
and pins the fix independently of every higher-level test built on top of it.

---

## 2026-08-15 — A "mount was declined, so the target should behave exactly like an ordinary push" assumption ignored that a *pulled* `MountableLayer`'s bytes are fetched lazily from its origin

**Where:** `internal/adapters/registry/push_test.go`,
`TestPush_CrossRepoMount_CrossRegistryRejected` (first draft, caught before
being reported as passing).

**What happened:** The test's first draft asserted that the *source*
registry (server A) must observe **zero** requests while pushing a composed
image to the *target* registry (server B), reasoning that "the client only
ever talks to the registry it's pushing to." That assertion failed on the
very first run: server A recorded one `GET .../blobs/<digest>` during the
push to server B.

**Root cause:** The composed image's mountable layer was obtained via
`remote.Get(refToServerA).Image()`, which wraps every layer in
go-containerregistry's `mountableImage`/`MountableLayer` — but the underlying
`v1.Layer` those wrap is still a *remote, lazily-read* layer: its
`Compressed()`/`Uncompressed()` readers stream from whichever registry it was
pulled from, on demand, rather than buffering the full blob into memory at
pull time. When server B declines the mount, go-containerregistry's
`streamBlob` calls that same lazy `Compressed()` to get bytes to `PATCH` to
server B — and that call has no choice but to reach back out to server A,
the layer's only actual data source. This is not a leak or a bug in
production code; it is the only way a "mount declined, fall back to a normal
stream" path *can* work for content the process never materialized locally in
the first place.

**Faulty assumption:** That "cross-host mount was attempted and declined" and
"the source registry sees zero traffic" were the same claim. They are not —
the correct claim is narrower: the source registry must see no *mount-shaped*
request (no `POST .../blobs/uploads/` — mounting is inherently a
target-registry-only operation), but it will legitimately see reads whenever
the fallback path needs bytes it doesn't already hold.

**Fix:** Replaced the blanket "zero requests" assertion with two precise
ones: (1) no `POST` of any kind reaches server A during the push, and (2) a
`GET` for the specific base-layer digest *does* reach server A, and is
treated as further positive proof of the decline (a successful mount would
require no such read at all, per the sibling
`TestPush_CrossRepoMount_ZeroEgress`, where no such `GET` occurs).

**Preventative rule:** When asserting "no cross-talk between two systems"
in a test, name precisely which *kind* of interaction must be absent (here:
mount-initiation requests) rather than asserting a system is silent overall —
a lazily-evaluated dependency (a remote-backed `io.Reader`, a pull-through
cache, a deferred fetch) can make "silent overall" both false and irrelevant
to the property actually under test. Read what the object you're wrapping
(`*remote.MountableLayer` here) is actually backed by before asserting an
absence of activity on its backing store.

---

## 2026-08-15 — `http.Transport.Clone()` mutates its receiver, so "clone equals unmodified copy" is not a safe test assertion

**Where:** `internal/adapters/registry/registry_test.go`,
`TestTransports_PreserveRemoteDefaultTransportTuning` (regression test for
`defaultTransport` / `insecureTransport` in `registry.go`).

**What happened:** While writing a test to assert that `defaultTransport`
(`cloneDefaultTransport(nil)`) has a `nil` `TLSClientConfig` — i.e. "an
unmodified clone of `remote.DefaultTransport`" — the assertion failed even on
a **freshly-called, first-ever** `cloneDefaultTransport(nil)`, before any
network request had been sent by anything in the test binary.

**Root cause:** `net/http`'s `(*Transport).Clone()` is not a pure copy. Its
first line is:

```go
func (t *Transport) Clone() *Transport {
	t.nextProtoOnce.Do(t.onceSetNextProtoDefaults)
	...
}
```

`onceSetNextProtoDefaults` runs **on the receiver `t`** — the transport being
cloned, not the clone — and, when `ForceAttemptHTTP2` is set (true for
`remote.DefaultTransport`), it lazily allocates a `TLSClientConfig` with
`NextProtos: ["h2", "http/1.1"]` if one isn't already set. `Clone()` then
copies that now-populated config onto the new `*Transport` via
`t2.TLSClientConfig = t.TLSClientConfig.Clone()`.

Consequence: the very first call to `.Clone()` on `remote.DefaultTransport` —
which happens unconditionally at `registry` package init, via
`var defaultTransport = cloneDefaultTransport(nil)` — permanently mutates the
shared `remote.DefaultTransport` singleton itself, giving it a non-nil
`TLSClientConfig` from that point forward. "The clone's `TLSClientConfig` is
nil" is therefore not just order-dependent on test execution — it is **never
true**, not even on the first call, because the mutation happens inside
`Clone()` before the copy is made.

**Faulty assumption:** I assumed `.Clone()` on an `http.Transport` behaves
like a value copy with no side effects on the source — reasonable for most
Go structs, wrong for `http.Transport` specifically because of its lazy
HTTP/2 self-configuration.

**Fix:** Replaced the "`TLSClientConfig == nil`" assertion with the
invariant that actually matters for correctness: `defaultTransport`'s
`TLSClientConfig`, whether nil or lazily populated, must never carry
`InsecureSkipVerify: true`. `insecureTransport`'s must always carry it.
Those two properties are stable regardless of `Clone()`'s side effect and
regardless of test execution order within the binary.

**Preventative rule:** When writing a test that inspects the *shape* of a
`*http.Transport` produced via `.Clone()`, do not assert on fields that
`onceSetNextProtoDefaults` can populate lazily (`TLSClientConfig`,
`TLSNextProto`) unless the assertion tolerates that populated state. Assert
on the security- or behavior-relevant *content* of those fields
(`InsecureSkipVerify`, proxy/idle-pool tuning) instead of their nil-ness.
More generally: before asserting "X is an unmodified copy of Y" for any
stdlib type with caching/once-init behavior, check whether the copy
operation itself (`Clone()`, `Do()`, etc.) has documented side effects on the
source — `go doc` and reading the stdlib source directly settled this in
under five minutes and would have prevented writing the wrong assertion in
the first place.
