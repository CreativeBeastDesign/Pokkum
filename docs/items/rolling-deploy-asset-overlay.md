<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: rolling-deploy-asset-overlay)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Rolling-deploy asset overlay (--asset-overlay)

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | moat |
| Area | Build & Packaging |

## Summary

Merges the last N generations' immutable /_app/immutable client assets into a separate overlay layer, registry-side, so a browser holding a prior generation's HTML never hits a 404 mid-rollout.

## Flags

- `--asset-overlay`
- `--asset-overlay-from`

## Implementation

- [internal/ports/assetoverlay.go](../../internal/ports/assetoverlay.go)
- [internal/adapters/assetoverlay](../../internal/adapters/assetoverlay)
- [internal/core/pipeline.go](../../internal/core/pipeline.go)
- [internal/adapters/packager/packager.go](../../internal/adapters/packager/packager.go)
- [tests/integration/asset_overlay_e2e_test.go](../../tests/integration/asset_overlay_e2e_test.go)

## Evidence

- Commits: `f9c2f1d`

## Known Limitations

- pokkum verify's rebuild-and-compare path does not reproduce this layer — see [pokkum verify doesn't reproduce the asset-overlay layer](asset-overlay-verify-gap.md).
- Lineage discovery is registry-side via a pokkum.dev/predecessor manifest annotation, deliberately independent of Kubernetes' pokkum.dev/image-history — a build-time flag cannot depend on cluster state without coupling build to Kubernetes. The annotation is only stamped when --asset-overlay is actually in use, so auto-discovery can only find a chain that opted in from the start of a rollout sequence.
- Requires --output=push for auto-discovery (there is no current tag to inspect for --local/--tarball); --asset-overlay-from's explicit refs work regardless of output mode.

