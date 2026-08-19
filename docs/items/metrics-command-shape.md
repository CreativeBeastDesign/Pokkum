<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: metrics-command-shape)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Reconsider pokkum metrics' shape

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.2 |
| Kind | dx |
| Tier | foundation |
| Area | Observability |

## Summary

Decided by removal: pokkum metrics claimed to run a metrics server, never listened on anything, and is deleted, since it neither runs a server nor exposes anything the way its own text claims.

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

## Recommendation

Option 1 (fold in and remove). Telemetry already works through the build path, so the command added no capability — only a promise. Removing a flag before publication is free; removing it after is a breaking change.

## Decision

Taken 2026-08-19: fold in and remove. `pokkum metrics` and its `--metrics-port` flag are
deleted, along with the now-unused `ports.MetricsOutput`. Vocabulary.md's phantom section for
the command is gone \u2014 it had been a duplicate "9" alongside `pokkum explain`, so removing it
also repaired the section numbering. The 4317/4318/8889 contract is documented where those
ports are actually opened: on the `--with-otel-sidecar` flag, with the point made explicit
that they belong to the injected `otel/opentelemetry-collector-contrib` container and that no
Pokkum binary ever binds them. Option 2 is kept as a separate backlog item rather than
discarded \u2014 see [Supervisor-side /metrics endpoint](supervisor-metrics-endpoint.md).

## Implementation

- [cmd/pokkum/main.go](../../cmd/pokkum/main.go)
- [internal/adapters/k8s/resolver.go](../../internal/adapters/k8s/resolver.go)

## Related

- [OpenTelemetry SDK bootstrap (--telemetry)](otel-sdk-bootstrap.md)
- [Supervisor-side /metrics endpoint](supervisor-metrics-endpoint.md)

