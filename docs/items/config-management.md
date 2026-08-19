<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: config-management)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum config view / validate, build profiles

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Developer Experience |

## Summary

Inspects resolved build configuration, strictly validates `.pokkum.yaml` schema and profile consistency, and applies named profile overrides at build time.

## Flags

- `--profile`
- `-P`

## Implementation

- [cmd/pokkum/config.go](../../cmd/pokkum/config.go)

