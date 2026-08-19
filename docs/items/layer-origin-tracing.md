<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: layer-origin-tracing)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum explain / explain why / explain diff

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Developer Experience |

## Summary

Reads a real OCI image and reports its actual per-layer digests, sizes, and file origins, and diffs two images layer-by-layer.

## Problem

All three commands originally returned hardcoded fabricated data — `explain` returned a literal
layer list with invented digests and sizes, `why` hardcoded a layer index for whatever dependency
was passed, and `diff` hardcoded a fixed "modified" layer, none of which ever touched a real
image (no `remote.`, `Fetch`, or tar walk in the original implementation). Implemented for real
by reusing the layer-diffing and per-layer file-record machinery `pokkum verify`'s L3 comparison
and the packager already had, rather than deleting the commands.

## Implementation

- [cmd/pokkum/explain.go](../../cmd/pokkum/explain.go)

