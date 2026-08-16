# adapter-node `handler.js` fixtures

Real, unmodified `handler.js` files shipped by `@sveltejs/adapter-node`, checked
in as regression-test fixtures for
`internal/adapters/bunexec/prerendered_patch.go`'s pattern matcher (which
patches the generated handler to read prerendered pages from
`POKKUM_PRERENDERED_DIR`). These are byte-for-byte copies of the npm package
contents — no manual edits.

## Why these two versions

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

## Sourcing

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
under a `dist/` or build-output directory) and is copied by adapter-node's own
`index.js` into the user's build output as the project's generated
`handler.js` essentially verbatim (SvelteKit's build embeds it via its own
bundler substitutions for import specifiers like `'SHIMS'`, `'MANIFEST'`,
`'SERVER'`, `'PRERENDERED'` — the prerendered-path logic itself is untouched
by that substitution).

## Prerendered-path pattern found

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
