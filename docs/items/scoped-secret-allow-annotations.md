<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: scoped-secret-allow-annotations)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Scoped secret-allow annotations

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | feature |
| Tier | polish |
| Area | Build & Packaging |

## Summary

--allow-secret-pattern is a global regex; a .secretguardignore or inline // pokkum:allow-secret comment would give a known-safe string in one fixture the scoped exemption it actually needs.

## Flags

- `--allow-secret-pattern`

