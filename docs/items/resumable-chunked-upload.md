<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: resumable-chunked-upload)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Resumable chunked layer upload

| Field | Value |
| --- | --- |
| Status | open |
| Stage | v1.2 |
| Kind | hardening |
| Tier | polish |
| Area | Build & Packaging |

## Summary

Back off and retry on 429/5xx during a large layer push instead of failing the whole push on one transient registry hiccup.

