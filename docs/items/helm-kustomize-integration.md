<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: helm-kustomize-integration)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Helm post-renderer + Kustomize KRM function

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | feature |
| Tier | foundation |
| Area | Kubernetes & Operations |

## Summary

Deferred: pokkum resolve only handles raw-YAML pokkum:// refs today, which most teams — who template with Helm or Kustomize — never reach at all.

## Problem

`pokkum resolve` handles `pokkum://` references in raw YAML only, which covers Knative-style
repos and roughly nobody else, since most teams template with Helm or Kustomize and will
therefore never reach `pokkum apply`. Deferred behind the dev-loop and asset-overlay work
because it is ergonomics, not differentiation — but per the roadmap's own framing, it is the
ergonomics item most likely to be the actual reason a team can't adopt Pokkum at all, so it is
the one to revisit first if the developer-experience tier stalls. Two thin entrypoints over the
existing `resolve` engine would unlock the two dominant templating toolchains at comparatively
low cost.

## Related

- [pokkum resolve](k8s-uri-resolution.md)

