<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: monorepo-affected-detection)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Monorepo affected-detection (--since)

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Kubernetes & Operations |

## Summary

Diffs each project's tree against a git ref and skips builds entirely for projects with no changes and a known prior digest.

## Problem

Stronger than digest-HEAD skipping: a git-diff per `pokkum://` app means no build is attempted
at all for an unaffected project, not just no push.

## Flags

- `--since`

## Implementation

- [internal/adapters/gitutils/affected.go](../../internal/adapters/gitutils/affected.go)
- [cmd/pokkum/k8s.go](../../cmd/pokkum/k8s.go)

