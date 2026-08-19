<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: kms-signing)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# KMS-backed signing

| Field | Value |
| --- | --- |
| Status | open |
| Stage | v1.2 |
| Kind | feature |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

Sign builds using a cloud KMS key instead of a local static key file.

## Problem

Static-key signing (see [[items/image-signing|Image signing with Cosign/DSSE]]) requires
operators to manage a private key file directly, which is a poor fit for teams already
using AWS KMS, GCP KMS, or HashiCorp Vault for key custody.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Pluggable KMS signer interface | Add a ports.KMSSigner interface with AWS/GCP/Vault adapters, following the existing ports.CosignSigner shape. | More adapters to maintain long-term; each cloud SDK adds dependency weight the zero-dependency invariant has to account for. |
| Delegate to cosign's built-in KMS support | Shell out to `cosign sign --key awskms://...` for the KMS case instead of implementing our own client. | Much less code to write, but couples pokkum to the cosign CLI being installed on PATH — directly contradicts the zero-dependency invariant. |

## Recommendation

Pluggable KMS signer interface, using each cloud's lightweight signing API directly rather than shelling out, to stay dependency-free.

## Flags

- `--signing-key`

## Related

- [[items/image-signing|Image signing with Cosign/DSSE]]

