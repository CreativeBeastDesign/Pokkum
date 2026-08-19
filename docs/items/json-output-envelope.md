<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: json-output-envelope)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Standardized machine-readable output (--output=json)

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Developer Experience |

## Summary

A global `--output=json` flag emits a versioned JSON envelope across every command, instead of callers parsing human-readable stdout.

## Flags

- `--output`

## Implementation

- [cmd/pokkum/build.go](../../cmd/pokkum/build.go)

