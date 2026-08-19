<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: fixture-isolation)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Real-build tests copy their fixture into t.TempDir() first

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | infra |
| Tier | foundation |
| Area | Testing & Infrastructure |

## Summary

Every mutating real-build test now copies its checked-in fixture into a fresh t.TempDir() before building, closing an order-dependence that had already caused three separate incidents, proven at -count=3 -shuffle=on.

## Implementation

- [tests/integration/harness_test.go](../../tests/integration/harness_test.go)
- [internal/adapters/bunexec/integration_test.go](../../internal/adapters/bunexec/integration_test.go)

## Evidence

- Commits: `ac5dc89`, `20ba1ec`
- Findings: #11 (see overnight-findings.md)

## Known Limitations

- Stale-claim correction: overnight-findings.md's finding 11 recorded this as 'not fixed — deliberately,' calling it a broader test-hygiene change than belonged at the end of that queue. It was, in fact, done the same day. Serena's mem:state, as read at the start of this migration, still described it as 'known fragility, deliberately not fixed yet' — also stale. Commit 20ba1ec generalized the t.TempDir()-copy pattern tests/integration/runtime_smoke_test.go had already established to all five affected real-build tests, moved the shared helper into harness_test.go, and proved order-independence empirically rather than asserting it.
- Read-only tests were deliberately left untouched, but only after confirming — by reading the actual production code, not assuming — that nothing in their dependency chain (sbom.Generator, packager.Packager, mockCompiler's StrategyExe branch) ever calls os.WriteFile/os.MkdirAll against ProjectDir.
- bunexec cannot import the tests/integration helper (a separate package, and adapter-to-adapter imports are architecturally forbidden), so it carries a small, deliberate duplicate that — unlike the shared helper — does not skip .svelte-kit, since that test's precondition is a pre-prepared fixture.

