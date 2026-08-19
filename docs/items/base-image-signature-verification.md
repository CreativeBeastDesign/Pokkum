<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: base-image-signature-verification)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Base image signature verification

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

Stock base presets are verified via keyless Sigstore by default; custom bases via static-key Cosign, completing the chain of custody Pokkum already applies to its own outputs.

## Flags

- `--base-verify-mode`
- `--base-verify-key`

## Implementation

- [internal/adapters/baseimage/resolver.go](../../internal/adapters/baseimage/resolver.go)
- [internal/adapters/sigstore/verifier.go](../../internal/adapters/sigstore/verifier.go)

## Evidence

- Commits: `efc1743`

## Known Limitations

- Keyless verification requires the operator to supply `--keyless-identity`/`--keyless-issuer` explicitly and refuses outright before any network I/O if keyless material is present with no configured identity — it does not trust anything derived from the certificate under verification (a prior version did, and that path was dead code).

