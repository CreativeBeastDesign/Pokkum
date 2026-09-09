<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: dokploy-ambiguous-response-poll)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Dokploy: disambiguate an unrecognised 2xx by polling, instead of failing outright

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.2 |
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

## Decision

Reported again from a live deployment on 2026-09-09, this time with the state readable
afterwards: `applicationStatus: "done"`, and a deployment row carrying the exact pushed
digest with `status: "done"`, `errorMessage: null`, finished 4.06s after it started —
while `pokkum deploy` had exited non-zero calling it failed.

`confirmRollout` now resolves an unrecognised 2xx by reading `application.one` and
matching **our** deployment row. Pokkum stamps every deploy it triggers with title
`pokkum` and a description naming the image reference, so a row carrying this build's
reference is evidence about *this* request; `applicationStatus` alone is not, since it is
a property of the application and can reflect somebody else's deploy. A row that is
`done`/`running` passes, one that is `error` fails with the platform's own message
(actionable, unlike "unconfirmed"), and no row, an unreachable endpoint or an unparseable
body all still fail.

Two properties preserved: the strict classifier stays the fast path (a positively
confirmed body costs no extra request), and only a positively observed rollout passes.

This item's own recommendation named an endpoint whose contract nobody here had verified —
the same reason [deploy --check](deploy-check.md) declines to confirm an application id.
So it was verified first, against Dokploy's own router source: `application.one` is a tRPC
`.query()`, not a `.mutation()`, and therefore cannot deploy or restart anything. That
check was the precondition, not a formality.

One detail the live payload forced: `deployments` is **not** in chronological order.
Matching is by image reference, and the no-reference fallback compares `createdAt` rather
than taking the first or last row — pinned by a test whose fixture interleaves decoys.

## Implementation

- [internal/adapters/deploy/dokploy.go](../../internal/adapters/deploy/dokploy.go)
- [internal/adapters/deploy/dokploy_confirm_test.go](../../internal/adapters/deploy/dokploy_confirm_test.go)
- [internal/adapters/deploy/README.md](../../internal/adapters/deploy/README.md)

## Known Limitations

- Applies to the unrecognised-2xx case only. A non-2xx status is still a failure with no poll.
- SwiftWave is unchanged: its ambiguous response (`200 OK - No rebuild`) is already positively identifiable from its body, so there is nothing to disambiguate.

## Related

- [pokkum deploy (Dokploy, SwiftWave)](paas-deploy-targets.md)
- [pokkum deploy --check](deploy-check.md)

