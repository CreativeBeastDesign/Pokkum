<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: vendor-cancellation-test-flake)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# TestPrepare_CancelledContextLeaksNeitherInstallNorGoroutine is timing-dependent

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.2 |
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

## Decision

The race was between two independent clocks: a fixed `sleep 3` fake install, and however
long cancellation took to propagate (context → the vendor job's goroutine → `cmd.Cancel` →
SIGKILL). On a loaded runner the second could exceed the first with teardown working
perfectly.

The fake install now blocks forever, so it has no natural end and the only way it can stop
is by being killed — there is no clock left to lose against. The assertion moved from the
completion proxy ("a marker file never appeared") to the signal that actually matters: the
OS process is gone, probed with signal 0. The goroutine-leak half is untouched; both halves
the test name promises are still asserted.

No production seam was needed — the existing `cmd.Cancel`/process-group-kill machinery
already provided everything.

One consequence of a fake install that never exits on its own: if the assertion fails, that
process outlives the run and sits in a sleep loop indefinitely. Proving the guard could
fail left exactly one such orphan behind, found with `ps` and killed by hand. So the kill
is unconditional `t.Cleanup` rather than part of the assertion — a red test must not also
litter the machine.

Verified 20/20 consecutive runs, and 10/10 under `-race`.

## Implementation

- [internal/adapters/bunexec/vendor_concurrency_test.go](../../internal/adapters/bunexec/vendor_concurrency_test.go)

## Related

- [Race detector + enforced coverage floor](race-detector-and-coverage-floor.md)

