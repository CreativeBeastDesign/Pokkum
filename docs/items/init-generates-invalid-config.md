<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: init-generates-invalid-config)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum init wrote a config pokkum build refused

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.1 |
| Kind | fix |
| Tier | foundation |
| Area | Developer Experience |

## Summary

Every generated .pokkum.yaml carried an invalid sbom.attach value, so the first two commands a new user runs did not work together.

## Problem

`GenerateDefault` wrote `sbom.attach: attestation` into every generated `.pokkum.yaml`.
`attestation` was never one of that mode's values (`referrer`, `tag`, `auto`) — a
plausible word, since an SBOM genuinely is attached as an attestation, which is why it
survived review. `pokkum build` then refused to start with `invalid sbom attach mode`.

Both halves were individually correct and tested: `GenerateDefault` had tests asserting
its fields, `ParseSBOMAttachMode` had tests rejecting bad input, and nothing exercised
the join. Worse, `cmd/pokkum/config.go`'s `validateConfigFields` already checks
`sbomAttach` — the binary shipped a validator that rejects exactly this, and `init`
never ran it on its own output.

Two further defects surfaced immediately once a regression test covered the whole matrix
of choices `init` offers: the prompt offered `chainguard-static`, an unimplemented
roadmap item rather than a preset, while omitting the real `distroless-node`; and the
prompts accepted any typed value verbatim, so a typo reached disk and failed much later.

Found by running the tool on a real project, not by any test or review pass.

## Decision

Shipped 2026-08-19. Generated enum values now come from the port constants
(`ports.DefaultSBOMAttachMode`, `ports.SBOMFormatSPDXJSON`), making this class of typo a
compile error. `init` validates what it is about to write through the same
`validateConfigFields` that backs `pokkum config validate`, and refuses with an explicit
"this is a bug in pokkum, not your project" — a generated config is Pokkum's own output,
so an invalid value there is never the user's fault. A new `promptChoice` constrains each
enum answer to a fixed set and re-asks rather than accepting, and the base-preset list is
now exactly the presets that exist.

## Flags

- `--sbom-attach`
- `--base`

## Implementation

- [internal/adapters/config/config.go](../../internal/adapters/config/config.go)
- [cmd/pokkum/init.go](../../cmd/pokkum/init.go)
- [cmd/pokkum/config.go](../../cmd/pokkum/config.go)

## Related

- [Dedicated chainguard-static base image preset](chainguard-static-preset.md)
- [pokkum config view / validate, build profiles](config-management.md)

