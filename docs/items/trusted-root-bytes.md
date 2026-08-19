<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: trusted-root-bytes)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# TrustedRootPath should take bytes, not a file path

| Field | Value |
| --- | --- |
| Status | shipped |
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

Option B, shipped 2026-08-19. `TrustedRootPath string` became `TrustedRootJSON []byte`; the
single `os.ReadFile` moved to the composition root in `cmd/pokkum/build.go`, so no adapter
touches the filesystem for this and both consumers provably verify against the same bytes.
`core.ErrBaseSignatureInvalid` is still wrapped, so existing `errors.Is` checks match.

Implementing it exposed a pre-existing fail-open on the same lines: the flag was read a
*second* time for `req.CacheVerify.TrustedRootJSON` as `if data, err := os.ReadFile(...);
err == nil`, so an unreadable or mistyped path silently left the field empty — which every
consumer reads as "use the embedded public-good root". An operator running a private
Sigstore deployment had cache signatures checked against the public-good root with no
warning, no log, and exit 0. `pokkum verify` already had the correct shape, with a comment
explaining why. The single shared read fixes it.

Behaviour change, deliberate: an unreadable `--sigstore-trusted-root` now fails the build up
front rather than only when keyless base verification happens to run, so `--no-verify-base`
no longer tolerates it.

## Implementation

- [internal/ports/baseimage.go](../../internal/ports/baseimage.go)
- [internal/adapters/baseimage/resolver.go](../../internal/adapters/baseimage/resolver.go)
- [cmd/pokkum/build.go](../../cmd/pokkum/build.go)

