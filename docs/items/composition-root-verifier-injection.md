<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: composition-root-verifier-injection)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Composition-root refactor for verifier injection

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | hardening |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

cmd/pokkum now injects verifiers at every construction site instead of adapters building their own defaults, closing an empty-by-construction adapter-to-adapter import allowlist.

## Implementation

- [internal/architecture_test.go](../../internal/architecture_test.go)
- [internal/adapters/provenance/resolver.go](../../internal/adapters/provenance/resolver.go)

## Evidence

- Commits: `e5dd73c`
- Findings: #8 (see [overnight-findings.md](../archive/overnight-findings.md))

## Known Limitations

- Deleting the implicit defaults exposed two latent fail-opens in provenance verification (see [Remove shared placeholder trust-anchor fallback](placeholder-pubkey-fallback-removed.md)) that were previously unreachable, not previously safe.

