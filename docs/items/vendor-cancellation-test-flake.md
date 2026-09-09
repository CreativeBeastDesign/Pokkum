<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: vendor-cancellation-test-flake)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# TestPrepare_CancelledContextLeaksNeitherInstallNorGoroutine is timing-dependent

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | infra |
| Tier | polish |
| Area | Testing & Infrastructure |

## Summary

The vendor-cancellation guard races a real `bun install` against context cancellation, and fails on a slow or loaded runner when the install wins.

## Problem

`internal/adapters/bunexec/vendor_concurrency_test.go` asserts that cancelling `Prepare`
tears down the concurrent vendor install. It observed the opposite on the macOS CI job
during the v1.2.0 release check — "the vendor install outlived the cancelled Prepare and
ran to completion" — and passed on a re-run of the same commit with no change.

It is not a regression: the test predates the branch that surfaced it, and that branch
touched no cancellation, goroutine or vendor code in `bunexec`. It passed three of three
local runs on the same OS.

The mechanism is a race the test creates rather than one it observes: it starts a real
install and cancels shortly after, so on a runner where the install finishes first the
assertion fires even though cancellation works correctly. A guard that reports a failure
when the code is right is worse than no guard — it trains readers to re-run rather than
to look, which is exactly what happened here.

## Recommendation

Make the install's duration controlled rather than incidental — a stub or a script that
blocks until released — so the test observes the teardown it is asserting about instead
of racing a real package manager. Failing that, assert on the observable teardown signal
directly rather than on "did the install complete".

Worth doing before it trains anyone to dismiss a red macOS job by reflex.

## Implementation

- [internal/adapters/bunexec/vendor_concurrency_test.go](../../internal/adapters/bunexec/vendor_concurrency_test.go)

## Related

- [Race detector + enforced coverage floor](race-detector-and-coverage-floor.md)

