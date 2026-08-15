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
  `POKKUM_STATIC_ROOTS`).
- Server features: GET/HEAD only, multi-root fall-through across `POKKUM_STATIC_ROOTS`
  (default `/app/client:/app/prerendered`), final 404, `Range`/`If-Range` (206),
  strong ETags, Content-Encoding negotiation against `.gz`/`.br`/`.zst` sidecars,
  immutable cache headers, `/healthz`+`/readyz` probes, graceful shutdown.
- Wiring: `core.Deps.StaticServer`; the pipeline fan-out resolves it only for
  `StrategyStatic`; the packager writes it to `/pokkum/static` via
  `buildStaticServerLayer` and sets `POKKUM_STATIC_ROOTS`.
- CLI: `--static` (shorthand for `--strategy=static`), defaults base to
  `cgr.dev/chainguard/static` when no `--base`/`--hardened` given.

## Determinism

Cross-compilation + zstd are reproducible; the layer cache key (`layercacheutils`)
derives from content hash + platform + `SOURCE_DATE_EPOCH`, so a given binary
version yields bit-identical layers across rebuilds.
