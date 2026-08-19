<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: build-environment-capture)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Build-environment capture (Go + SvelteKit versions)

| Field | Value |
| --- | --- |
| Status | open |
| Stage | v1.1 |
| Kind | feature |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

Round out toolchain provenance by also recording the SvelteKit version used, alongside Go and Bun which are already captured.

## Problem

Verified against the code, this item is half-done, not fully open as the original roadmap
framing implied: the Go toolchain version IS already recorded end-to-end
(`runtime.Version()` flows through `ports.SLSAToolchain.GoVersion` into a
`pkg:generic/go@<version>` resolvedDependency in `internal/adapters/slsa/generator.go`).
What is genuinely missing is the resolved `@sveltejs/kit` version: it is tracked as
`ports.PreflightResult.SvelteKitVersion` and surfaces in build logs
(`internal/core/pipeline.go`), but `ports.SLSAToolchain` has no field for it and the SLSA
generator never emits it as a resolvedDependency, unlike Bun and Go.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Add SvelteKitVersion to SLSAToolchain | Thread the already-resolved PreflightResult.SvelteKitVersion into ports.SLSAToolchain and emit it as a pkg:npm/@sveltejs/kit@<version> resolvedDependency, mirroring the existing Go/Bun entries. | Small, mechanical addition — the value is already resolved elsewhere, this is purely wiring. |

## Recommendation

Add SvelteKitVersion to SLSAToolchain — the value already exists, this closes a real but narrow gap.

## Implementation

- [internal/adapters/slsa/generator.go](../../internal/adapters/slsa/generator.go)
- [internal/core/pipeline.go](../../internal/core/pipeline.go)
- [internal/ports/supplychain.go](../../internal/ports/supplychain.go)

