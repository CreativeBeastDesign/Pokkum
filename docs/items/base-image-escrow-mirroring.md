<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: base-image-escrow-mirroring)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Base image escrow / mirroring

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

`--mirror-registry` mirrors upstream base images and signatures to a project-controlled registry, with pulled bytes verified against pokkum.lock's pinned digest.

## Flags

- `--mirror-registry`

## Implementation

- [internal/adapters/baseimage/resolver.go](../../internal/adapters/baseimage/resolver.go)

## Evidence

- Commits: `a149b28`

## Known Limitations

- Escrow-mirror pulls are digest-pinned against pokkum.lock's recorded digest; a mirror tag retargeted to different content fails closed rather than silently serving stale-pin content.

