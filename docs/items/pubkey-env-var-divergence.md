<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: pubkey-env-var-divergence)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# POKKUM_*_PUBKEY meant two different things

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.1 |
| Kind | hardening |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

The same public-key environment variable was resolved as a file path in one place and as literal PEM in another, so its meaning depended on which code path read it.

## Problem

`cmd/pokkum/build.go` resolved `POKKUM_CACHE_PUBKEY` as "try it as a path, and if
`os.ReadFile` fails treat the string itself as PEM", while every adapter reading the same
variable — `remotecacheutils`, `provenance`, `baseimage` — did a bare
`[]byte(os.Getenv(...))` and so accepted literal PEM only. A path therefore worked for
`pokkum build` and would have been handed to a Cosign verifier verbatim by any other
construction of those adapters.

Latent rather than exploitable, because `build.go` populates the request field before the
adapter fallback is reached — but it is checklist row 41's shape (one input read in
several places, error handling drifting between them) and the same class as the
`--sigstore-trusted-root` fail-open fixed alongside it.

The old shape carried a second, user-visible defect: "try ReadFile, fall back to literal
on any error" silently turned a mistyped filename into nonsense key bytes, which surfaced
later as "signature verification failed" — a message that sends the reader hunting for a
tampered artifact when the real problem is a typo.

## Decision

Shipped 2026-08-19. A new shared `keymaterialutils` helper resolves a key setting to PEM
bytes and is used at all six read sites. It classifies by *content* rather than by whether
a file happens to exist: anything containing a PEM preamble is literal key material, and
anything else is a path that must be readable — so a bad path now fails as a bad path,
wrapping `fs.ErrNotExist` and naming the variable at fault. A readable file with no PEM
marker is rejected here rather than left to produce an opaque verifier error.

A set-but-unresolvable candidate fails the chain instead of falling through to the next
variable: continuing would verify against a different key than the operator named, which
is the substitution they set the value to prevent.

Enforced, not just documented: `internal/architecture_test.go`'s
`TestPublicKeyEnvVarsGoThroughTheSharedResolver` bans the
`[]byte(os.Getenv("POKKUM_*_PUBKEY"))` shape across every first-party source root, since a
comment would not have stopped the next bare conversion. The pattern is self-checked
against a known-bad string so a regex that stopped matching cannot make the test pass by
vacuity.

## Flags

- `--cache-verify-key`
- `POKKUM_CACHE_PUBKEY`
- `POKKUM_SIGNING_PUBKEY`
- `POKKUM_BASE_IMAGE_PUBKEY`

## Implementation

- [internal/adapters/keymaterialutils/keymaterialutils.go](../../internal/adapters/keymaterialutils/keymaterialutils.go)
- [cmd/pokkum/build.go](../../cmd/pokkum/build.go)
- [internal/adapters/remotecacheutils/remotecacheutils.go](../../internal/adapters/remotecacheutils/remotecacheutils.go)
- [internal/adapters/provenance/resolver.go](../../internal/adapters/provenance/resolver.go)
- [internal/adapters/baseimage/resolver.go](../../internal/adapters/baseimage/resolver.go)

## Related

- [Remote-cache verify key should inherit the signing key](remote-cache-verify-key-inheritance.md)
- [TrustedRootPath should take bytes, not a file path](trusted-root-bytes.md)

