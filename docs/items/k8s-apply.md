<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: k8s-apply)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum apply

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Kubernetes & Operations |

## Summary

Resolves manifests and applies them directly to a Kubernetes cluster via `kubectl apply -f -`, seeding rollback history from live cluster state first.

## Problem

A pre-flight `ports.ClusterInspector` query reads each target workload's current annotations
and container images from the live cluster before resolution, so multi-generation rollback
works reliably even when deploying from a static, uncommitted `pokkum://` manifest template
across independent CLI runs — the case `pokkum resolve` alone cannot cover (see its own
limitations).

## Implementation

- [cmd/pokkum/apply.go](../../cmd/pokkum/apply.go)
- [internal/ports/k8s.go](../../internal/ports/k8s.go)

## Related

- [pokkum resolve](k8s-uri-resolution.md)
- [pokkum rollback](multi-generation-rollback.md)

