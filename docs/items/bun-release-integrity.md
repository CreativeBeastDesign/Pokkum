<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: bun-release-integrity)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Bun release checksum verification

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | hardening |
| Tier | foundation |
| Area | Build & Packaging |

## Summary

Every downloaded Bun release archive is checksum-verified before extraction — pinned digests for common versions, Bun's own GPG-signed SHASUMS256.txt.asc for anything else — failing closed rather than silently installing an unverifiable download.

## Implementation

- [internal/adapters/bunruntime/resolver.go](../../internal/adapters/bunruntime/resolver.go)

## Evidence

- Commits: `0e8ae5e`, `4d8ba1b`

## Known Limitations

- PB-2's first-contact gap is a stated, permanent limitation, not an open TODO: the very first resolve of a genuinely new, unlisted (version, target) on a fresh cache has no independent trust anchor beyond the GPG-signed manifest itself — GitHub's Releases API shares the same trust root as the download host and exposes no per-asset digests, so it adds no real signal. Re-running scripts/pin-bun-checksums periodically narrows this; nothing closes it fully.

