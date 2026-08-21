<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: tarball-output-drops-annotations)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Tarball output silently drops every OCI annotation

| Field | Value |
| --- | --- |
| Status | open |
| Stage | v1.1 |
| Kind | fix |
| Tier | foundation |
| Area | Build & Packaging |

## Summary

pokkum build --output=tarball writes the legacy docker-save format, which has no annotations field, so every annotation Pokkum stamps is lost without warning.

## Problem

`internal/adapters/registry/tarball.go`'s `Write` uses go-containerregistry's
`tarball.MultiWriteToFile`, the legacy `docker save` format. Its manifest entry carries only
`Config`, `RepoTags`, `Layers` and `LayerSources` — there is no annotations field in the
format at all, confirmed by reading the writer's own struct in
go-containerregistry v0.21.9.

Every annotation Pokkum stamps is therefore silently discarded for tarball output:
`pokkum.dev/predecessor`, `pokkum.dev/asset-overlay-sources`, `pokkum.dev/vex-exemptions`,
`pokkum.dev/env-baked` and the `org.opencontainers.image.*` set. Registry push is
unaffected. Nothing warns.

Found while implementing [pokkum verify doesn't reproduce the asset-overlay layer](asset-overlay-verify-gap.md),
whose fix depends on reading one of those annotations back — so for an `--asset-overlay`
image whose only output was a tarball, that fix cannot engage and the old false positive
remains for that output mode specifically.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Write an OCI layout instead | Emit an OCI image layout, which has first-class annotation support, either replacing the docker-save tarball or as a second output mode. | Correct and future-proof, and overlaps with the already-planned --to-oci-layout work — but docker load does not accept an OCI layout, so replacing the current format outright would break anyone piping into Docker. |
| Warn when annotations would be dropped | Keep the format and emit a loud warning listing the annotations being discarded whenever tarball output is used on an image that has any. | Cheap and honest, but leaves the data genuinely lost — it converts a silent failure into a visible one rather than fixing it. |

## Recommendation

Do both, in order: warn now, since a silent loss of provenance metadata is the actual defect, then fold real annotation support into the --to-oci-layout work rather than duplicating it.

## Decision

Warn half shipped 2026-08-19 (cd5c7f0). `Write` in tarball.go now calls a shared
`warnDroppedAnnotations` helper (registry.go) after a successful write, naming every
manifest annotation key actually being discarded, sorted, deterministic across runs. It
is a deliberate no-op when the image carries no annotations at all, so an ordinary build
stays silent. A `pokkum.dev/*` key gets its own clause calling out that other Pokkum
commands (e.g. `pokkum verify` reading `pokkum.dev/asset-overlay-sources`) cannot work
against this output, distinct from the purely descriptive `org.opencontainers.image.*`
set. `--local` shares the same fix: `daemon.Write` (daemon.go) calls `tarball.Write`
internally, so it hits the identical annotations-less docker-save format and now warns
too — beyond this item's original scope but the same root cause, so it was folded in
rather than filed separately.

Still open: the actual defect (no annotations field in the format at all) is unfixed.
Real annotation support remains folded into
[--to-oci-layout for daemonless cluster loading](oci-layout-dev-output.md) per the
recommendation above, rather than duplicated here.

## Implementation

- [internal/adapters/registry/tarball.go](../../internal/adapters/registry/tarball.go)
- [internal/adapters/registry/daemon.go](../../internal/adapters/registry/daemon.go)
- [internal/adapters/registry/registry.go](../../internal/adapters/registry/registry.go)
- [internal/adapters/registry/tarball_test.go](../../internal/adapters/registry/tarball_test.go)
- [internal/adapters/registry/daemon_test.go](../../internal/adapters/registry/daemon_test.go)

## Evidence

- Commits: `cd5c7f0`

## Related

- [pokkum verify doesn't reproduce the asset-overlay layer](asset-overlay-verify-gap.md)
- [--to-oci-layout for daemonless cluster loading](oci-layout-dev-output.md)

