# `sveltekit-adapter-node` fixture

A real, unmodified SvelteKit project using the real `@sveltejs/adapter-node`,
sourced verbatim (not hand-written) via:

```
bunx sv create sveltekit-adapter-node --template minimal --types ts \
    --add sveltekit-adapter=adapter:node --no-install
```

Captured 2026-08-17 with `sv@0.17.0`, `@sveltejs/kit@2.63.0` (range) /
resolved `2.70.2`, `@sveltejs/adapter-node@5.5.7`, `vite@8.2.1`.

`src/routes/about/{+page.svelte,+page.ts}` (a prerendered route, `export
const prerender = true`) was added on top of the scaffold so this fixture
exercises `patchPrerenderedHandler` — the only reason a project with real
`@sveltejs/adapter-node` and no prerendered content wouldn't. Every other
file is exactly what `sv create` emitted; no `svelte.config.js` exists
because current `sv create` scaffolds don't generate one (the adapter is
configured entirely in `vite.config.ts`, matching this project's real
default — see `Roadmap.md`'s "Layered-Strategy Real-Build Correctness" and
`concepts/zero-config-injection-concept.md`).

This is the fixture `tests/integration/layered_prerendered_e2e_test.go`
drives `--strategy=layered` against, end to end, through the real
`bunexec.Compiler` — the first test in this repo to exercise
`patchPrerenderedHandler` through the real pipeline rather than a synthetic
fixture. See `testdata/fixtures/sveltekit-basic/README` conventions (none
exists there either; provenance for that fixture lives in code comments) —
this file exists because, unlike `sveltekit-basic`, this fixture's very
reason for existing *is* its provenance.

`bun.lock` is committed for reproducible `bun install`; `node_modules/`,
`.svelte-kit/`, and `build/` are gitignored and must be installed locally
(`cd testdata/fixtures/sveltekit-adapter-node && bun install`) before running
the real-bun integration test — it skips gracefully when `bun` isn't on
`PATH` or dependencies aren't installed, same as `sveltekit-basic`.
