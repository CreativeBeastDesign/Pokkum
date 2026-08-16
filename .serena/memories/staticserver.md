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

## Determinism

Cross-compilation + zstd are reproducible; the layer cache key (`layercacheutils`)
derives from content hash + platform + `SOURCE_DATE_EPOCH`, so a given binary
version yields bit-identical layers across rebuilds.
