<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: expect-source-verified)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# `--expect-source` requires verified provenance

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | hardening |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

`--expect-source` now refuses to compare against unsigned source annotations unless the caller opts into the explicitly-marked-unverified escape hatch.

## Flags

- `--expect-source`
- `--allow-unverified-source`

## Implementation

- [internal/adapters/provenance/resolver.go](../../internal/adapters/provenance/resolver.go)
- [internal/adapters/slsa/generator.go](../../internal/adapters/slsa/generator.go)

## Evidence

- Commits: `91dc3cd`
- Findings: #1 (see [overnight-findings.md](../archive/overnight-findings.md))

## Known Limitations

- Breaking change: CI using `--expect-source` on unsigned images now fails until it signs or passes `--allow-unverified-source`.

