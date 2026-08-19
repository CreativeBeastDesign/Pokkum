<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: verify-suite-scope-gaps)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# make verify's five steps don't cover supervisor/ or the integration/golden test suites

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | infra |
| Tier | polish |
| Area | Testing & Infrastructure |

## Summary

supervisor/ (pokkum-init, pokkum-static) shares the root go.mod but needs its own explicit go build/go test, and tests/integration's golden-manifest and runtime-smoke suites also sit outside make verify's canonical five steps.

## Problem

`make verify`'s five steps run `go test ./internal/...` plus a `cmd/pokkum` build only.
`supervisor/...` is a separate build target entirely, and a diff that only touches
`supervisor/` can pass every one of the five steps while `go build ./supervisor/...` or
`go test ./supervisor/...` are actually broken. Separately, `tests/integration/golden_test.go`
(OCI manifest/config/index goldens) and the runtime-smoke suites are also outside `make
verify`'s scope and must be run explicitly for any change touching layer compression, tar
construction, OCI assembly, or a produced image's boot behavior. Both facts are currently
documented only as a reminder in `CLAUDE.md` and Serena's `mem:state`, not enforced by
tooling — nothing fails a CI run or a `make verify` invocation that skips them.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Fold supervisor/ into a sixth make verify step | Add go build ./supervisor/... && go test ./supervisor/... as a mandatory sixth step, always run rather than conditionally on the diff touching supervisor/. | Simple and impossible to forget, but adds supervisor's build/test time to every make verify run even when nothing under supervisor/ changed. |
| Path-based CI trigger | Add a CI job that runs only when the diff touches supervisor/ or tests/integration/, keeping the fast local make verify loop unchanged. | Keeps the common case fast, but a local make verify still can't be trusted alone for a supervisor-touching change — a contributor without CI access could still miss it. |

## Recommendation

Fold supervisor/ into make verify unconditionally, since its build/test time is small relative to the rest of the suite and 'always run' is the only version of this that can't be silently skipped; keep tests/integration's heavier suites as an explicit, documented extra step given their real Docker/network cost.

