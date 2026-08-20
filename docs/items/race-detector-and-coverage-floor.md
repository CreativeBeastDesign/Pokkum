<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: race-detector-and-coverage-floor)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Race detector + enforced coverage floor

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | infra |
| Tier | foundation |
| Area | Testing & Infrastructure |

## Summary

go test -race now runs in CI over the packages where concurrency actually lives, and coverage is measured and enforced at a 75% floor as a ratchet off the real baseline (77.8% measured 2026-08-20), not an aspirational number.

## Implementation

- [scripts/check-coverage.sh](../../scripts/check-coverage.sh)
- [Makefile](../../Makefile)

## Evidence

- Commits: `e6e4746`

## Known Limitations

- -race is deliberately scoped to registry/core/packager/supervisor rather than the full ./... tree, to keep the added CI cost (~6s) proportionate to where concurrency actually lives.
- Landed in the same change as fixing a structural CI blind spot: CI never installed Bun before this, so every genuinely-real-build e2e test silently skipped and CI's 'e2e' job was entirely mock-compiler. A separate e2e-real-build job now installs Bun, kept apart so the fast hermetic gate stays fast.

