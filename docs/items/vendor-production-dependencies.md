<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: vendor-production-dependencies)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Ship production dependencies so images are self-contained

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | fix |
| Tier | foundation |
| Area | Build & Packaging |

## Summary

Layered images shipped the server bundle without its dependencies; Bun's runtime auto-install silently fetched them from npm inside the running container.

## Problem

Vite externalises dependencies during the SSR build, so adapter-node output keeps bare
imports and expects a node_modules tree beside it — its documented deployment model is
"ship build/ AND production node_modules". Pokkum shipped only the first half.

On Bun the gap was masked rather than visible: the runtime auto-install fetched missing
packages from the public npm registry into the running container, so the app appeared to
work while executing code that is in neither the image, the SBOM, the signature nor the
provenance. Under `pokkum resolve`'s own `readOnlyRootFilesystem` the write failed and
the pod crash-looped after reporting a successful rollout; and because the default
NetworkPolicy permits TCP 443, the fetch succeeds in-cluster until an operator tightens
egress, at which point a working deployment breaks with no change to the image. On Node,
which has no auto-install, any route touching an externalised dependency simply 500s.

## Decision

Shipped 2026-08-21, found by an adversarial field test against a real application.

`Prepare` installs the project's production dependencies into a staging tree under
`.pokkum/` (never touching the user's own tree) and the packager mounts it at
**/app/node_modules**. The path is load-bearing: Node and Bun resolve a bare import by
walking upward from the importing file, so `/app/server/index.js` finds
`/app/node_modules` and never `/app/vendor` — which is where the pre-existing vendor
layer pointed, and which is why populating that layer would not have worked. That layer
turned out to be dead wiring: the pipeline pointed it at `<output>/vendor` and nothing
ever created the directory.

A missing lockfile warns rather than refusing. Making it an error was the first cut and
was over-reach: it turned a reproducibility preference into a gate on building at all
and refused every project without a lockfile, including three of the repo's own
fixtures. With a lockfile the install uses `--frozen-lockfile`, so versions are exactly
what is pinned; hermetic builds add `--offline` and fail with a warm-the-cache hint
rather than silently shipping an image without its dependencies.

The image entrypoint now also passes Bun's `--no-install`, so a remaining gap fails
loudly at startup instead of being papered over by a network fetch. Verified in both
directions: an `await import("valibot")` in an empty project resolves by downloading the
package at runtime, and the same import under `--no-install` fails with "Cannot find
package".

## Implementation

- [internal/adapters/bunexec/vendor_install.go](../../internal/adapters/bunexec/vendor_install.go)
- [internal/adapters/packager/packager.go](../../internal/adapters/packager/packager.go)
- [internal/ports/packager.go](../../internal/ports/packager.go)

## Known Limitations

- Build-time detection of a dependency that will be missing at runtime is **not** solved — see [Detect dependencies that will be missing at runtime](unresolved-import-guard.md). A static-analysis attempt was reverted for failing correctly-configured builds.
- `--runtime=node` images benefit automatically (they previously had no dependency store at all), but that combination has not been re-tested end to end against a real application since the fix.

