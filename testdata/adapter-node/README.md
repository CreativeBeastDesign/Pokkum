# adapter-node `handler.js` fixtures

Regression-test fixtures for
`internal/adapters/bunexec/prerendered_patch.go`'s pattern matcher (which
patches the generated handler to read prerendered pages from
`POKKUM_PRERENDERED_DIR`).

Two distinct pipeline stages are represented here, and the difference matters:

| Directory | Pipeline stage | Sourced from |
|---|---|---|
| `v3/`, `v5/` | adapter-node's **pre-bundling source template**, before Vite/Rollup ever sees it | `npm pack` → `package/files/handler.js` |
| `bundled-real/` | the **post-bundle build output** a real `bun run build` emits into `build/` — i.e. the artifact `prerendered_patch.go` actually opens at build time | a real `sv create` + `bun run build` |

`v3/` and `v5/` are byte-for-byte copies of the npm package contents (no
manual edits), but they are **not** what Pokkum patches at build time — see
`Lessons.md`'s 2026-08-16 entry "`patchPrerenderedHandler`'s 'real fixture'
regression tests exercised the wrong artifact". They are kept as honest
coverage of the upstream template shape, which the matcher must also keep
handling (some versions/configs still inline that shape into build output).

## Why these two upstream-template versions

Pokkum does not pin an `@sveltejs/adapter-node` version itself: it always
injects `@sveltejs/adapter-node` as `targetAdapter`
(`internal/adapters/bunexec/compiler.go:234`) for `StrategyLayered`, but the
actual installed/resolved version comes from the **user's own project**
(`sveltekitutils.ResolveVersion`, `internal/adapters/sveltekitutils/project.go:109`,
which reads `node_modules/@sveltejs/adapter-node/package.json` if present,
falling back to the declared range in the user's `package.json`). So there is
no single "the version Pokkum targets" — major versions 3 and 5 were sourced
as directed, covering an older major still plausibly installed in the wild
and the current latest stable major (5.x; a 6.0.0-next prerelease exists on
npm but is not yet stable, so 5 remains "current" for this purpose).

## Sourcing (`v3/`, `v5/` — upstream template)

| | v3 | v5 |
|---|---|---|
| Package requested | `@sveltejs/adapter-node@3` | `@sveltejs/adapter-node@5` |
| Resolved version | **3.0.3** | **5.5.7** |
| Path in npm tarball | `package/files/handler.js` | `package/files/handler.js` |
| Fixture path | `testdata/adapter-node/v3/handler.js` | `testdata/adapter-node/v5/handler.js` |
| SHA-256 of fixture | `a39477f86ebdaaa96c83070d60ece3c2ee671a206c181b4e123476656f184935`* | `8c5e9dcd5df5e07d0e36646b839e92dcc8820b173ed8270a3b3728650829cc26`* |

\* Computed with `shasum -a 256`; regenerate and compare if re-sourcing to confirm bit-for-bit match.

Date sourced: 2026-08-16.

Commands used:

```bash
mkdir -p /tmp/adapter-node-pack && cd /tmp/adapter-node-pack
npm pack @sveltejs/adapter-node@3 --pack-destination .
npm pack @sveltejs/adapter-node@5 --pack-destination .

mkdir -p v3 v5
tar -xzf sveltejs-adapter-node-3.0.3.tgz -C v3
tar -xzf sveltejs-adapter-node-5.5.7.tgz -C v5

cp v3/package/files/handler.js <repo>/testdata/adapter-node/v3/handler.js
cp v5/package/files/handler.js <repo>/testdata/adapter-node/v5/handler.js
```

`npm pack` writes a `.tgz` whose contents are rooted at `package/`; the
runtime handler template lives at `files/handler.js` inside the package (not
under a `dist/` or build-output directory). adapter-node's own `index.js`
feeds it through Rollup, substituting import specifiers like `'SHIMS'`,
`'MANIFEST'`, `'SERVER'`, `'PRERENDERED'`.

> **These two fixtures are the input to that bundling step, not its output.**
> They do not prove the matcher works against a real build — `bundled-real/`
> below is what does. Bundling can and does relocate the prerendered-path
> logic out of `handler.js` entirely (see that section).

## Prerendered-path pattern found (`v3/`, `v5/`)

Both versions build the prerendered-file server root with:

```js
const handler = serve(path.join(dir, 'prerendered'));
```

— i.e. the `path.join(dir, 'prerendered')` (single-quoted, `dir` variable)
variant, one of the 8 patterns `prerendered_patch.go` matches on. Confirmed
via `grep -n "prerendered"` on each extracted file (6 matches each, including
the `serve_prerendered()` function definition containing this line).

Notable version difference (informational, not a matcher concern since the
match is a plain string search): in v3.0.3, `dir` is declared locally in
`handler.js` itself (`const dir = path.dirname(fileURLToPath(import.meta.url));`,
line 1149). In v5.5.7, `handler.js` is a smaller, module-split bundle and
`dir` is imported from a sibling file (`import { env, dir, env_prefix } from
'./env.js';`) rather than declared inline — the file itself dropped from
~35.6kB/1313 lines (v3) to ~6.9kB/245 lines (v5) because v5 factors shared
code (env parsing, static-file serving helpers) into `./chunks/vendor.js`,
`./env.js`, and `./utils.js`, which are NOT included in these fixtures (only
`files/handler.js` was sourced, matching the scope of what
`prerendered_patch.go` patches). The `path.join(dir, 'prerendered')` line
itself is unaffected by this refactor and matches the same pattern in both
versions.

## `bundled-real/` — real post-bundle build output

```
bundled-real/handler.js                              <- thin re-export barrel
bundled-real/server/chunks/handler-Cl6LqmpI.js       <- the real handler implementation
```

Date sourced: **2026-08-17**. Toolchain: `@sveltejs/adapter-node@5.5.7`,
`@sveltejs/kit@2.70.2`, built with `bun`.

Commands used:

```bash
# fresh scaffold with adapter-node selected, plus a real prerendered route
bunx sv create --add sveltekit-adapter=adapter:node <scratch-dir>
# (added a src/routes/about/+page.js exporting `export const prerender = true;`)
bun install
bun run build
```

### What the real build emits

`build/handler.js` — the file `patchPrerenderedHandler` locates and opens —
**no longer contains the prerendered-path logic at all**. It is a thin
re-export barrel:

```js
export { h as handler } from './server/chunks/handler-Cl6LqmpI.js';
```

The real implementation lives in a **content-hashed chunk** under
`build/server/chunks/`. Both the local identifier (`h`) and the hash
(`Cl6LqmpI`) are Rollup-assigned and vary per build, so
`prerendered_patch.go` matches the shape, never those literals. That chunk
does contain one of the 8 known patterns completely unchanged —
`path.join(dir, 'prerendered')` — which is why the fix was "follow the
re-export", not "add new patterns". The chunk is **not minified** (Vite's
default Node SSR output keeps real names and comments), so literal string
matching stays valid. `dir` itself is imported from `build/env.js` via
`import { env, dir, env_prefix } from '../../env.js';`.

The build tree, as observed:

```
build/handler.js
build/env.js
build/index.js
build/server/chunks/handler-Cl6LqmpI.js
build/server/chunks/index.js-smFCi7sU.js
build/server/chunks/manifest.js-BFl5iliz.js
build/prerendered/about.html
```

### Fidelity of each checked-in file (read this before trusting them)

| File | Fidelity |
|---|---|
| `bundled-real/handler.js` | **Verbatim** capture of the real emitted barrel, with trailing side-effect `import` lines beyond the ones shown elided (they are irrelevant to the matcher and reference more per-build hashed chunk names). |
| `bundled-real/server/chunks/handler-Cl6LqmpI.js` | **Synthesized trim, not a verbatim byte capture.** The `serve_prerendered()` function — including the load-bearing `const handler = serve(path.join(dir, 'prerendered'));` line — and the `../../env.js` import line are verbatim from the real chunk. The surrounding code is the real `v5/handler.js` template (above) trimmed to a plausible bundled form: `export { handler as h }` (matching what the barrel imports), `PRERENDERED`/`BASE`/`PRECOMPRESS` specifiers resolved to their post-substitution values, and the `ssr` body elided. Nothing here was shaped to satisfy the matcher — the matched line is real. |

The directory nesting (`server/chunks/`) is preserved from the real build so
the fixture exercises the actual relative-path resolution
(`./server/chunks/...` against `handler.js`'s own directory), not a flattened
approximation.
