<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: route-exclusion-output-filter)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Exclude prerendered routes from the packaged image

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | moat |
| Area | Build & Packaging |

## Summary

A first phase of route exclusion: --exclude-route / build.exclude_routes drops prerendered routes from the output before packaging, and warns about links left pointing at them.

## Problem

[Exclude routes from the production build](route-exclusion-filter.md) is the full
feature: filter routes before the bundler runs, so a dev-only route's code, its imports
and its SBOM entries never enter the image. That needs a staged `kit.files.routes`
mirror and a measurement pass to justify it, and it was not the immediate need.

The immediate need was narrower and worth having on its own: a prerendered route that
should not be reachable in production — a component gallery, an internal dashboard —
still being served by the shipped image.

## Decision

Shipped 2026-08-22. `--exclude-route` (repeatable) and `build.exclude_routes` in
`.pokkum.yaml` drop matching prerendered files from the output tree after the build and
before packaging. Patterns are route paths, as the parent item recommends: a bare path
covers its subtree, `*` matches within a segment, `**` across segments.

Deletion goes through an `os.Root` scoped to the output tree, and the walk only collects
matches — nothing is removed inside the `WalkDir` callback — so a symlink swapped into
the tree cannot cause a delete outside it.

Links from surviving pages into an excluded route warn rather than fail. Removing the
route is the point of the flag, and failing the build would make the feature unusable
for the case it exists to serve; a silent 404 discovered in production is the outcome
worth preventing, and a warning does that.

A pattern matching no prerendered route is reported rather than ignored — it is either a
typo or a server-rendered route, and in both cases the route the operator asked to
exclude is still in the image.

Verified against a real project's built OCI layout: excluding a route removed its
`index.html` and all three precompressed variants from the packaged layer, and an
unmatched pattern warned instead of passing silently.

## Flags

- `--exclude-route`

## Implementation

- [internal/adapters/routefilterutils/routefilter.go](../../internal/adapters/routefilterutils/routefilter.go)
- [internal/adapters/routefilter/adapter.go](../../internal/adapters/routefilter/adapter.go)
- [internal/ports/routefilter.go](../../internal/ports/routefilter.go)

## Known Limitations

- This filters **output**, not the build. A route's JavaScript chunks, its imports and its SBOM entries still ship — only the prerendered HTML is removed. The size and SBOM-noise win needs the build-time filter in [Exclude routes from the production build](route-exclusion-filter.md), which stays open.
- Server-rendered routes on `--strategy=layered` are compiled into the server bundle and cannot be removed by deleting a file. A pattern naming one is reported as unmatched; it is not excluded.

## Related

- [Exclude routes from the production build](route-exclusion-filter.md)

