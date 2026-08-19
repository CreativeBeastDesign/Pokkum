<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: cache-control-contract)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Cache-Control contract, tested for every strategy

| Field | Value |
| --- | --- |
| Status | open |
| Stage | v1.2 |
| Kind | infra |
| Tier | foundation |
| Area | Build & Packaging |

## Summary

The /_app/immutable, version.json, service-worker.js and prerendered-HTML header contract is a tested invariant only for --strategy=static today; layered/exe rely on adapter-node's bundled sirv defaults with no Pokkum-side test.

## Problem

Getting this contract wrong either breaks SvelteKit's own `updated.check()` polling or serves
stale HTML forever. `supervisor/cmd/pokkum-static/server_test.go` and `integration_test.go`
already assert it precisely for [--strategy=static](strategy-static.md). The layered and
exe strategies serve prerendered content through adapter-node's bundled `sirv`, which sets its
own defaults — nothing in this codebase currently asserts they match the intended contract
(`public, max-age=31536000, immutable` for hashed assets; `no-cache` for `version.json`,
`service-worker.js`, and prerendered HTML).

## Related

- [--strategy=static](strategy-static.md)

