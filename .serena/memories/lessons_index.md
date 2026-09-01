# Lessons Index — find the incident before you repeat it

`Lessons.md` is 100+ post-mortems and growing. `CLAUDE.md`'s Pre-Task Checklist
says to grep it for keywords, which only works when you guess the same word the
author used. This index routes by CATEGORY instead, so the question becomes
"what kind of change am I about to make?" rather than "what word might they
have written?".

Read `mem:self_review_checklist` for the rules distilled from these; read the
entry itself when you are about to touch the same area, because the narrative
carries the *why* that the checklist row compresses away.

## How to use

1. Find the category below that matches the change you are about to make.
2. Open `Lessons.md` and search for the date + title shown.
3. Read the entry's **Root cause** and **Preventative rule** BEFORE writing
   code, not after — a prior incident in the same area is a strong signal you
   are about to repeat it.

## The recurring classes, largest first


### boundary (33)

- `2026-09-01` — `pokkum config validate` reported a config valid that `pokkum deploy` then refused, because it 
- `2026-09-01` — Two new PaaS integrations both had a "200 means nothing happened" path, and one had a write tha
- `2026-08-23` — The published GitHub Action's `digest` and `ref` outputs were empty on every run since v1.0.0, 
- `2026-08-23` — The published GitHub Action never loaded at all: an example expression written as documentation
- `2026-08-22` — A feature only ran for input it was supposed to be unnecessary for
- `2026-08-21` — SLSA provenance attested binaries that were never released
- `2026-08-21` — Every project whose vite.config.ts calls sveltekit() directly produced unreproducible images, b
- `2026-08-21` — Adding `/app/node_modules` to the packager's attestation manifest without adding it to pokkum-i
- `2026-08-20` — Three of four documented install paths were broken on a public repo
- `2026-08-19` — the live-registry tripwire's `cannot reach registry` skip guarded only the cheap manifest fetch
- `2026-08-19` — a bulk path-rewriting sweep changed a filename *argument* as if it were a *reference*, redirect
- `2026-08-19` — a `replace` directive that looked like it substituted x/crypto's openpgp applied to nothing at 
- `2026-08-19` — `pokkum init` wrote a config `pokkum build` refused, and the binary already contained the valid
- `2026-08-19` — `pokkum init` closed by recommending a command it had just guaranteed would fail
- `2026-08-19` — `cosign verify-attestation` rejected every Pokkum attestation over a missing annotation whose v
- `2026-08-19` — `Preflight` again made an independent, untested assumption about the target adapter that a same
- `2026-08-19` — The embedded Sigstore trust root was already rejecting valid signatures as forgeries, not merel
- `2026-08-19` — The embedded PID-1 binaries were gitignored local artifacts that no CI job or release pipeline 
- `2026-08-19` — Moving a trust-root file read to the composition root exposed a second consumer that had been s
- `2026-08-19` — Making custom `--base` lock slots per-reference silently unhooked `pokkum base check` and `Reco
- `2026-08-19` — Every custom `--base` reference shared one `pokkum.lock` slot, so resolving a second custom bas
- `2026-08-19` — Deleting the last adapter→adapter import edges exposed two latent fail-opens in provenance veri
- `2026-08-19` — A phantom-flag guard scoped to one surface let the same class recur through another
- `2026-08-18` — Provenance verification's static-key path was a literal fail-open: `SignatureValid: false` with
- `2026-08-18` — Git `--since` ref sat before the `--` terminator, letting a crafted ref inject a `git diff` opt
- `2026-08-18` — Escrow-mirror base images were pulled by a mutable tag, and the digest they were supposed to be
- `2026-08-18` — An early bounds check in `comparator.CompareImages` protected one branch but not the sibling br
- `2026-08-17` — `Preflight` hard-required `svelte.config.js` to exist, blocking every real `sv create` project 
- `2026-08-17` — Zero-Config Auto-Injection had no effect on real builds; two compounding causes, both traced to
- `2026-08-16` — Opt-in SPA-fallback: serving a fallback file that became non-regular at request time surfaced a
- `2026-08-16` — Multi-platform Static/Layered builds silently collided on a zero-value platform key
- `2026-08-16` — Adversarial review of SPA-fallback config detector: whole-file regex makes `fallback` detection
- `2026-08-16` — .pokkum.yaml config validation had three silent-failure gaps: dead Viper wiring, no per-profile

### multi-item (10)

- `2026-08-21` — The SBOM's unresolved-version marker had a real test, but a single-item fixture and no CycloneD
- `2026-08-19` — `paranoid-testing-guide.md`'s own `--output=json` premise was wrong in eight places, and it was
- `2026-08-19` — The synthetic static-strategy fixture fabricated the same flat `prerendered/index.html` shape t
- `2026-08-19` — Real-build tests wrote into `testdata/fixtures/*` in place, leaving order-dependent state (`bui
- `2026-08-18` — `--asset-overlay`'s merged overlay layer packaged every carried-forward asset one directory lev
- `2026-08-18` — A regression test asserted the supervisor layer's timestamp equals `SOURCE_DATE_EPOCH` — which 
- `2026-08-17` — `patchPrerenderedHandler`'s matcher was fixed, not by adding new patterns, but by pointing the 
- `2026-08-17` — Six new `ports.ImageConfig` fields (PB-4) were added to the struct and to base-config parsing b
- `2026-08-16` — `patchPrerenderedHandler`'s "real fixture" regression tests exercised the wrong artifact — the 
- `2026-08-16` — Multi-platform Static/Layered builds silently collided on a zero-value platform key

### determinism (9)

- `2026-09-01` — The first build of any project produced a different image digest from every later build, becaus
- `2026-08-22` — Sorting the output hid a nondeterministic choice of what went into it
- `2026-08-19` — Go's default VCS stamping churned the embedded PID-1 binaries on every commit, partially undoin
- `2026-08-18` — The Bun runtime, `pokkum-init`, and `pokkum-static` layers took their tar timestamp — and on-di
- `2026-08-18` — Closed the `docker.sock` half of `--hermetic`'s residual mount-namespace gap (opt-in `--hermeti
- `2026-08-18` — A content-addressed cache key made the new Bun-layer stability test vacuous: a shared cache wou
- `2026-08-17` — `make verify`'s 5-step suite doesn't cover `tests/integration/`'s own golden fixtures
- `2026-08-17` — Explicit `--sbom-attach=referrer` against a referrers-unsupported registry silently landed the 
- `2026-08-16` — Non-deterministic stub-launcher binary: the suspected root cause (ELF build-id) was wrong

### verification-gap (6)

- `2026-08-21` — Pinning the installer action is not pinning the tool it installs
- `2026-08-21` — A parallel wave of six fixes, and four checks that could never fail
- `2026-08-21` — A commit closed a roadmap item whose whole point was a measurement that was never taken
- `2026-08-20` — A floating version pin resolved differently per platform and broke the build
- `2026-08-19` — A docker-gated test kept a stale expectation invisible through a full "green" suite
- `2026-08-19` — A dead linter silently disabled the whole CI gate; then its replacement passed green while test

### process (4)

- `2026-09-01` — A `head -10` on a reachability grep produced a confident, wrong recommendation to delete live c
- `2026-08-21` — Test fixtures containing realistic credentials blocked the push
- `2026-08-21` — A commit closed a roadmap item whose whole point was a measurement that was never taken
- `2026-08-20` — A detached HEAD made committed work look like it had never happened

### fail-open (4)

- `2026-08-21` — Three verification-adjacent checks each answered "clean/valid" from no evidence, in three diffe
- `2026-08-21` — The SBOM was attached as an unsigned blob nothing bound to the image, so a signed image's SBOM 
- `2026-08-19` — Moving a trust-root file read to the composition root exposed a second consumer that had been s
- `2026-08-18` — `--expect-source` verified against the artifact's own unsigned annotations

### test-substance (3)

- `2026-08-19` — three of four guards for a filesystem race passed with the fix reverted, and the fix itself was
- `2026-08-19` — `paranoid-testing-guide.md`'s own `--output=json` premise was wrong in eight places, and it was
- `2026-08-18` — A content-addressed cache key made the new Bun-layer stability test vacuous: a shared cache wou

### concurrency (3)

- `2026-08-19` — three of four guards for a filesystem race passed with the fix reverted, and the fix itself was
- `2026-08-18` — `dev --watch` died after exactly one rebuild because a buffered result channel was reused acros
- `2026-08-18` — The k8s affected-detection goroutine leak was fixed once (checklist rows 1 and 4) and came back

### resource-leak (3)

- `2026-08-18` — `pokkum upgrade`'s binary self-replacement retried an identical rename after deleting the insta
- `2026-08-18` — The runtime smoke test leaked a Docker container on a failed run, because cleanup was registere
- `2026-08-18` — The k8s affected-detection goroutine leak was fixed once (checklist rows 1 and 4) and came back

### silent-degradation (3)

- `2026-08-19` — The embedded Sigstore trust root was already rejecting valid signatures as forgeries, not merel
- `2026-08-19` — Deleting the last adapter→adapter import edges exposed two latent fail-opens in provenance veri
- `2026-08-18` — Provenance verification's static-key path was a literal fail-open: `SignatureValid: false` with

### parallel-path drift (2)

- `2026-08-22` — The reproducibility fix reached only misconfigured projects, because the passthrough path got t
- `2026-08-21` — Every project whose vite.config.ts calls sveltekit() directly produced unreproducible images, b

### fixture fidelity (2)

- `2026-08-21` — The only test that boots a produced image passed throughout the startup-attestation outage, bec
- `2026-08-17` — Every `--strategy=layered` (default) image was missing its own entrypoint (`/app/server/index.j

### cache-key granularity (2)

- `2026-08-19` — Making custom `--base` lock slots per-reference silently unhooked `pokkum base check` and `Reco
- `2026-08-19` — Every custom `--base` reference shared one `pokkum.lock` slot, so resolving a second custom bas

### overclaiming (2)

- `2026-08-18` — Image signing was wired end-to-end for the first time; every image Pokkum ever pushed before th
- `2026-08-17` — Four shipped, `[x]`-marked, documented features were stubs or half-built; found only by verifyi

### no bug found (2)

- `2026-08-18` — `--hermetic`'s pathname-Unix-socket gap: researched a full fix, deliberately shipped a narrower
- `2026-08-17` — PR-4's own problem statement ("readiness races the `init` hook") was checked empirically agains

### validation-gap (2)

- `2026-08-16` — docker.repo (registry ref) was read from config but never validated for shape
- `2026-08-16` — .pokkum.yaml config validation had three silent-failure gaps: dead Viper wiring, no per-profile

### test-encodes-the-bug (2)

- `2026-08-19` — The synthetic static-strategy fixture fabricated the same flat `prerendered/index.html` shape t
- `2026-08-18` — A regression test asserted the supervisor layer's timestamp equals `SOURCE_DATE_EPOCH` — which 

### seam-untested (2)

- `2026-08-19` — `pokkum init` wrote a config `pokkum build` refused, and the binary already contained the valid
- `2026-08-19` — `pokkum init` closed by recommending a command it had just guaranteed would fail

### guard-scope (2)

- `2026-08-20` — Three of four documented install paths were broken on a public repo
- `2026-08-19` — A phantom-flag guard scoped to one surface let the same class recur through another

## One-off categories (72)

- `2026-09-01` — **validator-consumer disagreement** — `pokkum config validate` reported a config valid that `pokkum deploy` then refused, because it 
- `2026-09-01` — **external-contract** — Two new PaaS integrations both had a "200 means nothing happened" path, and one had a write tha
- `2026-09-01` — **build-state leakage** — The first build of any project produced a different image digest from every later build, becaus
- `2026-09-01` — **truncated-evidence** — A `head -10` on a reachability grep produced a confident, wrong recommendation to delete live c
- `2026-08-23` — **shadow-parser drift** — The published GitHub Action's `digest` and `ref` outputs were empty on every run since v1.0.0, 
- `2026-08-23` — **evaluated-position** — The published GitHub Action never loaded at all: an example expression written as documentation
- `2026-08-22` — **control-flow** — `else if` chained to a different `if` than its indentation showed
- `2026-08-22` — **vocabulary narrower than the external format it is written into** — `crane validate` rejected every multi-arch image Pokkum ever produced, because the index descri
- `2026-08-22` — **coupling** — A feature only ran for input it was supposed to be unnecessary for
- `2026-08-21` — **fabricated data in a success path** — `pokkum scan` fabricated a CVE against a version the project does not use, because "found nothi
- `2026-08-21` — **coverage-shape** — The only test that boots a produced image passed throughout the startup-attestation outage, bec
- `2026-08-21` — **supply-chain** — SLSA provenance attested binaries that were never released
- `2026-08-21` — **mirrored-constant drift** — Adding `/app/node_modules` to the packager's attestation manifest without adding it to pokkum-i
- `2026-08-19` — **test-infra** — the live-registry tripwire's `cannot reach registry` skip guarded only the cheap manifest fetch
- `2026-08-19` — **mechanical-refactor** — a bulk path-rewriting sweep changed a filename *argument* as if it were a *reference*, redirect
- `2026-08-19` — **false-assurance** — a `replace` directive that looked like it substituted x/crypto's openpgp applied to nothing at 
- `2026-08-19` — **untested-real-path** — `pokkum-static` never set `http.Server.Addr` on either listener, so every `--strategy=static` i
- `2026-08-19` — **resource-leak-adjacent** — `pokkum-static` never set `http.Server.Addr` on either listener, so every `--strategy=static` i
- `2026-08-19` — **feature reality check** — `pokkum-static` computed and sent a strong `ETag` on every response but had no `If-None-Match` 
- `2026-08-19` — **build-pipeline gap** — The embedded PID-1 binaries were gitignored local artifacts that no CI job or release pipeline 
- `2026-08-19` — **shared-mutable-state** — Real-build tests wrote into `testdata/fixtures/*` in place, leaving order-dependent state (`bui
- `2026-08-18` — **resource-limit-vs-realistic-input** — secretguard's post-build scan silently reported a clean directory it had never actually looked 
- `2026-08-18` — **multi-item (a bufio.Scanner token-limit bug plus a one-match-per-line bug** — secretguard's post-build scan silently reported a clean directory it had never actually looked 
- `2026-08-18` — **found together while extending secretguard to scan build output)** — secretguard's post-build scan silently reported a clean directory it had never actually looked 
- `2026-08-18` — **destructive-retry** — `pokkum upgrade`'s binary self-replacement retried an identical rename after deleting the insta
- `2026-08-18` — **scoped-decision** — `--hermetic`'s pathname-Unix-socket gap: researched a full fix, deliberately shipped a narrower
- `2026-08-18` — **self-referential-check** — `--expect-source` verified against the artifact's own unsigned annotations
- `2026-08-18` — **path-transformation round-trip** — `--asset-overlay`'s merged overlay layer packaged every carried-forward asset one directory lev
- `2026-08-18` — **validation logic** — The CVE gate's version comparison was byte-wise lexicographic, so `1.2.0 < 1.10.0` evaluated to
- `2026-08-18` — **security-gate correctness** — The CVE gate's version comparison was byte-wise lexicographic, so `1.2.0 < 1.10.0` evaluated to
- `2026-08-18` — **verify-before-shipping (row 17 family)** — PR-5's real fix found two more real bugs the moment it was actually compiled and run — includin
- `2026-08-18` — **self-referential security check (new category)** — Keyless Sigstore verification derived its expected signer identity from the certificate it was 
- `2026-08-18` — **fake-implementation** — Image signing was wired end-to-end for the first time; every image Pokkum ever pushed before th
- `2026-08-18` — **injection** — Git `--since` ref sat before the `--` terminator, letting a crafted ref inject a `git diff` opt
- `2026-08-18` — **tautological check** — Escrow-mirror base images were pulled by a mutable tag, and the digest they were supposed to be
- `2026-08-18` — **not covered by any existing checklist row)** — Closed the `docker.sock` half of `--hermetic`'s residual mount-namespace gap (opt-in `--hermeti
- `2026-08-18` — **isolation (row 18 family) + a genuinely new test-harness pitfall (process-boundary state** — Closed the `docker.sock` half of `--hermetic`'s residual mount-namespace gap (opt-in `--hermeti
- `2026-08-18` — **validation logic (length-without-charset)** — Bun's SHASUMS parser validated a checksum's length but not its character set, so 64 literal `g`
- `2026-08-18` — **multiple** — Adversarial review of `--asset-overlay` found and fixed four further real defects: a second shi
- `2026-08-17` — **narrow variant of the overclaiming category below** — `pokkum why`/`pokkum diff` were documented as top-level commands in three separate places; they
- `2026-08-17` — **documentation drift (new** — `pokkum why`/`pokkum diff` were documented as top-level commands in three separate places; they
- `2026-08-17` — **fabricated-looking value from an unverified assumption** — `pokkum explain`'s new `Platform` field echoed the requested `--platform` flag instead of the r
- `2026-08-17` — **test-fixture-fidelity (the fix for the gap logged in the entry immediately below)** — `patchPrerenderedHandler`'s matcher was fixed, not by adding new patterns, but by pointing the 
- `2026-08-17` — **verification-scope** — `make verify`'s 5-step suite doesn't cover `tests/integration/`'s own golden fixtures
- `2026-08-17` — **tokenizer (boundary condition in source-code transformations)** — `TransformViteConfig` naive substring matching risked mutating commented-out code and string li
- `2026-08-17` — **logic-error** — `TransformViteConfig` naive substring matching risked mutating commented-out code and string li
- `2026-08-17` — **test-fixture-fidelity (a third** — `Preflight` hard-required `svelte.config.js` to exist, blocking every real `sv create` project 
- `2026-08-17` — **previously-undiscovered instance of the same root shape as this file's other 2026-08 entries** — `Preflight` hard-required `svelte.config.js` to exist, blocking every real `sv create` project 
- `2026-08-17` — **test-fixture-fidelity** — Zero-Config Auto-Injection had no effect on real builds; two compounding causes, both traced to
- `2026-08-17` — **config-parity (Row 10 family** — Six new `ports.ImageConfig` fields (PB-4) were added to the struct and to base-config parsing b
- `2026-08-17` — **verify-before-fixing (Row 15** — PR-4's own problem statement ("readiness races the `init` hook") was checked empirically agains
- `2026-08-17` — **root cause is a documentation-drift risk avoided** — PR-4's own problem statement ("readiness races the `init` hook") was checked empirically agains
- `2026-08-17` — **16 family)** — PR-4's own problem statement ("readiness races the `init` hook") was checked empirically agains
- `2026-08-17` — **security** — PR-2's first cut of `--hermetic` network enforcement only sandboxed half the pipeline — Compile
- `2026-08-17` — **incomplete-scope (new checklist row 18)** — PR-2's first cut of `--hermetic` network enforcement only sandboxed half the pipeline — Compile
- `2026-08-17` — **fake-implementation (new category** — Four shipped, `[x]`-marked, documented features were stubs or half-built; found only by verifyi
- `2026-08-17` — **fixture-fidelity (Row 12 family)** — Explicit `--sbom-attach=referrer` against a referrers-unsupported registry silently landed the 
- `2026-08-17` — **packaging boundary mismatch** — Every `--strategy=layered` (default) image was missing its own entrypoint (`/app/server/index.j
- `2026-08-17` — **test-fixture-fidelity (a meta-instance of this file's recurring theme: this time the false claim was about provenance itself** — A sub-agent's "captured verbatim from a real `bunx sv create` project" fixture doc comment was 
- `2026-08-17` — **not about the artifact's pipeline stage)** — A sub-agent's "captured verbatim from a real `bunx sv create` project" fixture doc comment was 
- `2026-08-17` — **feature reality check (Row 16 family)** — A roadmap item's own wording assumed a CLI flag existed that was never actually wired
- `2026-08-17` — **documentation-drift** — A roadmap item's own wording assumed a CLI flag existed that was never actually wired
- `2026-08-16` — **test-fixture-fidelity (same root shape as the assets.generated.ts entry immediately below** — `patchPrerenderedHandler`'s "real fixture" regression tests exercised the wrong artifact — the 
- `2026-08-16` — **boundary (strategy-dependent default computed before the strategy-aware code path runs)** — `core.Build`'s `Normalize()` pre-defaults `Runtime.Entrypoint` to the exe shape before the stra
- `2026-08-16` — **boundary (strategy-gated logic applied outside its own strategy)** — `assets.generated.ts` normalization ran for `StrategyLayered`, but that file is exclusively a `
- `2026-08-16` — **logic** — Requesting an explicit `--profile` without a `.pokkum.yaml` file silently ignored the profile b
- `2026-08-16` — **boundary (silent failure on missing prerequisite configuration)** — Requesting an explicit `--profile` without a `.pokkum.yaml` file silently ignored the profile b
- `2026-08-16` — **resource (request-time re-validation vs construction-time-only validation)** — Opt-in SPA-fallback: serving a fallback file that became non-regular at request time surfaced a
- `2026-08-16` — **not by Pokkum's own code touching a clock or an unsorted collection)** — Non-deterministic stub-launcher binary: the suspected root cause (ELF build-id) was wrong
- `2026-08-16` — **external-tool-output (a new subcategory: non-determinism introduced by a *third-party compiler's* output** — Non-deterministic stub-launcher binary: the suspected root cause (ELF build-id) was wrong
- `2026-08-16` — **parsing (whole-file regex vs scoped match)** — Adversarial review of SPA-fallback config detector: whole-file regex makes `fallback` detection
- `2026-08-16` — **dead-code** — .pokkum.yaml config validation had three silent-failure gaps: dead Viper wiring, no per-profile
