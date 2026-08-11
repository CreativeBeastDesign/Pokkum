# OpenTelemetry & Unified Metrics Architecture

Pokkum unifies trace spans and metrics into a native, zero-config OpenTelemetry pipeline built on SvelteKit 2.31+ native observability (`kit.experimental.tracing.server` and `kit.experimental.instrumentation.server`).

## Key Subsystems
- **Version Inspector (`internal/adapters/sveltekit/project.go`)**: Parses `@sveltejs/kit` version from `package.json` (`CheckTelemetrySupported`). Skips telemetry if version < 2.31.0.
- **Single-Pass Config Transformer (`internal/adapters/sveltekit/injector.go`)**: Updates `svelte.config.js` in a single virtual pass (`TransformConfig`), combining adapter replacement, `SOURCE_DATE_EPOCH` version pinning, and `kit.experimental` telemetry flag injection.
- **Strict Precedence & Virtual Instrumentation (`internal/adapters/sveltekit/telemetry.go`)**: Checks `InstrumentationExists` for `src/instrumentation.server.ts|js|mjs`. If present, user file is preserved. If missing, writes virtual `.pokkum/src/instrumentation.server.ts` configured with lazy SDK init, trace sampling (`--trace-sample-rate`), metrics-only mode (`--metrics-only`), and OTLP exporters.
- **OTEL Collector K8s Sidecar (`internal/adapters/k8s/resolver.go`)**: When `--with-otel-sidecar` is set, `pokkum resolve` and `pokkum apply` inject an OpenTelemetry Collector sidecar container spec (`4317` gRPC, `4318` HTTP, `8889` metrics) into Pod manifests.
- **CLI Options (`cmd/pokkum/build.go`)**: Exposes `--telemetry`, `--no-telemetry`, `--otel-export`, `--telemetry-env`, `--trace-sample-rate`, `--metrics-only`, `--with-otel-sidecar`.
