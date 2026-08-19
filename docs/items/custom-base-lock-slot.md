<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: custom-base-lock-slot)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Per-ref pokkum.lock slot for custom --base images

| Field | Value |
| --- | --- |
| Status | shipped |
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

Option A, shipped 2026-08-19. Custom entries key as `custom:<sha256(normalized ref)[:12]>`;
the fixed presets keep their historical keys untouched. The `Ref`/`Digest` identity guard
from `69914ac` is kept on top of the new keying rather than deleted as redundant — a
truncated hash can collide, and `pokkum.lock` is a hand-editable plain file.

Migration, which was the hard part: lookup tries the per-ref key first and falls back to a
legacy bare `"custom"` entry only when its recorded ref matches the request; writes always
use the new key. The legacy entry is copied verbatim and deleted only when it was the entry
just consumed for this ref, so nothing is lost and no duplicate is stranded to diverge on
the next `--update-base`; a legacy entry belonging to a different ref is never touched,
since it is still that ref's only pin. The copy is written early rather than through the
normal write path, or an escrow-mirror resolve would record the mirror's pinned ref as the
upstream pin and a legacy entry that resolves cleanly would never migrate at all.

Widening the key was a caller-chain change that compiled silently: `RecordScanResult(preset)`
could no longer name a custom entry and would have returned nil having recorded nothing, and
`pokkum base check` parsed each slot name as a preset, so every `custom:<hash>` slot would
have vanished from its output. Both fixed.

Behaviour change: the first build after upgrading rewrites `pokkum.lock` once to migrate a
bare `"custom"` entry, so the lockfile hash recorded in SLSA `resolvedDependencies` changes
that once. No image bytes depend on it.

## Flags

- `--base`

## Implementation

- [internal/adapters/baseimage/resolver.go](../../internal/adapters/baseimage/resolver.go)
- [internal/adapters/lockfileutils/lockfile.go](../../internal/adapters/lockfileutils/lockfile.go)
- [cmd/pokkum/base.go](../../cmd/pokkum/base.go)

## Evidence

- Commits: `69914ac`
- Findings: #16 (see [overnight-findings.md](../archive/overnight-findings.md))

