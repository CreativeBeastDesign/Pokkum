<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: asset-overlay-verify-gap)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum verify doesn't reproduce the asset-overlay layer

| Field | Value |
| --- | --- |
| Status | open |
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

## Related

- [Rolling-deploy asset overlay (--asset-overlay)](rolling-deploy-asset-overlay.md)

