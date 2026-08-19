<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: otel-collector-sidecar)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Kubernetes OTel Collector sidecar injection (--with-otel-sidecar)

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Observability |

## Summary

Injects an OpenTelemetry Collector sidecar spec (4317 gRPC, 4318 HTTP, 8889 metrics) directly into generated Kubernetes workload manifests.

## Flags

- `--with-otel-sidecar`

## Implementation

- [cmd/pokkum/build.go](../../cmd/pokkum/build.go)
- [internal/adapters/k8s/resolver.go](../../internal/adapters/k8s/resolver.go)

