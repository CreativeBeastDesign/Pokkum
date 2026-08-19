<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: remote-cache-verify-key-inheritance)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Remote-cache verify key should inherit the signing key

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | hardening |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

A build signed via --signing-key alone doesn't automatically make its own remote-cache entries verifiable, since the cache-verify key chain never reads the signing public key.

## Problem

The cache-verify key chain (`--cache-verify-key`/`POKKUM_CACHE_PUBKEY`/`POKKUM_SIGNING_PUBKEY`/
`POKKUM_BASE_IMAGE_PUBKEY`) never reads `req.Signing.PublicKeyPEM` — `internal/core/pipeline.go`'s
`RemoteCache.Check` call doesn't populate it. The practical outcome today is fail-safe (falls
through to a full rebuild) rather than fail-fast with a clear story, but it means two keys
need configuring for full cache-verification acceleration when one might reasonably be
expected to suffice.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Auto-populate from the signing key | When no POKKUM_CACHE_PUBKEY/POKKUM_SIGNING_PUBKEY is explicitly set, fall back to the signing key configured via --signing-key. | Convenient default, but conflates two keys that may legitimately want to differ (signing vs. cache-verification trust). |
| Leave separate, document the burden | Keep the two key chains independent and clearly document that full cache-verification acceleration needs its own key configured. | No implicit trust-chain surprises, but a real ergonomic gap for the common case where one key would suffice. |

## Implementation

- [internal/core/pipeline.go](../../internal/core/pipeline.go)

