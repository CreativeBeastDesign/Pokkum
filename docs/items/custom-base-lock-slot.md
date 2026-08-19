<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: custom-base-lock-slot)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Per-ref pokkum.lock slot for custom --base images

| Field | Value |
| --- | --- |
| Status | open |
| Stage | v1.1 |
| Kind | hardening |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

Give every custom --base reference its own pokkum.lock slot instead of sharing one, since two custom bases in a project still evict each other today.

## Problem

Every custom `--base` reference locks under the single literal key `"custom"`
(`lockKey = string(req.Preset)` in `internal/adapters/baseimage/resolver.go`). The
dangerous half of this — silently returning a *different* base image's content than the
one requested — was already fixed narrowly in `69914ac`: a `"custom"`-keyed entry is now
only trusted when its recorded `Ref`/`Digest` match the current request. What remains is
that two custom bases in one project still share that one slot, so neither gets a stable
pin across builds; the second custom ref's resolve always evicts the first's lockfile
entry rather than getting its own.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Give each custom ref its own slot | Key custom entries as `"custom:" + sha256(ref)[:12]`, mirroring why `distroless-node` became its own preset (`f5229c3`) for exactly this reason. | Needs a `pokkum.lock` migration story for existing lockfiles with a bare `"custom"` entry, which is why it wasn't rushed into the CLI-reachability fix. |
| Keep the current narrow guard indefinitely | Leave the ref/digest match check as the permanent mitigation; accept that multiple custom bases in one project never get independent stable pins. | Zero migration risk, but the underlying correctness gap (two custom bases can't coexist with cached pins) stays open indefinitely. |

## Decision

Option A — give each custom ref its own slot (e.g. `custom:<hash-of-ref>`), mirroring why `distroless-node` became its own preset. Needs a `pokkum.lock` migration story, which is why it is not done yet.

## Flags

- `--base`

## Implementation

- [internal/adapters/baseimage/resolver.go](../../internal/adapters/baseimage/resolver.go)

## Evidence

- Commits: `69914ac`
- Findings: #16 (see [overnight-findings.md](../archive/overnight-findings.md))

