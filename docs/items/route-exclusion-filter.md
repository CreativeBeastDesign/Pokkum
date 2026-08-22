<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: route-exclusion-filter)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Exclude routes from the production build

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | moat |
| Area | Build & Packaging |

## Summary

Dev-only routes are bundle entry points, so tree-shaking cannot remove them; a build-time filter would keep their code out of the image entirely — which output filtering cannot do.

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

## Decision

Shipped 2026-08-22, taking the first option after proving it against real builds rather
than reasoning about it. Justified on developer experience rather than size: the
measurement below came out modest, and the reason to have it is being able to keep as
many `/dev/` routes in a project as you like knowing none of them ship.

`kit.files.routes` is pointed at `.pokkum/routes`, a mirror of the routes directory
built from symlinks with the excluded routes left out. Symlinks rather than copies are
required for correctness: Vite resolves a symlinked module to its real path, so a
route's relative import that escapes the routes tree (`../../lib/thing.js`) still
resolves. A copied tree breaks every one of those.

There is no supported way to point SvelteKit at a different config file — no
`SVELTE_CONFIG_PATH`, no CLI flag, and Kit sets vite-plugin-svelte's `configFile: false`
unconditionally. The supported override is passing config inline to the `sveltekit()`
plugin, which Pokkum already does for adapter injection and the version pin; routes are
one more injected key, applied as a wrapper so the object is evaluated once and any
`files` the project set survives.

Three guards, each from an observed failure rather than a precaution:

- `resolve.preserveSymlinks: true` is refused. It makes every escaping relative import
  fail to resolve — reproduced as a real `UNRESOLVED_IMPORT` build failure.
- A partially excluded directory is recreated with **all** its surviving entries,
  layouts included. A mirror that dropped `admin/+layout.svelte` while keeping
  `/admin/panel` built cleanly, served the page wrapped in the *root* layout, and warned
  about nothing. SvelteKit cannot detect it, so the mirror has to be right.
- SvelteKit below 2.62.0 cannot take config inline, so exclusion falls back to output
  filtering with a warning instead of editing the user's `svelte.config.js`.

Verified end to end on a real layered build: a `/dev` route's marker string appears 3
times in the image built without the flag and 0 times with it, its server chunk
(`app/server/.../entries/pages/dev/_page.svelte.js`) is gone, and the kept routes are
untouched in both.
Measurement, run before building it as this item asked. Three builds of a real project (static
strategy): baseline, output filtering only, and the two demo routes deleted from source
as a stand-in for build-time exclusion.

| | prerendered | client JS/CSS | image (compressed) |
|---|---|---|---|
| baseline | 384 KB | 6912 KB | 10196 KB |
| output filter only | 256 KB | 6912 KB | 10056 KB (-1.4%) |
| routes removed | 256 KB | 6272 KB | 9924 KB (-2.7%) |

So build-time exclusion is worth 132 KB more than output filtering on that project —
1.3% of the image. The premise this item was written on ("a style guide imports the
entire component library, so it can be one of the largest things in the image") did not
hold there, and the reason generalises: that project is itself a component-library
showcase, so every route imports the library and it cannot be dropped by removing two of
five routes. The size win is real only where an excluded route is the *sole* consumer of
something heavy. Built anyway, for the DX.

## Flags

- `--exclude-route`

## Implementation

- [internal/adapters/routefilterutils/mirror.go](../../internal/adapters/routefilterutils/mirror.go)
- [internal/adapters/bunexec/route_mirror.go](../../internal/adapters/bunexec/route_mirror.go)
- [internal/adapters/sveltekitutils/injector.go](../../internal/adapters/sveltekitutils/injector.go)

## Known Limitations

- The mirror filters the route graph, not the filesystem. A surviving route that reaches into an excluded directory by relative import still pulls that module into the bundle — the route entry points are excluded, arbitrary modules living under the excluded path are not.
- Falls back to output filtering (prerendered page removed, code still shipped) whenever Pokkum does not author the Vite config: a build script that does more than `vite build`, a bare `sveltekit()` whose options live in svelte.config.js, or SvelteKit below 2.62.0. The log says which mechanism applied.
- SvelteKit prints `svelte.config.js is ignored when options are passed via your Vite config` when config is passed inline. Cosmetic, but user-visible, and pre-existing for adapter injection.

## Related

- [`--strategy=exe` secret-scanning gap](exe-secret-scan-gap.md)
- [Exclude prerendered routes from the packaged image](route-exclusion-output-filter.md)

