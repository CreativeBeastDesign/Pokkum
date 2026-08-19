<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: runtime-boot-smoke-tests)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Real Docker boot smoke tests

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | infra |
| Tier | foundation |
| Area | Testing & Infrastructure |

## Summary

The first tests in this repo to actually boot a produced image and poll it, for both the default layered/Bun strategy and --runtime=node, instead of only proving the packaged bytes are right and stable.

## Implementation

- [tests/integration/runtime_smoke_test.go](../../tests/integration/runtime_smoke_test.go)
- [tests/integration/runtime_smoke_node_test.go](../../tests/integration/runtime_smoke_node_test.go)

## Evidence

- Commits: `fc53968`, `e918c52`

## Known Limitations

- Every existing layer-structure/determinism/golden-manifest test had proven the packaged bytes were correct and stable, never that the result actually runs — that gap is exactly what let every layered image ship without a working entrypoint for this codebase's entire prior history (see Lessons.md's 2026-08-18 entry on the missing /app/server/index.js).
- Gated on -short/bun/docker/network, each skipping cleanly rather than failing when unavailable.
- --strategy=static has its own separate fixture-driven boot test rather than this harness — see [Real @sveltejs/adapter-static test fixture](static-strategy-real-fixture.md).

