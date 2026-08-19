<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: service-mesh-integration)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Service mesh integration

| Field | Value |
| --- | --- |
| Status | wont-do |
| Kind | infra |
| Tier | non-goal |
| Area | Kubernetes & Operations |

## Summary

Will not be built: Istio/Linkerd sidecar config generation is real but narrow demand against real, ongoing API churn that dedicated mesh tooling already tracks.

## Decision

`pokkum mesh generate` (Istio/Linkerd sidecar configs, `--mtls` certificate/traffic-policy
wiring) would mean tracking real and ongoing service-mesh API churn for narrow demand.
Mesh-specific tooling already exists, and this isn't a place Pokkum's SvelteKit-specific
knowledge adds anything a generic Kubernetes tool wouldn't already provide.

