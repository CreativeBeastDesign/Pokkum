<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: secret-inlining-guard)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Secret-inlining guard (secretguard)

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

Regex-based build-time scan over both pre-build source and packaged build output, catching secrets baked in by build-time dependencies as well as the project's own source.

## Flags

- `--allow-secret-pattern`

## Implementation

- [internal/adapters/secretguard/guard.go](../../internal/adapters/secretguard/guard.go)
- [internal/core/pipeline.go](../../internal/core/pipeline.go)

## Known Limitations

- Five fixed regex patterns only (private key headers, AWS access keys, GitHub PATs, Google API keys, generic password/secret/token assignments) — not Shannon-entropy analysis. An entropy-based scan for arbitrary high-randomness strings was the original design language but was never built.
- See [--strategy=exe secret-scanning gap](exe-secret-scan-gap.md) for the one strategy this does not cover.
- A file too large or unreadable to scan fails the build (ErrSecretScanIncomplete) rather than silently reporting a clean pass.

