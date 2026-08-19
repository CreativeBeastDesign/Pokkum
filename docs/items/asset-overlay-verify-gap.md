<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: asset-overlay-verify-gap)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum verify doesn't reproduce the asset-overlay layer

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.1 |
| Kind | fix |
| Tier | foundation |
| Area | Build & Packaging |

## Summary

Verifying an image built with --asset-overlay reports a false-positive digest mismatch, because verify's rebuild path has no way to re-resolve the same predecessor chain.

## Problem

[Rolling-deploy asset overlay](rolling-deploy-asset-overlay.md) shipped with a known,
deliberately-not-closed gap: `pokkum verify`'s default rebuild-and-compare path has no
`--asset-overlay`/`--asset-overlay-from` flags of its own, so it cannot reproduce the merged
overlay layer when rebuilding for comparison. An image legitimately built with
`--asset-overlay` therefore fails verification with an overlay-layer digest mismatch, even
though nothing is wrong with it.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Accept the same --asset-overlay-from refs verify's caller already knows | Add --asset-overlay/--asset-overlay-from to pokkum verify, mirroring the build-side flags exactly. | Simple and consistent, but only works if the caller still knows (or can reconstruct) the exact generation chain the original build resolved. |
| Read the chain back off the image's own annotation | Stamp the resolved predecessor digests onto a pokkum.dev/asset-overlay-sources annotation at build time, and have verify read it back automatically before rebuilding. | No extra flags for the caller, self-describing — but adds another annotation to keep in sync with the actual overlay layer contents. |

## Recommendation

Read the chain back off a pokkum.dev/asset-overlay-sources annotation, since it needs no caller-side bookkeeping and keeps verify's rebuild path self-contained.

## Decision

Option B, shipped 2026-08-19. The annotation already existed on every per-platform manifest
of the pushed image, so only the read side was missing. `comparator.CompareImages` now reads
it, reconstructs the merged overlay from the named predecessors through the real
`assetoverlay` resolver, rebuilds the layer using the remote config's own `Created`
timestamp, and only splices it into the comparison once the reconstructed DiffID equals the
image's actual overlay-layer DiffID.

That equality check is what makes this safe rather than merely permissive: a tampered
annotation and a tampered layer are indistinguishable from outside, so any mismatch is
treated as tampering. Absent annotation, malformed entry, missing reconstruction support,
an unreachable predecessor, or an annotation with no matching overlay layer in the image's
own history are all hard errors. Stripping the annotation buys an attacker nothing: the
overlay layer is still there, the plain rebuild still lacks it, and the ordinary comparison
reports L3 — proven by test, not by argument.

A new `ports.LayerBuilder` port keeps this out of an adapter-to-adapter import.

## Flags

- `--asset-overlay`
- `--asset-overlay-from`

## Implementation

- [internal/adapters/comparator/comparator.go](../../internal/adapters/comparator/comparator.go)
- [internal/adapters/packager/layerbuilder.go](../../internal/adapters/packager/layerbuilder.go)
- [internal/ports/packager.go](../../internal/ports/packager.go)
- [cmd/pokkum/verify.go](../../cmd/pokkum/verify.go)

## Known Limitations

- Images whose only output is a local tarball carry no annotations at all, so this path cannot help them — see [Tarball output silently drops every OCI annotation](tarball-output-drops-annotations.md).

## Related

- [Rolling-deploy asset overlay (--asset-overlay)](rolling-deploy-asset-overlay.md)
- [Tarball output silently drops every OCI annotation](tarball-output-drops-annotations.md)

