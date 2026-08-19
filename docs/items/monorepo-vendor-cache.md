<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: monorepo-vendor-cache)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Shared vendor cache across a monorepo invocation

| Field | Value |
| --- | --- |
| Status | open |
| Stage | v1.2 |
| Kind | feature |
| Tier | foundation |
| Area | Build & Packaging |

## Summary

--since skips builds between unaffected projects but does nothing within a build when many packages in one invocation share dependencies; extending the layer cache into a content-addressable vendor-layer cache would close that.

## Problem

Extend `layercacheutils` into a cache keyed by `package.json` + lockfile subtree, shared
across every project built in one invocation — analogous to how `ko` leans on Go's build
cache. Confirmed by reading `internal/adapters/layercacheutils/layercacheutils.go`: the
existing cache has no cross-project sharing concept today, so this gap is real and not
merely asserted from the feature list, as the original roadmap note asked to verify.

## Flags

- `--since`

