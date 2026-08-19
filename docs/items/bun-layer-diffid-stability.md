<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: bun-layer-diffid-stability)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Bun/supervisor layer diffID stability, pinned twice

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | fix |
| Tier | foundation |
| Area | Build & Packaging |

## Summary

Immutable-binary layers (Bun, pokkum-init, pokkum-static) now use a fixed epoch and disable Go's VCS stamping, so their ~90MB digest stops churning on every commit for content that never actually changed.

## Implementation

- [internal/adapters/packager/packager.go](../../internal/adapters/packager/packager.go)
- [Makefile](../../Makefile)

## Evidence

- Commits: `1675d4c`, `81a6fb6`
- Findings: #17 (see overnight-findings.md)

## Known Limitations

- This was a real bug, not a missing assertion: writing the stability test found the diffID derived its tar timestamp from SOURCE_DATE_EPOCH, which changes every commit, actively inverting what was supposed to be the single biggest fleet-wide size lever (fixed 1675d4c).
- The fix was itself silently undermined until 81a6fb6: Go's default -buildvcs stamping made the pokkum-init/pokkum-static binaries' own content change every commit regardless of the tar-timestamp pin, so the two layers containing them kept churning anyway — the same failure class (build metadata leaking into a content-addressed artifact) as the first bug, a second independent source of it in the identical layers. Closed with -buildvcs=false on both embedded-binary build targets; the main CLI build deliberately keeps VCS stamping since it wants version reporting.

