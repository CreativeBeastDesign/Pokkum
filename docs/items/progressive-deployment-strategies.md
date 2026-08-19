<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: progressive-deployment-strategies)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Progressive deployment strategies

| Field | Value |
| --- | --- |
| Status | wont-do |
| Kind | infra |
| Tier | non-goal |
| Area | Kubernetes & Operations |

## Summary

Will not be built: canary, blue-green, and auto-rollback are Argo Rollouts/Flagger's turf, with Kubernetes-native primitives Pokkum has no reason to reimplement.

## Decision

Argo Rollouts and Flagger already own progressive deployment strategies with Kubernetes-native
primitives; building `pokkum deploy --canary`/`--blue-green`/`--auto-rollback` would be a
massive maintenance burden competing directly with tools that already do this well.
`pokkum rollback`'s one-hop-deep, annotation-based rollback covers the narrower case Pokkum is
actually positioned to solve — fast, with no extra controller required.

## Related

- [pokkum rollback](multi-generation-rollback.md)

