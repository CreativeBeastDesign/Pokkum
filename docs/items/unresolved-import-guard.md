<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: unresolved-import-guard)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Detect dependencies that will be missing at runtime

| Field | Value |
| --- | --- |
| Status | open |
| Kind | hardening |
| Tier | polish |
| Area | Build & Packaging |

## Summary

A build-time check that every externalised bare import resolves inside the image — attempted with static analysis, which proved unsound; needs the bundler's own externals list.

## Problem

[Ship production dependencies so images are self-contained](vendor-production-dependencies.md)
fixes the images built today. Nothing yet catches the next time something falls out of
the tree, and the failure it would cause is invisible until a request arrives in
production: the container starts, both probes pass, and the first route touching the
missing package 500s.

A static-analysis version was written and **reverted**, because it failed real,
correctly-configured builds. Recording why, so it is not attempted the same way twice:

- **Type-only imports survive as text.** The adapter-node fixture's built server
  contains `from 'types'` eleven times — a type import erased at runtime, naming a
  package that does not exist. A regex over the bundle cannot tell it from a real one.
- **adapter-node bundles most dependencies.** The fixture declares zero production
  dependencies, its built output still contains bare imports for `svelte`,
  `@sveltejs/kit`, `polka`, `sirv` and `cookie`, and its image boots and serves traffic
  (`TestRuntimeSmoke_LayeredStrategy_BootsAndServes`). The remaining specifiers are
  artifacts of bundling, not runtime requirements.

So "appears as a bare import in the output" does not imply "must resolve at runtime",
and a guard that fails a working build is worse than the bug it guards.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Read the bundler's own externals list | Vite/Rollup knows exactly which modules it externalised. Capture that during the build (a metafile, or an explicit ssr.external configuration Pokkum sets) and check only those specifiers against the staged tree. | Exact rather than heuristic, and cheap at build time — but requires reaching into the build's own output format, which differs across Vite versions and is not a stable contract. |
| Verify empirically at build time | After building, start the produced server with Bun's --no-install in the staged environment and confirm it reaches a listening state. | Authoritative — it is what actually happens in production — but slow, needs a free port, and turns every build into a partial smoke test. |
| Rely on the runtime check already in place | The image entrypoint passes --no-install, so a missing dependency now fails loudly at startup instead of being silently fetched from npm. | Zero cost and already shipped, but the failure surfaces at deploy time rather than build time — better than production, worse than CI. |

## Recommendation

First option. The bundler is the only component that actually knows which specifiers it
externalised, and every heuristic that does not consult it will keep mistaking type
imports and bundled-away packages for real dependencies.

Until then the third option is genuinely load-bearing rather than a placeholder: with
`--no-install` in the entrypoint, a gap fails at container start with "Cannot find
package", which is loud, immediate and honest — the property that was missing when Bun
silently downloaded the difference from npm.

## Related

- [Exclude routes from the production build](route-exclusion-filter.md)

