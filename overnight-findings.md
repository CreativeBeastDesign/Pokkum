# Overnight run — findings log

Bugs and defects discovered while working the overnight queue of 2026-08-18/19.
Each entry records what was found, where, whether it was fixed, and how it was
confirmed. Root causes and preventative rules for the substantial ones also go
to `Lessons.md` (the project's permanent incident log) and, where they imply a
new invariant, to Serena's `mem:self_review_checklist`. This file is the
chronological working record for this run specifically.

**Queue:** `pipeline_test.go` mock asserts · `--strategy=static` fixture + boot
smoke test · composition-root refactor for the allowlisted adapter→adapter
imports · `populateInputsFromSLSA` repository fallback · `--runtime=node` ·
Sigstore TUF root refresh.

---

## Findings

### 1. `populateInputsFromSLSA` filled the source repo from the target image repo

**Found:** carried over from the `--expect-source` work (`91dc3cd`), which flagged it in code comments and deliberately left it out of scope.
**Where:** `internal/adapters/provenance/resolver.go`, the `ep["repository"]` fallback.
**What:** SLSA external parameters' `repository` is the _target image_ repository — where the image was pushed. It was being used to fill `PinnedInputs.Repo`, which means the _git source_ repository and is what `pokkum verify` displays as the build's source. So on a statement with no `source-code` dependency, an operator saw `Source Repo: ghcr.io/acme/app` and could reasonably conclude the source had been verified as coming from there, when all that field proved was where the image was pushed.
**Severity:** correctness / misleading output, not a security hole — confirmed: the fallback never set `SourceProvenance`, so `--expect-source`'s gate (which requires `SourceProvenanceVerified`) rejected either way, before and after.
**Fixed:** fallback removed rather than relocated. `Repo` now stays empty and `pokkum verify` prints `(unrecorded)`, which is honest. Every reader of `PinnedInputs.Repo` was checked to handle the empty case. Pinned by a new test asserting that a verified statement with no `source-code` dependency leaves `Repo` unpopulated.
**Note:** an empty field the operator must interpret beats a populated field that means something other than its name.

### 2. `pokkum-static` ignores `PORT` and `POKKUM_PROBE_PORT` — every static image is unreachable

**Found by:** the new `--strategy=static` boot smoke test, on its first real run.
**Where:** `supervisor/cmd/pokkum-static/main.go` — both `&http.Server{...}` literals (content ~line 74, probe ~line 95) omit the `Addr` field entirely.
**What:** with no `Addr`, Go's `ListenAndServe` falls back to `:http`, i.e. port 80. Both servers therefore try to bind port 80, ignoring the configured ports completely: one wins the race and the other dies with "address already in use". Confirmed directly — the real binary was built and run with `PORT=3000 POKKUM_PROBE_PORT=8081`, `lsof` showed it bound to `*:80`, curl to 3000 and 8081 failed, curl to 80 returned 200.
**Severity:** High. Every image built with `--strategy=static` is non-functional through its documented ports, independent of the other two findings. A Kubernetes readiness probe on 8081 or a Service targeting 3000 never succeeds.
**Why it survived:** `main_test.go` and `integration_test.go` only exercise handlers through `httptest`; nothing ever called the real `ListenAndServe` path. This is the exact gap the runtime smoke test was built to close, and it found a live bug on its first outing.
**Status:** ✅ **Fixed** in `8306d37`. `Addr` is set on both servers from the parsed config; the test binds a real listener and makes a real TCP request rather than using an `httptest` handler, and neutering the assignment makes it fail. Confirmed against the built binary with `lsof` (`*:3000`, `*:8081`, nothing on 80). Note the fix did not reach produced images until the embedded blob was regenerated — see finding 6.

### 3. `Preflight` is not strategy-aware and rejects every real `adapter-static` project

**Where:** `internal/adapters/bunexec/compiler.go`'s `Preflight`; `ports.PreflightRequest` has no `Strategy` field.
**What:** `Preflight` hard-codes a requirement for `@jesterkit/exe-sveltekit` or `@sveltejs/adapter-node`. A correctly-configured `adapter-static`-only project is rejected before `Prepare`'s own adapter check — which _is_ strategy-aware — ever runs. So the static strategy cannot build a real static project at all.
**Severity:** High. Same class as `Lessons.md`'s earlier entry where a well-tested fix to `Prepare`'s adapter detection left every project unbuildable because `Preflight` made an independent, untested assumption about the same input (checklist row 13: grep the fixed function's callers).
**Status:** ✅ **Fixed** in `1c33509`. `ports.PreflightRequest` gained `Strategy`, threaded from the pipeline, and `Preflight` selects its target adapter with the same positive switch `Prepare` uses.

### 4. Real prerendered output nests under `pages/`; production assumes it is flat

**Where:** real `.svelte-kit/output/prerendered/` from `@sveltejs/adapter-static`; consumed via `internal/adapters/bunexec/compiler.go` (~line 436) and served from `/app/prerendered`.
**What:** a real build emits `prerendered/pages/index.html` and `prerendered/pages/about.html` — there is no top-level `prerendered/index.html`. The existence check therefore passes (the directory is there) while every prerendered route 404s at runtime.
**Compounding:** `staticFixtureCompiler` in `tests/integration/static_e2e_test.go` fabricates a _flat_ `prerendered/index.html`, matching the production assumption rather than reality. So the synthetic fixture has been validating a fiction, and `TestFixtureDrivenE2E_Static` passed throughout — exactly checklist row 12's failure mode, and the same shape as the missing-layered-entrypoint bug in `Lessons.md`.
**Severity:** High, and the most instructive of the three: a mock encoding the same wrong assumption as the code it tests cannot ever detect the mismatch.
**Status:** ✅ **Fixed** in `1c33509` via `bunexec.FlattenPrerenderedOutput`, which reproduces SvelteKit's own flattening of all three prerendered categories (`pages`, `dependencies`, `data`) and treats a cross-category path collision as a hard error rather than a silent overwrite. `staticFixtureCompiler` now models the real nested shape and calls that production code rather than reimplementing it, so it cannot drift back into agreeing with the bug.

### 5. Single-port mode silently has no probe endpoints

**Found by:** the F1 bind-address fix, while confirming neighbouring logic.
**Where:** `supervisor/cmd/pokkum-static/main.go`'s `if cfg.Port != cfg.ProbePort` guard.
**What:** the guard reads as "skip the redundant second listener because the content server covers probes in single-port mode" — but there is no mux merge anywhere in the package. When `PORT == POKKUM_PROBE_PORT`, the probe listener is skipped and `/healthz` and `/readyz` are served by _nothing_. A single-port deployment therefore has no working probes at all.
**Severity:** Medium. Pre-existing and independent of the bind bug; not introduced or touched by that fix. Only bites operators who deliberately collapse the two ports, which is why it has gone unnoticed.
**Status:** ✅ **Fixed.** The maintainer chose to reject the collapsed configuration outright rather than merge the probe handlers or document it — a container that serves pages while its probes are silently dead is worse than one that refuses to start, because Kubernetes routes traffic to it and the operator gets no signal. `Config.validate()` now rejects `PORT == POKKUM_PROBE_PORT` with an error naming both env vars, exiting `exitUsage` (2) alongside the other static configuration errors. `main.go`'s `if cfg.Port != cfg.ProbePort` guard became permanently true and was removed, with a comment recording the invariant that makes the two unconditional listeners safe — a guard that cannot fail misleads the next reader.
**Severity revised down on new evidence:** `pokkum build` could never have produced a collapsed config. `internal/core/model.go:1115` already rejects `Port == ProbePort` unconditionally for every build request, before the packager writes any env. So this only ever affected configs assembled outside `pokkum build` (hand-edited pod specs, a manually-run binary), and the new check is defence-in-depth rather than closing a build-time hole. Verified by tracing `--port`/`--probe-port` end to end — `pokkum build` has no such flags at all; only `.pokkum.yaml`'s `image.port`/`image.probe_port` feed them.
**Embedded blob regenerated** as part of the fix, per finding 6's lesson.
**Decision by André:** Reject the collapsed configuration.

### 6. Embedded PID-1 binaries are gitignored local artifacts, absent from CI and (for pokkum-static) from releases

**Correction.** This entry originally said the blobs were *checked-in* artifacts, and commit `1c33509`'s message claimed it had committed the regenerated ones. Both were wrong: `internal/adapters/{staticserver,supervisor}/bin/pokkum-*` are **gitignored** (`.gitignore:15,18`), only `.gitkeep` is tracked, and `git add` on an ignored path silently did nothing. The real situation is worse than the one first described, so the corrected version follows.

**What:** `pokkum-init` and `pokkum-static` are zstd blobs consumed via `go:embed all:bin`, produced only by `make supervisor` / `make static-server`. A fresh checkout (verified with `git archive HEAD`) contains neither — just `.gitkeep`. `go build ./cmd/pokkum` still succeeds, because embedding an almost-empty directory is legal; the failure surfaces only at runtime, when the provider looks for a blob that isn't there.

**Three consequences, all verified:**
1. **No CI job built them.** Nothing in `ci.yml`, `release.yml` or `slsa-builder.yml` ran either target, so CI's CLI could not produce a working image and the new real-build e2e job would have failed on a clean runner.
2. **`.goreleaser.yaml`'s before-hook ran `make supervisor` but not `make static-server`.** So released binaries embedded no static server at all: `--strategy=static` was non-functional in every published release, independent of findings 2/3/4/7.
3. **The PID 1 in every produced image was built on a developer laptop**, outside the attested pipeline — while the CLI's own SLSA provenance describes a hermetic CI build. For a supply-chain tool, the binary that runs as PID 1 and enforces the startup attestation being the one component not covered by the build attestation is the sharpest edge found tonight.

**How it was caught:** the smoke test kept failing after the bind fix was committed, with the container logging `addr=:8081` beside `listen tcp :80` — a pair the fixed code cannot emit. The old code logged the intended port while binding the default, which is also why the original bug never showed in logs.

**Fixed:** `make static-server` added to the goreleaser before-hooks, and a `Build Embedded PID-1 Binaries` step (`make supervisor static-server`) added to all three CI jobs. Still worth adding: an assertion that the embedded blob matches a fresh build of its source, so a stale local blob cannot silently ship. Tracked for the roadmap.

### 7. `pokkum-static` never tries `<path>.html`, so every non-root prerendered route 404s

**Found by:** the static smoke test, after findings 2, 3, 4 and 6 were resolved — `/` and `/robots.txt` served correctly while `/about` 404'd.
**Where:** `supervisor/cmd/pokkum-static/server.go`'s `tryServe`.
**What:** candidate resolution handles exactly two cases — an exact file at the request path, or a directory containing `index.html`. There is no extensionless fallback. `@sveltejs/adapter-static` with its default `trailingSlash: 'never'` prerenders route `/about` to `about.html`, so a request for `/about` finds neither a file named `about` nor a directory, and 404s. `/` worked only incidentally, via the directory→`index.html` branch.
**Severity:** High. Every prerendered route except the root is unreachable at its canonical URL in every `--strategy=static` image. The server even logs a misleading hint suggesting the operator configure an SPA fallback, when the file is present and simply not being looked for.
**Why it survived:** the synthetic fixture only ever fabricated a root `index.html` (finding 4), so no test exercised a non-root prerendered route.
**Status:** ✅ **Fixed** in `5693980`. Candidate order is exact file → `<rel>.html` → directory index, all three routed through one shared containment helper so the `EvalSymlinks`/`withinRoot` checks are identical for each. Proven with a symlinked `escape.html`, asserting the outside content never appears in the response rather than only that an error was returned. The misleading SPA-fallback hint now fires only for extensionless paths. Embedded blob regenerated.

### 8. Two latent fail-opens in `provenance`, reachable only once a default was deleted

**Found by:** the composition-root refactor, while removing the `cosign.NewSigner`/`sigstore.NewVerifier` defaults.
**Where:** `internal/adapters/provenance/resolver.go` — a `case r.signer != nil:` with no `default`, and a `&& r.keyless != nil` folded into the *material-presence* condition. Also `tryParseAndVerifySLSA` returning a bare `false` for a nil DSSE signer.
**What:** while the constructor always supplied a verifier these conditions were unreachable, so they read as harmless guards. With the default gone, each one silently *skips* verification instead of refusing: the first two produce `SignatureValid: false` with a **nil error** for a genuinely signed image, and the third yields `HasProvenance: false`, which is load-bearing for `--expect-source`.
**Severity:** High as a latent class — this is the third distinct instance tonight of the same shape (`false, nil` being indistinguishable from a working refusal). Not exploitable before the refactor, since the nil branch could not be entered.
**Fixed:** tracked via `sigVerifyOutcome.verifierMissing` and refused with a new `ErrVerifierNotInjected`; the SLSA path now returns a fatal error rather than a bare false. Each was confirmed by reintroducing the default and watching the new tests print the fail-open shape verbatim.
**Generalizable rule (proposed for the checklist):** a nil-tolerant condition that reads as a guard is a fail-open in waiting the moment whatever made nil unreachable is removed. When deleting a default, grep every `== nil`/`!= nil` test on that field and confirm each one *refuses* rather than *skips*.

### 9. Generated `.pokkum/` sandboxes inside fixtures were being committed, 16 of them by me

**What:** `testdata/fixtures/sveltekit-adapter-node/.pokkum/` accumulated 27 tracked content-hashed `handler-*.js` files, ~1.2MB. Pokkum writes virtual configs and patched handlers into `.pokkum/` during a build, and the adapter-node e2e path leaves one per run, so the directory grows every time the suite runs.
**Attribution, stated plainly:** 11 predated this session; **16 were added by my own commits during it**, swept in by `git add -A`. That is my error, not an agent's — I used a broad add while several agents were writing to the tree.
**Severity:** Low technically, but it is repo hygiene that compounds silently, and it was caused by exactly the kind of shortcut I should not have taken while orchestrating parallel work.
**Fixed:** `testdata/fixtures/*/.pokkum/` added to `.gitignore` and all 27 untracked (files left on disk; they are regenerable output). Verified first that the *deliberate* patch-target corpus lives separately in `testdata/adapter-node/` and remains tracked, and that the only test reading a fixture `.pokkum` path creates the file it reads.
**Preventative note for the rest of this run:** stage explicit paths, not `-A`, when agents may be mid-write.

### 10. The embedded Sigstore trust root was already rejecting valid signatures as forgeries

**Found by:** the Tier 2c TUF refresh work, which began by establishing facts rather than assuming staleness.
**Where:** `internal/adapters/sigstore/trusted-root-public-good.json`.
**What:** the embedded snapshot's newest anchor dated 2023-04-14 and it covered **one** Rekor transparency log. The live public-good root covers **two** — it gained `log2025-1.rekor.sigstore.dev` on 2025-09-23. Its TSA leaf had also expired 2024-04-13.
**Consequence, not hypothetical:** any Cosign keyless signature recorded on the Rekor v2 shard fails against this snapshot with `ErrTlogInvalid` and sigstore-go's text `not enough verified log entries from transparency log: 0 < 1` — **indistinguishable from a forged signature**. So a genuine signature read as an attack. This is precisely the "silent breakage on a security control is the worst failure mode available" case the roadmap names for item 2c, and it was already live rather than approaching.
**Why it mattered right now:** keyless verification was itself non-functional until `efc1743` (it derived its expected identity from the certificate under inspection). This trust root had therefore never been load-bearing — and was about to become so.
**Severity:** High. A verification path that reports "forgery" when it means "my anchor list is out of date" trains operators to distrust the tool or to bypass the check.
**Fixed:** hybrid. The snapshot is regenerated from the raw, TUF-signature-verified `trusted_root.json` target (reproducible: two independent fetches produced identical bytes), an opt-in TUF client can refresh it live, and three guards stop it rotting again — an always-on age/expiry test that *fails*, a network divergence test against the live repository, and a digest tripwire against the new provenance sidecar. The refresh path is the same test run with an env var, on Go's golden-file convention, so detection and repair cannot drift apart. Verification itself remains fully offline; the only network function refuses before constructing a client when hermetic, wrapping `core.ErrHermeticViolation` like `bunruntime` does.
**Also fixed, and the part that matters most operationally:** an unknown Rekor log ID still fails closed with the same verdict, but the error now says it is most likely a trust-root coverage gap rather than a bad signature, and names the covered logs. The verdict was deliberately not relaxed — only the diagnosis was added, proven by reverting the check and watching the four message assertions fail while the verdict stayed identical.
**Note on the refresh instructions:** `internal/adapters/sigstore/README.md` currently says to refresh by copying `sigstore-go`'s `examples/trusted-root-public-good.json`. That file *is* the stale pre-2023 snapshot, so following the documented procedure would reinstall the bug. ✅ **Corrected** in `7f2a4cc`: the README now names the real TUF origin and refresh command, and keeps an explicit "**Do not do that**" warning against the old instruction so nobody reverts to it from memory.

### 11. Tests mutate a shared checked-in fixture, making results order- and history-dependent

**Observed:** during Wave C verification, `TestLiveFixture_PreflightAndCompile` (`internal/adapters/bunexec`) failed once in a full-suite run, then passed in isolation and 3/3 on re-run with a cold cache, and the full suite has been green since.
**What:** several tests run real builds against the checked-in fixtures under `testdata/fixtures/` **in place**, so they leave build output (`build/`, `.svelte-kit/`, `.pokkum/`, and a written `pokkum.lock`) behind. A later test — or a later run — then starts from whatever the previous one left. Two independent agents hit a version of this tonight: the integration mock collided with its own previous run's already-flattened prerendered output (fixed by resetting the output dir, mirroring adapter-static's `builder.rimraf`), and the runtime smoke test copies the fixture into `t.TempDir()` specifically because a real `BaseImageResolver` writes `pokkum.lock` into `ProjectDir`.
**Severity:** Low as a defect, Medium as a fragility. Nothing is wrong with the production code; the risk is a test suite whose result depends on prior runs, which is exactly the kind of flake that gets re-run until green and then ignored. It also means a developer's local pass is not strong evidence for a clean CI checkout, and vice versa.
**Status:** not fixed — deliberately. The honest fix is to make every real-build test copy the fixture into `t.TempDir()` first (the pattern the smoke test already establishes) rather than patching individual collisions, and that is a broader test-hygiene change than belongs at the end of a long queue. Logged with the reproduction detail so it is not mistaken for a one-off flake.
**Note:** I could not reproduce the failure after the fact, so this entry records an observation and a mechanism, not a confirmed root cause. Stated that way on purpose.

### 12. `pokkum-static` emits `ETag` but never honours `If-None-Match` — no 304 responses

**Found by:** rewriting `paranoid-testing-guide.md`, whose §22 told the reader to verify a 304. Executing that step live showed a 200 every time.
**Where:** `supervisor/cmd/pokkum-static/server.go`. Confirmed by grep: no `If-None-Match`, `IfNoneMatch`, `StatusNotModified` or `304` anywhere in the package's non-test code.
**What:** the server computes and sends a strong content-hash `ETag`, and uses it for `If-Range` validation, but has no conditional-GET path. So a client that already holds the current copy re-downloads the whole body on every request.
**Severity:** Medium. Not a correctness or security fault — responses are valid, just wasteful — but this is a static file server whose whole job is serving cacheable assets, and it advertises the validator it then ignores. On prerendered HTML (deliberately `no-cache`, so revalidated every time) this is precisely the case 304 exists for.
**Status:** fix dispatched.

### 13. `--base`'s help promises a custom reference that no code path accepts

**Found by:** verifying §10's commands against the real CLI.
**What:** `--base`'s help text offers a custom image reference, but attempting one does not reach the build. Worth noting the *preset* path is fine and the `custom` preset itself works — it is the flag's documented free-form-ref affordance that dead-ends.
**Severity:** Medium as a UX/docs mismatch; the flag advertises a capability the code does not provide. Not a security issue.
**Status:** logged and recorded in the guide as a known gap so a reader does not file a bug against it. Not fixed — it needs a decision (wire the ref through, or narrow the help text to presets only) rather than a reflex patch.

### 14. `cosign verify-attestation` rejects Pokkum's DSSE attestation in tag-fallback mode

**Found by:** running the guide's §8 against a real registry with cosign v3.1.3.
**What:** cosign reports `no matching attestations: ... missing "dev.cosignproject.cosign/signature" annotation`. Pokkum's own `pokkum verify` reads the same attestation correctly. So this is an **interop gap in the tag-fallback layout**, not a broken signature — but a reader following the guide would reasonably conclude the attestation was bad.
**Severity:** Medium. It undercuts the independent-verification story that Tier 2d's dual-publish work exists to support: the whole point is that `cosign` and a Kyverno-style checker should both agree.
**Status:** ✅ **Fixed.** Diagnosed from cosign v3's own source rather than the error text: `VerifyImageAttestation` calls `static.Copy(att)` *before* the DSSE check, which unconditionally calls `Base64Signature()`, which errors on a **missing annotation key** — not on an empty value. Cosign's own attest path (`pkg/oci/static/signature.go`) always writes that key with an **empty string** for attestations, since the value is meaningless there (its own comment: `no-op for attestations`). `attestationImage()` omitted the annotation entirely on the assumption it was signature-only, while `signatureImage()` set it correctly — so the two paths had quietly diverged. Fixed by attaching the layer with `dev.cosignproject.cosign/signature: ""`, matching cosign's convention exactly. The media type already matched.
**Proven with the real tool, not a unit test:** against a local registry with a real ECDSA key and a real DSSE envelope, `cosign verify-attestation` reproduced the exact error before and succeeds after — for **both** the manifest digest and the index digest, so dual-publish is covered. The plain-signature path was confirmed unaffected, and Pokkum's own `FetchAttestation`/`FetchSignature` read path unregressed.
**Lesson worth recording:** an interop assumption about a third-party tool's wire format was encoded in a code comment and never checked against that tool's actual source. Existing checklist row 16 covers verifying a feature's *own* claims; this is the mirror — verifying an external consumer's contract. Flagged for a new row.

### 15. The guide's own `--output=json` assumption was wrong, and that is by design

**Found by:** the same live-execution pass. `pokkum build`'s stdout is unconditionally the plain `repo@sha256:...` ref, so `build --output=json` produces no JSON envelope and the guide's `jq -r '.data.digest'` extraction — reused in eight places — could never have worked.
**Not a product bug.** `internal/core/pipeline.go`'s Stage 11 is explicit that this is deliberate: "the one line of program output. Callers pipe this straight into `kubectl set image` or a manifest rewrite, so nothing else may ever share the stream." Also `--print-manifest`'s JSON has no `.data` wrapper — the envelope is per-command, not universal.
**Severity:** none for the product; High for the guide, since every digest-dependent step downstream was broken.
**Status:** ✅ fixed in the guide at the root and in all eight downstream uses. Recorded here because it is a good example of a doc that was internally consistent and entirely wrong — exactly what the guide's own "believe nothing" premise exists to catch, applied to itself.

### 16. Every custom base image shared one `pokkum.lock` slot, and could return the wrong image

**Found by:** implementing finding 13 (`--base` accepting a custom reference), while checking the lockfile-keying consequence rather than only the parse gap.
**Where:** `internal/adapters/baseimage/resolver.go` — `lockKey = string(req.Preset)`.
**What:** the lock key is the preset string, so every custom reference shared the literal `"custom"` slot. The consequence was worse than eviction: resolving custom base **B** after custom base **A** had been locked would trust A's entry and **silently return A's image content for a B request**. You ask for one base and get another, with no error.
**Severity:** High for anyone using more than one custom base in a project — a wrong-base build is a supply-chain correctness failure, not a cache miss. It was latent until now only because the CLI had no way to supply a custom reference at all (finding 13), so the collision was unreachable in practice.
**Reproduced:** two genuinely different images in a real in-memory registry; B resolved to A's digest before the fix, confirmed by stashing only the fix.
**Fixed, narrowly:** a `"custom"`-keyed entry is now only trusted when its recorded ref matches the request (and its digest matches, for scan-metadata carryover). No lockfile schema change, so no migration.
**Recommended proper fix, not done:** give custom refs their own slot — e.g. `"custom:" + sha256(ref)[:12]` — mirroring how `distroless-node` was made its own preset for exactly this reason (`f5229c3`), and what Roadmap Tier 2 already notes for `chainguard-static`. That changes lock keying and needs a migration story, so it is recorded rather than rushed.
**Related doc correction:** `Vocabulary.md` already claimed `--base` accepted "a custom image reference". That was false when written and is true now — an overclaim that closed itself by accident.

_(appended as work proceeds)_
