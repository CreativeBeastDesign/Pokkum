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
- **Conditional GET (`If-None-Match` → 304, fixed 2026-08-19).** The server always
  computed and sent a strong `ETag` but never checked `If-None-Match` at all — no
  304 was ever possible, so a client already holding the current copy re-downloaded
  the full body every time (worst for prerendered HTML, which is `no-cache` and thus
  revalidated on every load). Now parses `If-None-Match` as an entity-tag list per
  RFC 9110 (`*` matches anything; comparison is weak, since the server only ever
  emits strong tags), checked after the `ETag` is set and before the `Range` block
  so a match wins over a would-be 206. `If-Modified-Since` deliberately NOT
  implemented: no `Last-Modified` is sent and in-image mtimes are pinned to a fixed
  epoch, so a mtime-derived validator would be a constant, not a real signal. Found
  by executing `paranoid-testing-guide.md` rather than reading it. See `Lessons.md`.
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
- **Single-port mode (`PORT == POKKUM_PROBE_PORT`) is now rejected outright at
  startup (fixed 2026-08-19).** `config.go`'s `validate()` returns an error
  naming both `PORT` and `POKKUM_PROBE_PORT` when they collide (exit code
  `exitUsage`=2, the same convention as every other config-shape error in this
  binary), reached from every entry point — env, flags, and their defaults
  colliding (e.g. `PORT=8081` against `defaultProbePort=8081`). With the
  collapsed case now structurally unreachable past config parsing, `main.go`'s
  `if cfg.Port != cfg.ProbePort` guard around the probe listener was dead code
  and was removed — both `http.Server`s now start unconditionally. Confirmed
  build-time: `internal/core/model.go`'s `validateRuntime` (called
  unconditionally from `pipeline.go`'s `Build()` for every strategy including
  static) already rejects `rc.Port == rc.ProbePort` before packaging, so
  `pokkum build` could never have produced a collapsed static image in the
  first place — the runtime check in `pokkum-static` is defense-in-depth for
  configs assembled outside `pokkum build` (hand-written manifests, `.pokkum.yaml`
  edited directly, etc.), not a build-time gap.
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
in all three workflows. **The remaining gap is closed too (2026-08-19):**
`make check-embedded-blobs` (`blob_freshness_test.go`'s
`TestEmbeddedPID1Binaries_MatchSource`, `-count=1` since `go test`'s result
cache can't see through an exec'd `go build` into another package's source)
rebuilds both binaries fresh and compares them byte-for-byte against what's
embedded; wired into `make verify` as an explicit extra step, deliberately
NOT in the PR-gate CI job (CI always rebuilds both from the checked-out
commit first, so this exact staleness is a local-only hazard there). This
guard immediately found a second, unrelated bug: Go's default `-buildvcs`
stamping made both binaries' content change on every commit regardless of
source (byte counts matched, content didn't) — fixed with `-buildvcs=false`
on both `Makefile` build targets and on the guard's own rebuild. See
`Lessons.md` and `mem:self_review_checklist` row 31.

## Determinism

Cross-compilation + zstd are reproducible; the layer cache key (`layercacheutils`)
derives from content hash + platform + `SOURCE_DATE_EPOCH`, so a given binary
version yields bit-identical layers across rebuilds.
