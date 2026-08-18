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
**Status:** fix dispatched, with a test that binds a real listener rather than an `httptest` handler.

### 3. `Preflight` is not strategy-aware and rejects every real `adapter-static` project

**Where:** `internal/adapters/bunexec/compiler.go`'s `Preflight`; `ports.PreflightRequest` has no `Strategy` field.
**What:** `Preflight` hard-codes a requirement for `@jesterkit/exe-sveltekit` or `@sveltejs/adapter-node`. A correctly-configured `adapter-static`-only project is rejected before `Prepare`'s own adapter check — which _is_ strategy-aware — ever runs. So the static strategy cannot build a real static project at all.
**Severity:** High. Same class as `Lessons.md`'s earlier entry where a well-tested fix to `Prepare`'s adapter detection left every project unbuildable because `Preflight` made an independent, untested assumption about the same input (checklist row 13: grep the fixed function's callers).
**Status:** fix dispatched.

### 4. Real prerendered output nests under `pages/`; production assumes it is flat

**Where:** real `.svelte-kit/output/prerendered/` from `@sveltejs/adapter-static`; consumed via `internal/adapters/bunexec/compiler.go` (~line 436) and served from `/app/prerendered`.
**What:** a real build emits `prerendered/pages/index.html` and `prerendered/pages/about.html` — there is no top-level `prerendered/index.html`. The existence check therefore passes (the directory is there) while every prerendered route 404s at runtime.
**Compounding:** `staticFixtureCompiler` in `tests/integration/static_e2e_test.go` fabricates a _flat_ `prerendered/index.html`, matching the production assumption rather than reality. So the synthetic fixture has been validating a fiction, and `TestFixtureDrivenE2E_Static` passed throughout — exactly checklist row 12's failure mode, and the same shape as the missing-layered-entrypoint bug in `Lessons.md`.
**Severity:** High, and the most instructive of the three: a mock encoding the same wrong assumption as the code it tests cannot ever detect the mismatch.
**Status:** fix dispatched, including correcting the synthetic fixture so it stops agreeing with the bug.

### 5. Single-port mode silently has no probe endpoints

**Found by:** the F1 bind-address fix, while confirming neighbouring logic.
**Where:** `supervisor/cmd/pokkum-static/main.go`'s `if cfg.Port != cfg.ProbePort` guard.
**What:** the guard reads as "skip the redundant second listener because the content server covers probes in single-port mode" — but there is no mux merge anywhere in the package. When `PORT == POKKUM_PROBE_PORT`, the probe listener is skipped and `/healthz` and `/readyz` are served by _nothing_. A single-port deployment therefore has no working probes at all.
**Severity:** Medium. Pre-existing and independent of the bind bug; not introduced or touched by that fix. Only bites operators who deliberately collapse the two ports, which is why it has gone unnoticed.
**Status:** logged, not fixed — outside F1's stated scope, and it needs a deliberate decision (merge the probe handlers into the content mux vs. reject the collapsed configuration vs. document it) rather than a reflex patch. Tracked for the roadmap.
**Decision by André:** Reject the collapsed configuration.

### 6. Embedded PID-1 binaries are checked-in build artifacts, so a source fix does not reach images

**Found by:** the static smoke test still failing *after* the bind fix (finding 2) was committed.
**What:** `internal/adapters/staticserver/bin/pokkum-static-linux-{amd64,arm64}.zst` and the `pokkum-init` equivalents are prebuilt, zstd-compressed, checked-in blobs consumed via `go:embed`. Fixing `supervisor/cmd/pokkum-static/*.go` therefore changes nothing about produced images until `make static-server` regenerates the blob. The blobs on disk were dated Aug 16, predating the fix.
**How it was spotted:** the container log read `msg="probe server failed" addr=:8081 error="listen tcp :80: bind: address already in use"` — an impossible pair for the fixed code, since it sets `Addr` before logging it. The old code logged the *intended* port while binding the default, which is also why the original bug never showed up in logs.
**Severity:** Medium, process rather than code. Nothing enforces regeneration: no test compares the embedded blob against a fresh build of its source, and `make verify` does not rebuild it. Any future supervisor fix can be committed, reviewed, and merged while every produced image keeps the old behaviour.
**Status:** blobs regenerated. Worth a guard — a test or CI step asserting the embedded blob matches a fresh build of its source — tracked for the roadmap rather than bolted on here.

### 7. `pokkum-static` never tries `<path>.html`, so every non-root prerendered route 404s

**Found by:** the static smoke test, after findings 2, 3, 4 and 6 were resolved — `/` and `/robots.txt` served correctly while `/about` 404'd.
**Where:** `supervisor/cmd/pokkum-static/server.go`'s `tryServe`.
**What:** candidate resolution handles exactly two cases — an exact file at the request path, or a directory containing `index.html`. There is no extensionless fallback. `@sveltejs/adapter-static` with its default `trailingSlash: 'never'` prerenders route `/about` to `about.html`, so a request for `/about` finds neither a file named `about` nor a directory, and 404s. `/` worked only incidentally, via the directory→`index.html` branch.
**Severity:** High. Every prerendered route except the root is unreachable at its canonical URL in every `--strategy=static` image. The server even logs a misleading hint suggesting the operator configure an SPA fallback, when the file is present and simply not being looked for.
**Why it survived:** the synthetic fixture only ever fabricated a root `index.html` (finding 4), so no test exercised a non-root prerendered route.
**Status:** fix dispatched, with the traversal guards preserved and the embedded blob regenerated afterwards.

_(appended as work proceeds)_
