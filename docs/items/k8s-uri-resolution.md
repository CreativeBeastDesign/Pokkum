<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: k8s-uri-resolution)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum resolve

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Kubernetes & Operations |

## Summary

Resolves `pokkum://` image URIs embedded in Kubernetes YAML manifests to immutable `repo@sha256:...` digest references.

## Implementation

- [cmd/pokkum/resolve.go](../../cmd/pokkum/resolve.go)
- [internal/adapters/k8s/resolver.go](../../internal/adapters/k8s/resolver.go)

## Known Limitations

- Handles raw-YAML `pokkum://` references only. Most teams template with Helm or Kustomize and will never reach this path today — see [Helm post-renderer and Kustomize KRM function](helm-kustomize-integration.md).
- Operating on a static, untouched manifest file cannot accumulate multi-generation rollback history across independent CLI runs unless intermediate annotations are committed or seeded from live cluster state.

