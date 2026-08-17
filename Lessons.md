# Lessons

Post-mortems for bugs caught during self-review or debugging, with the
preventative rule each one produced. Newest entries first.

---

## 2026-08-17 — PR-2's first cut of `--hermetic` network enforcement only sandboxed half the pipeline — Compile ran fully unsandboxed, a complete bypass

**Category:** security / incomplete-scope (new checklist row 18) — caught by an adversarial Opus security review requested proactively before declaring the feature done, not by a test failure or user report

**Root cause:** PR-2 implements real kernel-enforced network isolation for `--hermetic` via an unprivileged Linux network namespace (`CLONE_NEWNET|CLONE_NEWUSER`, see the new-that-day `hermetic_linux.go`). The first cut wired this into `bunexec.Compiler.Prepare` (`bun run build`/`bun x vite build`) only — the stage the roadmap's own line reference (`compiler.go:324`) pointed at. `Compiler.Compile` (`bun build --compile`, stage two) was left completely unsandboxed, with no `Hermetic` field on `ports.CompileRequest` at all. This is a full bypass, not a partial one: `bun build --compile` bundles `req.EntrypointPath` — a file the third-party SvelteKit adapter *generated during stage one* — and Bun's bundler executes `bunfig.toml` preload plugins and `with { type: "macro" }` imports at bundle time, so a malicious build-time dependency does not need to defeat the sandbox at all; it only has to wait for stage two, which runs with the process's real, unrestricted network access. The two-stage nature of this package (documented in its own package doc comment) made "sandbox the subprocess spawn I was told about" feel complete while leaving the other spawn site — in the same file, using the identical `cmd := exec.CommandContext(...)`/`setNewProcessGroup(cmd)` pattern — untouched.

A dedicated adversarial review (Opus, prompted specifically to look for bypasses, not just "does it compile") caught this, along with four smaller real issues in the same diff: (F3) the Start-failure/build-failure error branch keyed off whether *any* stderr was captured, but `cmd.Stdout = os.Stderr` means a build that fails and prints to stdout was misdiagnosed as "sandbox failed to start" and told the operator to disable `--hermetic` — a security control's own error message nudging users to turn it off on a false diagnosis; (F4) `setNewProcessGroup` assigned `cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}` — a *replacing* assignment — meaning `applyHermeticSandbox`'s Cloneflags survive only because of call order, with no guard against a future reordering silently downgrading "sandbox active" (still logged) to no sandbox at all; (F5) SLSA provenance recorded `req.Hermetic` — which pipeline.go, it turned out, never actually populated in the `SLSAGeneratorRequest{}` literal at all, so provenance always silently claimed `hermetic: false` regardless of the real build, on top of the separate honesty gap that even a correctly-populated bool can't distinguish a Linux kernel-enforced build from a macOS advisory-only one; (F6) `bunruntime.Resolver` had no `Offline`/`Hermetic` awareness, so a `--hermetic` build with a cold Bun-runtime cache still reached the network to download it, unlike `Preflight`'s existing pre-populated-`node_modules` check for the exact same class of gap.

**Where:** `internal/adapters/bunexec/compiler.go` (`Compile`, previously no sandboxing at all; `Prepare`'s Start/Wait-split error handling), `internal/ports/compiler.go` (`CompileRequest.Hermetic`, added), `internal/ports/bunruntime.go` (`BunResolverRequest.Offline`, added), `internal/adapters/bunruntime/resolver.go` (download-path gate), `internal/ports/supplychain.go` (`SLSAGeneratorRequest.HermeticEnforcement`, added), `internal/core/pipeline.go` (`hermeticEnforcementMode`, and the previously-missing `Hermetic`/`HermeticEnforcement` wiring into the SLSA `Generate` call).

**Fix:** sandboxed `Compile` identically to `Prepare` (same `applyHermeticSandbox`/`verifyHermeticSandboxApplied` calls, same fail-closed Start-error handling), split `cmd.Run()` into `cmd.Start()`+`cmd.Wait()` in both methods so a namespace-setup failure is distinguished from a real build failure by construction rather than by an empty-stderr heuristic (this also fixed F8, a minor "sandbox active" log line firing before the sandbox was confirmed to exist, as a side effect of the same refactor), made `setNewProcessGroup` additive to `cmd.SysProcAttr` instead of replacing it and added `verifyHermeticSandboxApplied` as a last-resort pre-`Start()` assertion, added `HermeticEnforcement` (`"kernel-enforced-netns"` / `"advisory-env-only"` / omitted) alongside the existing `Hermetic` bool in SLSA provenance — derived once at the composition root from `runtime.GOOS`, safe to trust because `bunexec`'s own error paths already fail the whole build closed rather than let enforcement silently degrade — and gated `bunruntime.Resolver`'s download path on a new `Offline` field, mirroring `Preflight`'s existing pattern. Every fix has a real, execution-proving test: `TestCompile_HermeticModeBlocksRealNetworkEgress` (mirrors the existing `Prepare` version — a real TCP listener, a fake `bun` script attempting to reach it, run inside a real Docker container with `--security-opt seccomp=unconfined --cap-add=SYS_ADMIN` since Docker's own default sandbox otherwise blocks the nested unprivileged userns this feature needs), `TestResolver_Offline_FailsClosedOnCacheMiss`/`_SucceedsOnCacheHit`, and `TestBuild_HermeticThreadsIntoBunResolverAndSLSAProvenance` (one real `core.Build` call proving both new fields actually get threaded, not just that they exist on their structs).

**Not fixed this session, documented instead (see `Roadmap.md`'s PR-2 entry):** filesystem-*path* Unix domain sockets (as opposed to Linux's abstract-namespace sockets, which the network namespace does correctly isolate) are namespaced by the *mount* namespace, not the network namespace — `CLONE_NEWNS` is deliberately not unshared here (unsharing it safely would require remounting the project directory and any legitimately-needed paths, a much larger change), so a sandboxed build can still reach anything listening on a reachable socket path: `/var/run/docker.sock` if bind-mounted into the build environment, `$SSH_AUTH_SOCK` (passed through since `baseEnv` inherits `os.Environ()`), etc. The claim is therefore "no IP network egress," not "zero network egress" — `Vocabulary.md`/`Feature-list.md`/`ARCHITECTURE.md` are worded accordingly rather than overclaiming a guarantee this session's fix doesn't fully provide.

**Preventative rule (new checklist row 18):** when a security control wraps subprocess execution, enumerate every subprocess spawn site that runs the same class of untrusted input, not just the one a roadmap item's line reference or bug description happens to point at — grep the package for every `exec.Command`/`exec.CommandContext` call and ask "does the same untrusted-code-execution risk this control is closing also apply here" for each one, rather than trusting that fixing the first/most obvious site fixed the risk. A multi-stage pipeline (this package's own doc comment describes Prepare/Compile as a documented two-stage flow) is exactly where this kind of partial fix hides, because the fixed stage's tests pass cleanly and give a false sense of completeness.

**Category:** feature reality check (Row 16 family) — found while investigating PR-5 ("OTel spans will have unbounded cardinality"), before writing a single line of the fix, per `mem:self_review_checklist` row 16's "verify an open roadmap item's problem statement before starting" discipline

**Root cause:** PR-5's premise assumes OpenTelemetry auto-instrumentation already runs on every telemetry-enabled build and only names one refinement (route templating for span names). Grepping the actual call graph before starting found something more fundamental: `internal/adapters/sveltekitutils/telemetry.go`'s `PrepareVirtualInstrumentation`/`GenerateInstrumentationServer` — a complete, well-tested (`telemetry_test.go`) generator for `.pokkum/src/instrumentation.server.ts` (Node SDK + `getNodeAutoInstrumentations()` + OTLP exporters) — has **zero callers outside its own test file**, in the whole repository. `internal/core/pipeline.go`'s `ports.PrepareRequest{...}` literal never sets `Telemetry: req.Telemetry`, and `internal/adapters/bunexec/compiler.go`'s `Compiler.Prepare` never reads `req.Telemetry` even if it were set — confirmed by grepping both files for every spelling of "telemetry"/"instrumentation" and finding no hits outside doc comments. `ARCHITECTURE.md` §7 and `Feature-list.md`'s "Zero-Config Virtual Instrumentation" bullet both assert this "automatically injects" — a real overclaim, structurally identical to the `pokkum explain`/`why`/`diff` stub incident (`Lessons.md`'s PB-1 entry) and the PB-3/PB-4 config-field-parity misses, just in a different subsystem.

It goes one level deeper: even the *other* half of the mechanism — `injector.go`'s `TransformConfig`/`PrepareVirtualConfig`, which sets `kit.experimental.tracing.server`/`instrumentation.server` in a virtual `svelte.config.js` (the SvelteKit-level flag that makes native route/request tracing and the `instrumentation.server.ts` auto-load actually activate) — is *also* dead: `bunexec/compiler.go`'s own comment at the `baseEnv` construction site says outright that `PrepareVirtualConfig`'s `.pokkum/svelte.config.js` output "is never read by either build path" (real `bun run build` reads the real, unmodified `svelte.config.js`; the Option B wrapper only swaps `vite.config.ts`, and SvelteKit's Vite plugin independently loads `svelte.config.js` from the project root regardless of which `vite.config.ts` path is passed to `vite build --config`). And Option B itself — the only mechanism that ever stages files under `.pokkum/` in a location the real build actually reads — only engages when `checkEffectiveAdapter` finds the target adapter *misconfigured*; a project with `@sveltejs/adapter-node` already correctly wired (the common, expected case) never touches `.pokkum/` at all, so telemetry injection has no path to take effect even in principle for that case today.

**Where:** `internal/adapters/sveltekitutils/telemetry.go` (unused generator), `internal/core/pipeline.go` (missing `Telemetry:` field in the `PrepareRequest{}` literal), `internal/adapters/bunexec/compiler.go` (never reads `req.Telemetry`; comment near `baseEnv` construction already documents the separate `.pokkum/svelte.config.js` dead-output problem), `internal/adapters/sveltekitutils/injector.go` (`EnableTelemetry` option, set nowhere).

**Fix:** not attempted this session. This is not a narrow, low-blast-radius fix — closing it correctly requires the Option B virtual-config wrapper to engage whenever telemetry is enabled, independent of whether adapter injection is needed, which changes the trigger condition for the code path that decides how `bun run build` vs. `bun x vite build --config ...` gets invoked for *every* build, not just telemetry-enabled ones. That is exactly the kind of "strategy-gated / build-dispatch logic" change `mem:self_review_checklist` row 11 already warns is easy to get subtly wrong (its own origin incident was a build-dispatch branch silently breaking the default strategy for every user), and it would need genuine end-to-end verification (a real `bun run build` producing a working, telemetry-instrumented server — matching row 17's "must actually execute" standard) before it could be trusted, not just unit tests on the string-transform layer. Given the size and risk, and that this session is running unattended overnight, the responsible call was to stop, document the real scope, and not ship a route-templating refinement on top of a feature that does not run at all — that would have been a second, compounding instance of exactly the overclaim this entry documents.

**Preventative rule:** extends Row 16's "verify an open roadmap item's problem statement before starting" to cover *transitive* wiring, not just the top-level flag: grepping for the target flag's registration (row 16's existing check) is necessary but not sufficient — a flag can be wired, and the request field it sets can be threaded correctly one layer deep, while the deepest consumer (the actual adapter function) never reads it. When a roadmap item describes refining an existing behavior, grep every hop of the call chain from the CLI flag to the code that actually has the claimed effect (flag → `BuildRequest` field → `PrepareRequest`/equivalent field → the adapter function that reads it → the file it writes → confirmation that file is read by the real build command, not a `.pokkum/` path the build never visits) before designing the refinement — a broken link anywhere in that chain means the "refinement" would be built on top of dead code. `Roadmap.md`'s PR-5 entry is corrected to describe this finding rather than the original narrower cardinality framing.

---

## 2026-08-17 — Every `--strategy=layered` (default) image was missing its own entrypoint (`/app/server/index.js`); no image built by this codebase could actually start

**Category:** fixture fidelity / packaging boundary mismatch — the most severe bug found this session, discovered manually while smoke-testing `pokkum explain` against a real build, not by any existing test

**Root cause:** `internal/core/pipeline.go` set `pkgReq.AppServerDir = filepath.Join(prep.OutputDir, "server")` — packaging only the `server/` subdirectory of a SvelteKit build's output into the image's `/app/server` layer. But a real `@sveltejs/adapter-node` build emits its actual entrypoint (`index.js`, the file `ports.AppServerIndexPath = "/app/server/index.js"` names as what the supervisor execs by default) as a **sibling** of `server/`, not inside it — confirmed by inspecting the real `build/` directory of `testdata/fixtures/sveltekit-adapter-node`. `index.js` was therefore never packaged into any layer at all; `bun /app/server/index.js` had nothing to execute. This had been true since the layered strategy's packaging path was first written (`git log -S` traced it to the original `M1 & M2` commit) — it is not a regression, it is a bug that shipped from day one and was never caught, because:
1. No existing test extracts and actually *executes* a packaged image's entrypoint — every test (golden digests, determinism, layer-content assertions) checks structure and bytes, never runtime behavior.
2. The one test that models the layered strategy's directory shape at all (`tests/integration/e2e_test.go`'s `mockCompiler.Prepare`) placed its synthetic `index.js` **inside** the mocked `server/` directory — the same wrong assumption as the production code, so the mock could never have caught this even if something had tried to run it.

Fixing the first pass (staging `index.js` as a sibling of the packaged tree, with its `./server/...` import specifiers rewritten) was still wrong: real execution (extracting the actual packaged layer and running `bun index.js` against it) immediately surfaced a *second* problem — chunk files inside `server/` (e.g. `server/chunks/handler-<hash>.js`) reach back out to siblings like `shims.js`/`env.js` via `../../`-style relative imports that assume the *original* `build/` nesting is preserved. Flattening `server/` into `/app/server` (the packager's existing, pre-this-bug behavior) breaks those escapes regardless of where `index.js` itself is staged. The only fix that doesn't require parsing and rewriting an unbounded number of bundler-generated chunk files is to stop flattening at all: package `build/` as a whole, preserving every original relative path exactly, and exclude the `client/`/`prerendered/`/`vendor/`/`native/` subdirectories (packaged into their own layers elsewhere) via a new `pruneutils.PruneOptions.ExcludeDirs`.

**Where:** `internal/core/pipeline.go`'s `AppServerDir` assignment (StrategyLayered branch); `internal/adapters/packager/packager.go`'s server-layer `BuildDirectoryTreeLayerWithPruning` call; `internal/adapters/pruneutils/pruneutils.go` (new `ExcludeDirs`/`IsExcludedDir`); `internal/adapters/packager/layer.go`'s `WalkDir` callback; `tests/integration/e2e_test.go`'s `mockCompiler.Prepare` (the fixture that modeled the wrong shape).

**Fix:** `AppServerDir` now points at `prep.OutputDir` (the whole build output) instead of its `server/` subdirectory. `pruneutils.PruneOptions` gained `ExcludeDirs []string` — exact top-level subdirectory names to skip entirely via `filepath.SkipDir`, applying even under `NoPrune: true` since this is about avoiding duplication across layers, not disposable junk. The server layer's packaging call now excludes `client`/`vendor`/`native`/`prerendered`. No content rewriting of any kind is needed — every original relative import, at any depth, resolves correctly by construction because nothing is flattened anymore. `e2e_test.go`'s mock fixture was corrected to model the real shape (entrypoint at the top, a nested `server/chunks/` subtree). Verified by building the real `sveltekit-adapter-node` fixture end-to-end, extracting the actual packaged `/app/server` layer from the resulting OCI tarball, and running `PORT=... bun index.js` against the extracted files directly — it now boots and serves a real `HTTP 200`, which it did not before this fix (crashed with `Cannot find module '../../shims.js'` even after the first, incomplete fix attempt).

**Preventative rule (new checklist row, #17):** for any change to what gets packaged into a container layer (`internal/adapters/packager/*`, `internal/core/pipeline.go`'s `pkgReq.*Dir` assignments), verify the packaged output is *executable*, not just present — extract the actual built layer from a real end-to-end build and run its entrypoint (or the specific artifact under test) directly, for at least one real fixture. Every existing layer/packaging test in this codebase asserts tar-member presence, byte-for-byte determinism, or digest stability — none of that proves the bytes inside the tar actually run, and this bug is proof that "the file is present and has the right bytes" and "the file, once extracted into its real sibling directory structure, actually executes" are genuinely different claims. This generalizes Row 12's fixture-fidelity lesson one level further: even a test whose fixture *does* match the real upstream artifact's *shape* can still miss a bug if nothing ever executes the *result* of packaging that fixture.

---

## 2026-08-17 — `pokkum explain`'s new `Platform` field echoed the requested `--platform` flag instead of the resolved image's real platform, printing a value that was never actually verified against the image for two of its three input paths

**Category:** fabricated-looking value from an unverified assumption — a new instance of the same class this whole feature (PB-1) exists to eliminate, caught only by manual smoke testing against a real build, not by any of the (all-green) unit tests

**Root cause:** `runExplain`'s original implementation set `ports.ExplainOutput.Platform` directly from the parsed `--platform` flag (`platform.String()`), reasoning that this is the platform the user asked to inspect. That's only true for one of `resolveImage`'s three input shapes: a remote multi-arch *index*, where `--platform` genuinely drives which child gets selected. For a local `.tar` path (`tarball.ImageFromPath`) or a remote *single-image* (non-index) reference, `resolveImage` never consults `platform` at all — it just loads whatever image is there. In both of those cases the printed `Platform` field was silently echoing the default/requested flag value with zero verification that it matched the image's actual `OS`/`Architecture`. Caught by manually building a real `linux/amd64` image via `pokkum build --platform linux/amd64 --tarball=...` on an Apple Silicon (arm64) host, then running `pokkum explain <tarball>` with no `--platform` override (defaulting to `ports.LocalPlatform()`, i.e. `linux/arm64` on that host) — the output printed `Platform: linux/arm64` for an image that was, provably, `linux/amd64`. Every existing unit test always passed a `--platform` value matching the actual pushed/built image's real platform, so none of them could have caught a mismatch between "what was requested" and "what's actually there" — this is exactly Row 3's "differing content/outcome" gap, just for a single scalar field instead of a collection.

**Where:** `cmd/pokkum/explain.go`'s `runExplain`, the `ports.ExplainOutput{Platform: platform.String()}` line and its matching `fmt.Printf("Platform: %s\n", platform.String())` in the text-output path.

**Fix:** added `imageConfigPlatform(cfg *v1.ConfigFile) string`, which reads the *real* resolved image's `OS`/`Architecture`/`Variant` off its own `ConfigFile()` and formats it via `ports.Platform.String()` — the same real value used to build every other field in the payload (digests, sizes, purposes) — instead of the input flag. `--platform` still does its real job of selecting an index child in `resolveImage`; the output field just no longer trusts that selection blindly, it re-derives from what was actually returned. No test needed a behavior change since the existing `TestExplainCommand_PlatformSelection` fixture's per-child config already correctly matches the requested platform (real selection working correctly), so this fix couldn't be observed by that specific test — it's specifically the local-tarball and single-image-ref paths that were exposed, and neither had a test asserting the `Platform` field's value against anything (they only asserted layer counts).

**Preventative rule:** when a struct field is populated from *any* input parameter rather than from the resolved/verified result, ask specifically: does every code path that produces this field's *type* of information actually use that input parameter, or does the parameter only matter for a subset of paths (e.g. one of several `if`/`switch` branches in a shared resolver)? If the field can be derived from the verified result instead of the request, prefer that — it can never silently drift from reality the way echoing an unverified input can. This is the same "no fabricated data in the success path" hard constraint the whole explain/why/diff rewrite was built around, so its own new code violating it in one field is the kind of thing that specifically deserves a manual, real-build smoke test (not just unit tests against synthetic fixtures) before declaring the feature done — synthetic fixtures are usually built to already match what's being asked, exactly the failure mode Row 14 already warns about for a different feature class.

---

## 2026-08-17 — `pokkum why`/`pokkum diff` were documented as top-level commands in three separate places; they were never registered as anything but subcommands of `explain`

**Category:** documentation drift (new, narrow variant of the overclaiming category below — not a fabricated *feature*, a fabricated *command path* for a feature that, at the time, didn't work anyway)

**Root cause:** `cmd/pokkum/explain.go`'s `newExplainCommand` has always registered `why`/`diff` via `cmd.AddCommand(newWhyCommand(...))`/`cmd.AddCommand(newDiffCommand(...))` on the `explain` command itself, not on `rootCmd` — confirmed against `cmd/pokkum/main.go`, which only ever calls `rootCmd.AddCommand(newExplainCommand(...))`, never a separate `newWhyCommand`/`newDiffCommand` registration. The real, and never-changed, invocation paths are `pokkum explain why <file-path>` and `pokkum explain diff <img1> <img2>` — confirmed empirically by running `go run ./cmd/pokkum why --help`, which fails with `unknown command "why" for "pokkum"`. `Vocabulary.md`, `README.md`, and `Feature-list.md` all documented `pokkum why <file-path>` / `pokkum diff <img1> <img2>` as if they were top-level, independently invocable commands. Because the underlying commands were pure fabricated-data stubs until this same day's PB-1/PR-9 fix (see the entry below), nobody had a reason to actually run them and notice the path was wrong — the docs were never exercised against the real CLI.

**Where:** `Vocabulary.md`'s old §9 table, `README.md`'s command table, `Feature-list.md`'s Developer Experience bullet — vs. `cmd/pokkum/explain.go`'s actual `cmd.AddCommand` calls and `cmd/pokkum/main.go`'s `rootCmd.AddCommand` list.

**Fix:** corrected all three doc sites to `pokkum explain why` / `pokkum explain diff`, done as part of the same PB-1/PR-9 pass rather than a separate change, since touching those exact lines for the layer-count wording made the wrong command path visible in the same diff.

**Preventative rule:** extends this file's `overclaiming`/`fake-implementation` lesson (below) one level further: it is not enough to verify a documented *feature* against the code that implements it — a documented *invocation path* (command name, subcommand nesting, flag name) is also a checkable claim, and the check is even cheaper: run `--help` on the actual built binary and diff it against what the docs say the command line looks like. Do this whenever rewriting a command's docs, not only when the command's behavior changes.

---

## 2026-08-17 — Four shipped, `[x]`-marked, documented features were stubs or half-built; found only by verifying an outsider's review against the code instead of against the docs

**Category:** overclaiming / fake-implementation (new category — distinct from this file's recurring `test-fixture-fidelity` theme: no test was wrong here, because for the worst offender *no test asserted real behavior at all*)

**Root cause:** a documentation-first verification loop. Each of these features was described in `Feature-list.md` / `Roadmap.md` / `AdditionalFeatures.md`, and every subsequent review — human and agent — read those descriptions and treated them as evidence that the code existed. `pokkum explain`/`why`/`diff` shipped as three cobra commands returning hardcoded literals (`Digest: "sha256:base..."`, `"layer_index": 3`, `"modified": ["Layer #3 (App JS)"]`) and were marked `[x]` in the v0.4 milestone. The 143-line `cmd/pokkum/explain.go` never imports `remote`, never opens a tarball, never touches an image. `explain_test.go` exists and passes — it asserts the command's *output shape*, which the hardcoded data satisfies perfectly. Three further items were marked Done while only one of their two halves was built: supervisor `/metrics` (never built; only `/healthz`+`/readyz` exist — corrected 2026-08-17, this entry itself previously said "/livez", the wrong name; see the PR-4 entry below), app-side trace-context logging (no `trace_id` anywhere in `internal/` or `supervisor/`), and Helm/Kustomize GitOps export (raw-YAML `pokkum://` resolution only).

The trigger that finally exposed it was external: two outside reviews of `Feature-list.md` arrived, and verifying *their* claims against the code — rather than against the feature list — surfaced the stubs incidentally. One reviewer even asked for a layer-churn *sub-mode* of `pokkum diff`, assuming the command worked. Notably, this is the second audit to find this drift class in this project (`fixes-to-v1.md` was the first), which makes it a process property rather than an accident.

**Where:** `cmd/pokkum/explain.go:47`, `:100`, `:130` (stub data); `Feature-list.md:92` (the untrue claim); `Roadmap.md` v0.4 "Diff & Explain" (`[x]`); `AdditionalFeatures.md` matrix rows "Diff & Explain", "Built-in Metrics Endpoint (supervisor)", "Log Aggregation (app-side, trace context)", "Kubernetes (extended manifests/GitOps)" (all "Done").

**Fix:** documentation corrected in this commit — the `[x]` un-checked with an inline correction note, all four matrix rows re-labeled with what is actually built vs. claimed, and the code fix tracked as `Roadmap.md` **PB-1** under a new Pre-Publication Gate. The code itself is deliberately **not** fixed here (see the commit note): PB-1 carries a real decision — implement genuine layer introspection on top of the existing `layerdiffutils`/`comparator` machinery, or delete the three commands per the core-vs-adjacent scope split — and that is the user's call, not a mechanical patch.

**Preventative rule:** **A feature is not verified by reading a document that describes it, and a passing test is not evidence the feature is real if the test only asserts output shape.** Before marking any roadmap item `[x]` or any matrix row `Done`, grep the implementing file for the I/O the feature necessarily requires — a command that claims to inspect a remote image must reference `remote.`/`Fetch`/a tar reader somewhere; one that claims to expose an endpoint must register a handler for that exact path; one that claims to emit a field must contain that field's name. Absence of the required primitive is proof of absence of the feature, and it is a one-line check. Corollary for split features ("X and Y"): verify each half independently — three of the four items here had one real half, which is exactly what let them pass as done.

---

## 2026-08-16 — Non-deterministic stub-launcher binary: the suspected root cause (ELF build-id) was wrong

**Category:** determinism / external-tool-output (a new subcategory: non-determinism introduced by a *third-party compiler's* output, not by Pokkum's own code touching a clock or an unsorted collection)

**Root cause:** `internal/adapters/bunruntime/resolver.go`'s `compileStub` wrote the stub entry file (`stub-entry.ts`, constant content) into a fresh `os.MkdirTemp` directory on every call, then passed that file's *absolute path* as the entry argument to `bun build --compile`. `bun build --compile` embeds something derived from the entry file's path into the compiled binary's bytes — since the temp directory's random suffix differs on every invocation, the absolute path differed too, so the same stub source compiled to a different SHA256 on every call even for an identical `(version, variant, platform)`.

A prior investigation (handed off in `concepts/archive/stub-launcher-determinism-fix-handoff.md`) had already confirmed the binary was non-deterministic, but reproduced it using **two different `--outfile` names** (`bun-stub-x64` vs `bun-stub-x64-run2`) and, from a `file`/`BuildID` inspection showing different ELF build-ids, suspected the `.note.gnu.build-id` section (ordinary per-link linker randomness) was the culprit — a plausible but never-empirically-isolated hypothesis. Actually isolating the diff (per the handoff's own step 1, before designing a fix) told a different story:
- Fixing the `--outfile` name/directory alone (same entry-file path) → **byte-identical** output, no build-id difference at all.
- Varying only the entry file's directory (fixed outfile) → **non-identical** output, confirming the entry path — not the outfile path, not linker build-id randomness — was the actual variable.
- Passing the entry file as a **relative filename** with `cmd.Dir` set to its directory, invoked from arbitrarily different `os.MkdirTemp` directories → byte-identical output across 4 consecutive runs (x64) and 2 runs (arm64), with zero ELF patching needed.

**Where:** `internal/adapters/bunruntime/resolver.go`'s `compileStub` (~line 302).

**Fix:** `compileStub` now passes the entry file as a bare relative filename (`stub-entry.ts`) with `cmd.Dir` set to the containing temp directory, instead of `entryFile`'s absolute path. No ELF post-processing, no fixed/shared path across concurrent calls, no `SourceDateEpoch` threading needed — the previously-suspected build-id randomness turned out not to be an independent source of variance once the entry path was fixed. New regression test `TestResolver_StubLauncher_CompileIsDeterministic` in `resolver_test.go` (guarded like `TestRealBuildIsReproducibleAcrossRuns`: skipped under `-short` and when `bun` isn't on `PATH`) compiles the real stub launcher 3 times per platform (amd64, arm64) into fresh cache dirs and asserts identical SHA256; verified to fail against the pre-fix code (non-deterministic across all 3 runs on both platforms) before confirming it passes against the fix.

**Preventative rule:** An initial root-cause hypothesis for a non-deterministic build artifact — especially one based on a plausible-sounding mechanism (linker build-id, ASLR, timestamps) rather than an actual isolated byte-diff — is a claim to verify, not a design input. Before writing a fix (ELF patching, `SourceDateEpoch` threading, or otherwise), reproduce the exact production code path (same invocation shape, same use of temp directories/fixed names) and vary exactly one input at a time until the true variable is isolated; a repro that changes two things at once (as the original handoff's `--outfile` name did) can implicate the wrong mechanism entirely. This generalizes `mem:self_review_checklist` row 12's fixture-fidelity lesson ("would running the real tool right now actually produce this?") to root-cause hypotheses for non-code-path bugs, not just fixtures.

---

## 2026-08-17 — `TransformViteConfig` naive substring matching risked mutating commented-out code and string literals in Vite configs

**Category:** logic-error / tokenizer (boundary condition in source-code transformations)

**Where:** `internal/adapters/sveltekitutils/injector.go`'s `TransformViteConfig` (locating `sveltekit(...)` and `adapter: ...` properties).

**What happened:** During the clean-context sub-agent verification of Option B (zero-config Vite configuration injection), adversarial testing revealed that `TransformViteConfig` used raw `strings.Index(result, "sveltekit(")` and `regexp.ReplaceAllString` for adapter property replacement without lexical token scanning. When a user's `vite.config.ts` had a commented-out call (e.g. `// sveltekit({ adapter: fakeAdapter() })`) or a helper plugin passing string literals containing `"sveltekit({ ... })"`, the transformer mutated the comment/string literal while leaving the genuine `sveltekit(...)` plugin unconfigured.

**Root cause:** Naive regex and substring matches assume all occurrences of an identifier in a file are live JavaScript/TypeScript code. Comments and string literals easily fool basic index searches unless comment/string delimiters are parsed and skipped.

**Fix:** Implemented `findLiveSvelteKitCall` and `findLiveAdapterProp` scanners in `injector.go` that explicitly track and skip single-line comments (`//`), block comments (`/* ... */`), and all quote forms (`'`, `"`, `` ` ``), with paren/bracket/brace depth tracking to accurately extract and replace only live property expressions.

**Preventative rule:** When transforming user source files or config code, never rely on plain substring matching (`strings.Index`) or unanchored regular expressions across full file contents. Always use a minimal state scanner that accounts for JavaScript/TypeScript comments and string literals.

---

## 2026-08-17 — A sub-agent's "captured verbatim from a real `bunx sv create` project" fixture doc comment was false, caught only by re-running the real command during adversarial review

**Category:** test-fixture-fidelity (a meta-instance of this file's recurring theme: this time the false claim was about provenance itself, not about the artifact's pipeline stage)

**Where:** `internal/adapters/bunexec/compiler_strategy_test.go`'s `svCreateSvelteConfigFmt` constant, added the same day as the `checkEffectiveAdapter` fix it supports.

**What happened:** a sub-agent implementing `TestPrepare_StrategyDispatch`'s per-strategy fixtures wrote a `svelte.config.js`-shaped constant with a doc comment stating it was "captured verbatim from a real `bunx sv create` project ... when its adapter is configured there rather than inline in vite.config.ts." Adversarial review re-ran both `bunx sv create --add sveltekit-adapter=adapter:node` and `bunx sv add sveltekit-adapter=adapter:node` (sv@0.17.0) against real, fresh projects specifically to check this claim — neither ever writes a `svelte.config.js`; both configure the adapter exclusively in `vite.config.ts`. The claimed source scenario ("adapter configured in svelte.config.js rather than vite.config.ts, via `sv create`") does not exist in current tooling at all.

**Root cause:** the fixture content itself was reasonable (a syntactically valid, standard SvelteKit `svelte.config.js` shape, and a legitimate input for what the test actually needed — Prepare's dispatch when svelte.config.js governs) — but the sub-agent's doc comment asserted a specific, checkable provenance ("captured verbatim from a real ... project") that was never actually verified against the real CLI, only assumed plausible. A confident, specific claim written to satisfy this repo's own "no fixture may be hand-crafted, must trace to real content" rule is not the same as the claim being true — and a false provenance claim is more dangerous than an honest "hand-written, standard shape" label, because it reads as already-verified and discourages the next reader from checking.

**Fix:** corrected the doc comment to state plainly that this is a hand-written-but-standard SvelteKit config shape, not a captured real-tool artifact, and to name the real vite.config.ts-only shape (`realSvCreateAdapterNodeViteConfig` in `sveltekitutils/project_test.go`) that current tooling actually produces instead. The fixture's *use* in the test was unaffected — it remains a valid, syntactically real SvelteKit config shape for what `TestPrepare_StrategyDispatch` exercises; only the false claim about where it came from needed fixing.

**Preventative rule:** a "captured from a real run" or "verified against real tooling" claim in a fixture's doc comment is itself a testable assertion, not documentation — before trusting it (as a reviewer) or writing it (as an author), actually re-run the claimed command and diff the output, the same way you'd verify any other fact in a diff. This applies with extra force to sub-agent-authored fixtures: a sub-agent instructed to "source from real content" has every incentive to write a confident provenance claim whether or not it actually did the verification, and nothing downstream catches the gap unless someone re-runs the real command.

---

## 2026-08-17 — `Preflight` hard-required `svelte.config.js` to exist, blocking every real `sv create` project independently of two other fixes landed the same day

**Category:** boundary / test-fixture-fidelity (a third, previously-undiscovered instance of the same root shape as this file's other 2026-08 entries — a check written when a file was assumed mandatory, never revisited after it stopped being mandatory)

**Where:** `internal/adapters/bunexec/compiler.go`'s `Preflight` (the `os.ReadFile(cfgPath)` block preceding the adapter-configured check, ~line 165-172 before the fix).

**What happened:** the same day's fixes to `Prepare`'s adapter detection (`checkEffectiveAdapter`, see the two entries below) were verified by running `core.Build` end to end against a real `sv create --add sveltekit-adapter=adapter:node` project (`testdata/fixtures/sveltekit-adapter-node`, `tests/integration/layered_prerendered_e2e_test.go`) — the first test in the repo to drive the *full* pipeline against such a project rather than `Prepare` in isolation. It failed immediately, before `Prepare` ever ran, at `Preflight`: `"svelte.config.js not found: sveltekit project not found"`. `Preflight` — a separate, strategy-agnostic check that runs earlier in `core.Build` — unconditionally required `svelte.config.js` to exist, treating its absence as proof the directory "does not look like a SvelteKit project." Current `sv create` scaffolds generate no such file at all (confirmed empirically the same day), so this genuinely blocked every real project this whole day's other two fixes were built to unblock.

**Root cause:** `Preflight` was written when `svelte.config.js` was, in practice, always present in any real SvelteKit project — a correct assumption at the time that silently stopped being true once `sv create` moved adapter configuration into `vite.config.ts`. Nothing forced a re-check of that assumption because `Preflight` was never exercised against a project without the file: every existing fixture and every existing test supplied one. The same day's `Prepare`-level fix (`checkEffectiveAdapter`) was unit- and Prepare-level-tested thoroughly, but no test called `core.Build` (which calls `Preflight` before `Prepare`) against a `svelte.config.js`-less project until the end-to-end test was written specifically to raise confidence beyond the unit level — and it immediately earned that effort back.

**Fix:** `Preflight` no longer treats a missing `svelte.config.js` as `core.ErrProjectNotFound`; only a genuine read failure (permission error, not "does not exist") does. The existing package.json-dependency fallback in `Preflight`'s adapter check (`pkg.HasDependency(adapterPackage) || pkg.HasDependency("@sveltejs/adapter-node")`) already tolerated an empty/missing config source once the hard gate was removed — no further change to that check was needed. See `TestPreflight_MissingSvelteConfig_NotAnError`.

**Preventative rule:** a same-day, well-tested fix to one function in a call chain does not prove the chain works — if a caller runs other checks before or after the fixed function, at least one test must exercise the *caller*, with the same real-world input shape the fix was built around, not just the fixed function in isolation. This is a variant of this file's recurring theme (fixture-vs-reality mismatch) at the integration-test level rather than the unit-fixture level: the "fixture" that was stale here wasn't test data, it was an unstated assumption in a *different* function than the one being fixed.

---

## 2026-08-17 — Zero-Config Auto-Injection had no effect on real builds; two compounding causes, both traced to code that never ran a real `bun run build` against a project it wasn't allowed to hand-configure first

**Category:** boundary / test-fixture-fidelity

**Where:** `internal/adapters/bunexec/compiler.go`'s `Prepare` (the `bun run build` invocation always targeted the real, unmodified `svelte.config.js`/`vite.config.ts` — `PrepareVirtualConfig`'s `.pokkum/svelte.config.js` output and `POKKUM_AUTO_INJECT` env var were both write-only, read by nothing); no code anywhere accounted for Vite's own "svelte.config.js is ignored when options are passed via your Vite config" rule.

**What happened:** documented in full in `concepts/archive/zero-config-injection-concept.md` (written the same day this was found, before the fix). In short: v0.2 shipped "Zero-Config Auto-Injection" claiming Pokkum auto-injects the correct adapter without manual `svelte.config.js` edits. It never did — the transformed config Pokkum computed was written to a file nothing read, and separately, current `sv create` scaffolds (`sv@0.17.0`+) don't even generate a `svelte.config.js`, configuring the adapter entirely via `vite.config.ts` instead, which real SvelteKit ignores `svelte.config.js` for. Both gaps were invisible to every existing test because the repo's one real-`bun` fixture (`sveltekit-basic`) ships with its adapter already hand-configured correctly — sidestepping the exact question "does Pokkum's injection make an incorrectly-configured project buildable" that the feature claims to answer.

**Root cause:** the feature was built and tested against a fixture that could never exhibit the failure it was written to prevent. `VirtualConfigResult.VirtualConfigPath` being read only for a log line, and `POKKUM_AUTO_INJECT` being read by nothing, are the kind of gaps a fixture with the *wrong* adapter configured (or none) would have caught immediately — no such fixture existed until this investigation added one.

**Fix:** per the concept doc's recommendation, shipped Option C (detect-and-fail-clearly) rather than Option B (a real fix that would make the build adapter-agnostic — deferred, scoped separately) or Option A (real-file swap — rejected, source-mutation risk). New `sveltekitutils.EffectiveAdapterConfigured`/`ViteConfigOverridesSvelteConfig` determine, from real captured `vite.config.ts`/`svelte.config.js` shapes (a genuine `bunx sv create` scaffold, both with and without an adapter add-on, real content captured verbatim into test fixtures — not hand-written), which file SvelteKit will actually read; `Prepare`'s new `checkEffectiveAdapter` calls it before any subprocess is spawned or `.pokkum/` is written, failing with a message naming the exact file and fix. `Roadmap.md`'s v0.2 entry now carries a correction note rather than silently continuing to overstate what shipped.

**Preventative rule:** a "auto-fix" or "auto-inject" feature's regression tests must include at least one fixture the feature is supposed to *fix*, not only fixtures that are already correct. A fixture that never needs the feature to do anything can't tell you whether the feature does anything.

---

## 2026-08-17 — `patchPrerenderedHandler`'s matcher was fixed, not by adding new patterns, but by pointing the existing ones at the right file

**Category:** multi-item / test-fixture-fidelity (the fix for the gap logged in the entry immediately below)

**Where:** `internal/adapters/bunexec/prerendered_patch.go`'s `patchPrerenderedEnv`.

**What happened:** the entry below found that real bundled `build/handler.js` contains none of the 8 known prerendered-path patterns. Empirical follow-up (a real `bunx sv create --add sveltekit-adapter=adapter:node` project with a real prerendered route, real `bun install` + `bun run build`) found why: `@sveltejs/adapter-node@5.5.7`'s bundled `handler.js` is a thin re-export barrel (`export { h as handler } from './server/chunks/handler-<hash>.js';`); the actual `path.join(dir, 'prerendered')` expression — an exact, byte-identical match of one of the existing 8 patterns — lives in that content-hashed chunk file. The pattern-matching logic was never wrong; it was reading the one file in the build output guaranteed not to contain what it was looking for.

**Root cause:** the original patcher (and the 2026-08-16 fixture-sourcing effort that "verified" it) both assumed `build/handler.js` was a single, self-contained file, because that was true of whatever adapter-node version or bundler configuration was last observed directly. Nothing re-checked that assumption against a genuinely fresh real build until this investigation did.

**Fix:** `patchPrerenderedEnv` now tries a direct match inside `handler.js` first (byte-for-byte the old behavior, kept as the first attempt since some adapter-node versions/configs may still inline the logic), and only on no-match parses `handler.js`'s re-export statement, resolves the referenced chunk file relative to `handler.js`'s own directory, and retries there — patching and staging that file instead. The re-export identifier and chunk hash are matched structurally (`export\s*\{[^}]*\bhandler\b[^}]*\}\s*from\s*['"]([^'"]+)['"]`), never hardcoded, since Rollup assigns both per build. A genuine no-match in both stays a hard failure — this is a correctness gate, not a best-effort transform. New fixtures under `testdata/adapter-node/bundled-real/` capture the real re-export-barrel shape; the existing `v3`/`v5` fixtures are kept (still real, still useful as coverage of the pre-bundling template) with corrected doc comments.

**Preventative rule:** when a "wrong file" bug is found, check whether the *matching logic itself* is actually broken before rewriting it — sometimes the fix is routing, not detection. Conflating the two here would have meant guessing at new literal patterns for a shape that was never actually broken, while leaving the real bug (never looking at the chunk file) unfixed.

---

## 2026-08-16 — `patchPrerenderedHandler`'s "real fixture" regression tests exercised the wrong artifact — the checked-in npm template, not real bundled build output

**Category:** multi-item / test-fixture-fidelity (same root shape as the assets.generated.ts entry immediately below — a fixture that doesn't match real tool output masking a real gap)

**Where:** `internal/adapters/bunexec/prerendered_patch.go` (`patchPrerenderedEnv`'s 8 literal patterns); `internal/adapters/bunexec/prerendered_patch_test.go`'s `TestPatchPrerenderedEnv_RealAdapterNodeV3`/`V5` (added earlier the same day); `testdata/adapter-node/{v3,v5}/handler.js` (sourced via `npm pack`, straight from each package's `files/handler.js`).

**What happened:** verifying the `assets.generated.ts` fix (below) against a genuinely fresh `sv create` scaffold with a real prerendered page, `patchPrerenderedHandler` failed with "no recognizable prerendered path pattern" against the **actual** `build/handler.js` Vite/Rollup emitted — even though `TestPatchPrerenderedEnv_RealAdapterNodeV5` (checked in the same day) asserts the patcher succeeds against `testdata/adapter-node/v5/handler.js`, sourced from the same adapter-node version. `grep -c "prerendered\|path.join"` against the real build's `handler.js` returns zero matches; the checked-in fixture contains `path.join(dir, 'prerendered')` verbatim. The two files are not the same artifact: `testdata/adapter-node/v5/handler.js` is adapter-node's **pre-bundling source template** (`package/files/handler.js`, copied close to verbatim into a project's build output today, but not a promise upstream makes); the file `patchPrerenderedHandler` actually opens at build time is **post-Vite/Rollup-bundled** output, and bundling appears to restructure or rename the prerendered-serving code path enough that none of the 8 literal string patterns survive.

**Root cause:** the fixture-sourcing task (same day) reasoned "get the real npm package's handler.js, not a synthetic one" and stopped there — a real npm package artifact felt like a strong enough proxy for "real build output" not to need a real build to confirm it. But the actual consumer (`patchPrerenderedHandler`) never reads the npm package directly; it reads whatever the project's own bundler produced from that template. A fixture one build step removed from what the code under test actually consumes is still a synthetic fixture, even when it's byte-for-byte real content sourced from a real package.

**Impact:** unknown how many real adapter-node projects' bundled `handler.js` actually retains one of the 8 patterns — this specific `sv create --template minimal` scaffold's output does not, meaning `--strategy=layered` (already broken by the `assets.generated.ts` bug below) hits a **second**, independent hard-failure immediately after that one is fixed, for what may be the common case, not an edge case.

**Fix:** not fixed in this entry — flagged for a separate task. The regression tests added earlier the same day give false confidence and should be either supplemented with a fixture built via a real `bun run build` (not `npm pack`) or clearly re-labeled as "tests the upstream template, not build output" so nobody mistakes them for proof the patcher works end-to-end.

**Preventative rule:** when sourcing a "real" fixture to regression-test code that operates on a build *artifact* (not a source file), source it from an actual run of the tool chain that produces that artifact — not from the nearest real-but-upstream file that resembles it. "Real, but from the wrong pipeline stage" fails silently the same way a synthetic fixture does, and is more dangerous because it reads as verified.

---

## 2026-08-16 — `assets.generated.ts` normalization ran for `StrategyLayered`, but that file is exclusively a `@jesterkit/exe-sveltekit` artifact `@sveltejs/adapter-node` never produces

**Category:** boundary (strategy-gated logic applied outside its own strategy)

**Where:** `internal/adapters/bunexec/compiler.go`'s `Prepare`, the non-static `else` branch (~line 350-369 before the fix): `normalizeGeneratedAssetsFile` ran unconditionally for every non-static strategy instead of `StrategyExe` only; `internal/adapters/bunexec/compiler_strategy_test.go`'s "layered" fake-bun fixture (~line 84) wrote a synthetic `build/assets.generated.ts` that a real `@sveltejs/adapter-node` build never produces, so the golden-master test added the same day passed despite the bug being present the whole time.

**What happened:** manually verifying the readOnlyRootFilesystem question against a genuinely fresh `sv create` scaffold with `@sveltejs/adapter-node` correctly configured, `pokkum build` failed every time at `bunexec: normalize .../build/assets.generated.ts: no such file or directory` — even though the SvelteKit/Vite build itself succeeded cleanly (`build/index.js`, `build/handler.js` all present and correct). `--strategy=layered` is `DefaultBuildStrategy`; this means the default build path could not complete a real build against its own documented, correct adapter at all.

**Root cause:** the non-static `else` branch was written as if "not static" meant "must be exe" — a leftover from before `StrategyLayered` existed as a distinct, adapter-node-based path, never revisited when it was added. The golden-master test written the same day (`TestPrepare_StrategyDispatch`) exists specifically to catch exactly this class of bug, but its own "layered" fixture fabricated a fake `assets.generated.ts` file to get the fake-bun harness past this line — masking the very bug the test was built to catch. Nobody had run a real `bun run build` with a correctly-configured adapter-node project against `--strategy=layered` since this branch was last touched.

**Fix:** gated the `normalizeGeneratedAssetsFile` call to `req.Strategy == ports.StrategyExe` only; also fixed the adjacent "expected entrypoint ... not found" error's hint to name `@sveltejs/adapter-node` for non-exe strategies instead of always naming `@jesterkit/exe-sveltekit`. Removed the fake `assets.generated.ts` write from the golden-master test's "layered" fixture so it now genuinely regression-guards this (a real `@sveltejs/adapter-node`-shaped fixture, not one hand-crafted to satisfy whatever the code currently checks for).

**Preventative rule:** when a test fixture (fake-bun script, mock adapter output, etc.) exists to get a unit test past a real dependency, its content must model what the REAL tool would produce for that exact code path — not just whatever satisfies the current implementation. A fixture that's shaped to pass the test, rather than shaped to match reality, will happily keep passing after the production code drifts from reality too. When adding a strategy-specific (or any mode-specific) fixture to a table-driven test, ask "would the real tool actually produce this for this exact strategy?" before writing it — and periodically prove it by running the real path at least once, which is what surfaced this.

---

## 2026-08-16 — .pokkum.yaml config validation had three silent-failure gaps: dead Viper wiring, no per-profile validation, no strict key parsing

**Category:** boundary / dead-code / validation-gap

**Root cause:** The config loader was built incrementally: `viper.Viper` was wired up first (a generic dotted-key → `POKKUM_*` env-binding scheme), then `Load()` was switched to parse YAML directly via `yaml.Unmarshal` without anyone removing the now-unreachable Viper construction or its `GetString`/`GetBool` fallback call sites in `build.go` — each fallback ran only *after* an explicit `os.Getenv` had already handled the same key (or, for `compile.sourcemap`, a dotted key that was never a real schema field at all), so the fallback looked defensive but was provably dead. Separately, `pokkum config validate` was written to check only the top-level `ProjectConfig` fields and was never revisited when profile support was added, so a profile with an invalid `strategy`/`base`/`sbom` passed validation silently. And `Load` called plain `yaml.Unmarshal`, which drops unknown keys instead of erroring, so a typo like `strategey:` silently produced a zero-value field — contradicting this repo's own fail-fast-before-any-network-call convention.

**Where:** `internal/adapters/config/config.go` (`New`, `Load`), `cmd/pokkum/build.go:428,546,726` (removed), `cmd/pokkum/config.go` (`runConfigValidate`)

**Fix:** Removed the `viper.Viper` field/construction and the three dead fallback call sites; `go mod tidy` dropped `spf13/viper` and 7 exclusive transitive deps (`fsnotify`, `pelletier/go-toml/v2`, `sagikazarmark/locafero`, `sourcegraph/conc`, `spf13/afero`, `spf13/cast`, `subosito/gotenv`). Extracted `validateConfigFields` in `cmd/pokkum/config.go` and now call it once for the base config and once per profile (profile names sorted before iterating the `map[string]BuildProfile`, so error ordering is deterministic), prefixing errors with `profile %q:` so a multi-profile config names the offending one. Switched `Load` to `yaml.NewDecoder(...).KnownFields(true)`, special-casing `io.EOF` so a present-but-empty `.pokkum.yaml` still parses to a valid zero-value config exactly as `yaml.Unmarshal` did before.

**Preventative rule:** When a fallback/legacy code path only fires after an earlier check on the same value already ran, that is a signal it is dead — grep for every prior check on the same key before trusting a "belt and suspenders" layer is actually reachable. When a config schema grows a `profiles`/nested-override section, any validation written against the top-level struct must be re-audited (or, better, extracted into a shared helper from the start) so nested overrides get equal coverage automatically — this codebase now enforces that via the checklist (see `mem:self_review_checklist` row 10). Any hand-rolled YAML/JSON `Unmarshal` on user-authored config should default to strict/unknown-field-rejecting decoding unless there is a documented reason not to.

---

## 2026-08-16 — docker.repo (registry ref) was read from config but never validated for shape

**Category:** validation-gap

**Root cause:** `Docker.Repo` was plumbed all the way from `.pokkum.yaml` into `BuildRequest.Repo` without any shape validation at the config layer — the only check happening anywhere was `BuildRequest.Validate`'s late, narrow whitespace/tag-suffix check immediately before a real build. A malformed repo (e.g. containing characters the registry API would reject) passed `pokkum config validate` cleanly and only failed much later in the pipeline, or not until the actual registry HTTP call rejected it — one indirection later and with a worse error than the config layer could have given directly. Profiles additionally had no way to override `docker.repo` at all, despite every other override-relevant field on `ProjectConfig` (`base`, `strategy`, `security.*`, `sbom.*`, ...) already existing on `BuildProfile`.

**Where:** `internal/ports/config.go` (`BuildProfile.Docker`, new field), `internal/core/model.go` (`ValidateDockerRepo`), `cmd/pokkum/config.go` (`validateConfigFields`), `internal/adapters/config/config.go` (`ApplyProfile` merge)

**Fix:** Added `core.ValidateDockerRepo`, reusing `go-containerregistry/pkg/name.NewRepository` with the same `name.WeakValidation` option `internal/adapters/registry` and `internal/adapters/baseimage` already use for every reference this tool actually builds — so a config value that fails here fails identically to (and earlier than) how it would fail at push time. Wired into both base-config and per-profile validation. Added a `Docker` override field to `BuildProfile` and wired its merge into `ApplyProfile`, so a `production` profile can now push to a different repo than `local`/the base config.

**Preventative rule:** A field that is read from config but only reaches a "real" check deep in the pipeline or in a third-party call should get an explicit, early check in `pokkum config validate` too — validate at the boundary where the user can see and fix the mistake, not just where the failure eventually surfaces. When adding validation for a value this tool already builds `go-containerregistry` references from elsewhere, reuse that exact parser/option set rather than inventing a new regex — two independent notions of "valid repo ref" is itself a bug waiting to happen.

---

## 2026-08-16 — Opt-in SPA-fallback: serving a fallback file that became non-regular at request time surfaced a 500 instead of an honest 404 miss

**Category:** boundary / resource (request-time re-validation vs construction-time-only validation)

**Where:** `supervisor/cmd/pokkum-static/server.go` (`serveHTTP` fallback branch)

**What happened:** The opt-in SPA-fallback mode validated at construction that the configured fallback path resolves (via `EvalSymlinks`) to a regular file within a served root, and stored the canonical path. The documented contract said that if the fallback file is *not* a regular file at request time (e.g. removed or replaced after construction), the server should treat it as a miss and return an honest 404. The implementation unconditionally re-entered `serveFile(w, r, s.fallbackPath)` on any clean route miss, so `fileETag(bodyPath)` → open failed → returned **500 "internal error"** instead of a 404 (and in the narrower case of a surviving `.gz` sidecar, would even serve a stale 200). An adversarial clean-context sub-agent confirmed this with a request-time deletion test (actual 500, spec requires 404).

**Root cause:** Validation was treated as a one-shot construction step; the fallback branch assumed the validated file would remain a regular file forever, so it never re-checked the *runtime* precondition it actually depends on. The construction-time check (a policy decision) was conflated with the request-time invariant (a liveness/type precondition).

**Fix:** Add a request-time `fallbackFileOK()` re-check (`os.Stat` + `Mode().IsRegular()`) before serving the fallback; on failure fall through to `warnFallbackOnce` + `http.NotFound`, so a gone/degraded fallback is a clean 404, never a 500. The stored path is already canonical, so this is a direct stat, not a re-resolve. Added regression test `TestStaticServer_Fallback_DeletedAtRequestTimeIsMiss`.

**Preventative rule:** When a code path depends on a precondition that can change at runtime (a file's existence/type, a resource's openness), validate it at the point of use — a construction-time check only proves the state at construction. Never let a resource that can vanish between validation and use flow unguarded into an operation that escalates to a 5xx or serves stale state; degrade to the same fallback the normal-miss path uses.

---

## 2026-08-16 — Adversarial review of SPA-fallback config detector: whole-file regex makes `fallback` detection scoping-fragile (F2/F3, hardening gap, not fixed in-scope)

**Category:** boundary / parsing (whole-file regex vs scoped match)

**Where:** `internal/adapters/sveltekitutils/project.go` (`StaticFallbackFilename`)

**What happened:** An adversarial clean-context sub-agent confirmed two config-detector edge cases:
- **F2 (false-negative):** any occurrence of `fallback: false` anywhere in the adapter-static config source — including inside a comment or an unrelated key — returns `("", false)` and silently disables a genuine `fallback: 'index.html'` SPA shell.
- **F3 (false-positive):** a `fallback: true` in an unrelated key (e.g. `routing: { fallback: true }`) matches and guesses `"200.html"`, which either wrongly marks a non-SPA site (compiler then hard-fails "configured but not emitted") or injects a guessed fallback the site never opted into.

Both are the direct consequence of the plain whole-file-regex approach. Severity: minor (contrived configs), no security impact, not a regression in this diff — but they can flip the SPA-fallback decision for otherwise-valid configs.

**Root cause:** Detection scans raw source text without scoping to the `adapter({...})` call or stripping comments, so `fallback` tokens outside the intended object/scope are treated as authoritative.

**Fix (NOT applied in this scope — recorded for follow-up):** scope detection to the adapter-static call — strip `//` and `/* */` comments before scanning, and/or match the `adapter({...})` block — rather than whole-file regex.

**Preventative rule:** When parsing a declarative config from source text, scope the matcher to the construct that actually owns the key (the containing call/object), and strip comments first; a whole-file regex for a common key name will be fooled by tokens in unrelated or commented positions.

---

## 2026-08-16 — Requesting an explicit `--profile` without a `.pokkum.yaml` file silently ignored the profile because `projCfg != nil` guarded profile merging

**Category:** logic / boundary (silent failure on missing prerequisite configuration)

**Where:** `cmd/pokkum/build.go:397-405` (`buildRequestFromConfigAndFlags`)

**What happened:** When a user explicitly supplied `--profile <name>` on the CLI in a workspace where `.pokkum.yaml` did not exist, `cfg.Load(projectDir)` returned `os.ErrNotExist` and set `projCfg = nil`. The profile application check was written as:
```go
activeProfile := flags.profile
if activeProfile != "" && projCfg != nil {
    merged, err := cfg.ApplyProfile(projCfg, activeProfile)
    ...
}
```
Because `projCfg` was `nil`, the entire block was skipped with no warning or error. The build continued with default flags, silently dropping all user intent specified by `--profile`.

**Root cause:** Conflating optional configuration loading (it is normal for `.pokkum.yaml` to be omitted when running vanilla builds) with explicit CLI feature flags (`--profile` requires a config file to resolve against). The guard was defensive against `nil` dereference on `projCfg` but failed to validate the prerequisite when the user explicitly asked for a profile.

**Fix:** Added an explicit validation check right after loading:
```go
activeProfile := strings.TrimSpace(flags.profile)
if activeProfile != "" && projCfg == nil {
    return nil, fmt.Errorf("profile %q requested but no %s found in project", activeProfile, config.ConfigFilename)
}
```
If `.pokkum.yaml` does not exist and the user passed `--profile`, the command fails fast with a clear error.

**Preventative rule:** When a CLI feature depends on an optional configuration file or context object, always distinguish between *implicit* activation (defaulting/no-op when the file is absent) and *explicit* activation (when the user passes an explicit flag demanding that feature). Explicit activation against a missing prerequisite must always fail fast with an explanatory error, never silently fall back to default behavior.

---

## 2026-08-16 — `core.Build`'s `Normalize()` pre-defaults `Runtime.Entrypoint` to the exe shape before the strategy is known, so `StrategyLayered` images built through the real pipeline get an unrunnable entrypoint

**Category:** boundary (strategy-dependent default computed before the strategy-aware code path runs)

**Where:** `internal/core/model.go:688` (`BuildRequest.Normalize` calls `r.Runtime = r.Runtime.WithDefaults()` unconditionally, before per-platform strategy dispatch); `internal/ports/packager.go:212-232` (`RuntimeConfig.WithDefaults` defaults `Entrypoint` to `ports.DefaultEntrypoint()` — the StrategyExe shape — whenever it's nil, with no knowledge of `Compile.Strategy`); `internal/adapters/packager/packager.go:150-153` (the StrategyLayered branch's own default, `if req.Runtime.Entrypoint == nil { req.Runtime.Entrypoint = ports.DefaultLayeredEntrypoint() }`, is a dead guard by the time it runs, because `Normalize()` already claimed the nil).

**What happened:** `tests/integration/strategy_e2e_test.go`'s new `TestFixtureDrivenE2E_AllStrategies` — the first test in the repo to drive the full `core.Build` pipeline (not `packager.Build` directly) with `Compile.Strategy = StrategyLayered` and assert on `Config.Entrypoint` — failed: the pushed image's `Config.Entrypoint` was `["/pokkum/init", "--", "/app/server"]` (the StrategyExe shape) instead of `["/pokkum/init", "--", "/usr/local/bin/bun", "/app/server/index.js"]` (`ports.DefaultLayeredEntrypoint()`). The layer *contents* were correct (8 layers: base, bun, supervisor, server, client, vendor, native, prerendered — matching `internal/adapters/packager/packager_strategy_test.go`'s `TestBuild_StrategyDispatch/layered` exactly), proving the layer-building dispatch works; only the entrypoint dispatch is broken. `cmd/pokkum/build.go` never sets `Runtime:` on its `core.BuildRequest` for any strategy, so this is not a test-only artifact — it is the exact code path a real `pokkum build --strategy layered` invocation takes.

**Root cause:** `RuntimeConfig.WithDefaults()` was written as if every field it defaults (User, WorkingDir, Port, ProbePort, ShutdownTimeout, Entrypoint) is strategy-independent. Entrypoint isn't — its correct default depends on `Compile.Strategy`, which `WithDefaults()` has no access to and `Normalize()` calls it without knowing. Separately, `packager.Build`'s StrategyLayered branch was written assuming it would be the *first* code to see the request's `Runtime.Entrypoint`, so a nil-check was "good enough" — nobody traced the call chain back far enough to see `core.Build`'s `Normalize()` (pipeline.go:273) already ran `WithDefaults()` on the same struct earlier in the same request lifecycle. Only the static branch survives, and only by accident: it unconditionally overwrites `rc.Entrypoint` rather than checking for nil.

**Impact (uncaught until this test):** any real `pokkum build --strategy layered` run would ship an image whose entrypoint execs `/app/server` directly — but StrategyLayered packages `/app/server` as a *directory* (containing `index.js`), not an executable. The container would fail to start (`exec: is a directory`) on every run. No prior test caught this because every existing StrategyLayered test either constructs `ports.PackageRequest` directly (bypassing `core.Build`'s `Normalize()` entirely — see `packager_strategy_test.go`) or never asserted on `Config.Entrypoint` at the `core.Build` level.

**Fix:** `internal/adapters/packager/packager.go`'s StrategyLayered branch now sets `req.Runtime.Entrypoint = ports.DefaultLayeredEntrypoint()` unconditionally, dropping the nil-guard that `Normalize()`'s earlier pass could pre-empt — mirroring the static branch's existing unconditional overwrite. `RuntimeConfig.WithDefaults()` itself was left untouched (StrategyExe still legitimately relies on its generic `Entrypoint` default, and no code path anywhere sets a custom entrypoint that this could clobber — verified by grep before making the change unconditional instead of conditional). `tests/integration/strategy_e2e_test.go`'s `TestFixtureDrivenE2E_AllStrategies/layered` subtest was flipped from pinning the buggy exe-shaped entrypoint to asserting the correct `{SupervisorPath, "--", BunBinaryPath, AppServerIndexPath}`.

**Preventative rule:** When one request field's correct default depends on another field of the *same* request (here: `Runtime.Entrypoint`'s default depends on `Compile.Strategy`), never default it inside a generic, strategy-agnostic `Normalize()`/`WithDefaults()` pass that runs before the strategy-aware code path sees the request. Either default it lazily, only once the dependent field is known, or make the strategy-aware defaulting unconditional (never gated on "is it still nil") — a nil-guard silently loses to any earlier generic pass that already claimed the zero value, and unit tests that construct the downstream port request directly (skipping the earlier pass) will never observe the interaction.

---

## 2026-08-15 — The 4-step verification suite does not run `golangci-lint`, so a CI-breaking `errcheck` finding survived every "green" report

**Where:** `internal/adapters/registry/mount_test.go`,
`TestMountObserver_ConcurrentRoundTrips_RaceFree` (caught during the final
adversarial review gate, after the feature had already been reported as
verified).

**What happened:** The concurrency test derived each fake response's outcome
from the request's digest via `fmt.Sscanf(..., "%d", &idx)`, discarding the
returned error. `gofmt`, `go vet`, `go build` and `go test ./internal/...
-race` all pass on that line, so every step of `CLAUDE.md` §5's verification
suite reported green — but `.golangci.yml` enables `errcheck`, its
`_test\.go$` exclusion covers only `gosec`/`staticcheck`/`revive`, and
`.github/workflows/ci.yml` runs `golangci-lint run ./...` on every push. The
change would have failed CI on the first run.

**Root cause:** `fmt.Sscanf`'s error was ignored because the happy path was
"obviously" fine — the digests are generated two lines away by the same test.
That reasoning is correct about behavior and irrelevant to the lint gate,
which is what actually blocks the merge.

**Faulty assumption:** that "the CLAUDE.md verification suite is green" is the
same claim as "this change is mergeable." It isn't: the suite covers
formatting, vet, build and tests, and deliberately says nothing about the
linters CI additionally enforces. `HEAD~3` (`chore: fix lint findings…`)
exists precisely because this gap has been walked into before.

**Fix:** Replaced `fmt.Sscanf` with `strconv.Atoi` and returned a transport
error on a parse failure, so an unparseable fixture surfaces as a named
`RoundTrip` error instead of silently defaulting `idx` to 0 (which would have
sent every request down the 201 branch and produced an unexplained summary
mismatch). `golangci-lint run ./...` is now clean repo-wide.

**Preventative rule:** Run `make lint` (or `golangci-lint run ./...`) as a
fifth step alongside `CLAUDE.md` §5's four, before declaring any code change
complete — especially for new `_test.go` files, which people assume are
lint-exempt and which this repo's config only partially exempts. Never report
"verification suite passed" as a proxy for "CI will pass" when CI runs gates
the suite does not.

---

## 2026-08-15 — Found the same bare-`&http.Transport{}` anti-pattern in 3 places; deliberately fixed only 1 to keep diff scoped

**Where:** `internal/adapters/registry/registry.go` (fixed), `internal/adapters/baseimage/resolver.go:92` (not fixed), `internal/adapters/remotecacheutils/remotecacheutils.go:432, 725, 766` (not fixed).

**What happened:** While fixing HTTP/2 negotiation on the insecure-TLS path in `registry.go`, a search for `&http.Transport{` literals revealed the same pattern — a bare struct literal instead of cloning from `remote.DefaultTransport` — in three other locations in the codebase.

**Why not fixed:** `resolver.go`'s `insecureTransport` and `remotecacheutils.go`'s three inline `remote.WithTransport(&http.Transport{...})` calls were identified but deliberately left unmodified to keep this task's scope tight. The `registry.go` change (which is on the critical push path) was the priority; the other two modules (base image resolution and cache/pull operations) are separate concerns with different risk profiles.

**Preventative rule:** When a code search discovers the same anti-pattern in multiple places, do not assume "finding one means fixing them all" or vice versa. Be explicit in the code review / task plan about which instances are in-scope and why, so a future maintainer (yourself in 6 months) does not think "we fixed this" means "it's fixed everywhere."

---

## 2026-08-15 — Upstream's own repo-path math splits "reads" and "chunked-upload writes" into different key shapes; a repo-scoped test double must normalize both onto one key

**Where:** `internal/adapters/registry/mount_test.go`, `repoScopedBlobHandler`
(the in-memory `(repo, digest)`-keyed blob store backing
`newMountAwareTestRegistry`, used by `push_test.go`'s cross-repo-mount
integration tests).

**What happened:** A prior task flagged, but did not fix, that a real
`remote.Write`-driven push against this harness would store a blob under a
different key than any subsequent read of that same blob would look it up
under — meaning every non-mounted layer (freshly built layers, and the image
config, which is *always* a plain blob) would appear to vanish (`BLOB_UNKNOWN`)
on the very next `remote.Head`/`remote.Image` call. This task's job depended
on that being fixed first, since three of the four planned integration tests
push at least one non-mountable blob.

**Root cause:** go-containerregistry's own in-memory registry
(`pkg/registry/blobs.go`, `blobs.handle`) computes the repo string once per
request as `req.URL.Host + path.Join(elem[1:len(elem)-2]...)` — trimming
exactly the *last two* path segments before rejoining the rest. That produces
the correct repo only when a request's final two segments are
`blobs/<digest-or-"uploads">`, which holds for every read (`GET`/`HEAD`) and
for the mount-initiation POST (`.../blobs/uploads/`, no id yet). The chunked
upload's `PATCH`/`PUT` requests (`streamBlob`/`commitBlob` in
`pkg/v1/remote/write.go`) instead hit `.../blobs/uploads/<id>` — one segment
deeper — so trimming the same "last two" leaves the literal segment `"blobs"`
inside the joined repo string. Every real streamed blob therefore lands under
`"<repo>/blobs"` while every read asks for `"<repo>"`.

**Faulty assumption (in the harness, not this task):** that a single `repo`
string received by a `BlobHandler` implementation is already normalized and
safe to use as a map key verbatim, regardless of which HTTP verb produced it.
It isn't — upstream's *own* path arithmetic is verb-shape-dependent, which is
easy to miss because `isBlob()` (the *routing* predicate, same file) correctly
handles both shapes; only the separate `repo :=` line does not.

**Fix:** Added `normalizeBlobRepo(repo string) string { return
strings.TrimSuffix(repo, "/blobs") }`, applied at the top of
`repoScopedBlobHandler`'s `Get`/`Stat`/`Put`. This is a no-op for the
already-correct shapes (they never end in the literal segment `"blobs"`), so
it unifies both call shapes onto the one true repo key without needing to
know which code path a given call came from. Verified with a regression test
(`TestMountAwareTestRegistry_RealWriteThenReadAgreeOnRepo`) that fails with
`BLOB_UNKNOWN` when the normalization is reverted, and passes with it in
place.

**Preventative rule:** When a test double receives a value that a *third
party's* routing code derived from a URL path via positional slicing (not a
documented, stable API), do not trust that the same logical value comes out
identically shaped across every HTTP verb that routes through it. Grep the
real implementation for every place the value is computed/reused, not just
the one call site the bug report points at — and write the round-trip
regression test (`write` via the real client path, then `read` via the real
client path, against the same identifier) before writing any test that
*depends* on that round trip working, since it is the cheapest possible proof
and pins the fix independently of every higher-level test built on top of it.

---

## 2026-08-15 — A "mount was declined, so the target should behave exactly like an ordinary push" assumption ignored that a *pulled* `MountableLayer`'s bytes are fetched lazily from its origin

**Where:** `internal/adapters/registry/push_test.go`,
`TestPush_CrossRepoMount_CrossRegistryRejected` (first draft, caught before
being reported as passing).

**What happened:** The test's first draft asserted that the *source*
registry (server A) must observe **zero** requests while pushing a composed
image to the *target* registry (server B), reasoning that "the client only
ever talks to the registry it's pushing to." That assertion failed on the
very first run: server A recorded one `GET .../blobs/<digest>` during the
push to server B.

**Root cause:** The composed image's mountable layer was obtained via
`remote.Get(refToServerA).Image()`, which wraps every layer in
go-containerregistry's `mountableImage`/`MountableLayer` — but the underlying
`v1.Layer` those wrap is still a *remote, lazily-read* layer: its
`Compressed()`/`Uncompressed()` readers stream from whichever registry it was
pulled from, on demand, rather than buffering the full blob into memory at
pull time. When server B declines the mount, go-containerregistry's
`streamBlob` calls that same lazy `Compressed()` to get bytes to `PATCH` to
server B — and that call has no choice but to reach back out to server A,
the layer's only actual data source. This is not a leak or a bug in
production code; it is the only way a "mount declined, fall back to a normal
stream" path *can* work for content the process never materialized locally in
the first place.

**Faulty assumption:** That "cross-host mount was attempted and declined" and
"the source registry sees zero traffic" were the same claim. They are not —
the correct claim is narrower: the source registry must see no *mount-shaped*
request (no `POST .../blobs/uploads/` — mounting is inherently a
target-registry-only operation), but it will legitimately see reads whenever
the fallback path needs bytes it doesn't already hold.

**Fix:** Replaced the blanket "zero requests" assertion with two precise
ones: (1) no `POST` of any kind reaches server A during the push, and (2) a
`GET` for the specific base-layer digest *does* reach server A, and is
treated as further positive proof of the decline (a successful mount would
require no such read at all, per the sibling
`TestPush_CrossRepoMount_ZeroEgress`, where no such `GET` occurs).

**Preventative rule:** When asserting "no cross-talk between two systems"
in a test, name precisely which *kind* of interaction must be absent (here:
mount-initiation requests) rather than asserting a system is silent overall —
a lazily-evaluated dependency (a remote-backed `io.Reader`, a pull-through
cache, a deferred fetch) can make "silent overall" both false and irrelevant
to the property actually under test. Read what the object you're wrapping
(`*remote.MountableLayer` here) is actually backed by before asserting an
absence of activity on its backing store.

---

## 2026-08-15 — `http.Transport.Clone()` mutates its receiver, so "clone equals unmodified copy" is not a safe test assertion

**Where:** `internal/adapters/registry/registry_test.go`,
`TestTransports_PreserveRemoteDefaultTransportTuning` (regression test for
`defaultTransport` / `insecureTransport` in `registry.go`).

**What happened:** While writing a test to assert that `defaultTransport`
(`cloneDefaultTransport(nil)`) has a `nil` `TLSClientConfig` — i.e. "an
unmodified clone of `remote.DefaultTransport`" — the assertion failed even on
a **freshly-called, first-ever** `cloneDefaultTransport(nil)`, before any
network request had been sent by anything in the test binary.

**Root cause:** `net/http`'s `(*Transport).Clone()` is not a pure copy. Its
first line is:

```go
func (t *Transport) Clone() *Transport {
	t.nextProtoOnce.Do(t.onceSetNextProtoDefaults)
	...
}
```

`onceSetNextProtoDefaults` runs **on the receiver `t`** — the transport being
cloned, not the clone — and, when `ForceAttemptHTTP2` is set (true for
`remote.DefaultTransport`), it lazily allocates a `TLSClientConfig` with
`NextProtos: ["h2", "http/1.1"]` if one isn't already set. `Clone()` then
copies that now-populated config onto the new `*Transport` via
`t2.TLSClientConfig = t.TLSClientConfig.Clone()`.

Consequence: the very first call to `.Clone()` on `remote.DefaultTransport` —
which happens unconditionally at `registry` package init, via
`var defaultTransport = cloneDefaultTransport(nil)` — permanently mutates the
shared `remote.DefaultTransport` singleton itself, giving it a non-nil
`TLSClientConfig` from that point forward. "The clone's `TLSClientConfig` is
nil" is therefore not just order-dependent on test execution — it is **never
true**, not even on the first call, because the mutation happens inside
`Clone()` before the copy is made.

**Faulty assumption:** I assumed `.Clone()` on an `http.Transport` behaves
like a value copy with no side effects on the source — reasonable for most
Go structs, wrong for `http.Transport` specifically because of its lazy
HTTP/2 self-configuration.

**Fix:** Replaced the "`TLSClientConfig == nil`" assertion with the
invariant that actually matters for correctness: `defaultTransport`'s
`TLSClientConfig`, whether nil or lazily populated, must never carry
`InsecureSkipVerify: true`. `insecureTransport`'s must always carry it.
Those two properties are stable regardless of `Clone()`'s side effect and
regardless of test execution order within the binary.

**Preventative rule:** When writing a test that inspects the *shape* of a
`*http.Transport` produced via `.Clone()`, do not assert on fields that
`onceSetNextProtoDefaults` can populate lazily (`TLSClientConfig`,
`TLSNextProto`) unless the assertion tolerates that populated state. Assert
on the security- or behavior-relevant *content* of those fields
(`InsecureSkipVerify`, proxy/idle-pool tuning) instead of their nil-ness.
More generally: before asserting "X is an unmodified copy of Y" for any
stdlib type with caching/once-init behavior, check whether the copy
operation itself (`Clone()`, `Do()`, etc.) has documented side effects on the
source — `go doc` and reading the stdlib source directly settled this in
under five minutes and would have prevented writing the wrong assertion in
the first place.

## 2026-08-16 — Multi-platform Static/Layered builds silently collided on a zero-value platform key

**Category:** multi-item / boundary

**Root cause:** In `internal/core/pipeline.go`'s per-platform fan-out
(inside `fanOut`), `art.Platform` was only ever set as a side effect of
calling `deps.Compiler.Compile(...)` — which populates
`compiledArt.Platform` from `CompileRequest.Platform` — and that call only
happens on the `else if !req.Compile.Strategy.ApplyStatic()` branch, i.e.
**only for `StrategyExe`**. `StrategyLayered` (which resolves a Bun runtime
instead of compiling) and `StrategyStatic` (which has nothing to compile —
the SvelteKit build output is the entire artifact) both leave `art` at its
Go zero value, so `art.Platform` stayed `ports.Platform{}` (empty OS/Arch)
for every platform in a Layered or Static build.

Two lines later, `built[i] = platformBuild{artifact: art, image: img}` is
built per platform, and back in `Build`, the map that becomes the packaged
OCI index's platform set is constructed as:
```go
images := make(map[Platform]v1.Image, len(built))
for i, b := range built {
    images[b.artifact.Platform] = b.image
}
```
For a Layered or Static build with more than one requested platform, every
entry collided under the same zero-value key — the last platform processed
silently overwrote all earlier ones in the map, then
`Packager.Index` failed with `packager: index: platform "": unsupported
platform` because `ports.Platform{}.Supported()` is false. A single-platform
Layered/Static build never surfaced this (map has exactly one entry
regardless of its key), which is exactly why it went uncaught: every
existing test exercising `StrategyLayered` or `StrategyStatic` used a single
platform.

**Where:** `internal/core/pipeline.go`, `fanOut`'s per-platform goroutine
(the block setting `art`/`bunResult` around what was originally lines
934–974).

**Fix:** Added `art.Platform = p` unconditionally, immediately after the
strategy branch that may or may not have populated it, so every strategy
(Exe, Layered, Static) gets a correctly keyed `ports.Artifact` regardless of
whether that strategy's branch happens to set `Platform` as a side effect of
some other field it needed anyway.

**Preventative rule:** When a per-platform fan-out loop derives a
downstream map/collection key from a field on a struct that's built up
piecemeal across multiple conditional branches (here: `art.Platform`,
populated only as an incidental side effect of the Exe branch's `Compile`
call), don't trust that every branch populates it — set the key field
explicitly and unconditionally, once, from the loop variable itself. This is
the same failure shape as other multi-item bugs in this codebase: correct
for N=1, silently wrong for N>1, because a single-element collection can't
expose a colliding key. Any test added for a strategy that skips a
per-platform field-populating call (no `Compile`, no `BunRuntime.Resolve`,
etc.) should use `>1` platform specifically to catch this class of bug —
single-platform coverage of a new strategy is not sufficient confidence that
its multi-platform path works.

## 2026-08-17 — A roadmap item's own wording assumed a CLI flag existed that was never actually wired

**Category:** documentation-drift / feature reality check (Row 16 family)

**Root cause:** `Roadmap.md`'s PB-2 entry read "Any other `--bun-version`
downloads and installs a ~90MB binary with no integrity check at all,"
written as if `--bun-version` were an existing, reachable CLI flag. It
wasn't: `ports.BunResolverRequest.Version` / `core.BunRuntimeOptions.Version`
existed as Go struct fields, and `internal/adapters/bunruntime/resolver.go`
correctly consumed them, but `cmd/pokkum/build.go` and `cmd/pokkum/dev.go`
only ever registered `--bun-binary` and `--bun-variant` — never
`--bun-version`. The original external-review audit that produced this
roadmap entry inferred a CLI surface from the existence of a Go field
without checking whether any flag actually set it, and the entry's own
wording was never checked against `cmd/pokkum/build.go`'s flag list before
being written down as fact.

**Where:** `cmd/pokkum/build.go`, `cmd/pokkum/dev.go` (missing flag
registration); `Roadmap.md`'s PB-2 row (the unverified claim).

**Fix:** Added `--bun-version` to both `pokkum build` and `pokkum dev`,
wired to `core.BunRuntimeOptions.Version`, matching the existing
`--bun-binary`/`--bun-variant` pattern exactly. This was found while
building the GPG-signature-verification fix for PB-2 itself (a separate,
correctly-scoped change) — the verification logic was fully correct and
tested, but had zero real CLI attack surface until this flag existed, so the
fix would have silently shipped as library-only/dead-from-the-CLI code
without this second look.

**Preventative rule:** Row 16 of `mem:self_review_checklist` says to grep
the implementing file for the I/O primitive a feature's description
requires before marking something Done. This is the mirror case for
*consuming* a roadmap claim rather than *writing* one: before implementing a
fix for an item that references a `--flag`, grep the actual `cmd/pokkum/*.go`
flag registrations for that exact flag name first — a struct field or port
request parameter existing is not evidence a CLI flag reaches it. A roadmap
entry describing "how a bug is triggered" is itself a claim to verify, same
as a "Done" claim.

## 2026-08-17 — `make verify`'s 5-step suite doesn't cover `tests/integration/`'s own golden fixtures

**Category:** verification-scope / determinism

**Root cause:** Earlier the same day, `internal/adapters/packager/layer.go` and
`internal/adapters/precompressutils/precompressutils.go` were switched from
stdlib `compress/gzip` to `github.com/klauspost/compress/gzip` (closing
PR-1's cross-toolchain gzip framing skew). `internal/adapters/packager/golden_test.go`'s
hardcoded digest constants were correctly re-recorded and `go test ./internal/...`
passed clean. But `tests/integration/golden_test.go` independently pins full
OCI manifest/config/index JSON (`testdata/golden/manifest_linux_amd64.json`,
`config_linux_amd64.json`, `index_multi_arch.json`) against the exact same
underlying compressed-layer-digest-sensitive build path — and `tests/integration/`
is outside `make verify`'s 5-step scope (`./internal/...` + `./cmd/pokkum`
only). The gzip-implementation swap was declared complete, verified, and
committed with these two golden files silently stale; only a later, broader
`go test ./...` sweep (run for an unrelated reason, while working on PB-2/PR-7)
caught it.

**Where:** `tests/integration/golden_test.go` (`TestGoldenOCIManifestAndConfig`,
`TestGoldenOCIIndex`); `testdata/golden/*.json`.

**Fix:** Regenerated both stale golden files with `go test ./tests/integration/...
-run <TestName> -update`, then diffed the result to confirm only compressed-bytes
digests moved (matching the same DiffID-unchanged invariant already verified
in `internal/adapters/packager/golden_test.go`) before committing.

**Preventative rule:** `mem:task_completion` now says explicitly: any change
touching layer compression, tar construction, or OCI manifest/config assembly
must also run `go test ./tests/integration/...` (or a full `go test ./...`),
not just the standard 5-step suite — `make verify`'s `./internal/...` scope is
a deliberate boundary (keeps the fast inner loop fast), not a claim that
nothing outside it can break. The general form of this lesson: a passing
`make verify` proves the tests inside its scope pass, never that no golden
fixture *anywhere in the repo* depends on what changed — when a change is
known to affect a widely-depended-on primitive (compression output, hashing,
serialization format), search the whole tree for other consumers of that
primitive's output before declaring done, not just the package that was
directly edited.

## 2026-08-17 — PR-4's own problem statement ("readiness races the `init` hook") was checked empirically against real adapter-node source and turned out to be false — but the fix was still worth building, for a different reason

**Category:** verify-before-fixing (Row 15/16 family) / no bug found, root cause is a documentation-drift risk avoided

**Root cause:** `Roadmap.md`'s PR-4 entry claimed "`/readyz` proves only a TCP listener... a pod passes readiness before `init` resolves and takes traffic it cannot serve." Before designing a fix around that claim, it was checked against the real, currently-vendored `@sveltejs/adapter-node@5.5.7` + `@sveltejs/kit@2.70.2` source in `testdata/fixtures/sveltekit-adapter-node/node_modules/`, not assumed. Finding: `index.js` does `import { handler } from './handler.js'` at its top; `handler.js`'s own top-level code does `await server.init({...})` (an ES module top-level `await`); ES module semantics guarantee an importer's own top-level code cannot proceed until every static import's top-level code — including its top-level awaits — has fully resolved. `server.listen()` in `index.js` runs *after* that import line. `@sveltejs/kit`'s `Server.init()` (`src/runtime/server/index.js:108-143`) is exactly where `hooks.server.js`'s exported `init()` hook gets awaited. Chained together: the TCP port literally cannot open before the user's `init()` hook (DB pool setup, cache warming) has resolved — there is no race window for the specific failure PR-4 described. A raw TCP-connect readiness check, which is what `supervisor/cmd/pokkum-init/probe.go`'s `/readyz` already does, was therefore already correct for that specific concern.

Separately, and found while looking at the wider claim: `readinessProbe`/`livenessProbe`/`startupProbe` injection was entirely absent from `internal/adapters/k8s/resolver.go` — not just `startupProbe`, which is all the roadmap item named. The `req.ResourceDefaults`/`req.SecurityDefaults` per-container injection pattern existed for CPU/memory and security context, but nothing generated any probe at all; a user gets probes only if they hand-write `readinessProbe`/`livenessProbe` pointing at the right port in their own Deployment YAML.

**Where:** `testdata/fixtures/sveltekit-adapter-node/node_modules/@sveltejs/adapter-node/files/{index.js,handler.js}`, `.../@sveltejs/kit/src/runtime/server/index.js:56-143` (the empirical check); `internal/adapters/k8s/resolver.go` (the actual, different gap).

**Fix:** did not build a readiness-vs-init race fix, because there is no race to fix. Built `injectContainerProbeDefaults` instead, for the *correct*, still-real reason a `startupProbe` matters here: with only a normal-cadence `livenessProbe`, a legitimately slow `init()` (a slow DB connection, a large cache warm) can get the container killed by kubelet for "failing" liveness before it ever finishes starting and opens its port — `startupProbe`'s `failureThreshold * periodSeconds` grace period (60s by default here) exists specifically to protect a slow-but-healthy startup from that premature kill, and `readinessProbe`/`livenessProbe` don't count against the container until `startupProbe` has succeeded once. Also injected `readinessProbe`/`livenessProbe` themselves, since neither existed either, gated per-probe-type so a container with its own custom `livenessProbe` still gets the other two filled in. `Roadmap.md`'s PR-4 entry is corrected to state the actual verified mechanism, not the originally-assumed one.

**Preventative rule:** matches Row 16's mirror-case extension from the same day's earlier `--bun-version` entry, applied to a technical (not just CLI-surface) claim: a roadmap item's problem statement about a third-party dependency's runtime behavior is an empirical claim, not a given. When real vendored source for that dependency is available in the repo (`testdata/fixtures/...node_modules/...`), read it before designing a fix around the claim — a wrong root cause can still lead to *a* correct-feeling fix (a `startupProbe` genuinely helps here) while completely misdiagnosing *why*, which would have left the doc/comments asserting something false indefinitely. Also folds in Row 15's existing guidance: this is the same "verify the mechanism, don't ship a fix around a plausible-but-unverified hypothesis" discipline, just applied to a framework's documented lifecycle behavior instead of a build-determinism bug.

## 2026-08-17 — Six new `ports.ImageConfig` fields (PB-4) were added to the struct and to base-config parsing but not to `ApplyProfile`'s per-field profile merge

**Category:** multi-item / config-parity (Row 10 family — caught by running the self-review checklist, not by a failing test)

**Root cause:** PB-4 added `Origin`/`ProtocolHeader`/`HostHeader`/`AddressHeader`/`XFFDepth`/`BodySizeLimit` to `ports.ImageConfig` and wired them into base `.pokkum.yaml` parsing (`cmd/pokkum/build.go`'s `projCfg.Image.Origin` read). `internal/adapters/config/config.go`'s `ApplyProfile`, which merges a named profile's `Image` overrides onto the base config, is not generic reflection over the struct — it's an explicit, hand-written list of `if profile.Image.X != zero { merged.Image.X = profile.Image.X }` lines, one per existing field (`Port`, `ProbePort`, `User`, `WorkingDir`, `ShutdownTimeout`, ...). A new `ImageConfig` field is invisible to this merge unless a matching line is added for it — nothing about adding the field to the struct forces that. Undetected because there is no existing test that adds a new `ImageConfig` field and checks it flows through `ApplyProfile`; the omission would have shipped as `profiles.<name>.image.origin: ...` parsing successfully, validating successfully, and then being silently discarded the moment `--profile <name>` was used — the base config's `Origin` (or empty, if unset) would apply instead, with no error or warning anywhere.

**Where:** `internal/adapters/config/config.go`'s `ApplyProfile`, Image-override block (~line 180-219).

**Fix:** added the six missing `if profile.Image.X != zero { merged.Image.X = profile.Image.X }` lines, matching the existing fields' exact style. Confirmed `deepCopyProjectConfig` needed no corresponding change — it starts from a full struct value copy (`dst := *src`) and only needs explicit re-cloning for reference-typed fields (maps/slices/pointers) to avoid aliasing; the six new fields are all plain `string`/`int`, already copied correctly by value. Added `TestApplyProfile_OriginContractFields`, which sets each of the six fields only in a profile (not the base) and asserts they appear in the merged result, plus a same-test check that an empty profile leaves the base value untouched.

**Preventative rule:** this is exactly `mem:self_review_checklist` row 10, re-confirmed rather than revised — the row already existed from a near-identical incident (validation, not merge, that time) and caught this one on the first checklist pass over the PB-4 diff, before any test run surfaced it. Restating the general form since it keeps recurring in this exact area: whenever a field is added to `ports.ImageConfig` (or any struct with a hand-written per-field override/merge/validate function elsewhere, as opposed to generic reflection), grep for every existing hand-written function operating on that struct — `ApplyProfile`, `validateConfigFields`, `deepCopyProjectConfig` — and add the new field to each one that has a reason to touch it, don't assume adding the field to the struct alone is sufficient.

## 2026-08-17 — Explicit `--sbom-attach=referrer` against a referrers-unsupported registry silently landed the SBOM on the wrong, undiscoverable tag — and the one test covering this path was inadvertently validating the bug as correct

**Category:** determinism / fixture-fidelity (Row 12 family) — a real bug found while implementing PR-8 (`--sbom-attach=auto`), not by a failing test

**Root cause:** `internal/adapters/registry/sbom.go`'s old `AttachSBOM` referrer-mode branch called `mutate.Subject` + `remote.Write` with go-containerregistry's *default* option set — no `remote.WithReferrersTagFallback(false)`. go-containerregistry's own `remote.Write`, when it detects the target registry doesn't support the OCI 1.1 Referrers API, silently falls back to its own internal tag scheme (`sha256-<hex>`, no suffix) rather than erroring — this is documented behavior in `WithReferrersTagFallback`'s doc comment (go-containerregistry@v0.21.9, `pkg/v1/remote/options.go`), not a bug in that library; it exists so a naive caller doesn't hard-fail. But Pokkum's own SBOM read path (`ports.SBOMTag`, the `<algo>-<hex>.sbom` convention) and `cosign download sbom` both look for the `.sbom`-suffixed tag — go-containerregistry's fallback tag is a *different, unsuffixed* tag, so the SBOM was technically pushed somewhere but invisible to every consumer that knows Pokkum's real convention. This had been true since referrer mode became the default (PR-8's predecessor), on every push to ECR, older Harbor, or older Artifactory.

The only pre-existing test for this path, `TestAttachSBOM_RoundTripReferrer`, used `newTestRegistry(t)` — go-containerregistry's in-process test registry, which (unless explicitly opted in) does **not** support the Referrers API. So this "referrer mode" test was, the entire time, silently exercising the exact silent-fallback path described above and asserting its result was correct — a fixture that happened to share the real bug's triggering condition, satisfying the test instead of catching the bug. This is the same class of gap as Row 12's origin incident (a fixture resembling the real thing closely enough to pass, while not actually modeling what the code path under test needs to prove): here the test registry's *capability*, not its content, was the mismatch with what "referrer mode success" actually requires.

**Where:** `internal/adapters/registry/sbom.go`'s `AttachSBOM` (referrer branch, pre-PR-8); `internal/adapters/registry/sbom_test.go`'s `TestAttachSBOM_RoundTripReferrer` (pre-PR-8, used the wrong-capability test registry).

**Fix:** two independent changes, not one. (1) `remote.WithReferrersTagFallback(false)` is now always set on the referrer-mode push, so an explicit `--sbom-attach=referrer` against an unsupported registry fails loudly (a clear, distinguishable error) instead of silently mis-landing. (2) A new `--sbom-attach=auto` default does its own explicit fallback — on exactly that "unsupported" error, it retries via Pokkum's own `attachSBOMTag` (the real `.sbom`-suffixed convention), not go-containerregistry's incompatible one. Found the gap in the existing test suite while writing the new auto-mode tests, then added `internal/adapters/registry/registry_test.go`'s `newTestRegistryWithReferrers` (`registry.New(registry.WithReferrersSupport(true))`) so a "referrer mode succeeds" test actually exercises a registry capable of it, and repointed `TestAttachSBOM_RoundTripReferrer` at that helper.

**Preventative rule:** extends `mem:self_review_checklist` row 12 — for a test claiming to exercise a registry-capability-gated code path (OCI 1.1 referrers, or any other opt-in registry feature), verify the test double actually has that capability turned on, the same way row 12 already requires verifying a fixture's *content* matches the real pipeline stage under test. A mock/fake that defaults to the *less-capable* configuration will make an explicit "does the advanced path work" test pass by accident, via whatever fallback exists for the less-capable case — silently converting a positive test into a no-op. When adding a new registry-adapter test around a capability flag, grep the test-registry constructor's own options for whether that capability is actually enabled, don't assume "it's a registry, so it must support X."

