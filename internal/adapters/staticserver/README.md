# staticserver adapter (`internal/adapters/staticserver`)

Embedded, statically-linked **`pokkum-static`** PID-1 static file server binary
assets, mirroring the `supervisor` adapter's compressed-embedding approach.

## What it is

For the `--strategy=static` build path, Pokkum does **not** ship a Bun runtime,
compiled server JS, or a separate supervisor. Instead a single statically-linked
Go binary — `pokkum-static` (built from `supervisor/cmd/pokkum-static`) — plays
PID 1 inside a minimal libc-free `cgr.dev/chainguard/static` image. The CLI embeds
zstd-compressed `linux/amd64` and `linux/arm64` cross-compiled blobs under
`bin/pokkum-static-linux-*.zst` and decompresses them on the fly to the raw ELF.

## Adapter contract

- Implements `ports.StaticServerProvider` (`Binary`, `Version`).
- `Binary(ctx, platform)` returns the raw ELF bytes for the requested platform,
  decompressed from the embedded `.zst` blob. Empty input is treated as a corrupt
  blob (returns `core.ErrStaticServerUnavailable`).
- `Version(ctx)` returns the embedded build version string.

## Building the binaries

```sh
make static-server   # cross-compiles + zstd-compresses both platforms into bin/
```

`go:embed all:bin` picks the blobs up at CLI build time. The `make build` target
depends on `static-server`, so a normal `make build` always embeds fresh blobs.

## The runtime server (`supervisor/cmd/pokkum-static`)

`pokkum-static` is a self-contained PID-1 HTTP file server (no imports from
`internal/ports` — it duplicates the small set of env literals). It serves the
roots listed in `POKKUM_STATIC_ROOTS` (set by the packager to
`/app/client:/app/prerendered`) from a fixed directory path, and implements:

- `GET`/`HEAD` only; multi-root fall-through; final 404 when nothing serves.
- Candidate resolution for a request path, in order: exact file, then
  `<rel>.html`, then a directory's `index.html` (the `.html` candidate was
  added 2026-08-19 — its absence meant every non-root prerendered route
  404'd under `adapter-static`'s default `trailingSlash: 'never'`, since
  `/about` prerenders to `about.html`, not a directory; `/` had worked
  only incidentally, via the directory-index branch. All three candidates
  go through one shared containment-checked resolver).
- `Range`/`If-Range` support (206 responses).
- Strong `ETag`s (`"<hex>"` of content hash) for revalidation, and, as of
  2026-08-19, an actual conditional-GET path: `If-None-Match` is checked
  (parsed as a list of entity-tags, `*` matches any representation, weak
  comparison so a `W/`-prefixed request tag still matches) and answered with
  a bodiless `304` rather than resending the full response — previously the
  server computed and sent the `ETag` but never consulted `If-None-Match` at
  all, so it never returned 304, which mattered most for prerendered HTML
  (marked `no-cache`, so revalidated on every load). The check runs after the
  `ETag` is set and before the `Range` block, so a matching conditional wins
  over a would-be 206. `If-Modified-Since` is deliberately not implemented —
  no `Last-Modified` is sent, and in-image mtimes are pinned to a fixed epoch
  for reproducibility, so a mtime-derived validator would be constant across
  every build. Found by executing `paranoid-testing-guide.md`'s static
  section rather than reading it. See `Lessons.md`'s 2026-08-19 entry.
- `Content-Encoding` negotiation against the `.gz`/`.br`/`.zst` sidecars that
  `precompressutils` generates at build time.
- Immutable cache headers on hashed/versioned assets.
- Health/readiness probes on `POKKUM_PROBE_PORT` (default `8081`), graceful
  shutdown on `SIGTERM`/`SIGINT`.
- **Opt-in SPA fallback** (`POKKUM_STATIC_FALLBACK` / `-fallback`): when set to
  an in-image file path, unmatched `GET`/`HEAD` routes serve that file with
  `200` (same negotiation as other files). Default (empty) keeps plain-404
  behavior; a one-per-process `Warn` on the first 404 (while unset) points
  operators at `Vocabulary.md`. The fallback path is validated at construction
  to be a regular file within a served root — an out-of-root or missing path is
  rejected and disables the fallback (never serves content outside the roots).

## Binding to the configured ports (fixed 2026-08-19)

Neither `http.Server` literal in `main.go` used to set `Addr`, so both the
content and probe servers fell back to Go's `:http` default (port 80) and
raced for it — one won, the other died with "address already in use," and
every `--strategy=static` image was unreachable on its documented `PORT`/
`POKKUM_PROBE_PORT` ports regardless of the routing fixes above. `Addr` is
now built from the already-parsed config on both servers, host part left
empty (the wildcard address — correct for a container network namespace,
where the Pod/Service reaches the process only through the runtime-attached
interface, never loopback). This went uncaught for as long as it did
because every existing test used `httptest.NewServer`'s own substitute
listener, never the real `ListenAndServe` bind path — the regression tests
added with the fix bind a real ephemeral port and issue a real TCP request
instead. See `Lessons.md`'s 2026-08-19 entry.

## A collapsed port configuration is rejected outright, not merged or documented (fixed 2026-08-19)

`PORT == POKKUM_PROBE_PORT` used to skip the probe listener on the (false)
assumption that the content mux covered `/healthz`/`/readyz` too — there is
no mux merge anywhere in this package, so a single-port deployment had no
working probes at all while still serving pages happily; Kubernetes would
route traffic to it with no signal that anything was wrong. `config.go`'s
`validate()` now fails closed on this combination (naming both env vars,
exit code `exitUsage`) before either listener is constructed, reached from
env, from flags, and from an explicit `PORT` colliding with the *default*
`POKKUM_PROBE_PORT`. `main.go`'s `if cfg.Port != cfg.ProbePort` guard around
the probe listener is consequently always true and was removed — both
listeners are now unconditional, with a comment recording the invariant
that makes that safe. `pokkum build` could never have produced this
configuration itself (`internal/core/model.go` rejects `Port == ProbePort`
for every build request before packaging, and `build` has no `--port` flags
at all), so this is defense-in-depth for configs assembled outside
`pokkum build` — a hand-edited pod spec, or the binary run directly — not a
build-time gap it closes.

## `--strategy=static`'s production bugs were only found once a real fixture existed (2026-08-19)

Until `testdata/fixtures/sveltekit-static` (a genuine `@sveltejs/adapter-static`
project, scaffolded and built with real tooling), this strategy had no real
fixture at all — only a synthetic compiler mock that fabricated its own,
incorrect idea of what `@sveltejs/adapter-static` emits (a flat
`prerendered/index.html`, when the real tool nests output under
`prerendered/pages/`, plus sibling `dependencies/`/`data/` categories).
The real fixture immediately found the bind bug above, a `Preflight` check
that rejected every real `adapter-static` project before `Prepare`'s own
strategy-aware check ran, and the flat-vs-nested prerendered mismatch — all
three are consumed via `internal/adapters/bunexec`, not this package
directly, but they gated whether this package's binary ever received
correctly-shaped input to serve. See `ARCHITECTURE.md` §8 and `Lessons.md`.

## Embedded binaries are only as fresh as the pipeline that builds them (fixed 2026-08-19)

`bin/pokkum-static-linux-*.zst` are gitignored, locally-built artifacts —
only `.gitkeep` is tracked. Until 2026-08-19, no CI job and no step in
`.goreleaser.yaml` ever ran `make static-server`, so a fresh checkout (or a
published release) embedded no static-server binary at all: `go:embed
all:bin` happily embeds an almost-empty directory with no build error, so
the gap was invisible until something tried to use the missing blob at
runtime. `static-server` now joins the goreleaser before-hooks and a
`Build Embedded PID-1 Binaries` CI step in all three workflows. **The
remaining gap — nothing asserted the embedded blob matches a fresh build of
its own source — is closed too (2026-08-19):** `make check-embedded-blobs`
(`blob_freshness_test.go`'s `TestEmbeddedPID1Binaries_MatchSource`, run
`-count=1` since `go test`'s result cache can't see through an exec'd
`go build` into another package's source) rebuilds both binaries from
source with the exact `Makefile` flags and compares them byte-for-byte
against what's actually embedded, wired into `make verify` as an explicit
extra step. Not part of the PR-gate CI job on purpose — CI always rebuilds
both blobs from the checked-out commit before any test runs, so this
specific staleness is a local working-tree hazard, not something a fresh
CI checkout could ever hit. Building this guard immediately surfaced a
second bug: Go's default `-buildvcs` stamping made both binaries' *content*
change on every commit regardless of source, closed with `-buildvcs=false`
on both build targets — see `Lessons.md`'s 2026-08-19 entry.

## Determinism

Cross-compilation and zstd compression are reproducible; layer cache keys derive
from the content hash + platform + `SOURCE_DATE_EPOCH`, so a given binary version
yields bit-identical layers across rebuilds.
