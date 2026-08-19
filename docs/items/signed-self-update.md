<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: signed-self-update)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum upgrade

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Developer Experience |

## Summary

Checks for new releases and verifies the release binary's checksum signature via Cosign before self-replacing.

## Implementation

- [cmd/pokkum/upgrade.go](../../cmd/pokkum/upgrade.go)

