<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: oci-layout-dev-output)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# --to-oci-layout for daemonless cluster loading

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | dx |
| Tier | polish |
| Area | Developer Experience |

## Summary

Writes a standards-conformant OCI image layout to a directory — no daemon, no registry — preserving every annotation and the full multi-platform index, ready for `ctr images import` / kind / k3d / minikube.

## Problem

Adjacent to [pokkum dev --cluster](cluster-dev-loop.md) but structurally simpler and cheaper
to build — it doesn't touch the in-pod sync problem, it just gives daemonless environments a way
to get a built image into a local cluster without a registry round-trip. Ordered after the
cluster dev loop because it is a hermetic-CI nicety, not a faster inner loop, but it's cheap
enough to pick up independently.

It also carries the second half of
[Tarball output silently drops every OCI annotation](tarball-output-drops-annotations.md),
whose recommendation was to warn first and then fold real annotation support into this work
rather than duplicating it. The two existing local output modes both go through the legacy
docker-save format, which has neither an annotations field nor any representation of a manifest
list, so `--tarball` silently discarded every `org.opencontainers.image.*` and `pokkum.dev/*`
annotation and flattened a multi-platform build into one platform-suffixed tag per child. An
OCI image layout has first-class support for both, which makes this the lossless local output.

## Decision

Implemented against go-containerregistry's own `pkg/v1/layout` rather than hand-rolled, so
the on-disk result is whatever that library — and therefore crane, skopeo and containerd —
already agree an OCI layout is.

Four decisions worth recording. (1) `index.json` holds one descriptor per requested tag
pointing at the payload's own top-level blob, which is the shape `crane pull --format=oci`
produces; writing the payload index *as* `index.json` would have left nowhere to record the
tag. (2) Each descriptor carries two name annotations, not one, because the consumers this
mode exists to serve disagree: `org.opencontainers.image.ref.name` (bare tag) is what
skopeo's `oci:` transport and podman match on, `io.containerd.image.name` (fully qualified)
is what containerd's archive importer reads first — and therefore what `ctr images import`,
`k3d image import` and `minikube image load` see. (3) A single-image descriptor gets its
platform read off the image's own config, since `partial.Descriptor` never populates it and
an index child is not there to carry it; an index descriptor deliberately does not, because
claiming one platform for a multi-platform artefact would be wrong. (4) The layout is
assembled in a staging directory and swapped into place, so an interrupted run leaves either
the previous layout or nothing — never an `index.json` referencing blobs that were never
written — and a re-run replaces rather than merges, so a repeated dev loop cannot accumulate
orphaned blobs.

The three destination flags (`--local`, `--tarball`, `--to-oci-layout`) are mutually
exclusive, following the wording the pre-existing `--local`/`--tarball` check established.
`--require-signed` still rejects this mode along with the other two: signatures live in a
registry, keyed to a pushed digest, and a layout on disk has nothing to attach them to.

## Flags

- `--to-oci-layout`

## Implementation

- [internal/adapters/registry/ocilayout.go](../../internal/adapters/registry/ocilayout.go)
- [internal/adapters/registry/ocilayout_test.go](../../internal/adapters/registry/ocilayout_test.go)
- [internal/ports/registry.go](../../internal/ports/registry.go)
- [internal/core/model.go](../../internal/core/model.go)
- [internal/core/pipeline.go](../../internal/core/pipeline.go)
- [cmd/pokkum/build.go](../../cmd/pokkum/build.go)

## Known Limitations

- `docker load` does not accept an OCI image layout, so this mode does not replace `--tarball` — anyone piping into Docker still needs the docker-save format, which is exactly why the lossy mode was kept alongside rather than fixed in place.
- SBOM, signature and attestation attachment still happen only for a registry push: they are separate manifests keyed to a pushed digest, and the layout carries the image alone. A layout build reports itself as unsigned rather than silently claiming otherwise.
- `--asset-overlay` auto-discovery still requires a registry push. The layout preserves the `pokkum.dev/predecessor` annotation, but walking a lineage backwards means fetching predecessors, which a directory on disk cannot serve.

## Related

- [pokkum dev --cluster](cluster-dev-loop.md)
- [Tarball output silently drops every OCI annotation](tarball-output-drops-annotations.md)

