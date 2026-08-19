<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Features

## Supply Chain & Attestation

### [[items/image-signing|Image signing with Cosign/DSSE]]

Builds are signed via Cosign static-key or DSSE, with a fetch-back-and-reverify step before the build is allowed to report `Signed: true`.

- Flags: `--sign`, `--signing-key`, `POKKUM_SIGNING_KEY`
- Implementation:
  - [internal/adapters/cosign/signer.go](../internal/adapters/cosign/signer.go)
  - [internal/core/pipeline.go](../internal/core/pipeline.go)

## Known Limitations

- The placeholder trust-anchor fallback was removed; an unconfigured key now hard-fails instead of silently no-op signing (a breaking change for anyone who relied on the old default). ([[items/image-signing|Image signing with Cosign/DSSE]])

