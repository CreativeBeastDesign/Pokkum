<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: init-base-image-recommendations)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum init explains the base image presets instead of listing them

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.1 |
| Kind | dx |
| Tier | polish |
| Area | Developer Experience |

## Summary

Each preset is shown with a one-line reason and the detected default marked, rather than as a bare three-way choice.

## Decision

The base-image prompt printed three preset names and nothing else, which asks the operator
to already know the difference between `distroless` and `chainguard` — and gives no hint
that `distroless-node` is required for `runtime: node` and pointless otherwise. Each preset
now carries a one-line rationale, and the default derived from the runtime detection is
marked so the recommended answer is visible without reading it off a separate line.

The offered list is built from the `ports` preset constants rather than written out in the
prompt, which is what stops the 2026-08-19 defect recurring: that prompt offered
`chainguard-static`, an unimplemented roadmap item, because the list was hand-maintained.

## Implementation

- [cmd/pokkum/init_analysis.go](../../cmd/pokkum/init_analysis.go)
- [cmd/pokkum/init.go](../../cmd/pokkum/init.go)

## Related

- [pokkum init detects bun vs node, and the base image that carries it](init-runtime-detection.md)
- [pokkum init wrote a config pokkum build refused](init-generates-invalid-config.md)

