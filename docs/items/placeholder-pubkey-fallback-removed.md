<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: placeholder-pubkey-fallback-removed)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Remove shared placeholder trust-anchor fallback

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | hardening |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

Deleted the single hardcoded placeholder public key that silently backstopped signing, base-image, and remote-cache verification when no key was configured.

## Implementation

- [internal/adapters/cosign/signer.go](../../internal/adapters/cosign/signer.go)
- [internal/adapters/baseimage/resolver.go](../../internal/adapters/baseimage/resolver.go)

## Evidence

- Commits: `a149b28`
- Findings: #8 (see overnight-findings.md)

## Known Limitations

- Breaking change: static-key verification now requires an explicitly configured key rather than silently trusting an undocumented shared fallback that nobody's private key ever matched.
- The fallback removal exposed two latent fail-opens in `internal/adapters/provenance/resolver.go` (a nil-tolerant signer check and a bare `false` for a nil DSSE signer) — both now refuse via `ErrVerifierNotInjected` instead of silently skipping verification.

