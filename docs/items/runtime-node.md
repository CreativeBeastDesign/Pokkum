<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: runtime-node)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# --runtime=node, the second runtime dimension

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Build & Packaging |

## Summary

Targets a distroless-node base and execs adapter-node output directly under /nodejs/bin/node with no Bun layer at all, proven by a real Docker boot and, since e918c52, an automated smoke test.

## Flags

- `--runtime=bun`
- `--runtime=node`

## Implementation

- [internal/core/model.go](../../internal/core/model.go)
- [internal/core/pipeline.go](../../internal/core/pipeline.go)
- [internal/core/runtime_node_test.go](../../internal/core/runtime_node_test.go)
- [tests/integration/runtime_smoke_node_test.go](../../tests/integration/runtime_smoke_node_test.go)

## Evidence

- Commits: `f5229c3`, `e918c52`

## Known Limitations

- --telemetry is rejected outright for node, not silently ignored: the layered strategy's OTel bootstrap is a bun --preload mechanism with no Node equivalent.
- Node-core CVEs are unqueryable: distroless-node ships Node outside dpkg, invisible to the OS-package scanner, and the zero-dependency scanner has no Node-core ecosystem entry to query against OSV.dev.
- pokkum dev, pokkum resolve/apply, and standalone pokkum scan have zero runtime awareness — verified: no RuntimeNode reference anywhere in cmd/pokkum/dev.go, k8s.go, or scan.go.
- Correction to the source docs: Roadmap.md and Feature-list.md both still read, at the time of this migration, as if no automated boot smoke test existed for this runtime. That is stale — TestRuntimeSmoke_NodeRuntime_BootsAndServes (tests/integration/runtime_smoke_node_test.go) shipped in e918c52 and also asserts the *absence* of a Bun layer two independent ways, so it is real coverage, not a manual one-off.

