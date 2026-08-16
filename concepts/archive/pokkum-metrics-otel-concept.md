# Concept: Unified Metrics & Telemetry for Pokkum (`metrics-and-telemetry-concept.md`)

## 1. Goal & Problem Statement

Pokkum's `AdditionalFeatures.md` currently lists "Built-in Metrics Endpoint" and "OpenTelemetry
Auto-Instrumentation" as two separate features with separate dependencies (a Prometheus client
for metrics, a custom `--with-otel` SDK injection for traces). This concept replaces both with a
single, unified observability feature built on SvelteKit's native OpenTelemetry support
(available since SvelteKit 2.31), delivered through Pokkum's existing zero-config injection
pipeline rather than as a bolted-on CLI flag.

### Objective

A developer running `pokkum build ./my-app` gets working traces and metrics out of the box —
without installing OTEL packages, writing `instrumentation.server.ts`, or editing
`svelte.config.js` — unless they explicitly opt out.

---

## 2. Why Unify Instead of Splitting Metrics and Traces

- SvelteKit only emits OpenTelemetry **spans** (traces), not Prometheus-style metrics, natively.
  Metrics (request duration, error rate, GC pauses, event loop lag) require the separate OTEL
  Metrics API/SDK — they are not a byproduct of tracing.
- Both signal types share the same OTLP transport and the same instrumentation entry point
  (`instrumentation.server.ts`), so it makes sense to configure them together in one injection
  pass instead of two independent features with two independent maintenance surfaces.
- Splitting them, as originally scoped, means Pokkum owns two custom instrumentation code paths
  instead of one thin injection shim around an upstream API SvelteKit already maintains.

---

## 3. Architecture: Reusing the Injection Pipeline

Pokkum already has a Virtual Build Sandbox & Interception Pipeline for adapter injection and
`SOURCE_DATE_EPOCH` pinning (see `pokkum-injection-concept.md`). Extend it with a third injection
target:

```
                 Pokkum Build Request
                          │
                          ▼
              Config Inspection (svelte.config.js)
                          │
          ┌───────────────┼────────────────┐
          ▼               ▼                ▼
   Adapter Injection  Version Pinning  Observability Injection (NEW)
          │               │                │
          └───────────────┴────────┬───────┘
                                    ▼
                     Virtual Config / AST or Loader-Hook Pass
                                    │
                                    ▼
                    bun run build (injected env + temp config)
```

### 3.1 Config Injection

Using the same AST transform (Option 1) or module loader hook (Option 2) already designed for
adapter swapping, inject into `svelte.config.js`:

```javascript
kit: {
  experimental: {
    tracing: { server: true },
    instrumentation: { server: true }
  }
}
```

### 3.2 `instrumentation.server.ts` Injection

If the project does not already have one, Pokkum writes (virtually, not to disk) a default
`src/instrumentation.server.ts`:

```javascript
import { NodeSDK } from "@opentelemetry/sdk-node";
import { getNodeAutoInstrumentations } from "@opentelemetry/auto-instrumentations-node";
import { OTLPTraceExporter } from "@opentelemetry/exporter-trace-otlp-proto";
import { PeriodicExportingMetricReader } from "@opentelemetry/sdk-metrics";
import { OTLPMetricExporter } from "@opentelemetry/exporter-metrics-otlp-proto";
import { createAddHookMessageChannel } from "import-in-the-middle";
import { register } from "node:module";

const { registerOptions } = createAddHookMessageChannel();
register("import-in-the-middle/hook.mjs", import.meta.url, registerOptions);

const sdk = new NodeSDK({
  serviceName: process.env.POKKUM_SERVICE_NAME ?? "pokkum-app",
  traceExporter: new OTLPTraceExporter({
    url: process.env.POKKUM_OTLP_TRACES_ENDPOINT,
  }),
  metricReader: new PeriodicExportingMetricReader({
    exporter: new OTLPMetricExporter({
      url: process.env.POKKUM_OTLP_METRICS_ENDPOINT,
    }),
  }),
  instrumentations: [getNodeAutoInstrumentations()],
});

sdk.start();
```

### 3.3 Dependency Bundling

Bundle the required OTEL packages (`@opentelemetry/sdk-node`, `auto-instrumentations-node`,
`exporter-trace-otlp-proto`, `sdk-metrics`, `exporter-metrics-otlp-proto`, `import-in-the-middle`)
the same way `@jesterkit/exe-sveltekit` is embedded via `embed.FS`, writing them into
`.pokkum/node_modules/` at build time if missing — no `bun add` required in the target repo.

### 3.4 Export Target

Metrics and traces both go out via OTLP to a collector, not directly to Prometheus. A sidecar
OTEL Collector (deployed as part of Pokkum's Kubernetes manifests) receives OTLP and exposes a
Prometheus-scrapeable `/metrics` endpoint downstream. This keeps the app container itself free of
a Prometheus client library, preserving the minimal distroless footprint.

---

## 4. CLI Surface

```bash
pokkum build ./my-app                         # tracing + metrics on by default (configurable)
pokkum build ./my-app --no-telemetry          # opt out entirely
pokkum build ./my-app --otel-export=<url>     # override OTLP endpoint
pokkum build ./my-app --telemetry-env=dev     # enable only in dev/preview builds
```

---

## 5. Best Practices

- Default to tracing/metrics **enabled in dev and preview builds, opt-in for production** —
  SvelteKit's own docs warn that instrumentation overhead is nontrivial and recommend evaluating
  whether you need it in production at all.
- Ship a sane default OTLP endpoint (e.g. `localhost:4318` for local collector) and make
  production endpoints configurable via env var, never hardcoded.
- Keep the injected `instrumentation.server.ts` overridable: if the project already has one,
  Pokkum should skip injection and respect the user's file, not overwrite it silently.
- Tag every span/metric with build-level metadata Pokkum already knows (image digest, git commit,
  `SOURCE_DATE_EPOCH`) so traces are correlatable back to a specific reproducible build.
- Co-locate the OTEL Collector sidecar with the app in the same Kubernetes manifest generation
  Pokkum already produces, rather than requiring separate manual setup.

---

## 6. Potential Optimisations

- **Sampling**: inject a tail-based or probability sampler config so high-traffic apps don't pay
  full tracing overhead per request; expose `--trace-sample-rate` as a CLI flag.
- **Lazy SDK init**: only initialize the OTEL SDK if an OTLP endpoint is actually reachable/set,
  avoiding dead weight when telemetry is configured but unused.
- **Shared collector across services**: for monorepo/multi-service deployments, generate one
  collector sidecar/service per cluster instead of per pod to reduce resource duplication.
- **Metrics-only mode**: allow disabling trace spans while keeping OTEL metrics active, for teams
  that want dashboards but not full distributed tracing overhead.
- **Binary size**: since OTEL SDK dependencies are bundled into the build sandbox rather than the
  final compiled binary where possible, verify via `pokkum explain` (per your existing "Diff &
  Explain" feature) that they don't leak into the shipped image unnecessarily.

---

## 7. Caveats (Read Before Building)

- **Experimental API risk**: `kit.experimental.tracing.server` and
  `kit.experimental.instrumentation.server` are explicitly experimental, "not subject to semantic
  versioning," and can break or disappear without notice. Baking this into a tool whose core
  value proposition is reproducible, stable builds is a real tension — pin the exact SvelteKit
  version Pokkum supports and test on every SvelteKit release, not just at initial implementation.
- **Adapter compatibility is unverified**: `instrumentation.server.ts` is "guaranteed to run prior
  to your application code being imported, providing your deployment platform supports it and
  your adapter is aware of it." Pokkum uses a custom adapter (`@jesterkit/exe-sveltekit`) compiled
  via `bun build --compile` into a single binary — this execution-order guarantee must be tested
  explicitly against that adapter, not assumed to hold.
- **Two injection mechanisms coexisting**: adapter injection and observability injection both
  patch `svelte.config.js` (and potentially both need loader hooks). They need integration tests
  to confirm they don't conflict when both run in the same build pass.
- **`@opentelemetry/api` is an optional peer dependency** of SvelteKit — if your bundling omits it
  under certain conditions, users may see a confusing "can't find `@opentelemetry/api`" error even
  when telemetry is intentionally disabled.
- **Overhead is real, not hypothetical**: SvelteKit's docs explicitly call out nontrivial overhead
  from instrumentation — don't default this to "always on" in production without benchmarking
  actual latency/memory impact on a representative app first.
- **Silent config overwrites**: if injection logic isn't careful about detecting existing
  `instrumentation.server.ts` or existing `experimental.tracing`/`instrumentation` flags, it risks
  clobbering a user's manual OTEL setup — this needs explicit precedence rules, not just
  overwrite-by-default.
- **Collector as a new operational dependency**: this design assumes users run (or Pokkum
  deploys) an OTEL Collector. That's a new piece of infrastructure Pokkum implicitly becomes
  responsible for documenting, templating, and keeping compatible — scope this explicitly rather
  than treating it as "someone else's problem."

### 8. Important

- Default to off
- Detect existing `instrumentation.server.ts` before injecting
