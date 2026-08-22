# `sveltekit-kit3` fixture

A real SvelteKit **3** project (still pre-release at the time this fixture
was captured) using the real `@sveltejs/adapter-node@6.x` — the only
adapter-node line that supports Kit 3 (`5.5.7`, the version
`sveltekit-adapter-node` pins, peers on `^2.4.0`; `6.0.0-next.10` peers on
`^3.0.0-next.0`, verified from the installed package's own
`peerDependencies`).

**Provenance:** hand-assembled, not captured from `bunx sv create`. As of
this writing `sv`'s scaffolder does not target SvelteKit 3 pre-releases, so
there is no "verbatim `sv create` output" to source from for this fixture
the way `sveltekit-adapter-node`'s README correctly can for SvelteKit 2 —
recording that plainly here rather than claiming a provenance that was never
actually checked (see `Lessons.md`'s 2026-08-17 entry on exactly that
mistake). Every file was written by hand against the real, installed
`@sveltejs/kit@3.0.0-next.25` + `@sveltejs/adapter-node@6.0.0-next.10` and
verified empirically: `bun install --frozen-lockfile`, `bun x vite build`,
and a real `pokkum build --tarball` all succeed against it.

**Exact pinned versions** (no `next`/`latest`/floating ranges — a fixture
that silently upgrades under a dist-tag move is worse than no fixture):

| package | version |
| --- | --- |
| `@sveltejs/kit` | `3.0.0-next.25` |
| `@sveltejs/adapter-node` | `6.0.0-next.10` |
| `@sveltejs/vite-plugin-svelte` | `7.3.0` |
| `svelte` | `5.56.10` |
| `vite` | `8.2.2` |
| `typescript` | `6.0.3` |

## SvelteKit 3 breaking changes this fixture depends on getting right

Each of these was verified against the real installed package before being
relied on here, not assumed by analogy with SvelteKit 2:

- **No `svelte.config.js`.** SvelteKit 3 treats its presence as a hard
  error. This fixture has none; the adapter is configured entirely in
  `vite.config.ts`'s `sveltekit({ ... })` call, the only place Kit 3
  projects can configure it.
- **`$lib` is gone.** Library code is reached through `#lib`
  (`package.json`'s own `"imports"` field: `"#lib": "./src/lib"`,
  `"#lib/*": "./src/lib/*"`), not the `$lib` alias SvelteKit 2 injects.
  `src/routes/remotes/+page.svelte` imports from `#lib/data.remote` and
  `#lib/counter.remote` to exercise this for real.
- **`tsconfig.json` extends `"$app/tsconfig"`**, not
  `"./.svelte-kit/tsconfig.json"` (every SvelteKit 2 fixture in this repo,
  including `sveltekit-adapter-node`, extends the latter; doing so under Kit
  3 does not resolve).
- **Only `@sveltejs/adapter-node@6.x` supports Kit 3.** `5.5.7` (what
  `sveltekit-adapter-node` pins) peers on `^2.4.0` and cannot be used here.

## Remote functions

`kit.experimental.remoteFunctions: true` is set in `vite.config.ts`. Three
`.remote.ts` files exercise the three shapes: `src/lib/counter.remote.ts`
(`query`/`command`), `src/lib/data.remote.ts` (`query`/`prerender`), and
`src/routes/contact/contact.remote.ts` (`form`). `src/routes/remotes`
renders all of them; `src/routes/contact` renders the form.

## Reproducibility note

Because the adapter is already correctly configured here (the common,
expected shape), `bunexec.Compiler.Prepare` takes the
`PrepareVirtualViteConfigPassthrough` path, not `PrepareVirtualViteConfig` —
only the latter calls `injectViteVersionPin`. This fixture's own
`vite.config.ts` therefore pins `kit.version.name` to `SOURCE_DATE_EPOCH`
itself (see the comment there), the same way `sveltekit-basic`'s
`svelte.config.js` pins it for its own adapter. This is a real, currently
uncovered gap in Pokkum itself, not something this fixture works around:
today, a correctly-configured SvelteKit 3 project's `vite.config.ts` that
does *not* self-manage the version pin (the more common real-world case)
gets no reproducibility fix from Pokkum on either path. Flagged separately;
not fixed here since this fixture may only touch `testdata/**`.

## Local setup

`bun.lock` is committed for reproducible `bun install`; `node_modules/`,
`.svelte-kit/`, `build/`, and `.pokkum/` are gitignored and must be
installed locally (`cd testdata/fixtures/sveltekit-kit3 && bun install`)
before running the real-bun integration test — it skips gracefully when
`bun` isn't on `PATH` or dependencies aren't installed, same as
`sveltekit-basic` and `sveltekit-adapter-node`.
