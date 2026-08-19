<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: image-signing)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Image signing with Cosign/DSSE

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

Builds are signed via Cosign static-key or DSSE, with a fetch-back-and-reverify step before the build is allowed to report `Signed: true`.

## Flags

- `--sign`
- `--signing-key`
- `POKKUM_SIGNING_KEY`
- `--require-signed`

## Implementation

- [internal/adapters/cosign/signer.go](../../internal/adapters/cosign/signer.go)
- [internal/core/pipeline.go](../../internal/core/pipeline.go)

## Evidence

- Commits: `2f03609`
- Findings: #14 (see overnight-findings.md)

## Known Limitations

- The placeholder trust-anchor fallback was removed; an unconfigured key now hard-fails instead of silently no-op signing (a breaking change for anyone who relied on the old default).
- Static-key signing only — there is no keyless (Fulcio/OIDC) signing path. Keyless Sigstore exists only on the verification side (base images, `pokkum verify`).

