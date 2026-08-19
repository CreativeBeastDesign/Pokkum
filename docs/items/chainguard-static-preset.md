<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: chainguard-static-preset)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Dedicated chainguard-static base image preset

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | hardening |
| Tier | polish |
| Area | Supply Chain & Attestation |

## Summary

Give --strategy=static its own base-image preset so it stops sharing a pokkum.lock slot with an explicit chainguard glibc-dynamic --base build.

## Problem

`--strategy=static`'s default base reuses the `BaseImageChainguard` preset — correct for
signature identity (fixed from an earlier `BaseImageDistroless` misassignment that broke
verification on every default `--static` build) — but this leaves a narrow `pokkum.lock`
collision: an explicit `--base cgr.dev/chainguard/glibc-dynamic` build and a `--static`
build in the same project share one lock slot, the same class of problem
[the custom-base lock-slot decision](custom-base-lock-slot.md) addresses for custom refs.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Add a dedicated BaseImageChainguardStatic preset | Own default ref, own lock key — eliminates the collision entirely, mirroring how distroless-node got its own preset. | One new CLI-visible preset name, plus a self-healing orphaned-lockfile-entry migration note for existing --static users. |

## Recommendation

Add a dedicated BaseImageChainguardStatic preset — small, well-precedented change.

## Decision

Deferred 2026-08-19, deliberately left in `backlog` rather than promoted to a numbered
stage: unscheduled is the honest status, and v1.1/v1.2/v2.0 would each imply a commitment
that has not been made.

What changed is that this stopped being a user-visible defect. `pokkum init`'s prompt used
to offer `chainguard-static` as base-image option 3, so anyone picking it got a
`.pokkum.yaml` that `pokkum build` refused — the preset was advertised before it existed.
That prompt now lists only the presets that are real (`distroless`, `chainguard`,
`distroless-node`), so the gap is no longer reachable by accident; see
[pokkum init wrote a config pokkum build refused](init-generates-invalid-config.md).

What remains is the original narrow issue only: an explicit
`--base cgr.dev/chainguard/glibc-dynamic` build and a `--strategy=static` build in the
same project still share one `pokkum.lock` slot. That is a stable-pin annoyance rather
than a correctness hole — the identity guard shipped with the per-ref lock work means
neither build can be served the other's image content.

## Related

- [Per-ref pokkum.lock slot for custom --base images](custom-base-lock-slot.md)
- [pokkum init wrote a config pokkum build refused](init-generates-invalid-config.md)

