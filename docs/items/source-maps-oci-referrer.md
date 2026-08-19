<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: source-maps-oci-referrer)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Source maps as an OCI referrer

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | feature |
| Tier | polish |
| Area | Build & Packaging |

## Summary

Strip source maps from the shipped image and attach them as a digest-keyed OCI referrer artifact, so Sentry release tagging works without shipping maps to production.

