# Static Server Adapter (`internal/adapters/staticserver`)

Provides the embedded, statically-linked **`pokkum-static`** PID-1 static file
server binary for the `--strategy=static` build path. This is the static
analogue of the `supervisor` adapter: it plays PID 1 in a minimal libc-free
`cgr.dev/chainguard/static` image, replacing BOTH the supervisor and the Bun
runtime.

## Key facts

- Implements `ports.StaticServerProvider` (`Binary(ctx, platform)`, `Version(ctx)`).
  Sentinel: `core.ErrStaticServerUnavailable` for an absent asset; a present-but-
  corrupt blob surfaces as a distinct internal `errStaticServerCorrupt`.
- Cross-compiled `linux/amd64` + `linux/arm64` blobs live under
  `bin/pokkum-static-linux-*.zst` (zstd-compressed; `make static-server` builds
  them; `make build` depends on `static-server`).
- Binary source: `supervisor/cmd/pokkum-static` (must NOT import `internal/ports`
  — it duplicates the small env-literal set: `PORT`, `POKKUM_PROBE_PORT`,
  `POKKUM_STATIC_ROOTS`, `POKKUM_STATIC_FALLBACK`).
- Server features: GET/HEAD only, multi-root fall-through across `POKKUM_STATIC_ROOTS`
  (default `/app/client:/app/prerendered`), final 404, `Range`/`If-Range` (206),
  strong ETags, Content-Encoding negotiation against `.gz`/`.br`/`.zst` sidecars,
  immutable cache headers, `/healthz`+`/readyz` probes, graceful shutdown.
- **Candidate resolution order (fixed 2026-08-19): exact file → `<rel>.html` →
  directory `index.html`, all three through one shared containment-checked
  resolver.** Previously there was no `.html` candidate at all, so every
  non-root prerendered route 404'd under `adapter-static`'s default
  `trailingSlash: 'never'` (`/about` prerenders to `about.html`, not a
  directory); `/` worked only incidentally via the directory branch.
- **Both `http.Server`s now actually bind `Addr` (fixed 2026-08-19).** Neither
  the content nor the probe server previously set `Addr`, so both fell back to
  Go's `:http` default and raced for port 80 — `PORT`/`POKKUM_PROBE_PORT` were
  parsed correctly and then never applied. Every `--strategy=static` image was
  unreachable on its documented ports until this fix. Undetected for as long
  as it was because every existing test used `httptest`'s own substitute
  listener, never the real `ListenAndServe` bind path. See `Lessons.md`.
- **Single-port mode (`PORT == POKKUM_PROBE_PORT`) has no working probes at
  all — known, decided, not yet implemented.** The `if cfg.Port !=
  cfg.ProbePort` guard skips the second listener on the assumption the content
  mux covers probes too; it doesn't. Decision: reject the collapsed
  configuration outright rather than merge mux handlers or silently document
  it. Tracked in `Roadmap.md`.
- **Opt-in SPA fallback (implemented 2026-08-17):** `POKKUM_STATIC_FALLBACK` / `-fallback`,
  default empty = plain 404 (honest 404s preserved). When set to an in-image file path,
  unmatched GET/HEAD routes serve that file with 200 via the SAME `serveFile` negotiation
  (ETag/Range/Content-Encoding). Fallback path validated once at construction — must be a
  regular file within a served root; out-of-root/missing ⇒ Warn + disabled (never serves
  outside the roots). Discovery: one-per-process `sync.Once` Warn on the first 404 while
  unset, pointing at Vocabulary.md; 404 body stays clean (no dev-marker HTML).
- Wiring: `core.Deps.StaticServer`; the pipeline fan-out resolves it only for
  `StrategyStatic`; the packager writes it to `/pokkum/static` via
  `buildStaticServerLayer` and sets `POKKUM_STATIC_ROOTS`.
- CLI: `--static` (shorthand for `--strategy=static`), defaults base to
  `cgr.dev/chainguard/static` when no `--base`/`--hardened` given.

## Real fixture and boot smoke test (2026-08-19)

`testdata/fixtures/sveltekit-static` is the first real `@sveltejs/adapter-static`
fixture this strategy has had — previously only a synthetic mock existed, and
it fabricated the same flat `prerendered/index.html` shape the (also buggy)
production flattening code assumed, so it validated a fiction rather than
catching the mismatch. The real fixture immediately found the Addr-bind bug
above, a `Preflight` check (in `internal/adapters/bunexec`, not this package)
that rejected every real static project before `Prepare`'s strategy check ran,
and the flat-vs-nested prerendered mismatch. See `mem:self_review_checklist`
rows 12/13 and `Lessons.md`.

## Embedded binaries need the pipeline that ships them to actually build them (fixed 2026-08-19)

`bin/pokkum-static-linux-*.zst` are gitignored; only `.gitkeep` is tracked. No
CI job and no `.goreleaser.yaml` hook ran `make static-server` before
2026-08-19, so every published release embedded no static-server binary at
all — `go:embed all:bin` embeds an almost-empty directory without error, so
this was invisible until something looked for the missing blob at runtime.
Fixed: `static-server` joins the goreleaser before-hooks and a CI build step
in all three workflows. Still open: nothing asserts the embedded blob matches
a fresh build of its own source (a stale local blob could still ship
unnoticed) — tracked in `Roadmap.md`.

## Determinism

Cross-compilation + zstd are reproducible; the layer cache key (`layercacheutils`)
derives from content hash + platform + `SOURCE_DATE_EPOCH`, so a given binary
version yields bit-identical layers across rebuilds.
