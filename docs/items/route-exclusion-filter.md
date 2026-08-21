<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: route-exclusion-filter)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Exclude routes from the production build

| Field | Value |
| --- | --- |
| Status | open |
| Kind | feature |
| Tier | moat |
| Area | Build & Packaging |

## Summary

Dev-only routes are bundle entry points, so tree-shaking cannot remove them; a build-time filter would keep them out of the image entirely.

## Problem

A project with dev-only routes — `/dev/style-guide`, a component gallery, a fixtures
browser — ships them to production. They are `+page.svelte` files, which makes them
bundle **entry points**, and no amount of tree-shaking removes an entry point:
reachability is its definition. Everything they import ships with them, and a style
guide typically imports the entire component library on purpose, so it can be one of
the largest things in the image.

SvelteKit offers no way to exclude them. Checked against kit 2.70.2's
`src/core/config/options.js`: there is `moduleExtensions` and `files.*`, and nothing
that drops routes. The community answer is a runtime guard — a `+layout.server.ts`
that throws 404 unless `dev` — which controls *access* while leaving the code, its
imports, and its dependencies in the image and in the SBOM.

Note what is NOT a problem, since it is the common worry: colocated test files are
already safe. SvelteKit's routes walker skips any file not starting with `+`
(`create_manifest_data/index.js:237`), so `foo.test.ts` beside a `+page.svelte` is
invisible to the router. It only ships if route code imports it.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Stage a filtered routes tree and point kit.files.routes at it | Mirror src/routes into .pokkum/routes as symlinks, omitting excluded paths, and set kit.files.routes to the mirror. Uses a supported config option (options.js:158) and mutates nothing the user wrote — the same shape as the existing vite-config injection. | Relies on symlink resolution behaving: relative imports escaping the routes tree (`../../lib/x`) resolve from the link's real path under Vite's default realpath handling, which must be proven before trusting it. Excluding a parent whose children inherit its +layout must be rejected rather than silently breaking, and links to excluded routes become dead — worth a build-time warning. |
| A Vite plugin that resolves excluded route modules to empty | Intercept resolution and return an empty module for matching route files. | Cheaper to write, but wrong in a subtle way: SvelteKit's manifest is generated independently of Vite resolution, so the routes still exist and would render blank rather than be absent. Trades a size win for a confusing runtime surface. |
| Document the runtime guard and ship nothing | Point users at the `+layout.server.ts` 404 guard. | Zero cost and correct for access control, but leaves the code, its imports and its SBOM entries in the image — which is the part Pokkum exists to remove. |

## Recommendation

First option, but measure before building it. Build a real project twice, once with the
dev routes present and once with the directory removed, and compare image size and SBOM
package counts. If a style guide really does drag in the whole component library the
delta justifies the feature on size alone; if it is small, the honest answer is that the
runtime guard covers most of the value and this belongs lower down.

Whatever the size result, the attack-surface and SBOM-noise arguments stand on their own:
a dev route that reaches privileged endpoints is worth not shipping at all, rather than
shipping behind a guard that one refactor can remove.

Config shape should name route paths rather than file globs (`exclude_routes: ["/dev/**"]`),
since that is how the user thinks about them, and the guard test should assert the produced
manifest lacks those routes rather than asserting on the filter's own bookkeeping.

## Related

- [`--strategy=exe` secret-scanning gap](exe-secret-scan-gap.md)

