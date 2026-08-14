# Vendor Layer Pruning

## 1. The Optimization Concept
When a SvelteKit application is compiled, the resulting `node_modules` directory often contains tens of megabytes of redundant files that are strictly unnecessary for running the application in production. This includes Markdown files, unit tests, TypeScript declaration files (`.d.ts`), source maps (`.map`), and development tooling configurations.

**The Solution:**
Implement an aggressive pruning step during the `Compiler.Prepare` phase (before archiving the vendor layer) that recursively deletes files matching a predefined "junk" blocklist. This can reliably reduce the final container image size by 15-35MB, significantly improving Kubernetes pod startup latency and registry storage costs.

**Target Exclusions:**
- `*.d.ts`, `*.d.ts.map`, `tsconfig.json`
- `*.map` (unless a `--sourcemap` flag is passed)
- `README*`, `CHANGELOG*`, `LICENSE*`
- `__tests__/`, `*.test.js`, `*.spec.js`
- `.github/`, `.git/`

## 2. Potential Pitfalls & Risks
Pruning is a heuristic process, and the Node.js ecosystem is notoriously unpredictable.

**Specific Risks:**
- **Dynamic Imports:** If a library dynamically attempts to `require()` a file that was pruned (e.g., a specific `.js.map` file hardcoded in a stack trace generator, or a `.md` file read at runtime to serve documentation), the application will crash with `MODULE_NOT_FOUND` on startup.
- **Native Addon Compilation:** If pruning occurs before or during native module binding (`node-gyp`), essential C++ headers or Python scripts might be deleted, breaking the build.
- **Ecosystem Norm Shifts:** A file extension that is safely ignored today might become functionally required tomorrow (e.g., if a new JS runtime starts eagerly parsing `.d.ts` files).

## 3. The Automation Tripwire (How to not bugger up)
Because you cannot predict every NPM package's runtime behavior, you must establish an automated verification matrix and a user-facing escape hatch.

### CI Tripwire Implementation
Create an end-to-end (E2E) test suite in CI that builds and runs heavy SvelteKit fixtures with pruning fully enabled.

1. **The Fixture Matrix:**
   - Create 3-4 SvelteKit fixture apps utilizing known "heavy" or complex dependencies (e.g., `prisma`, `sharp`, `puppeteer-core`, `better-sqlite3`).
2. **The Execution:**
   - Run `pokkum build` on each fixture.
   - Start the resulting OCI container (`docker run -d -p 3000:3000 <image>`).
3. **The Assertion:**
   - Ping the `/healthz` or a designated test route (`curl -f http://localhost:3000/`).
   - If a pruned file causes a runtime crash, the container will exit, or the HTTP request will fail, turning the CI pipeline red.

### User Escape Hatch
No heuristic is perfect. You must provide users with an out-of-bounds configuration to bypass the pruning logic if their specific dependency graph requires it.

- **Implementation:** Add support for a `keep-vendor-files` array in `.pokkumignore` or via a CLI flag (e.g., `--no-prune` or `--keep-vendor="*.md"`). If a user reports a crash due to pruning, they can immediately self-serve the fix while you update the global defaults.
