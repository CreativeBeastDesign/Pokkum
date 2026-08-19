<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: layered-runtime-hardening)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Layered-strategy runtime hardening (stub launcher + startup attestation)

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | hardening |
| Tier | foundation |
| Area | Build & Packaging |

## Summary

Two composable mitigations for stock Bun's full CLI attack surface in a layered image: a non-foldable compiled entrypoint launcher, and a supervisor-verified startup digest over the /app tree.

## Flags

- `--stub-launcher`
- `POKKUM_STUB_LAUNCHER`
- `POKKUM_ATTESTATION_DIGEST`

## Implementation

- [internal/adapters/bunexec/compiler.go](../../internal/adapters/bunexec/compiler.go)
- [internal/adapters/packager/packager.go](../../internal/adapters/packager/packager.go)

## Evidence

- Commits: `fb23335`

## Known Limitations

- Startup attestation only exists for --strategy=layered; --strategy=exe and --strategy=static don't attest.

