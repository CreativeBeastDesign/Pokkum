<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: native-addon-provenance)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Native addon (.node binary) provenance

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | feature |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

Hash and record prebuilt native addon binaries in provenance instead of leaving them outside the SBOM/SLSA story entirely.

## Problem

Packages like `sharp` and `better-sqlite3` ship prebuilt `.node` binaries fetched outside
the npm tarball integrity model; Syft will not meaningfully catalog them, and Pokkum's own
`nativeinspect`/`striputils` adapters currently strip their symbols for size but do not hash
or attest them. Verified against the code: neither package computes a digest or records
anything into provenance — this is the one class of build artifact the existing SBOM/SLSA
story genuinely cannot see, not partially covered.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Hash at strip time, fold into SLSA resolvedDependencies | Compute a SHA-256 for each discovered .node/.so during the existing nativeinspect walk and add it as a ResourceDescriptor, flagging any addon whose bytes aren't covered by a lockfile tarball. | Small, additive change reusing an existing file walk; doesn't address why the addon bypassed the integrity model in the first place. |

## Implementation

- [internal/adapters/nativeinspect/closured.go](../../internal/adapters/nativeinspect/closured.go)
- [internal/adapters/striputils/striputils.go](../../internal/adapters/striputils/striputils.go)

