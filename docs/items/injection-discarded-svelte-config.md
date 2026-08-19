<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: injection-discarded-svelte-config)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Adapter injection silently discarded the project's whole SvelteKit config

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.1 |
| Kind | fix |
| Tier | foundation |
| Area | Developer Experience |

## Summary

Rewriting a bare sveltekit() to inject an adapter makes SvelteKit ignore svelte.config.js entirely, so aliases, csp, prerender settings and kit.experimental flags were all lost.

## Problem

Reported as a build failure that named the missing flag while the project already had it:

  [vite-plugin-sveltekit-guard] Could not load virtual:env/dynamic/private:
  To enable remote functions, add kit.experimental.remoteFunctions

SvelteKit calls `load_svelte_config()` only when the `sveltekit()` plugin receives **no**
argument — verified in `@sveltejs/kit`'s `src/exports/vite/index.js`, which also prints
"svelte.config.js is ignored when options are passed via your Vite config" in the other branch.
Zero-config adapter injection rewrote a bare `sveltekit()` into
`sveltekit({ adapter: adapter() })`, which supplies an argument — so the project's entire
`svelte.config.js` was discarded to inject one option. Aliases, csp, prerender settings and
every `kit.experimental` flag went with it.

The failure is worse than a lost setting: `remoteFunctions` being absent makes
`virtual:env/dynamic/private` unresolvable, so the build fails outright, pointing at a config
the user has already written correctly.

## Decision

Shipped 2026-08-19. When the plugin call is bare and the project has a svelte config, the
injected `.pokkum/vite.config.ts` now imports that file and merges it, overriding only the
adapter.

The conversion is the substance, because the two shapes differ. `svelte.config.js` nests
SvelteKit's own options under `kit`, while the Vite-config form is flat: `split_config()`
destructures `extensions`/`compilerOptions`/`vitePlugin`/`preprocess`, routes every other
recognised key into `kit`, splits `experimental` between SvelteKit and vite-plugin-svelte, and
passes anything unrecognised to the latter. A literal `kit` key is not recognised, so spreading
the file unflattened would hand `kit` to vite-plugin-svelte as an unknown option and still lose
its contents. The generated config therefore spreads the non-kit remainder, then the flattened
kit options, then the adapter last so it always wins.

Untouched in the other two cases: a project with no svelte config keeps the plain injected
form, and a project already passing options has itself opted out of `svelte.config.js`, so
importing it would change that project's semantics rather than preserve them.

Verified empirically rather than by inspection, which mattered here: unit tests assert the
generated source contains the right spreads, and that is a different claim from "SvelteKit
honours it" — a config that looks right and is shaped wrong passes them. A real bun build with
`kit.appDir` set to a probe value now asserts the custom directory appears in the output and
the default `_app` does not; reverting the merge makes it fail with `_app`.

## Flags

- `--inject`
- `--no-inject`

## Implementation

- [internal/adapters/sveltekitutils/injector.go](../../internal/adapters/sveltekitutils/injector.go)
- [internal/adapters/bunexec/compiler.go](../../internal/adapters/bunexec/compiler.go)
- [tests/integration/svelte_config_merge_test.go](../../tests/integration/svelte_config_merge_test.go)

## Known Limitations

- A project defining both `kit.experimental` and a top-level `experimental` for vite-plugin-svelte would see the kit one win after flattening. Unusual, and preferable to dropping the config entirely.

## Related

- [Zero-config adapter injection declined silently, with undocumented preconditions](injection-preconditions-undocumented.md)
- [pokkum adopt](adopt-codemod.md)

