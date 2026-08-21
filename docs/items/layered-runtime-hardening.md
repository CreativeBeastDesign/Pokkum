<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: layered-runtime-hardening)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Layered-strategy runtime hardening (stub launcher + startup attestation)

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | hardening |
| Tier | foundation |
| Area | Build & Packaging |

## Summary

Two composable mitigations for stock Bun's full CLI attack surface in a layered image: a non-foldable compiled entrypoint launcher, and a supervisor-verified startup digest over the /app tree.

## Flags

- `--stub-launcher`
- `POKKUM_STUB_LAUNCHER`
- `POKKUM_ATTESTATION_DIGEST`

## Implementation

- [internal/adapters/bunexec/compiler.go](../../internal/adapters/bunexec/compiler.go)
- [internal/adapters/packager/packager.go](../../internal/adapters/packager/packager.go)

## Evidence

- Commits: `fb23335`

## Known Limitations

- Startup attestation only exists for --strategy=layered; --strategy=exe and --strategy=static don't attest.
- The attested root set lives in two places that cannot share a value: ports.AttestationRoots and pokkum-init's hand-copied attestRoots (the supervisor must not import ports). Adding /app/node_modules to the packager's manifest without updating the mirror made every layered image exit 125 at startup, with the full test suite green — the build hashed 11762 files while the runtime could find 509. Both lists now carry node_modules, and TestAttestationRoots_MatchSupervisorMirror parses the supervisor's declaration with go/ast and fails on any divergence; TestAttestation_StampedDigestMatchesImageFilesystem re-derives the digest by replaying a real built image's layers rather than from the packager's own directory table, which had the same omission and so agreed with the bug.

