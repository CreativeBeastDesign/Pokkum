<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: app-readiness-endpoint)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# App-controlled readiness (/readyz follows the app's own endpoint)

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Observability |

## Summary

pokkum-init can poll an endpoint the app owns (src/routes/api/ready, auto-detected) so /readyz reflects whether the app can serve, not only whether its port is open.

## Problem

`/readyz` meant "the app port accepts a TCP connection", which is true the moment the server
listens: before migrations finish, before caches are warm, while the database is
unreachable. An app had no way to tell the load balancer "do not send me traffic yet".
`/healthz` was the wrong place for it: a dependency outage that failed liveness would
restart every pod at once, in a loop, and restarting fixes nothing.

## Decision

Taken 2026-10-25: the supervisor asks the app (pull); the app does not push state to the
supervisor. A pushed "not ready" cannot come from an app that has hung, and a mutating
supervisor endpoint would need its own access control. With `POKKUM_READY_PATH` set,
pokkum-init's existing readiness loop sends one GET per tick (800ms limit, redirects not
followed) and `/readyz` is 200 only on 2xx. Probe traffic still never reaches the app,
because `/readyz` answers from the cached result. The build auto-detects
`src/routes/api/ready/+server.(ts|js)` (route groups ignored, a literal `paths.base`
prefixed), and `image.ready_path` / `--ready-path` override it (`off` disables it). With no
route and nothing configured, no variable is written and the image is byte-identical to
before. `/healthz` is unchanged and stays process-only. Verified end to end against a real
image whose endpoint answered 503 while the app served pages.

## Flags

- `--ready-path`

## Implementation

- [supervisor/cmd/pokkum-init/probe.go](../../supervisor/cmd/pokkum-init/probe.go)
- [supervisor/cmd/pokkum-init/config.go](../../supervisor/cmd/pokkum-init/config.go)
- [internal/adapters/sveltekitutils/readyroute.go](../../internal/adapters/sveltekitutils/readyroute.go)
- [cmd/pokkum/build.go](../../cmd/pokkum/build.go)

## Known Limitations

- A `paths.base` that is not a string literal (e.g. read from `process.env`) cannot be evaluated at build time. Auto-detection then steps aside with a warning, and `image.ready_path` must give the full path.
- Static images (pokkum-static) have no application to ask, so they never use a readiness path.

## Related

- [Supervisor-side /metrics endpoint](supervisor-metrics-endpoint.md)

