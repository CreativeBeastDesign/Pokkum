<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: exe-secret-scan-gap)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# `--strategy=exe` secret-scanning gap

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | backlog |
| Kind | hardening |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

The compiled exe strategy's single binary output has no post-build secret scan, unlike layered/static/asset-overlay.

## Problem

`secretguard`'s post-build scan (`internal/core/pipeline.go`'s `runSecretScan`, added
2026-08-18) covers `layered`/`static`/asset-overlay output directories, but a compiled
`--strategy=exe` binary is opaque to text scanning — only its pre-compile input tree is
covered. This is a documented, verified gap, not an implied-covered one.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Scan the compiled binary directly | Extend secretguard to look for the same fixed patterns inside a binary's readable string sections. | Harder — binaries aren't line-oriented text, and compiled string constants may be transformed or split by the compiler, risking both false negatives and noisy false positives. |
| Scan exe's pre-compile intermediate output more thoroughly | Treat exe the same as layered/static by scanning its bundled JS output before `bun build --compile` runs, closing the gap at the same stage the other strategies are covered. | Cheaper and reuses the existing text-scan path, but a secret injected by the compile step itself (rather than present in JS source) would still be missed. |

## Decision

Option B in spirit, shipped 2026-08-19 — but implementing it corrected this item's own
premise. `postBuildScanDirs` already returned `[outputDir]` for `StrategyExe`, so exe's
pre-compile tree was never the uncovered surface.

The genuinely unscanned surface was the compile entrypoint's own directory when it sits
outside `prep.OutputDir`. With `--telemetry`, `PrepareVirtualTelemetryEntry` rewrites
`EntrypointPath` to `<projectDir>/.pokkum/telemetry-entry.ts` and generates
`.pokkum/otel-bootstrap.ts`, both bundled into the binary by `bun build --compile`.
Neither was scanned at either stage: they are written by `Prepare`, so they do not exist
at the pre-build scan, and `secretguard.ScanDirectory` hard-skips `.pokkum` and
`.svelte-kit` subdirectories — though only when `rel != "."`, which is why handing them
in as a scan *root* works. `postBuildScanDirs` now takes the entrypoint path and returns
both trees, deduped, with an unresolvable path resolving to "scan it".

Scanning the compiled binary's string sections was rejected: not line-oriented,
size-unbounded, and compiled constants can be transformed or split, risking both false
negatives and the noisy false positives that get a scanner switched off entirely.

## Implementation

- [internal/adapters/secretguard/guard.go](../../internal/adapters/secretguard/guard.go)
- [internal/core/pipeline.go](../../internal/core/pipeline.go)

## Known Limitations

- exe is **not** at parity with layered/static: a secret injected by the `bun build --compile` step itself — a `bunfig.toml` preload plugin, a `with { type: "macro" }` import — is present in neither scanned tree.
- Covered by unit-level tests through real `core.Build` with the real secretguard adapter, but not yet by a real `bun build --compile` run; that empirical test class has caught nearly every severe bug in Lessons.md and remains the highest-value follow-up here.

## Related

- [Secret-inlining guard (secretguard)](secret-inlining-guard.md)

