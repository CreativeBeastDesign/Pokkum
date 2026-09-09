<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: init-runtime-detection)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum init detects bun vs node, and the base image that carries it

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.1 |
| Kind | feature |
| Tier | foundation |
| Area | Developer Experience |

## Summary

`runtime: bun|node` is inferred from the project's own toolchain, and drags the paired base preset along so the two cannot disagree.

## Problem

`runtime` is a real config field and a real `--runtime` flag, but `ports.InitConfigOptions`
had no `Runtime` member at all, so `pokkum init` could not write it under any circumstances.
A Node project got a Bun image by silence, and the operator had to discover the flag and
hand-edit `.pokkum.yaml` afterwards.

Worse, the field does not stand alone. core rejects `runtime: node` outside
`strategy: layered`, and on any base preset that ships no Node binary — the runtime IS base
image content for node. Those are three fields that must compose, and
`pokkum config validate` checks fields, not their composition. A generator emitting them
independently would produce a config that validates and then refuses to build: the exact
2026-08-19 `init-generates-invalid-config` shape, one field wider.

## Decision

Shipped as `sveltekitutils.DetectAppRuntime`, reading three tiers of evidence in a fixed
precedence: `packageManager` in package.json (explicit and singular), then lockfiles
(`bun.lock`/`bun.lockb` vs `package-lock.json`/`pnpm-lock.yaml`/`yarn.lock`), then
`engines`. Contradictory evidence inside a tier returns NO opinion with a `Conflict`
reason rather than picking a side; the caller keeps its own default and says why.

Explicitly a preference signal, not a compatibility verdict — nothing here proves a project
cannot run under the other runtime, and nothing refuses a build over it.

The composition is enforced in `config.GenerateDefault`, the one place that picks all three
values: `runtime: node` upgrades the base to `distroless-node`, and is dropped rather than
emitted next to a non-layered strategy. `pokkum init`'s prompt does not offer the runtime
question at all when the strategy is `static`, since a static image ships no JavaScript
runtime and every answer would be meaningless or invalid.

Guarded by a test that walks EVERY combination of strategy, runtime, base preset and local
profile the generator can emit and asserts each is accepted by both `config validate`'s
per-field checks and core's cross-field `Validate()`. Reverting the composition clamp turns
six of those combinations red.

## Implementation

- [internal/adapters/sveltekitutils/runtimedetect.go](../../internal/adapters/sveltekitutils/runtimedetect.go)
- [internal/adapters/config/config.go](../../internal/adapters/config/config.go)
- [internal/ports/config.go](../../internal/ports/config.go)
- [cmd/pokkum/init.go](../../cmd/pokkum/init.go)

## Related

- [Static-viability analysis (does this project need a server?)](static-viability-analyzer.md)
- [pokkum init wrote a config pokkum build refused](init-generates-invalid-config.md)
- [pokkum init](workspace-init-wizard.md)

