<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: multi-arch-attestation-subject)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Multi-arch signature/attestation subject (dual-publish)

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

Signatures and attestations attach to both the image index and every per-platform manifest digest, so any verifier agrees regardless of which digest it targets.

## Implementation

- [internal/core/pipeline.go](../../internal/core/pipeline.go)
- [internal/adapters/cosign/signer.go](../../internal/adapters/cosign/signer.go)

## Evidence

- Commits: `2f03609`

## Known Limitations

- Interop with `cosign verify-attestation` in tag-fallback mode required a follow-up fix (see [cosign verify-attestation interop fix](cosign-attestation-interop.md)) — dual-publish alone did not guarantee third-party tool agreement.

