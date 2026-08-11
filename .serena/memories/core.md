# Pokkum Core Architecture

Pokkum is a Go-based zero-dependency OCI container image compiler for SvelteKit applications. It compiles multi-arch images (`linux/amd64`, `linux/arm64`) using `@jesterkit/exe-sveltekit` and `bun build --compile`, embedding a PID-1 supervisor (`/pokkum/init`), generating SBOMs, and pushing directly to registries without a Docker daemon.

## Major Domains
- Architecture & Ports: `mem:conventions`
- Tech Stack & Dependencies: `mem:tech_stack`
- OpenTelemetry & Metrics Pipeline: `mem:telemetry`
- Verification & Test Commands: `mem:task_completion`

## Invariants
- Hexagonal architecture: `internal/ports` is the leaf dependency graph node; core never imports adapters.
- Bit-for-bit reproducibility: Timestamps, tar headers, and `kit.version.name` are pinned to `SOURCE_DATE_EPOCH`.
- Zero-config auto-injection: `svelte.config.js` and `instrumentation.server.ts` transforms happen in virtual memory sandboxes without mutating disk sources.
