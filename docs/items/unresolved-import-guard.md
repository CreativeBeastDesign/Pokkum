<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: unresolved-import-guard)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Detect dependencies that will be missing at runtime

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | hardening |
| Tier | polish |
| Area | Build & Packaging |

## Summary

A build-time check that every externalised dependency resolves inside the image, read from the manifest adapter-node itself externalises from rather than from the bundle.

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

## Decision

Shipped 2026-08-22, taking the first option but arriving at it from the other end.

Vite does not expose its externals list: `shouldExternalize` caches verdicts in a
closure-local map and a module-local WeakMap, neither exported (vite 8.2.1), and the
manifest SvelteKit forces (`manifest: true`) contains only internal chunk keys. Neither
adapter-node version installed here writes a metafile.

It turns out not to be needed. adapter-node's rollup `external` option is literally

    ...Object.keys(pkg.dependencies || {}).map(d => new RegExp(`^${d}(\/.*)?$`))

(5.5.7 `index.js:76-79`; 6.0.0-next.10 adds `@opentelemetry/api`). Everything else,
every devDependency included, is bundled into the output. So the set of specifiers that
must resolve at runtime *is* `package.json`'s `dependencies` — read from the same file
the bundler read it from. The guard checks each one resolves as a package directory
(with its own `package.json`, since that is what Node resolves through) in the staged
tree, and fails the build naming the missing packages.

Both failure modes that got the previous attempt reverted are unreachable by
construction rather than worked around: a bundled-away package is by definition absent
from `dependencies`, and a specifier appearing only inside a JSDoc comment
(`/** @import { X } from 'types' */` — which is what the reverted regex was actually
tripping on) never enters a manifest.

Verified on a real layered build of `testdata/fixtures/sveltekit-adapter-node`: `clsx`
is declared in `dependencies`, adapter-node left it external (`from 'clsx'` in the built
output), it was staged, and it shipped to `app/node_modules/clsx` — the guard passed
without a false positive. The failure direction is covered by a Prepare-level test whose
fake `bun install` exits 0 having staged nothing, which is exactly how the real command
behaves when the lockfile disagrees with the manifest.

Fixture correction found on the way: the in-test `validPackageJSON` declared
`@sveltejs/kit` under `dependencies`, where no real scaffold puts it. That described a
project that could not work — kit would have been externalised and then absent — so the
guard was right and the fixture was wrong.

## Implementation

- [internal/adapters/bunexec/unresolved_imports.go](../../internal/adapters/bunexec/unresolved_imports.go)
- [internal/adapters/bunexec/compiler.go](../../internal/adapters/bunexec/compiler.go)

## Known Limitations

- Scoped to the layered strategy, which is the only one with a runtime dependency tree. `exe` compiles a self-contained binary and `static` ships no JS runtime.
- adapter-node 6 externalises `@opentelemetry/api` unconditionally, beyond `dependencies`. A project using it without declaring it would not be caught.
- A production dependency that is declared but never imported still has to be installed to pass. That is a manifest that overstates its needs rather than a false positive, and the fix — move it to devDependencies — is in the error message.

## Related

- [Exclude routes from the production build](route-exclusion-filter.md)

