<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: deploy-check)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum deploy --check

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.1 |
| Kind | dx |
| Tier | foundation |
| Area | Developer Experience |

## Summary

Validates the deploy configuration through the same resolution a real deploy uses, and deploys nothing.

## Problem

Every `deploy:` answer was unverifiable until the moment it mattered. A wrong endpoint, an
unset `POKKUM_DEPLOY_TOKEN` or a missing `application` surfaced as a failed deploy at the end
of a build, and `pokkum config validate` could not help: it checks field shapes, not the
cross-field resolution the deploy performs.

## Decision

Shipped as step 1 of [the deployment plan](init-deployment-section.md). It calls
`core.ResolveDeployRequest` — the exact call `executeDeploy` makes — rather than
re-implementing the rules. That is the whole design constraint: this repo has already shipped
a validator that disagreed with its consumer (2026-09-01, `config validate` reporting a
config valid that `pokkum deploy` refused), and a `--check` that could do the same would be
worse than none, since its entire promise is that agreeing with it means the deploy resolves.
`TestDeployCheck_AgreesWithTheResolutionItClaimsToPredict` asserts that biconditional
directly over configs on both sides of every rule the resolver applies.

Three outcomes per item, not two. `unverified` is distinct from `ok` and never fails the
command: Pokkum will not claim to have confirmed something it did not test, and will not
treat its own inability to check as the user's error.

What is NOT verified, and why, is stated in the output rather than papered over. The
application id is unconfirmed because every Dokploy endpoint whose contract this repo has
verified against Dokploy's own source mutates — `application.deploy` queues a rollout,
`application.saveDockerProvider` overwrites credentials — and a check must not deploy.
Guessing at an unverified read-only endpoint to make the line say "ok" would be a fabricated
confirmation, which is the shape of the 2026-08-21 `pokkum scan` incident.

The endpoint probe is a bare TCP dial to host:port, never an HTTP request. That is what makes
it safe for a SwiftWave webhook endpoint whose URL PATH is the secret, and it is why the
output reports reachability only. No credential, token or URL is ever printed.

## Flags

- `--check`
- `--offline`

## Implementation

- [cmd/pokkum/deploy_check.go](../../cmd/pokkum/deploy_check.go)
- [cmd/pokkum/deploy_check_test.go](../../cmd/pokkum/deploy_check_test.go)
- [cmd/pokkum/deploy.go](../../cmd/pokkum/deploy.go)

## Known Limitations

- Cannot confirm the credential is accepted or the application exists; both need a read-only endpoint with a verified contract, and neither platform offers one Pokkum has verified.
- The endpoint probe proves the host accepts a connection, not that the panel is running at that path.

## Related

- [A deployment section in pokkum init, and what pokkum deploy would need to earn it](init-deployment-section.md)
- [pokkum deploy (Dokploy, SwiftWave)](paas-deploy-targets.md)

