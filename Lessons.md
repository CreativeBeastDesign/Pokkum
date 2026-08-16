# Lessons

Post-mortems for bugs caught during self-review or debugging, with the
preventative rule each one produced. Newest entries first.

---

## 2026-08-16 — `core.Build`'s `Normalize()` pre-defaults `Runtime.Entrypoint` to the exe shape before the strategy is known, so `StrategyLayered` images built through the real pipeline get an unrunnable entrypoint

**Category:** boundary (strategy-dependent default computed before the strategy-aware code path runs)

**Where:** `internal/core/model.go:688` (`BuildRequest.Normalize` calls `r.Runtime = r.Runtime.WithDefaults()` unconditionally, before per-platform strategy dispatch); `internal/ports/packager.go:212-232` (`RuntimeConfig.WithDefaults` defaults `Entrypoint` to `ports.DefaultEntrypoint()` — the StrategyExe shape — whenever it's nil, with no knowledge of `Compile.Strategy`); `internal/adapters/packager/packager.go:150-153` (the StrategyLayered branch's own default, `if req.Runtime.Entrypoint == nil { req.Runtime.Entrypoint = ports.DefaultLayeredEntrypoint() }`, is a dead guard by the time it runs, because `Normalize()` already claimed the nil).

**What happened:** `tests/integration/strategy_e2e_test.go`'s new `TestFixtureDrivenE2E_AllStrategies` — the first test in the repo to drive the full `core.Build` pipeline (not `packager.Build` directly) with `Compile.Strategy = StrategyLayered` and assert on `Config.Entrypoint` — failed: the pushed image's `Config.Entrypoint` was `["/pokkum/init", "--", "/app/server"]` (the StrategyExe shape) instead of `["/pokkum/init", "--", "/usr/local/bin/bun", "/app/server/index.js"]` (`ports.DefaultLayeredEntrypoint()`). The layer *contents* were correct (8 layers: base, bun, supervisor, server, client, vendor, native, prerendered — matching `internal/adapters/packager/packager_strategy_test.go`'s `TestBuild_StrategyDispatch/layered` exactly), proving the layer-building dispatch works; only the entrypoint dispatch is broken. `cmd/pokkum/build.go` never sets `Runtime:` on its `core.BuildRequest` for any strategy, so this is not a test-only artifact — it is the exact code path a real `pokkum build --strategy layered` invocation takes.

**Root cause:** `RuntimeConfig.WithDefaults()` was written as if every field it defaults (User, WorkingDir, Port, ProbePort, ShutdownTimeout, Entrypoint) is strategy-independent. Entrypoint isn't — its correct default depends on `Compile.Strategy`, which `WithDefaults()` has no access to and `Normalize()` calls it without knowing. Separately, `packager.Build`'s StrategyLayered branch was written assuming it would be the *first* code to see the request's `Runtime.Entrypoint`, so a nil-check was "good enough" — nobody traced the call chain back far enough to see `core.Build`'s `Normalize()` (pipeline.go:273) already ran `WithDefaults()` on the same struct earlier in the same request lifecycle. Only the static branch survives, and only by accident: it unconditionally overwrites `rc.Entrypoint` rather than checking for nil.

**Impact (uncaught until this test):** any real `pokkum build --strategy layered` run would ship an image whose entrypoint execs `/app/server` directly — but StrategyLayered packages `/app/server` as a *directory* (containing `index.js`), not an executable. The container would fail to start (`exec: is a directory`) on every run. No prior test caught this because every existing StrategyLayered test either constructs `ports.PackageRequest` directly (bypassing `core.Build`'s `Normalize()` entirely — see `packager_strategy_test.go`) or never asserted on `Config.Entrypoint` at the `core.Build` level.

**Fix:** `internal/adapters/packager/packager.go`'s StrategyLayered branch now sets `req.Runtime.Entrypoint = ports.DefaultLayeredEntrypoint()` unconditionally, dropping the nil-guard that `Normalize()`'s earlier pass could pre-empt — mirroring the static branch's existing unconditional overwrite. `RuntimeConfig.WithDefaults()` itself was left untouched (StrategyExe still legitimately relies on its generic `Entrypoint` default, and no code path anywhere sets a custom entrypoint that this could clobber — verified by grep before making the change unconditional instead of conditional). `tests/integration/strategy_e2e_test.go`'s `TestFixtureDrivenE2E_AllStrategies/layered` subtest was flipped from pinning the buggy exe-shaped entrypoint to asserting the correct `{SupervisorPath, "--", BunBinaryPath, AppServerIndexPath}`.

**Preventative rule:** When one request field's correct default depends on another field of the *same* request (here: `Runtime.Entrypoint`'s default depends on `Compile.Strategy`), never default it inside a generic, strategy-agnostic `Normalize()`/`WithDefaults()` pass that runs before the strategy-aware code path sees the request. Either default it lazily, only once the dependent field is known, or make the strategy-aware defaulting unconditional (never gated on "is it still nil") — a nil-guard silently loses to any earlier generic pass that already claimed the zero value, and unit tests that construct the downstream port request directly (skipping the earlier pass) will never observe the interaction.

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

## 2026-08-16 — Multi-platform Static/Layered builds silently collided on a zero-value platform key

**Category:** multi-item / boundary

**Root cause:** In `internal/core/pipeline.go`'s per-platform fan-out
(inside `fanOut`), `art.Platform` was only ever set as a side effect of
calling `deps.Compiler.Compile(...)` — which populates
`compiledArt.Platform` from `CompileRequest.Platform` — and that call only
happens on the `else if !req.Compile.Strategy.ApplyStatic()` branch, i.e.
**only for `StrategyExe`**. `StrategyLayered` (which resolves a Bun runtime
instead of compiling) and `StrategyStatic` (which has nothing to compile —
the SvelteKit build output is the entire artifact) both leave `art` at its
Go zero value, so `art.Platform` stayed `ports.Platform{}` (empty OS/Arch)
for every platform in a Layered or Static build.

Two lines later, `built[i] = platformBuild{artifact: art, image: img}` is
built per platform, and back in `Build`, the map that becomes the packaged
OCI index's platform set is constructed as:
```go
images := make(map[Platform]v1.Image, len(built))
for i, b := range built {
    images[b.artifact.Platform] = b.image
}
```
For a Layered or Static build with more than one requested platform, every
entry collided under the same zero-value key — the last platform processed
silently overwrote all earlier ones in the map, then
`Packager.Index` failed with `packager: index: platform "": unsupported
platform` because `ports.Platform{}.Supported()` is false. A single-platform
Layered/Static build never surfaced this (map has exactly one entry
regardless of its key), which is exactly why it went uncaught: every
existing test exercising `StrategyLayered` or `StrategyStatic` used a single
platform.

**Where:** `internal/core/pipeline.go`, `fanOut`'s per-platform goroutine
(the block setting `art`/`bunResult` around what was originally lines
934–974).

**Fix:** Added `art.Platform = p` unconditionally, immediately after the
strategy branch that may or may not have populated it, so every strategy
(Exe, Layered, Static) gets a correctly keyed `ports.Artifact` regardless of
whether that strategy's branch happens to set `Platform` as a side effect of
some other field it needed anyway.

**Preventative rule:** When a per-platform fan-out loop derives a
downstream map/collection key from a field on a struct that's built up
piecemeal across multiple conditional branches (here: `art.Platform`,
populated only as an incidental side effect of the Exe branch's `Compile`
call), don't trust that every branch populates it — set the key field
explicitly and unconditionally, once, from the loop variable itself. This is
the same failure shape as other multi-item bugs in this codebase: correct
for N=1, silently wrong for N>1, because a single-element collection can't
expose a colliding key. Any test added for a strategy that skips a
per-platform field-populating call (no `Compile`, no `BunRuntime.Resolve`,
etc.) should use `>1` platform specifically to catch this class of bug —
single-platform coverage of a new strategy is not sufficient confidence that
its multi-platform path works.

