<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: exe-secret-scan-gap)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# `--strategy=exe` secret-scanning gap

| Field | Value |
| --- | --- |
| Status | open |
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

## Implementation

- [internal/adapters/secretguard/guard.go](../../internal/adapters/secretguard/guard.go)
- [internal/core/pipeline.go](../../internal/core/pipeline.go)

## Related

- [Secret-inlining guard (secretguard)](secret-inlining-guard.md)

