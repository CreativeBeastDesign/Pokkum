<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: doctor-preflight)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum doctor

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Developer Experience |

## Summary

Audits local Bun runtime, SvelteKit version compatibility, `.pokkumignore`, and registry credentials, with `--fix` for mechanical repairs.

## Flags

- `--fix`

## Implementation

- [cmd/pokkum/doctor.go](../../cmd/pokkum/doctor.go)

