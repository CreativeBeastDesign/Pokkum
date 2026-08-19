<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: cluster-hardening-defaults)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Cluster hardening defaults

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Kubernetes & Operations |

## Summary

Injects secure `securityContext`, resource requests/limits, `NetworkPolicy`/`PodDisruptionBudget` manifests, and probe defaults into resolved Kubernetes workloads.

## Problem

`securityContext` defaults (`runAsNonRoot`, `readOnlyRootFilesystem`, `seccompProfile:
RuntimeDefault`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`) and
`readinessProbe`/`livenessProbe`/`startupProbe` defaults against the supervisor's
`/readyz`/`/healthz` are each checked and injected independently per field/probe type, so an
existing custom setting of one kind doesn't block defaulting of the others.

## Flags

- `--security-context`
- `--no-security-context`
- `--network-policy`
- `--with-otel-sidecar`

## Implementation

- [internal/adapters/k8s/resolver.go](../../internal/adapters/k8s/resolver.go)

