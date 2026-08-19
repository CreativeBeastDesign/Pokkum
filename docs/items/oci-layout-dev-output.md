<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: oci-layout-dev-output)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# --to-oci-layout for daemonless cluster loading

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | dx |
| Tier | polish |
| Area | Developer Experience |

## Summary

Emit an OCI layout on disk and load it directly into kind/k3d/minikube, for contributors and CI environments with no Docker/Podman daemon at all.

## Problem

Adjacent to [pokkum dev --cluster](cluster-dev-loop.md) but structurally simpler and cheaper
to build — it doesn't touch the in-pod sync problem, it just gives daemonless environments a way
to get a built image into a local cluster without a registry round-trip. Ordered after the
cluster dev loop because it is a hermetic-CI nicety, not a faster inner loop, but it's cheap
enough to pick up independently.

## Flags

- `--to-oci-layout`

## Related

- [pokkum dev --cluster](cluster-dev-loop.md)

