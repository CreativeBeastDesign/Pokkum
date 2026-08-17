# Concept: Making Zero-Config Auto-Injection Actually Take Effect

## 1. Problem Statement & Motivation

v0.2 shipped "Zero-Config Auto-Injection" (`Roadmap.md`): "Auto-injecting the adapter and `SOURCE_DATE_EPOCH` pinning without manual `svelte.config.js` edits." As implemented, it does not do this. Confirmed by tracing the code and by running a real `bun run build` against a fresh `sv create` scaffold:

- `internal/adapters/bunexec/compiler.go`'s `Prepare` calls `sveltekitutils.PrepareVirtualConfig(req.ProjectDir, opts)` (~line 272), which reads the real `svelte.config.js`, transforms it (rewriting the adapter import, pinning the version), and writes the result to `<ProjectDir>/.pokkum/svelte.config.js` — a **separate file**, matching the Zero-Mutation Build Sandbox invariant (`CLAUDE.md` §2: user-authored source files must never be overwritten).
- `Prepare` then runs `exec.CommandContext(ctx, "bun", "run", "build")` with `cmd.Dir = req.ProjectDir` — i.e. it runs the user's own `package.json` `"build"` script, in the **real** project directory, against the **real** `svelte.config.js`.
- Nothing connects the two. Grep the whole repo: `VirtualConfigResult.VirtualConfigPath` is read exactly once, for a log line (`compiler.go:276`). `POKKUM_AUTO_INJECT` (set into the build subprocess's env by `sveltekitutils.BuildEnv`) is never read anywhere — not by any Pokkum code, and there is no reason to expect SvelteKit's own `@sveltejs/kit/vite` plugin (a third-party package Pokkum does not patch) to recognize a Pokkum-specific env var.
- **The virtual config file is inert.** It gets written, and nothing ever reads it again.

### How this stayed undiscovered

`tests/integration/reproducibility_e2e_test.go`'s `TestRealBuildIsReproducibleAcrossRuns` — the repo's only real-`bun` integration test — builds `testdata/fixtures/sveltekit-basic`, whose checked-in `svelte.config.js` already imports the **correct** adapter (`@jesterkit/exe-sveltekit`, matching `StrategyExe`) directly. The test proves builds are reproducible when the adapter is already correctly configured; it has never exercised whether Pokkum's injection is what makes that adapter correct in the first place. No test anywhere asserts that a project with the **wrong** (or no) adapter configured gets fixed up by Pokkum before the real build runs.

### A second, compounding bug: current `sv create` scaffolds don't even use `svelte.config.js` for the adapter

Confirmed with `npx sv create test-app --template minimal --types ts` against `@sveltejs/kit@2.70.2` / `sv@0.17.0` (current as of 2026-08-16): the generated `vite.config.ts` configures the adapter via the `sveltekit({...})` Vite plugin's options object, not `svelte.config.js`. When `vite.config.ts` passes options this way, SvelteKit prints, verbatim, on every build:

```
svelte.config.js is ignored when options are passed via your Vite config
```

...and does not read `svelte.config.js` **at all** — not even the real one, let alone Pokkum's virtual copy. This means even a correct fix to the bug above (making the virtual config actually get read) would still fail against a project scaffolded by the current, officially-recommended `sv create` — which is precisely the workflow `paranoid-testing-guide.md` §1 and countless new users follow first.

### Severity

Both bugs together mean: a user who runs `pokkum build` against a project they have not **already** hand-configured with the correct adapter — via `svelte.config.js`, in the one specific shape Pokkum's injector understands, which the current scaffolding tool doesn't even produce — gets a failed build with a message that doesn't explain why. "Zero-config" currently means "works if you already did the config Pokkum claims you don't need to do."

---

## 2. Constraint: what Pokkum controls vs. what it doesn't

Pokkum does not vendor or patch `@sveltejs/kit`, `vite`, or any adapter package — it drives them as an external `bun run build` subprocess. Any fix has to work through inputs those tools actually consume: real files on disk, real CLI flags, real env vars *those specific tools* document and read. `POKKUM_AUTO_INJECT` was never going to work because nothing on the SvelteKit/Vite side was ever going to look for it — Pokkum would have had to invent that contract and get upstream to implement it, which was never in scope.

Vite itself **does** support `vite build --config <path>` — an explicit override of which `vite.config.(js|ts)` file to load, independent of what's in the project directory. This is the one real lever available: Pokkum can construct its own config file and hand it to Vite directly, instead of hoping a file dropped in `.pokkum/` gets picked up by convention.

---

## 3. Proposed Designs

### Option A — Temporary real-file swap, restore after build

Back up the real `svelte.config.js`, overwrite it in place with the transformed content, run `bun run build` (still via the user's own `package.json` script, unchanged), restore the original from the backup in a `defer` (covering both success and error paths, including a hard process kill via a `.pokkum/svelte.config.js.orig` on-disk backup checked for and restored at the *start* of the next `Prepare` call, not just relying on the in-process defer).

**Pros:** simplest to implement; keeps running the user's real `"build"` script unchanged, so anything else that script does (env setup, workspace-level pre/post hooks) keeps working.
**Cons:** directly violates the letter of the Zero-Mutation Build Sandbox invariant, even though the violation is transient and best-effort-restored. Does **not** fix the `sv create`/vite.config.ts case (§1) — SvelteKit ignores `svelte.config.js` regardless of its content whenever `vite.config.ts` passes plugin options, so swapping it changes nothing for that case. A crash between swap and restore (OOM kill, SIGKILL, host power loss) leaves the user's real file mutated on disk with no in-process cleanup having run — the on-disk `.orig` backup mitigates but does not eliminate the window, and a project without git (or with uncommitted changes to `svelte.config.js` at exactly that moment) could genuinely lose work.

### Option B — Generated wrapper `vite.config.ts`, invoked via `vite build --config`

Instead of `bun run build` (the user's `package.json` script), invoke Vite directly: `bunx vite build --config .pokkum/vite.config.ts`, where the generated wrapper:

```ts
// .pokkum/vite.config.ts (generated, never written to the real project dir)
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig, mergeConfig } from 'vite';
import userConfig from '<projectDir>/vite.config.ts'; // dynamic import of the real file

export default defineConfig(
  mergeConfig(userConfig, {
    plugins: [
      // Replaces any existing sveltekit() plugin instance from userConfig
      // with one carrying Pokkum's resolved adapter/options — see open
      // question below on plugin-array surgery vs. full override.
      sveltekit({ adapter: pokkumAdapter(/* ... */) })
    ]
  })
);
```

**Pros:** uses Vite's own documented override mechanism — no mutation of any real file, ever; genuinely fixes both bugs in §1 at once, since the wrapper controls the `sveltekit()` call directly regardless of whether the user's real config put adapter options in `svelte.config.js`, `vite.config.ts`, or nowhere at all.
**Cons:** bypasses the user's real `package.json` `"build"` script entirely — if that script does anything beyond `vite build` (env setup, a monorepo task runner, a pre-build codegen step), Pokkum silently skips it, which could break a real project in a way that's hard to diagnose (the failure shows up as "the app doesn't work" much later, not as a build error). "Replace the existing `sveltekit()` plugin instance" is real surgery on an arbitrary user AST/config object — merging Vite configs correctly (plugin arrays, in particular) is not always a clean override; a user with plugin ordering that matters (e.g. `sveltekit()` composed with other Vite plugins that need to run before/after it) could get a build that differs from what they'd get running their own script.

### Option C — Detect and fail clearly; stop calling it zero-config until it is

Keep the current behavior of running the real `svelte.config.js`/`vite.config.ts` as-is, but replace the current generic "expected entrypoint not found" failure with a real preflight check: read both files, determine (a) whether the correct target adapter is actually configured somewhere that will actually be read (accounting for the `vite.config.ts`-options-wins rule in §1), and (b) if not, fail *before* running `bun run build` at all, with an exact, actionable message — e.g. "project uses `@sveltejs/adapter-auto`; `--strategy=layered` requires `@sveltejs/adapter-node` configured in `vite.config.ts`'s `sveltekit({ adapter: ... })` call (svelte.config.js is ignored because your vite.config.ts already passes plugin options) — see \<docs link\>." This requires the SAME detection logic Option A/B would need (does `vite.config.ts` pass options that make `svelte.config.js` inert; if so, is the adapter there instead), just without attempting to fix it automatically.

**Pros:** lowest engineering risk by a wide margin; ships fast; never silently does the wrong thing (no invisible bypass of a custom build script, no real-file mutation risk). Turns every currently-confusing failure into an actionable one.
**Cons:** is a real reduction in the shipped v0.2 feature's claim — "zero-config" becomes "clear error if not configured, here's exactly what to change." `Roadmap.md`/`Vocabulary.md`/marketing copy referencing "Zero-Config Auto-Injection" needs correcting either way (it doesn't currently do what it says), but Option C makes that correction more visible rather than fixing the underlying gap.

---

## 4. Recommendation

**Ship Option C first, unblock beta, then treat Option B as a properly-scoped follow-up — do not attempt Option A.**

Option A is worse than either alternative on every axis that matters here: it doesn't fix the `sv create` case that's arguably the more common failure mode today, and it introduces a real (if narrow) risk of corrupting a user's real source file, for a feature that's supposed to be the *safe*, non-mutating alternative to hand-editing config. It should not be built regardless of timeline pressure.

Between B and C: C is the right thing to ship under beta-readiness time pressure specifically because it's low-risk and immediately honest — a clear, correct error is a functioning product; a feature that silently doesn't do what it claims is not, no matter how good the error message eventually gets. B is the right long-term fix (it's the only option that actually delivers on "zero-config"), but "bypass the user's own build script" is exactly the kind of design decision that deserves its own scoped implementation task with real test coverage against a *build script that does something Pokkum doesn't expect* — not something to build under the same pressure that's currently blocking beta.

---

## 5. Open Questions

- Does Option C's detection logic belong in `sveltekitutils` (extending `AdapterConfigured`) or as a new `doctor`-style preflight check (`pokkum doctor` already exists for exactly this class of "tell the user what's wrong before they waste a build" concern per `Roadmap.md` v0.4) — should this actually be `pokkum doctor`'s job rather than `Prepare`'s?
  - **Resolved 2026-08-17: `Prepare`, not `doctor`.** `Prepare` runs on every real `pokkum build`; `doctor` is opt-in, and the exact new-user workflow that surfaced this bug (`sv create` → `pokkum build`) never runs `doctor` first — a `doctor`-only check would not have fixed anything a real user hits. `Prepare` also already has the strategy-aware context (`targetAdapter`, chosen per `ports.BuildStrategy`) that `doctor` structurally lacks today (no `--strategy` flag), so building this in `Prepare` needed no new plumbing. Implemented as `sveltekitutils.EffectiveAdapterConfigured`/`ViteConfigOverridesSvelteConfig` (extends `sveltekitutils` as this question's first option proposed) plus `bunexec.Compiler.Prepare`'s `checkEffectiveAdapter`, which calls it and fails before any subprocess is spawned. A follow-up `pokkum doctor --strategy` check (catching the same misconfiguration before a user even attempts a build) remains a reasonable low-priority enhancement, not built here.
- For Option B (follow-up): does "bypass `package.json`'s build script" need to become an explicit, documented, opt-out-able behavior change (a new flag?), or is driving Vite directly always strictly a superset of what `bun run build` would have done for a SvelteKit project specifically (i.e. is there ever a legitimate reason a SvelteKit project's `"build"` script differs from `vite build`)? Needs real survey of what real projects put in that script before committing to B.
  - _Undecided._
- Should the fix (whichever option) also cover `StrategyStatic`'s equivalent adapter-detection path (`internal/adapters/bunexec/compiler.go`'s static branch, `@sveltejs/adapter-static`), which shares the same "is the right adapter actually configured somewhere that gets read" question, or is that narrow enough (fewer real-world adapter-static + custom-vite-config combinations observed) to defer separately?
  - _Undecided._
- Once Option C ships, does `Roadmap.md`'s "Zero-Config Auto-Injection" v0.2 entry need a correction note (matching the pattern already used elsewhere in this doc for overstated `[x]` items — see `fixes-to-v1.md`), or does it wait until Option B actually closes the gap?
  - **Resolved 2026-08-17: corrected immediately**, per the recommendation. See `Roadmap.md`'s "Layered-Strategy Real-Build Correctness" section, whose "Zero-Config Auto-Injection" bullet now states plainly that Option C delivers a fast, actionable error on misconfiguration — not the zero-config build v0.2 originally claimed. Option B is unaffected and still tracked as a separate, deferred follow-up.
