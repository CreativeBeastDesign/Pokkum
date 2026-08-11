# Pokkum Core Architecture

Pokkum is a Go-based zero-dependency OCI container image compiler for SvelteKit applications. It compiles multi-arch images (`linux/amd64`, `linux/arm64`) using `@jesterkit/exe-sveltekit` and `bun build --compile`, embedding a PID-1 supervisor (`/pokkum/init`), generating SBOMs, and pushing directly to registries without a Docker daemon.

## Major Domains
- Architecture & Ports: `mem:conventions`
- Tech Stack & Dependencies: `mem:tech_stack`
- OpenTelemetry & Metrics Pipeline: `mem:telemetry`
- Verification & Test Commands: `mem:task_completion`

## Invariants
- Hexagonal architecture: `internal/ports` is the leaf dependency graph node; core never imports adapters.
- Bit-for-Bit reproducibility: Timestamps, tar headers, base image digests (`pokkum.lock`), and `kit.version.name` are pinned to `SOURCE_DATE_EPOCH`.
- Zero-config auto-injection: `svelte.config.js` and `instrumentation.server.ts` transforms happen in virtual memory sandboxes without mutating disk sources.
- Base Image Lockfile: `pokkum.lock` automatically resolves and pins base image digests to guarantee reproducible builds across machines and time (`--update-base`, `--offline`, `pokkum base update`).
- Supply chain security: SLSA v1.0 provenance, Cosign digests, and DSSE envelopes are automatically generated and signed during `pokkum build` pipeline unless `--no-sign` is provided.
- SBOM Attachment: SBOMs are attached by default as OCI 1.1 referrers (`--sbom-attach=referrer`), with legacy `.sbom` tag fallback (`--sbom-attach=tag`).
- Hot-Reload Dev Environment: `pokkum dev` builds, loads into Docker daemon, runs, and watches source files for auto-rebuilding with interactive shell debugging (`--debug`).