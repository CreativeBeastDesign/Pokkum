<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: adopt-reorders-package-json-and-duplicates-adapter-import)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# adopt reordered untouched package.json keys and could duplicate a third-party adapter import

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.1 |
| Kind | fix |
| Tier | foundation |
| Area | Developer Experience |

## Summary

A real, non-dry-run `pokkum adopt` alphabetized every nested `package.json` object on write, and `--write-config` could emit two colliding `adapter` bindings for a third-party adapter package, breaking every build strategy.

## Problem

An independent tester running paranoid-testing-guide.md §18 against a real SvelteKit app
found two defects in the same command:

1. `git diff package.json` after a real (non-dry-run) `pokkum adopt .` showed the entire
   `scripts` object re-serialized in alphabetical order (`dev, build, preview, prepare,
   check` became `build, check, check:watch, dev, pokkum:build, prepare`), rather than just
   the new `pokkum:build` key being added. `Adopt` unmarshalled `package.json` into
   `map[string]any` and re-marshalled it; Go maps have no iteration order, so
   `json.Marshal` alphabetizes every key at every depth. A prior fix
   (`marshalPreservingTopLevelKeyOrder`) had already addressed this for top-level keys, but
   its own doc comment recorded that nested objects — `scripts`, `dependencies`,
   `devDependencies`, exactly what a codemod actually edits — were not order-preserved,
   since the map had already lost their order by the time that function saw them.
2. On a project whose `svelte.config.js` imports a third-party adapter
   (`import adapter from "svelte-adapter-bun"` — a real, widely used package, not a
   `@sveltejs/adapter-*` one), `pokkum adopt . --write-config` prepended a second import
   instead of replacing the first: `replaceAdapterImport` only recognized
   `@sveltejs/adapter-[a-z-]+` specifiers, so the existing import went unmatched. The
   result, two `import adapter from ...` lines binding the same name, is a parse-time
   `SyntaxError: Identifier 'adapter' has already been declared`, not just a messy diff —
   `pokkum build` then failed under every strategy.

## Decision

`Adopt` (`internal/adapters/sveltekitutils/adopt.go`) now decodes `package.json` into a
small order-preserving JSON object model (`internal/adapters/sveltekitutils/orderedjson.go`)
instead of `map[string]any`, and re-encodes through it — preserving member order at every
nesting depth the codemod touches, not only the top level. `marshalPreservingTopLevelKeyOrder`
is retired; the ordered model replaces it outright.

`replaceAdapterImport` (`internal/adapters/sveltekitutils/injector.go`) now traces which
*identifier* the config actually invokes as the adapter factory (`adapter: X(...)`) and
replaces whichever import/require statement binds that identifier, regardless of package
name or local variable name — covering a default import, a `{ default as X }` re-export, a
CommonJS `require`, and a solo named import. An import it can't safely isolate (one name
among several in a multi-specifier named import) is left untouched rather than risk
prepending a second, colliding declaration — the same failure mode this fix exists to close.

## Flags

- `--write-config`

## Implementation

- [internal/adapters/sveltekitutils/adopt.go](../../internal/adapters/sveltekitutils/adopt.go)
- [internal/adapters/sveltekitutils/orderedjson.go](../../internal/adapters/sveltekitutils/orderedjson.go)
- [internal/adapters/sveltekitutils/injector.go](../../internal/adapters/sveltekitutils/injector.go)

## Related

- [pokkum adopt](adopt-codemod.md)

