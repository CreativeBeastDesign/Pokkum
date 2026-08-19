<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: metrics-command-shape)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Reconsider pokkum metrics' shape

| Field | Value |
| --- | --- |
| Status | awaiting-decision |
| Stage | v1.2 |
| Kind | dx |
| Tier | foundation |
| Area | Observability |

## Summary

Decide whether pokkum metrics should become the honest thing it currently only sounds like, since it neither runs a server nor exposes anything the way its own text claims.

## Problem

`pokkum metrics` reads like a client-side scraper rather than a server the image exposes, and
verifying it against the code shows the gap is more than an ergonomics complaint: `runMetrics`
(`cmd/pokkum/metrics.go`) parses `--metrics-port`, prints "Metrics pipeline ready. Press Ctrl+C
to stop.", and then returns — there is no `http.ListenAndServe`, `net.Listen`, or any blocking
call anywhere in the command. It reports `Status: "active"` and `UptimeSeconds: 0` in its JSON
output unconditionally. Nothing is actually listening on the port the command names. Separately,
the supervisor-level `/metrics` endpoint this command's description implies (`pokkum-init`
registers only `/livez` and `/readyz`) does not exist either — neither Bun runtime metrics
(event-loop lag, heap) nor cgroup limits are exported from PID 1. The app-side OTel metrics
pipeline itself (see [OpenTelemetry SDK bootstrap](otel-sdk-bootstrap.md)) is real; this
command's own claim about what it does is not.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Fold into build flags, document the port contract, remove the standalone command | Drop the pretense of a running server and document `--metrics-port`/the OTel metrics export contract as build-time flags instead, matching what the app-side pipeline actually does. | Cheapest option and the one the roadmap's scope-discipline pass already leans toward, but removes a command name users may already reference in scripts. |
| Implement a real supervisor-side /metrics listener | Give `pokkum-init` an actual `/metrics` HTTP handler and make `pokkum metrics` a thin client against it, so the command's text stops overclaiming. | Closes the gap honestly, but is real new supervisor surface (Bun runtime metrics, cgroup limits) for a command currently scoped as a CLI nicety, not a monitoring server. |

## Flags

- `--metrics-port`

## Implementation

- [cmd/pokkum/metrics.go](../../cmd/pokkum/metrics.go)

## Related

- [OpenTelemetry SDK bootstrap (--telemetry)](otel-sdk-bootstrap.md)

