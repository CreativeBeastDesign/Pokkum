# Concept: Zero-Config Auto-Injection for Pokkum (`pokkum-injection-concept.md`)

## 1. Goal & Problem Statement

Currently, adopting Pokkum requires a developer to manually make code and configuration changes to their SvelteKit repository:
1. Install `@jesterkit/exe-sveltekit` as a dependency in `package.json`.
2. Edit `svelte.config.js` to import and configure `@jesterkit/exe-sveltekit`.
3. Edit `svelte.config.js` to manually override `kit.version.name = process.env.SOURCE_DATE_EPOCH ?? 'dev'` (to prevent `Date.now()` from ruining byte-for-byte image reproducibility).

### Objective
Lower the barrier to entry to **zero configuration**. A developer with a stock SvelteKit app should be able to run `pokkum build ./my-app` and have Pokkum automatically handle adapter injection, dependency resolution, and version pinning at build time without mutating their source repository files on disk.

---

## 2. Injection Architecture Overview

Pokkum uses a **Virtual Build Sandbox & Interception Pipeline** during the `bun run build` phase:

```
                      Pokkum Build Request
                               │
                               ▼
                    ┌─────────────────────┐
                    │  Config Inspection  │
                    │ (package.json &     │
                    │  svelte.config.js)  │
                    └──────────┬──────────┘
                               │
                               ▼
                 Need Injection / Overrides?
                               │
                    ┌──────────┴──────────┐
                YES │                     │ NO
                    ▼                     ▼
      ┌─────────────────────────┐   ┌─────────────────────────┐
      │ Virtual Config / AST    │   │ Execute Existing        │
      │ Injection Pipeline      │   │ Build Directly          │
      └────────────┬────────────┘   └────────────┬────────────┘
                   │                             │
                   └──────────────┬──────────────┘
                                  │
                                  ▼
                      ┌──────────────────────┐
                      │    bun run build     │
                      │  (With Injected Env  │
                      │    & Temp Config)    │
                      └──────────────────────┘
```

---

## 3. Detailed Technical Approaches

### Feature A: Automated `kit.version.name` Pinning (`SOURCE_DATE_EPOCH`)

#### The Problem
SvelteKit's default configuration evaluates `kit.version.name` as `Date.now()`. That timestamp gets embedded into `client/_app/version.json` and compiled into the executable binary, causing two identical builds of the same commit to yield different binary hashes.

#### Solution: Environment & Interception
1. Pokkum computes `SOURCE_DATE_EPOCH` from the latest Git commit (or host environment).
2. Pokkum exports `SOURCE_DATE_EPOCH` into the build process environment.
3. During config loading, Pokkum injects a SvelteKit config wrapper that overrides `config.kit.version.name`:
   ```javascript
   // Auto-injected by Pokkum if not explicitly set
   config.kit.version = {
     ...config.kit.version,
     name: process.env.SOURCE_DATE_EPOCH || 'pokkum-reproducible-build'
   };
   ```

---

### Feature B: Auto-Injecting `@jesterkit/exe-sveltekit` Adapter

When a user has `@sveltejs/adapter-auto`, `@sveltejs/adapter-node`, or `@sveltejs/adapter-static` configured in `svelte.config.js`, Pokkum can intercept the config loading without requiring `npm install` / `bun add` in the user's project repository.

#### Option 1: AST Manipulation (SWC / Babel / Oxc via Bun) — *Recommended for JS/TS Configs*

Pokkum can parse `svelte.config.js` (or `svelte.config.ts`), transform the AST, and write a temporary resolved config (`.pokkum/svelte.config.js`) used exclusively for the build pass.

**AST Transformation Rules:**
1. Locate import statements importing `@sveltejs/adapter-*` or default adapter exports.
2. Replace the adapter import with `@jesterkit/exe-sveltekit`.
3. Wrap the exported config object to inject `binaryName: 'server'` and `version.name = process.env.SOURCE_DATE_EPOCH`.

**Example Transformation:**

*Original `svelte.config.js`:*
```javascript
import adapter from '@sveltejs/adapter-auto';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

export default {
  preprocess: vitePreprocess(),
  kit: {
    adapter: adapter()
  }
};
```

*Transformed Virtual Config:*
```javascript
import adapter from '@jesterkit/exe-sveltekit';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

export default {
  preprocess: vitePreprocess(),
  kit: {
    adapter: adapter({
      binaryName: 'server',
      out: 'build'
    }),
    version: {
      name: process.env.SOURCE_DATE_EPOCH || 'pokkum-reproducible-build'
    }
  }
};
```

---

#### Option 2: Preload / Loader Hook Interception (`--import` / `NODE_OPTIONS`)

Instead of parsing JavaScript ASTs, Pokkum can leverage Bun/Node's module resolution hook or `--import` flag during `bun run build`.

1. **Adapter Alias Hook**: When SvelteKit resolves `import adapter from '@sveltejs/adapter-auto'`, the loader hook redirects the module specifier to `@jesterkit/exe-sveltekit` bundled internally by Pokkum or cached in `~/.pokkum/adapters/`.
2. **Config Proxy Hook**: Intercepts the `export default` of `svelte.config.js` at runtime using a ES Module proxy, automatically patching `config.kit.adapter` and `config.kit.version.name`.

*Advantages of Option 2:*
- Requires zero AST parsing or code rewriting.
- Works seamlessly with TypeScript, ES Modules, CommonJS, and complex dynamic `svelte.config.js` setups.
- Does not modify any project files.

---

### Feature C: Bundling `@jesterkit/exe-sveltekit` into Pokkum

To eliminate `bun add @jesterkit/exe-sveltekit` in the target project, Pokkum can embed the JS adapter bundle directly within the `pokkum` Go binary (via `embed.FS`).

1. During `pokkum build`, Pokkum checks if `@jesterkit/exe-sveltekit` is present in `node_modules`.
2. If missing, Pokkum writes the embedded adapter bundle into a temporary workspace cache (`.pokkum/node_modules/@jesterkit/exe-sveltekit`) and sets `NODE_PATH` / Bun module search paths accordingly.

---

## 4. Summary of Zero-Config Developer Experience

With this concept implemented, the adoption flow becomes:

```bash
# Old Way (3 manual setup steps required):
# 1. bun add -D @jesterkit/exe-sveltekit
# 2. Edit svelte.config.js adapter
# 3. Edit svelte.config.js version.name
pokkum build ./my-app

# New Zero-Config Way:
# Just run pokkum build on any stock SvelteKit app!
pokkum build ./my-app
```

Pokkum automatically detects, injects, pins reproducibility timestamps, compiles, packages, and ships the multi-arch image without altering a single line of the user's source code.
