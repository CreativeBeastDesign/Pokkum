# Concept: Layered-Strategy Runtime Hardening (Option A) & Real Zero-Config Injection (Option B)

## 0. Why these two are in one document

Both are the same shape of unfinished business: each was analyzed, partially spiked, and explicitly **deferred** rather than built, because building it under time pressure risked getting the mechanism wrong in a way that's hard to detect later. Both are now the top of their respective backlogs:

- **Option A** (compiled stub launcher) is `docs/archive/Roadmap.md`'s "2. Layered-Strategy Runtime Hardening" — the one item left in that section after Option C (startup attestation) shipped 2026-08-17.
- **Option B** (generated `vite.config.ts` wrapper) is `concepts/archive/zero-config-injection-concept.md`'s deferred follow-up to Option C (fail-fast detection), which shipped the same day (see `docs/archive/Roadmap.md`'s "0. Layered-Strategy Real-Build Correctness").

Neither is scoped as a "just build it" ticket. Both need a real design decision recorded before implementation starts, because both have at least one plausible-looking mechanism that turns out to be broken or fragile — and this document exists to record that now, empirically, rather than have someone rediscover it mid-implementation.

**The single thing worth internalizing before reading further: for both A and B, the naive first idea does not work, and the reason it doesn't work is instructive.** Section 1.3 and Section 2.3 each open with that failure.

---

## Part 1: Option A — Compiled Stub Launcher

### 1.1 What it is today

`--strategy=layered` ships stock `bun` at `/usr/local/bin/bun`, and the supervisor execs it as `/pokkum/init -- /usr/local/bin/bun /app/server/index.js`. Stock `bun` exposes its full CLI via `BUN_BE_BUN=1` (and, per open upstream bugs — `oven-sh/bun#23205`, `#19536` — sometimes without the env var at all): `BUN_BE_BUN=1 /usr/local/bin/bun evil.js` runs arbitrary scripts on any host that already has an exec primitive. `/usr/local/bin/bun x <pkg>` is `bunx`. This is the "recommended but never built" half of the v0.3 layer-caching migration's own hardening analysis (`concepts/archive/pokkum-layer-caching-concept.md` §5.2) — only Option C (attestation) and the `readOnlyRootFilesystem` piece of Option B shipped.

**Important context, not an excuse:** the v0.1 compiled-exe baseline this is being compared against is _not_ actually sealed either. Bun single-file executables embed the full runtime and expose the same `BUN_BE_BUN` surface (`concepts/archive/pokkum-layer-caching-concept.md` §5.1). So Option A's bar is **parity**, not closing a gap that v0.1 didn't have — it's explicit about this in the archived doc, and it's worth repeating here so nobody builds this expecting a strictly stronger security property than v0.1 ever had.

### 1.2 What it should be

A tiny `bun build --compile` executable, built by Pokkum itself (not downloaded), whose only code is:

```ts
// stub-entry.ts — compiled per (Bun version × arch)
const p = "/app/server/" + "index.js"; // see 1.3 — must not be a static literal
await import(p);
```

Shipped in place of stock `bun` at the same `/usr/local/bin/bun` path (or a new path — open question, see §1.5). The path is **compiled into the binary**, not read from argv or env, so there is no way to redirect it at runtime the way `BUN_BE_BUN`/`bunx` redirect stock `bun`. CLI surface becomes _identical_ to the v0.1 compiled-exe baseline — including its own `BUN_BE_BUN` caveat (no worse, no better than v0.1, which is the honest bar per §1.1).

### 1.3 The pitfall that breaks the naive version, and how to avoid it — CONFIRMED

**Spike result (2026-08-17, real `bun` 1.3.14, from the archived concept doc — reproduced here because it's the load-bearing fact of this whole option):** `bun build --compile` **can** produce an executable that runtime-imports a live external file — including TypeScript transpilation of that file — but **only if the import specifier is not statically foldable**. A literal

```ts
await import("/app/server/index.js");
```

gets **bundled in at compile time** — Bun's bundler treats a static string literal `import()` the same as a static `import` statement and inlines the target module's contents into the compiled executable. That silently defeats the entire point: the layer-caching premise (`docs/archive/Roadmap.md`'s v0.3 migration, `pokkum-layer-caching-concept.md` §2) depends on the app-JS layer changing independently of the runtime-stub layer, so an app.js digest change must never force a rebuild of the stub. Baking the app into the stub does exactly that.

The fix that was confirmed to work: express the path so the bundler cannot constant-fold it — string concatenation (`"/app/server/" + "index.js"`), a value computed via a small helper function, or `new Function('return import(...)')`. **Before writing any implementation code, re-verify this against whatever Bun version ships at build time** — bundler constant-folding heuristics are exactly the kind of thing that can change between minor versions without a changelog entry calling it out. Treat the "is this import call actually left un-inlined" check as a build-time assertion (inspect the compiled binary's size, or grep its strings for a path fragment that should only be present if inlining happened), not a one-time fact to trust forever.

### 1.4 Other pitfalls and how to avoid them

- **Build-time Bun becomes a hard CLI dependency.** Today `bunruntime` only _downloads_ a pinned `bun`; Option A needs it to _compile_ — meaning the CLI's own build/release pipeline needs a `bun` toolchain to produce the stub, per (Bun version × arch) combination Pokkum supports. This is a real new supply-chain and CI surface, not a detail: it means `.goreleaser.yaml`/`slsa-builder.yml` need a `bun` install step, and the stub-compilation step itself needs to be SLSA-provenance-tracked the same way the CLI binary itself is (`scripts/check-build-flags.sh`'s pattern is a reasonable template). Avoid by treating stub compilation as its own first-class release artifact with its own checksum-pinning discipline — exactly the discipline that was recently found stale for the _downloaded_ bun runtime (see the bunruntime checksum incident logged in `Lessons.md` 2026-08-17) and just as capable of silently going stale here.
- **Per-(version × arch) stub caching must not become per-project.** The whole point of Option A is that the stub layer's digest changes only on a Bun _version_ bump, same as today's downloaded-runtime layer. Getting caching wrong here (e.g. embedding anything project-specific into the compiled stub, even indirectly via environment capture at compile time) silently reintroduces the ~180MB-per-commit cost the v0.3 migration existed to eliminate. Guard this the same way `TestPackagingIsDeterministicAcrossRuns`/vendor-chunk-stability guards work today: assert in CI that a change to app source never touches the stub layer's digest.
- **Cross-compilation.** `bun build --compile --target=bun-linux-x64` (and `-baseline`, and `-aarch64`) needs to run from whatever host CI builds on — confirm cross-compile actually produces a working target-arch binary (Bun documents cross-compilation support, but verify it empirically per the same "SPIKE DONE" discipline as §1.3, not just from docs) before committing to this as the delivery mechanism.
- **Runtime `import()` of the entrypoint still goes through the same module resolution as `bun run /app/server/index.js` today** — verify streaming responses, WebSockets (if any), and any dynamic `import()` calls _inside_ the app itself (e.g. route-level code-splitting, native `.node` addon loading via `ClosuredNativeAdapter`) still resolve correctly from within a compiled-exe process rather than a plain `bun` process. This is exactly the kind of long-tail correctness risk `pokkum-layer-caching-concept.md` §8 already flags for the adapter itself; Option A adds one more layer where the same class of subtle behavioral difference could hide.
- **`--bun-binary`/`--hermetic` escape hatches.** The stub replaces the _runtime_ binary Pokkum embeds, not the _build-time_ Bun a user's host already has. `--bun-binary=<path>` (the existing local-binary escape hatch) and `--hermetic` mode need an explicit decision: does supplying a custom binary bypass stub compilation entirely (falling back to stock-bun parity, i.e. today's status quo) or does it still get wrapped? Silently doing the wrong one either surprises a user who thought they opted into the harder-to-abuse stub, or breaks a legitimate custom-binary workflow. Document and test both paths.
- **Don't let this become Option D by accident.** Option D (custom-built, syscall/CLI-stripped Bun compiled from source) was explicitly rejected in the archived doc — it "genuinely shrinks the surface below v0.1" but at the cost of owning a Zig toolchain build of a fast-moving runtime and losing upstream binary provenance. Option A's build-time Bun dependency (previous bullet) is a much smaller version of the same _category_ of cost (Pokkum now runs Bun at build time to produce a runtime artifact) — watch that scope doesn't creep from "compile a trivial stub" into "maintain a semi-custom Bun build," which would be re-litigating Option D under a different name.

### 1.5 Open questions (Option A)

- Does the stub replace `/usr/local/bin/bun` at the same path (so the supervisor's exec target doesn't change), or does it get its own path (clearer separation, but touches the supervisor's compiled-in argv and `ports.BunBinaryPath`/`AppBinaryPath` constants)?
  - **Decision:** Same path for the initial build.
- Interaction with `--push-concurrency`/layer-cache-key composition: does the stub layer participate in `RemoteCacheInputRequest`'s existing `bunVersion` key component as-is, or does it need its own key component (e.g. stub-build-toolchain version) distinct from the _runtime_ Bun version it wraps?
  - **Decision:** Distinct `stub_version` component to `InputParams`.
- Should Option A ship behind a flag initially (`--strategy=layered --stub-launcher`, opt-in) with a time-boxed default flip once parity is proven in production, mirroring how v0.3 kept `--strategy=exe|layered` coexisting during its own migration?
  - **Decision:** Ship opt-in with a time-boxed default flip.

**IMPORTANT:** `--bun-binary` should **bypass stub compilation entirely** and fall back to stock-bun parity (today's status quo), documented loudly. `--hermetic` and `--stub-launcher` are orthogonal and can both be on (stub is built from the pinned/offline Bun).

---

## Part 2: Option B — Real Zero-Config Injection (generated `vite.config.ts` wrapper)

### 2.1 What it is today

`docs/archive/Roadmap.md`'s "0. Layered-Strategy Real-Build Correctness" shipped **Option C** 2026-08-17: `bunexec.Compiler.Prepare`'s `checkEffectiveAdapter` fails fast, before `bun run build` runs, when the strategy's target adapter isn't configured in whichever file SvelteKit will actually read. This is a real, useful fix — it turns a silent wrong build or a confusing later failure into an immediate, actionable error. **It is not zero-config.** A misconfigured project still requires a manual one-line edit; Pokkum tells the user exactly what to change and where, but does not change it for them. `PrepareVirtualConfig`'s `.pokkum/svelte.config.js` transform still runs and is still never read by the actual build (that part of the original v0.2 bug is unfixed, just no longer silently wrong — see `internal/core/core.md`'s "Zero-config auto-injection" entry for the current, corrected state).

### 2.2 What it should be

Per the original concept doc's Option B: invoke Vite directly — `vite build --config .pokkum/vite.config.ts` — instead of `bun run build` (the user's `package.json` `"build"` script), where the generated wrapper controls the `sveltekit()` call's `adapter` option directly, regardless of what the user's real `svelte.config.js`/`vite.config.ts` configured.

### 2.3 The pitfall that breaks the naive version, and how to avoid it — CONFIRMED (new spike, 2026-08-17)

The original concept doc's pseudocode used `mergeConfig(userConfig, { plugins: [sveltekit({ adapter: pokkumAdapter() })] })` and flagged, as an open risk, that "merging Vite configs correctly (plugin arrays, in particular) is not always a clean override." **This was spiked just now, empirically, against a real project (`sv create`'s default scaffold, `@sveltejs/adapter-auto` configured) — and the risk is real and severe, not hypothetical:**

`mergeConfig`'s `plugins` handling is **array concatenation, not replacement**. A wrapper built as

```ts
import adapter from "@sveltejs/adapter-node";
import { sveltekit } from "@sveltejs/kit/vite";
import { defineConfig, mergeConfig } from "vite";
import userConfig from "../vite.config.ts";

export default defineConfig(
  mergeConfig(userConfig, { plugins: [sveltekit({ adapter: adapter() })] }),
);
```

does not override the user's `sveltekit()` plugin instance — it **adds a second one**, so both instances' Svelte-compile sub-plugins run against the same source files. The actual failure mode observed was not an adapter mismatch; it was a hard build crash with a misleading error (`RolldownError: Expected token }` inside Svelte-compiler-generated output) that gives no hint the real cause is a duplicated plugin, not a Svelte syntax problem. **This is the kind of failure that would be very expensive to debug for real users if it shipped** — it doesn't say "adapter conflict," it says "your generated code is broken," pointing the user at exactly the wrong place.

**The fix that was confirmed to work:** don't merge the `plugins` key — replace it outright.

```ts
import adapter from "@sveltejs/adapter-node";
import { sveltekit } from "@sveltejs/kit/vite";
import { defineConfig } from "vite";
import userConfig from "../vite.config.ts";

const resolved =
  typeof userConfig === "function"
    ? await userConfig({ command: "build", mode: "production" })
    : userConfig;

export default defineConfig({
  ...resolved,
  plugins: [sveltekit({ adapter: adapter() })],
});
```

Verified: this actually builds, actually uses `@sveltejs/adapter-node`, against a project whose own `vite.config.ts` configures `@sveltejs/adapter-auto` — real `build/` output (`handler.js`, `env.js`, `shims.js`, `client/`) was produced. **The core mechanism works.** But replacing `plugins` wholesale has a cost that must be designed for, not silently accepted — see next.

### 2.4 The cost the fix above doesn't solve, and why it can't be solved by "just merging harder"

Full plugin-array replacement means:

- **Any other Vite plugin the user configured is silently dropped** — Tailwind, mdsvex, paraglide, a custom plugin, anything. A real project using any of the official `sv create` add-ons (`docs/archive/Roadmap.md`'s v0.2 add-on list: tailwindcss, mdsvex, paraglide, storybook, drizzle...) would build successfully but **wrongly** — missing CSS processing, missing MDX support, etc. — which is worse than today's Option C, because it fails _silently correct-looking_ rather than fails _fast and loud_.
- **`sveltekit()`'s own other options are dropped too** — the real captured default `sv create` scaffold (see `sveltekitutils/project_test.go`'s `realSvCreateDefaultViteConfig`) passes `compilerOptions.runes` inside the same call. A naive full-replacement wrapper drops that silently as well.

**This is not a solvable-by-cleverer-merging problem in general** — Vite plugin objects are opaque (a plain object with hook functions closed over whatever state the factory captured at call time); there is no generic, version-stable way to say "take this plugin instance but change one option it was constructed with" without either (a) knowing the specific shape `@sveltejs/kit/vite`'s `sveltekit()` factory produces (an implementation detail, not a public API, that can change between SvelteKit versions without notice — the exact kind of fragility this whole file's Lessons.md entries are about avoiding), or (b) re-deriving the user's _other_ `sveltekit()` options textually (the same `stripJSComments`/regex-scan approach `sveltekitutils` already uses for detection) and re-passing them into a freshly-constructed `sveltekit()` call — i.e., surgical _option_ replacement within a freshly-called plugin, not surgical _object_ replacement of an opaque instance.

**Recommended approach, not yet spiked:** (b) is more tractable than it sounds, because Pokkum already has the textual-scan infrastructure this needs. `sveltekitutils.sveltekitPluginOptions` (added for `ViteConfigOverridesSvelteConfig`, see `internal/adapters/sveltekitutils/project.go`) already extracts the raw argument text of a `sveltekit(...)` call. A wrapper generator could:

1. Extract the user's `sveltekit()` call's argument object text (already have this).
2. Textually strip or override just the `adapter:` key within it (a bounded, well-scoped text transform — much narrower than a general JS/TS parse, and in the same spirit as the config-source transforms `sveltekitutils.TransformConfig` already does for `svelte.config.js`).
3. Re-emit a `sveltekit({ ...user's other options unchanged..., adapter: pokkumAdapter() })` call in the wrapper, and keep every _other_ plugin in the user's `plugins` array as-is (only the `sveltekit()` entry needs replacing — the array's other members are just re-exported unchanged, since only the SvelteKit plugin instance carries `adapter`).

This preserves other plugins and other `sveltekit()` options, at the cost of a second, more surgical textual transform (find-the-`sveltekit()`-call-in-the-plugins-array-and-replace-just-it, rather than blanket `plugins: [...]` replacement). **This needs its own spike before implementation** — specifically: does the plugins array always contain the `sveltekit()` call as a _direct_ array element (the common/documented shape), or can real projects nest it (e.g. `plugins: [[sveltekit(...)], otherPlugin()]`, or wrap it in a conditional/ternary)? A real-world survey of `sv create` add-on output plus a handful of popular starter templates would answer this empirically, the same way this whole roadmap item was resolved by running real tools instead of assuming.

### 2.5 The other risk the original concept doc already named, still true

**Bypassing `bun run build` (the user's real `package.json` `"build"` script) in favor of driving Vite directly is a real behavior change**, not a detail. If that script does anything beyond `vite build` — env var setup, a monorepo task-runner invocation, a pre-build codegen step — Option B silently skips it. The original concept doc recommended surveying real projects' `"build"` scripts before committing to this; that survey still hasn't happened and should be step zero of implementing Option B, not an afterthought. A reasonable middle ground worth considering: detect when the user's `"build"` script is _exactly_ `vite build` (the common, unmodified case) and only take the Option B fast path then, falling back to today's Option C (fail-fast, no override) when the script does anything else — this bounds the blast radius of "silently skipped a step" to zero, at the cost of Option B not helping projects with a customized build script at all.

### 2.6 Interaction with the already-shipped Option C

Option C's `checkEffectiveAdapter` should **not** be deleted when Option B ships — it should become the fallback for exactly the case in §2.5 (custom build script) and the case in §2.4 (unhandled plugin-array shape) where Option B's textual transform can't confidently apply. The two compose: Option B handles the common case silently and correctly; Option C's existing fail-fast-with-a-clear-message remains the answer whenever Option B's own preconditions (bounded plugins-array shape, unmodified build script) aren't met. This mirrors how Option A + Option C already compose in Part 1 — a "make it actually work" mechanism paired with a "detect and refuse the shapes it doesn't handle" backstop, rather than one replacing the other.

### 2.7 Other problems and how to avoid them

- **Vite config file resolution order.** `readViteConfigSource` (added for Option C) already knows Vite's real candidate list (`vite.config.js`, `.mjs`, `.ts`, `.cjs`, `.mts`, `.cts`, first-match-wins) — reuse it rather than re-deriving. The wrapper's own dynamic `import('../vite.config.ts')` must resolve to the _same_ file Vite itself would have loaded, not just "a file that exists."
- **The static-strategy equivalent.** `checkEffectiveAdapter` already runs for `StrategyStatic` too (target `@sveltejs/adapter-static`), so Option B's wrapper generation needs the same `@sveltejs/adapter-static`-aware branch — this is a parameterization of the same mechanism, not a separate design, but it needs its own real-project verification pass (does `sv create --add sveltekit-adapter=adapter:static` produce the same vite.config.ts-only shape confirmed for adapter-node in this session's empirical work? Verify, don't assume — matching this whole roadmap item's own lesson).
- **TypeScript loading of the user's `vite.config.ts` from the wrapper.** The spike above used `bunx vite build --config <wrapper>`, letting Vite's own config loader (which already handles `.ts` config files via esbuild) do the dynamic import. If Pokkum ever needs to inspect the resolved config _before_ invoking Vite (e.g. to decide whether Option B's preconditions are met, per §2.5/§2.6), that inspection needs the same TS-aware loading Vite itself uses — don't hand-roll a separate TS-to-JS step that could disagree with Vite's own.
- **Monorepo/workspace root detection.** A `vite.config.ts` that itself imports from a workspace-relative path (`../../shared/vite-config-base.ts`, common in monorepos) needs the wrapper's dynamic import to resolve from the _project_ directory, not Pokkum's own `.pokkum/` sandbox directory — confirm Node/Bun's module resolution does the right thing here relative to where the wrapper file physically lives versus where it's conceptually "for."

### 2.8 Open questions (Option B)

- Does the surgical `adapter:`-key replacement in §2.4 belong in `sveltekitutils` (as a new `TransformViteConfig`, alongside the existing `TransformConfig` for `svelte.config.js`) or in a new package — given it's meaningfully more complex (nested-array-shape detection) than the existing textual transforms?
  - **Decision:** Keep the text-transform in `sveltekitutils` as an exported `TransformViteConfig`. If/when the wrapper _file generation_ (writing the whole `.pokkum/vite.config.ts`) grows enough to stand alone, extract only that emission step into a separate package/type; the detection-and-surgical-rewrite core belongs in `sveltekitutils`. This also puts the new function adjacent to the exact `sveltekitPluginOptions` it builds on, which is where the future maintainer will look.
- Should the real-project `"build"`-script survey from §2.5 be automated (e.g. a `pokkum doctor` check that flags "your build script isn't `vite build`, Option B won't engage") or is a one-time manual survey (a handful of popular starter templates + all `sv create` add-on combinations) sufficient to set the initial policy?
  - **Decision:** **Do the one-time manual survey first, as step zero** — it's a prerequisite regardless (it answers the §2.4 nested-shape question and the §2.7 static-strategy-shape question), is cheap, and gates everything. Then, once Option B ships, add the `pokkum doctor` advisory check so the Option B active/inactive state is visible and self-documenting. Don't let the doctor check _precede_ the survey — it can't answer the questions the survey exists to answer.
- Given both Option A and Option B are now spiked with a confirmed-working core mechanism and a confirmed real pitfall each, should they be scoped as one combined implementation task (both touch the layered strategy's build path) or kept independent (they're otherwise unrelated — one is supervisor/runtime, the other is SvelteKit-build-time)? Recommend independent: no shared code path, no reason to couple their schedules.
  - **Decision:** **Independent, and I'd even put Option B before Option A.** Rationale: (a) Option B's _fix_ is already spiked working and only needs the surgical-replacement design completed plus the template survey — it's closer to shippable than Option A, which still needs cross-compile + build-time-Bun CI plumbing that no spike has touched; (b) Option B _completes_ a product promise ("zero-config") that today is only a fail-fast error, whereas Option A buys parity with a baseline users may never have had; (c) they compose with the same backstop (Option C) but don't depend on each other, so ordering is free. Independent scope on the _same_ backlog is fine — just don't ship Option B without §2.4's surgical replacement (the blanket `plugins` replacement in §2.3 is a silent-wrongness regression vs. Option C, which the doc itself flags).

---

## Summary table

|                                                | Option A (runtime hardening)                                                  | Option B (real zero-config)                                                                                                |
| ---------------------------------------------- | ----------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| Status before this doc                         | Spiked (2026-08-17, archived doc), deferred                                   | Deferred, unspiked                                                                                                         |
| Status after this doc                          | Spiked, deferred (unchanged)                                                  | **Newly spiked (2026-08-17), core mechanism confirmed working, deferred pending §2.4's surgical-replacement design**       |
| Naive approach                                 | Static `import("/app/server/index.js")`                                       | `mergeConfig(userConfig, { plugins: [...] })`                                                                              |
| Why it fails                                   | Bundler constant-folds the literal, bakes app into stub, breaks layer caching | `plugins` arrays concatenate, not replace — duplicate SvelteKit plugin instance corrupts the build with a misleading error |
| Confirmed fix                                  | Non-foldable path expression (string concat / helper function)                | Full `plugins` key replacement (works, but drops other plugins/options — see §2.4 for the better fix, not yet spiked)      |
| What still needs a spike before implementation | Cross-compilation per (version × arch); build-time Bun CI dependency plumbing | Surgical `adapter:`-only replacement inside an existing `sveltekit()` call, across real plugins-array shapes               |
| Composes with its "detect and refuse" backstop | Option C (attestation) — already shipped                                      | Option C (`checkEffectiveAdapter`) — already shipped                                                                       |
