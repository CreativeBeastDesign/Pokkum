<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: remote-cache-verify-key-inheritance)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Remote-cache verify key should inherit the signing key

| Field | Value |
| --- | --- |
| Status | shipped |
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

## Decision

Option A, shipped 2026-08-19. The derived key is offered as a dedicated
last-resort chain entry (`RemoteCacheVerifyOptions.SigningPublicKeyPEM`), not by
writing `PublicKeyPEM` from core — that field is the *first* link, and the three
`POKKUM_*_PUBKEY` links are resolved afterwards inside `remotecacheutils`, so
populating it from core would have pre-empted three explicit sources and forked the
precedence chain into two copies free to drift. Precedence therefore stays in exactly
one place, and every explicit source still wins.

Why this narrows rather than widens trust, verified against the code and not assumed:
the static-key arm accepts a candidate only when its signature verifies against the
single key handed to it, and keyless-vs-static mode selection keys on
`KeylessIdentity` alone, so no key field can flip the mode. Falling back to the
operator's own signing public key therefore means "accept only cache entries I signed
myself", against a status quo of no key at all and every candidate refused. A test
proves an entry signed by a different key still fails closed.

Logged once per check rather than silently: an implicitly derived trust anchor nobody
is told about is the shape checklist rows 38/41 warn about.

## Implementation

- [internal/ports/cache.go](../../internal/ports/cache.go)
- [internal/core/pipeline.go](../../internal/core/pipeline.go)
- [internal/adapters/remotecacheutils/remotecacheutils.go](../../internal/adapters/remotecacheutils/remotecacheutils.go)

