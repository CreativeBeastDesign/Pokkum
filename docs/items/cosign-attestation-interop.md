<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: cosign-attestation-interop)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# cosign verify-attestation interop fix

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | fix |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

Fixed a missing annotation key that made real cosign v3 reject Pokkum's own valid DSSE attestations in tag-fallback mode.

## Implementation

- [internal/adapters/cosign/signer.go](../../internal/adapters/cosign/signer.go)

## Evidence

- Commits: `e918c52`
- Findings: #14 (see [overnight-findings.md](../archive/overnight-findings.md))

## Known Limitations

- The bug was an interop assumption about cosign's own wire format encoded in a code comment and never checked against cosign's actual source — the attestation layer now writes `dev.cosignproject.cosign/signature: ""` to match cosign's convention exactly.

