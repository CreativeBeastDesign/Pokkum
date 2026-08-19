<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: hooks-system)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Pre/post-build shell hooks

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | dx |
| Tier | foundation |
| Area | Developer Experience |

## Summary

Deferred: pre/post-build shell hooks would defuse plugin-system demand cheaply, but add new maintenance surface for something CI pipelines already provide natively.

## Problem

`pokkum hook pre-build`/`pokkum hook post-build` (database migrations, asset compilation, smoke
tests against the new image) scores well on DX but is real, ongoing cross-platform shell-exec
maintenance for a capability that is not differentiated — every CI pipeline already runs
pre/post steps around a build natively. Deferred rather than built; revisit if repeatedly
requested rather than build speculatively.

