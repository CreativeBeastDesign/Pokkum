<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: build-time-test-gate)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Build-time test gate (--test)

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | feature |
| Tier | polish |
| Area | Build & Packaging |

## Summary

Condition image creation on the project's own test suite passing, once its interaction with --hermetic (a test suite needing network access conflicts with hermetic-by-default) has an explicit answer.

## Flags

- `--hermetic`

