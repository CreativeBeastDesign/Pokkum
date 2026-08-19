<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: precompress-fanout-race)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Concurrent per-platform builds race on precompressed sidecars

| Field | Value |
| --- | --- |
| Status | open |
| Stage | v1.1 |
| Kind | fix |
| Tier | foundation |
| Area | Build & Packaging |

## Summary

Every platform re-runs precompression over the same output directory while other platforms are tarring it, so a multi-arch build can fail with archive/tar write too long.

## Problem

`fanOut` runs the per-platform compile/supervisor/package chain **concurrently**, and each
platform's `appendPrerenderedLayer` calls
`precompressutils.PrecompressDirectory(req.AppPrerenderedDir, ...)` against the **same**
directory, then immediately walks that directory to build the layer.

`PrecompressDirectory` writes each sidecar with `os.WriteFile`, which truncates before
writing. So platform A can stat `index.html.br` while platform B is mid-rewrite, take the
partial size into the tar header, then read the completed file — writing more bytes than the
header declared. `archive/tar` rejects that outright:

  packager: build linux/arm64: prerendered layer: write tar entry
  "app/prerendered/index.html.br": archive/tar: write too long

This is not test-only. The default platform set is `linux/amd64,linux/arm64`, so any
multi-arch build of a project with prerendered pages runs exactly this pattern. It is
intermittent, which is worse than deterministic: it presents as a flaky build.

Observed as `TestFixtureDrivenE2E_Static_SPAFallback` failing under full-suite parallelism
and passing in isolation. It was **previously misattributed** — including by me — to load
contention from two test packages that were hanging on a Docker credential helper. Fixing
those hangs made it stop reproducing for a while, which looked like confirmation and was
coincidence. `-race` cannot see it: the race is mediated by the filesystem, not memory.

Two adjacent defects in the same lines: `PrecompressDirectory`'s error is discarded
(`_ = precompressutils...`), so a genuine precompression failure is silent; and the work is
duplicated per platform, compressing identical bytes N times for N platforms.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Precompress once, before the fan-out | Move precompression ahead of fanOut so it runs a single time over the shared output tree, and have each platform's packager only read the sidecars. | Correct and strictly less work — precompression output does not depend on the target platform, so per-platform execution was never meaningful. Requires moving the step across the core/adapter boundary, so the packager must no longer assume it may generate sidecars itself. |
| Serialise precompression per directory | Guard PrecompressDirectory with a per-path mutex or sync.Once so only one platform generates sidecars while the others wait. | Smaller diff and keeps the call where it is, but leaves the duplicated-work problem and keeps a shared-mutable-directory design that the next concurrent consumer can trip over again. |

## Recommendation

Precompress once before the fan-out. The per-platform call is not just unsafe but meaningless — the bytes are identical for every platform — so hoisting it fixes the race and removes N-1 redundant compressions rather than merely making the collision orderly.

## Implementation

- [internal/adapters/precompressutils/precompressutils.go](../../internal/adapters/precompressutils/precompressutils.go)
- [internal/adapters/packager/packager.go](../../internal/adapters/packager/packager.go)
- [internal/core/pipeline.go](../../internal/core/pipeline.go)

## Related

- [--strategy=static](strategy-static.md)
- [Cache-Control contract, tested for every strategy](cache-control-contract.md)

