<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: hermetic-build-mode)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Hermetic build mode (--hermetic)

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | hardening |
| Tier | foundation |
| Area | Build & Packaging |

## Summary

Enforces real Linux network-namespace isolation for the build subprocess (no IP egress regardless of what a compromised dependency's build-time code tries), falling back to advisory-only isolation elsewhere.

## Flags

- `--hermetic`
- `--hermetic-mount-isolation`

## Implementation

- [internal/core/pipeline.go](../../internal/core/pipeline.go)

## Evidence

- Commits: `28582b3`, `00f791d`

## Known Limitations

- Requires cached base image resolution, pre-populated node_modules/, and a pre-cached Bun binary — fails closed rather than downloading, so a cold cache cannot hermetic-build.
- --hermetic-mount-isolation's docker.sock mask has an honest residual gap: the sandboxed process retains CAP_SYS_ADMIN in its own namespace (the capability that created the mask), so a sufficiently sophisticated dependency aware of the mechanism could in principle umount() it. Closing this needs capset(2) to drop the capability before the final exec — not attempted.

