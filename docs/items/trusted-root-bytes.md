<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: trusted-root-bytes)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# TrustedRootPath should take bytes, not a file path

| Field | Value |
| --- | --- |
| Status | open |
| Stage | v1.1 |
| Kind | hardening |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

Change the base-image trusted-root field from a file path to bytes so all three Sigstore trust-root consumers take the same shape.

## Problem

`--sigstore-tuf-refresh` (`eeaa83a`) feeds two trusted-root consumers with bytes
(`sigstore.Verifier`'s `trustedRootJSON` and the TUF refresh option), but a third,
`ports.BaseImageRequest.TrustedRootPath` (`internal/ports/baseimage.go` ~line 332), takes a
file *path* instead, read via `os.ReadFile` in `internal/adapters/baseimage/resolver.go`
(~line 931). It was found and deliberately left alone while wiring the refresh flag —
called out in that commit message as "a bigger call than belongs in this change."

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Bridge with an internal temp-file write | When bytes arrive from elsewhere (e.g. a live TUF refresh), write them to a temp file and hand baseimage its expected path. | Extra indirection, and raises a real question of where that temp file should live relative to the hermetic sandbox — exactly the complication that made this a deliberate non-decision at the time. |
| Change TrustedRootPath to bytes | Change `ports.BaseImageRequest.TrustedRootPath` to a byte slice and have the CLI/caller read the file itself, matching the other two consumers. | One field-shape change plus updating its single caller; makes all three trust-root consumers structurally consistent going forward. |

## Decision

Option B — change it to take bytes so all three consumers are consistent, rather than bridging with a temp file whose location relative to the hermetic sandbox would need its own decision.

## Implementation

- [internal/ports/baseimage.go](../../internal/ports/baseimage.go)
- [internal/adapters/baseimage/resolver.go](../../internal/adapters/baseimage/resolver.go)

