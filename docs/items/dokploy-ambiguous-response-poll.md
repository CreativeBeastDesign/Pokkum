<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: dokploy-ambiguous-response-poll)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Dokploy: disambiguate an unrecognised 2xx by polling, instead of failing outright

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | dx |
| Tier | polish |
| Area | Developer Experience |

## Summary

An HTTP 200 with an empty body is reported as a failed deploy even when the rollout in fact started; a follow-up `application.one` read could tell the two apart without weakening fail-closed.

## Problem

`dokployReportsSuccess` (`internal/adapters/deploy/dokploy.go`) accepts `true` and
`{"result":{"data":true}}` and rejects everything else, including an empty body. The
reasoning in its doc comment is correct and should not be softened: a 200 carrying a body
this code cannot identify must not be read as a confirmed mutation, because a reverse proxy
can produce one.

The cost is a false negative. Observed twice in one real session (2026-09): Dokploy
answered 200 with an empty body, `pokkum deploy` reported failure and exited non-zero, and
the rollout had in fact succeeded both times. The image was already pushed, so the operator
is left with a non-zero exit that says nothing about the actual state.

## Recommendation

Keep the strict classifier as the fast path. On a response that is 2xx but unrecognised —
and only then — poll `application.one` and decide from the application's observed state.
Report failure if the poll does not positively confirm a started rollout, so the fail-closed
contract is preserved: the change converts "unknown" from an assumed failure into a
question that gets asked, rather than into an assumed success.

Applies to the unrecognised-2xx case only. A non-2xx status stays a failure with no poll.

## Implementation

- [internal/adapters/deploy/dokploy.go](../../internal/adapters/deploy/dokploy.go)

## Related

- [pokkum deploy (Dokploy, SwiftWave)](paas-deploy-targets.md)
- [pokkum deploy --check](deploy-check.md)

