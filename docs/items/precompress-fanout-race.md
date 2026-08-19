<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: precompress-fanout-race)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Concurrent per-platform builds race on precompressed sidecars

| Field | Value |
| --- | --- |
| Status | shipped |
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

## Decision

Shipped 2026-08-19 — but not by hoisting, and the reason is a hard constraint the
recommendation missed: `internal/core` may not import a `*utils` package (enforced by
`TestUtilityPackagesNotImportedFromCoreOrPorts`), so core cannot call precompression at all
without introducing a new port. That is a larger change than the defect warrants.

What shipped instead achieves the same two effects inside the adapter:

1. **Freshness made meaningful.** A sidecar's on-disk mtime now comes from its source rather
   than the build epoch. `isStale` compares those two, and pinning sidecars to the epoch while
   a build writes its sources *now* made every sidecar permanently stale — so every platform
   re-ran brotli at BestCompression over the whole tree. Safe for reproducibility: the only
   `ModTime` that reaches a tar header is the pinned value `writeTar` receives, so on-disk
   mtimes never influence image bytes. This removes the N-1 redundant compressions the
   recommendation wanted.
2. **Per-directory serialisation.** Precompressors no longer overlap each other.

Together they give the invariant the race needed: **writes never overlap a walk.** The first
platform writes while the others block on the lock; every later platform then finds the
sidecars fresh and writes nothing; so all writing has finished before any tar walk begins.

The three discarded `_ =` errors now warn through the packager's logger — a precompression
failure previously shipped a slower image with no signal.

**Two rejected approaches, recorded because both looked better than they were.** Atomic
writes (temp file plus rename) seemed strictly safer and are wrong here: `os.CreateTemp` puts
the temporary file in the very directory the packager walks, so a concurrent walk either fails
its lstat when the file is renamed away or packages a `.tmp-*` file into the image — caught by
a test. And a test forcing rewrites during a walk was written, failed intermittently, and was
deleted: it asserts write atomicity, a guarantee this design deliberately does not provide.

## Implementation

- [internal/adapters/precompressutils/precompressutils.go](../../internal/adapters/precompressutils/precompressutils.go)
- [internal/adapters/packager/packager.go](../../internal/adapters/packager/packager.go)
- [internal/core/pipeline.go](../../internal/core/pipeline.go)

## Known Limitations

- The race has no reproducing test. Four designs were tried; the vulnerable window is roughly one syscall wide and `-race` is blind to a filesystem-mediated race. The guard is instead the freshness test — if later passes ever start rewriting again the ordering argument collapses and it fails, verified 12/12 against the reverted fix.
- Safety rests on freshness detection being correct. A source genuinely modified mid-build would be recompressed by a later platform while an earlier one tars; that does not happen in a normal build, and is stated in the code rather than assumed away.

## Related

- [--strategy=static](strategy-static.md)
- [Cache-Control contract, tested for every strategy](cache-control-contract.md)

