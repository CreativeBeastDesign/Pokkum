<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: sbom-determinism)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# SBOMs of identical source were not identical

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | fix |
| Tier | foundation |
| Area | Testing & Infrastructure |

## Summary

Every npm-family lockfile parser resolved duplicate package names by Go's randomized map order, so two builds of unchanged source produced different package sets, versions and SBOM digests.

## Problem

A lockfile routinely records one package name at several versions: a hoisted copy keyed
by the bare name, plus nested copies keyed by their dependency path. The package
catalogue is keyed by name and holds one of them, and all four parsers
(`bun.lock`, npm v2, npm v1, pnpm) picked the winner by ranging over the parsed map —
which Go deliberately randomises.

Because the same loop also builds the reachability graph that assigns
production/development scope, the effect was not limited to versions: it changed how
many packages the document contained at all. Six builds of one unchanged project
produced 9, 10, 11, 13, 14 and 15 packages, and six SBOM documents with six different
SHA-256s.

For a tool whose central claim is bit-for-bit reproducibility, the supply-chain artefact
was the part that moved.

## Decision

Shipped 2026-08-22. Fixed at the collision rather than at the output: iterate keys in
sorted order, and make the winner an explicit rule — the hoisted copy wins, because it
is the one at `node_modules/<name>` that a bare import actually resolves to.

Why it survived review is the part worth recording. The output was carefully sorted,
with a comment explaining that package order must be deterministic. It was. Sorting a
list assembled by a nondeterministic *selection* yields a stably-ordered list of
unstable contents, and looks exactly like the fix.

Regression tests run 200 iterations per lockfile format, because a two-entry collision
passes a single run about half the time, and assert both stability and that the
semantically correct duplicate won. All four fail against the previous parsers.
Verified end to end: six SBOM generations over a real project now produce one identical
digest.

## Implementation

- [internal/adapters/scannerutils/scannerutils.go](../../internal/adapters/scannerutils/scannerutils.go)

