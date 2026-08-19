<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: supervisor-metrics-endpoint)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Supervisor-side /metrics endpoint

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | feature |
| Tier | polish |
| Area | Observability |

## Summary

Give pokkum-init a real /metrics HTTP handler exporting Bun runtime and cgroup metrics, which is the capability the removed pokkum metrics command only claimed to provide.

## Problem

`pokkum-init` registers `/livez` and `/readyz` only. Nothing exports Bun runtime metrics
(event-loop lag, heap) or the container's cgroup limits from PID 1, so an operator wanting
those has no Pokkum-provided source for them \u2014 the app-side OTel pipeline covers
application spans, not the runtime the app sits on. This is the honest version of what
[Reconsider pokkum metrics' shape](metrics-command-shape.md) was asked to decide; that item
was closed by deleting the overclaiming command, which removed the false promise without
adding the capability. Kept as a distinct item so the capability question stays visible
instead of disappearing with the command that misrepresented it.

## Implementation

- [supervisor/cmd/pokkum-init/main.go](../../supervisor/cmd/pokkum-init/main.go)

## Related

- [Reconsider pokkum metrics' shape](metrics-command-shape.md)
- [Kubernetes OTel Collector sidecar injection (--with-otel-sidecar)](otel-collector-sidecar.md)

