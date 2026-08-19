<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: lifecycle-script-provenance)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Lifecycle-script execution provenance

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | feature |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

Record which packages actually ran install-time lifecycle scripts during the build as a provenance field.

## Problem

Bun blocks `postinstall` for untrusted dependencies and requires `trustedDependencies`, but
Pokkum does not currently emit which packages ran lifecycle scripts during a given build.
Verified against the code: `trustedDependencies`/`postinstall` only appear as an example in
a hermetic-sandbox doc comment (`internal/adapters/bunexec/hermetic_linux.go`), not as
anything recorded into the SLSA statement. This would be a cheap provenance field nobody
else publishes, answering "what build-time code actually ran" directly.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Parse Bun's lifecycle-script log into a resolvedDependencies annotation | Capture which packages Bun actually ran scripts for during install and add them as named entries in the SLSA statement. | Depends on Bun's install output being stable enough to parse reliably across versions. |

## Implementation

- [internal/adapters/bunexec/hermetic_linux.go](../../internal/adapters/bunexec/hermetic_linux.go)

