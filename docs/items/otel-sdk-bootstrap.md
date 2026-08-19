<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: otel-sdk-bootstrap)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# OpenTelemetry SDK bootstrap (--telemetry)

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Observability |

## Summary

Starts a real OTel NodeSDK + OTLP trace exporter before the app runs, via a compile-entrypoint wrapper for `--strategy=exe` and a packaged `bun --preload` file for `--strategy=layered`.

## Problem

Two mechanisms depending on strategy, both wired through `pipeline.go` -> `bunexec.Compiler.Prepare`
-> the packager: a compile-entrypoint wrapper (`PrepareVirtualTelemetryEntry`) for `exe`, and a
packaged `bun --preload`'d bootstrap file (`PrepareLayeredTelemetryBootstrap`) for `layered`.
Verified end-to-end for both strategies against a real OTLP export reaching a fake collector,
not just unit-tested.

## Flags

- `--telemetry`
- `--no-telemetry`
- `--otel-export`
- `--telemetry-env`
- `--trace-sample-rate`
- `--metrics-only`

## Implementation

- [internal/adapters/sveltekitutils/telemetry.go](../../internal/adapters/sveltekitutils/telemetry.go)
- [internal/core/pipeline.go](../../internal/core/pipeline.go)

## Known Limitations

- No automatic HTTP/framework instrumentation: `@opentelemetry/auto-instrumentations-node`'s module-patching approach does not take effect under Bun's runtime. Real spans require the documented `hooks.server.ts` snippet, never auto-injected.
- `--metrics-only` is non-functional: combining an OTLP metrics exporter with the SDK crashes once compiled via `bun build --compile` — a real Bun bundler bug, not a Pokkum bug. It warns at runtime rather than silently doing nothing.
- Rejected outright for `--runtime=node` — the layered bootstrap's `bun --preload` mechanism is Bun-specific with no Node equivalent yet.

## Related

- [Reconsider pokkum metrics' shape](metrics-command-shape.md)

