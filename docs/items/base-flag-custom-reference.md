<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: base-flag-custom-reference)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# --base accepts a custom image reference

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | fix |
| Tier | polish |
| Area | Developer Experience |

## Summary

`--base` now accepts a free-form custom image reference, closing a docs/CLI mismatch where the help text promised it and no code path accepted one.

## Problem

`--base`'s help text offered a custom image reference, but every value was routed through
`ParseBaseImagePreset`, so `--base gcr.io/my/base:tag` was rejected outright — a documented
capability the flag simply did not provide. The underlying port already supported it
(`BaseImageCustom` exists and its `Ref` field was already documented as required); only the
CLI could not reach it. A UX/docs mismatch, not a security issue — the preset path and the
`custom` preset itself worked throughout.

## Flags

- `--base`

## Implementation

- [internal/core/model.go](../../internal/core/model.go)

## Evidence

- Commits: `69914ac`
- Findings: #13 (see [overnight-findings.md](../archive/overnight-findings.md))

## Known Limitations

- Presets are tried first, and only a value containing `/`, `.`, `:`, or `@` is parsed as a reference — this ordering is load-bearing, since `name.ParseReference` would otherwise accept a typo'd preset (e.g. `distrolss`) as valid Docker Hub shorthand instead of surfacing a clear "unknown preset" error.

