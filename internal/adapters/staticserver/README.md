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
- `Range`/`If-Range` support (206 responses).
- Strong `ETag`s (`"<hex>"` of content hash) for revalidation.
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

## Determinism

Cross-compilation and zstd compression are reproducible; layer cache keys derive
from the content hash + platform + `SOURCE_DATE_EPOCH`, so a given binary version
yields bit-identical layers across rebuilds.
