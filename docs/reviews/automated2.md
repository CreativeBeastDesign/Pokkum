# Field re-test — Pokkum against a real SvelteKit app (round 2)

**Date:** 2026-08-21
**Binary:** `v1.0.1-228-ga836c67-dirty`, built with `make build` from `main` @ `a836c67`
**Subject project:** Cheetah — SvelteKit 2.68.0, `svelte-adapter-bun` (Pokkum injects adapter-node), no prerendering, 14 routes
**Environment:** darwin/arm64 · node 26.7.0 · bun 1.3.14 · docker 29.7.2 · cosign v3.1.3

**Method.** Every build ran against a `cp -a` copy of the project in a scratch directory, never the user's tree
(`CLAUDE.md` §2, zero-mutation). The subject repo finished the session with `git status` reporting 0 entries and
no `.pokkum/` or `pokkum.lock`. Registry-backed checks used a throwaway local registry on `localhost:5099`.

**Scope.** This round re-verified the 10 findings reported as fixed from the previous field report, plus 2 that had
never been tested. It was not a re-run of `paranoid-testing-guide.md`.

---

## Headline: every layered image built from `main` refused to start

Found while setting up F1, before any of the intended tests could run.

```
docker run --network none pokkum.local/cheetah:rt-bun
level=ERROR msg="startup attestation failed" error="startup attestation mismatch:
  /app runtime tree does not match the build-time manifest
  (expected e21ac357…, got 9916a872…); refusing to start."     → exit 125
```

The previous session's image boots, which localises it exactly:

| image | pokkum version | attested files | boots |
| --- | --- | --- | --- |
| `cheetah:latest` (prior session) | `v1.0.1-173-g9ce740a` | 509 | yes |
| `cheetah:rt-bun` (current `main`) | `v1.0.1-228-ga836c67` | 11762 | **no — exit 125** |

509 → 11762 is exactly the `node_modules` tree the F1 fix added. Recomputing both digests from the exported
container rootfs reproduces both values byte-for-byte:

```
files under attestRoots (server/client/prerendered/vendor/native) : 509
files under app/node_modules                                      : 11253
digest(roots only)      = 9916a872…   ← supervisor's "got"
digest(roots+node_mods) = e21ac357…   ← build's stamped "expected"
```

**Cause.** `packager.go` folds the node_modules layer's records into `attestRecords`, but `/app/node_modules` is
not in the runtime walk set — `supervisor/cmd/pokkum-init/attest.go`'s `attestRoots` lists only
server/client/prerendered/vendor/native. The build hashes 11762 files; the runtime can only ever find 509.

Reproduces on bun **and** node runtimes, `--local` **and** registry push. See finding **N1**.

---

## F1 — production dependencies ship at `/app/node_modules`

**Verdict: holds.** One amendment to the acceptance criterion.

Decisive test — app on `--network none`, probe container joined into its network namespace (loopback only, zero egress):

```
wget https://registry.npmjs.org/valibot   →  wget: bad address 'registry.npmjs.org'
GET /login                                →  HTTP/1.1 200 OK    Content-Length: 5872
```

`valibot` is genuinely externalised, not bundled: **18 bare `from "valibot"` imports** in the emitted server JS.
All 7 real external specifiers (`valibot`, `@standard-schema/spec`, `bcryptjs`,
`@simplewebauthn/{server,server/helpers,browser}`, `@sveltejs/kit`) resolve under `/app/node_modules`.
Entrypoint confirms `bun --no-install`.

> **Amendment — the acceptance criterion as written will fail.**
> `/home/nonroot/.bun/install/cache` is **not** empty after serving. `docker diff` shows 9 files under
> `cache/@t@/*.pile`. Extracting one shows transpiled Svelte runtime source: this is Bun's **transpiler** cache,
> written locally with no network reachable — not downloaded packages. Zero npm tarballs. The underlying concern
> (packages fetched from npm mid-request) is fixed. Restate the criterion as *"no npm tarballs / no package
> fetches"* rather than *"nothing appears in the cache directory"*.

> **Caveat.** These results required overriding the startup attestation (`POKKUM_ATTESTATION_DIGEST=disabled`),
> because of N1. F1's runtime behaviour is unverifiable as shipped.

---

## F2 — client assets and precompressed sidecars

**Verdict: holds.**

Not just status codes — the served bytes are the real asset:

```
/_app/immutable/chunks/D3t4FiY-.js
  sha256 in image = 0358594086487138e86c244121ae7c7465790a0449944ad9b1e8c3cdb1e1eaec
  sha256 served   = 0358594086487138e86c244121ae7c7465790a0449944ad9b1e8c3cdb1e1eaec   MATCH
  Cache-Control: public,max-age=31536000,immutable
```

| request | result |
| --- | --- |
| hashed asset, no `Accept-Encoding` | 200, identity |
| `Accept-Encoding: br` | 200, `Content-Encoding: br` |
| `Accept-Encoding: gzip` | 200, `Content-Encoding: gzip` |
| direct `.js.br` sidecar | 200 |
| nonexistent hash | 404 |

The 404 matters: it proves the 200s are real lookups, not a catch-all. Sidecars are 1:1 — 84 js / 84 `.br` / 84 `.gz`,
15 css each. Same caveat as F1 (attestation override).

---

## F3 — `--runtime=node --strategy=layered` (never previously verified)

**Verdict: holds.**

```
Entrypoint: ["/pokkum/init","--","/nodejs/bin/node","/app/server/index.js"]
GET /login                                  → 200, 5871 bytes   (offline)
GET /_app/immutable/entry/app.BrzMK9yg.js   → 200, 7189 bytes
  same asset with Accept-Encoding: br       → 200, Content-Encoding: br
```

The route that previously 500'd now serves. No bun binary present in the image. Same caveat as F1.

---

## F4 — `repro doctor`

**Verdict: holds, both directions.**

```
--perturb   → "--perturb is not implemented: … Refusing rather than reporting a pass
               it did not earn. To compare real rebuilds today, use `pokkum verify`"   exit 1
dirty tree  → [! WARN] Clean Git Repository: Uncommitted working tree modifications detected
clean tree  → [✓ PASS] Clean Git Repository: No dirty uncommitted working tree
                       modifications detected (ignoring Pokkum's own .pokkum/ and pokkum.lock)
```

Both checks were exercised in both states, so neither is hardcoded. The Vite non-determinism noted upstream was
not re-reported.

---

## F5 — `dev.pokkum.bun.version`

**Verdict: holds.** Static clause untested (see omissions).

The default pinned bun *is* 1.2.2, so label-equals-binary proves nothing on its own. Overriding separates them:

| build | label | binary in image |
| --- | --- | --- |
| default | 1.2.2 | 1.2.2 |
| `--bun-version 1.2.5` | 1.2.5 | 1.2.5 |

Absent on `--runtime=node`, which also carries no bun binary at all (`dev.pokkum.runtime: node` present instead).

---

## F6 — SBOM resolved versions

**Verdict: holds** (380/380 resolved, 0 ranges). Two qualifications.

Coverage is real, not achieved by dropping what could not be resolved: **0 packages shipped in `/app/node_modules`
are absent from the SBOM.** 380 ≈ 391 lockfile keys − 13 nested duplicates + `bun` + the image itself.

> **Caveat (known, tracked — not a new finding).** For duplicate package names the SBOM reports one version, and it
> picked the nested one. The image ships `d3-array` **3.2.4** at top level and **2.12.1** nested under `d3-sankey`;
> the SBOM lists only `d3-array = 2.12.1`. Same for `d3-shape` (3.2.0 / 1.3.7 → SBOM 1.3.7). This is the tracked
> non-deterministic duplicate-name resolution item, recorded here only because it bounds how far F6's verdict
> extends.

> **Untested.** The "unresolvable package is visibly marked" clause. No package in this project is unresolvable
> (380/380 resolved), so 0 SPDX comments is not evidence either way.

---

## F7 — `pokkum history` annotation set

**Verdict: partial — fixed in JSON only.**

`--output json` carries everything: all 7 annotations, identical to registry truth, plus
`"annotations_source": "manifest"`.

The **default text output is unchanged** — 5 fields, no source:

```
$ pokkum history <ref>
Git Source:    https://github.com/…/Cheetah.git @ bc5c6ab05962…
Built:         2026-08-19T15:05:42Z
Version:       bc5c6ab
```

The finding's own repro command (`pokkum history "$(pokkum build .)"`) shows the old view. See **N2**.

---

## F8 — SLSA provenance dirty detection

**Verdict: holds, both directions.**

```
untracked .pokkum/ + pokkum.lock only        → gitCommit = bc5c6ab05962…          (clean)
+ untracked src/lib/f8-untracked-source.ts   → gitCommit = bc5c6ab05962…-dirty
```

> **Caveat.** The OCI labels on the *same image* disagree with the provenance for the untracked case —
> `pokkum history` reports a clean revision for a build whose provenance says `-dirty`. See **N3**.

---

## F9 — secret guard

**Verdict: holds, 8/8.**

Re-tested with independently constructed, length-asserted fixtures (not the repo's own test values), all using
innocuous names `const a` … `const h`:

| format | caught | format | caught |
| --- | --- | --- | --- |
| AWS Access Key ID | ✅ | GitLab PAT | ✅ |
| GitHub PAT (`ghp_`) | ✅ | JWT | ✅ |
| Slack bot token | ✅ | Google API key | ✅ |
| Stripe live secret | ✅ | RSA private key | ✅ |

Each finding reports file, line and column; values redacted by default; `--show-secret-values` reveals them; the
message names both remedies. Marker verified: adding `// pokkum:allow-secret` above the Stripe line took the count
8 → 7 with only Stripe removed.

**No false positives on minified output** — the post-build stage scanned 9.28 MB of build JS including a 3.2 MB
minified chunk and reported `secret guard ok`.

Unquoted `.env`/`.npmrc` assignments remain uncaught; known gap, not re-reported.

---

## F10 — `pokkum verify`, unsigned vs. failed verification

**Verdict: holds**, with a positive control.

```
never signed  → "has no Cosign signature or SLSA provenance attestation at all --
                 it was never signed. Sign it with `pokkum build --sign`"                  exit 2
wrong key     → "carries a Cosign signature and an SLSA provenance attestation that did
                 NOT verify -- the signature/attestation is present but invalid (wrong
                 key, wrong --keyless-identity/--keyless-issuer, or tampering), not an
                 absence of signing"                                                       exit 2
correct key   → Verdict: ATTESTATION_VALIDATED                                             exit 0
```

The correct-key run is what makes the two failure messages meaningful — without it, both could be a blanket refusal.

---

## F11 — `pokkum scan --offline`

**Verdict: holds** for the reported symptom.

```json
"incomplete": true,
"warnings": ["project dependency OSV lookup skipped (--offline), coverage reduced:
              no dependency CVEs were checked"]
```

Text output carries a `⚠ SCAN INCOMPLETE` banner naming the reason. No longer a clean 0-CVE pass.

> **Caveat.** The same offline run also emits a **fabricated advisory** against a version the project does not
> have — so the output is simultaneously honest about coverage and wrong about findings. See **N4**.

---

## Guide errata — confirmed

```
cosign verify-attestation --type slsaprovenance   → Error: none of the attestations matched
                                                     the predicate type … found: https://slsa.dev/provenance/v1
cosign verify-attestation --type slsaprovenance1  → The signatures were verified against the specified public key
in-toto statement _type                           → https://in-toto.io/Statement/v1
```

Both errata items are accurate as written.

---

## New findings

| # | Severity | Finding | Evidence |
| --- | --- | --- | --- |
| **N1** | **Blocker** | Every layered image refuses to start. The packager folds `/app/node_modules` records into the attestation manifest; `pokkum-init`'s walk set omits `/app/node_modules`, so build and runtime can never agree. Reproduces on bun and node, `--local` and registry push. | `exit 125`; `digest(roots+node_modules)` == stamped `expected`, `digest(roots only)` == runtime `got`, both reproduced byte-for-byte from the exported rootfs |
| **N1a** | High | Root cause behind N1: the root set exists as **three** hardcoded copies plus one implicit fourth. `ports.AttestationRoots` — the constant documented as the set "both the packager and pokkum-init iterate" — **is referenced by no production code at all**; the packager derives records from its append sites, the supervisor keeps a hand-copy, and the parity test keeps a third inline copy whose doc comment claims it uses `AttestationRoots`. | `grep AttestationRoots` returns only its declaration and a comment |
| **N1b** | High | The designated tripwire cannot detect this class of drift. `TestParity_…AgreeOnSameTree` compares the two digest *functions* on synthetic trees, not the two *root sets*; `recomputeLayeredDigest`'s oracle table has no `AppNodeModulesDir` row and the test never sets it. Whole suite green (49/49 packages) while every produced image is unbootable. | `go test ./... exit = 0`, `packages ok: 49`, `FAIL: 0` |
| **N2** | Medium | F7 landed in `--output json` only; the default text output still prints 5 fields with no `annotations_source`, so the finding's own repro command shows the pre-fix view. | see F7 |
| **N3** | Medium | OCI labels and SLSA provenance disagree about the same build. Labels come from `git describe --tags --always --dirty`, which ignores **untracked** files; provenance uses `slsa.WorkingTreeDirty`, which does not. `gitdiscovery.go`'s own comment asserts the version label "reports it too" — true only for tracked edits. | untracked-only: `describe` → `bc5c6ab`, provenance → `…-dirty`; after touching a tracked file: `describe` → `bc5c6ab-dirty` |
| **N4** | Medium | `scan` fabricates an advisory. When no toolchain advisory is found, `adapter.go` appends `checkEmbeddedAdvisories(…, "2.2.0")` — a hardcoded literal — so there is "always a fallback advisory". Its comment scopes this to *"no readable package.json"*, but the **condition is `len(toolchainAdvisories) == 0`**, which also fires when a real version resolved cleanly and correctly produced nothing. Offline, the project's actual 2.68.0 is reported as 2.2.0. | online → 3 real advisories at 2.68.0; offline → 1 synthetic at 2.2.0; installed = 2.68.0 |
| **N5** | Low | `--allow-incomplete` help reads *"default: fail closed on reduced coverage"*, but `scan --offline` returns `passed: true`, exit 0, and the flag changes nothing. Either the help or the behaviour is wrong. | `scan --offline` → 0; `scan --offline --allow-incomplete` → 0 |

---

## Omitted tests

| Area | Why omitted |
| --- | --- |
| `--strategy=static`, end to end | Deferred by the brief; independently confirmed impossible here — preflight refuses cleanly: *"--strategy=static requires @sveltejs/adapter-static … it cannot install the package itself"*. F5's static clause and the entire static surface remain unexercised outside this repo's fixtures. |
| F6's unresolvable-package marker | No unresolvable package exists in this project (380/380 resolved); 0 SPDX comments proves nothing. Needs a fixture with a git/tarball/workspace dependency. |
| Runtime behaviour with the startup attestation **enabled** | Structurally impossible — N1. Every F1/F2/F3 result assumes `POKKUM_ATTESTATION_DIGEST=disabled`. |
| Distinguishing a *skipped* from a *failed* CVE lookup (N5) | Would require severing host networking mid-run; could not isolate the two cases, so N5 is reported as unconfirmed rather than as a definite defect. |
| Kind / live Kubernetes, `--hermetic`, `--asset-overlay`, `pokkum adopt`/`explain`/`dev`, `--require-env`, `--mirror-registry` | Outside the brief's scope (handoff §0 rule 3 — test what was asked, leave the rest to the user). |
| `pokkum upgrade` | Known-open (release lacks `checksums.txt.sig`); skipped as instructed. |
| Keyless (Fulcio/Rekor) signing paths | Only static-key signing was exercised; a throwaway ECDSA P-256 key was used against a local registry. |

---

## Method notes

- Two false starts are recorded here because they affected intermediate conclusions: an import-scanning regex
  initially missed `from 'x'` (space + single quotes) and reported zero externalised imports; and a
  `${PIPESTATUS[0]}` under zsh expanded to empty, so an early "suite is green" claim rested on inference rather
  than a captured exit code. Both were corrected and re-run before the verdicts above.
- `pokkum explain` cannot read a daemon-local image (`--local`); it requires a registry reference.
- `--signing-key` requires a PKCS#8/SEC1 PEM. A `cosign generate-key-pair` key is rejected with
  *"unsupported signing key format"* — worth a line in the guide, as cosign is the natural first thing to reach for.

---

## Resolution (same session)

All seven findings were fixed after this report was written. Verification below is end-to-end against
the same real project, not unit tests alone.

| # | Fix | Proof |
| --- | --- | --- |
| N1 | `AppNodeModulesDirPrefix` added to `ports.AttestationRoots` and to `pokkum-init`'s mirror | A real image now logs `startup attestation verified … files=11762` and serves `/login` **with the control enabled and `--network none`** — on both bun and node runtimes |
| N1a | `walkSupervisor` now iterates `ports.AttestationRoots` instead of a third inline copy; `AttestationRoots` gained a doc contract naming the two tests that enforce it | `grep AttestationRoots` now returns real consumers |
| N1b | `TestAttestationRoots_MatchSupervisorMirror` parses `pokkum-init`'s `attestRoots` with `go/ast` and compares as a set; `TestAttestation_StampedDigestMatchesImageFilesystem` replays a built image's layers and walks `AttestationRoots` over the merged filesystem | Both proven capable of failing: removing the root from **either** side independently fails them, and the packager oracle now populates `AppNodeModulesDir` (it never did) |
| N2 | `history` text output prints the full annotation set and `annotations_source` | `pokkum history <ref>` → `Annotations: 7 (source: manifest)` plus all seven keys |
| N3 | The version label's dirty marker now comes from `slsa.WorkingTreeDirty`, the same source provenance uses | untracked source file → label `bc5c6ab-dirty`, provenance `…-dirty`; clean tree → both clean |
| N4 | Fallback advisory removed; a `toolchainResolved` flag distinguishes "checked, found nothing" from "could not check", the latter reported as reduced coverage | `scan --offline` on the same project → `(no toolchain advisories)`, `incomplete: true` |
| N5 | Confirmed the **help text** was wrong, not the behaviour — `--offline` is a deliberate fail-closed exemption. Help and `Vocabulary.md` now state it | — |

**Three tests were found to have been depending on the fabricated advisory** (N4) rather than on the
behaviour they name. The worst declared `@sveltejs/kit 2.15.0` — *newer* than the advisory's fixed
version, so not vulnerable — and still asserted the scan must fail; it would have kept passing had the
threshold logic itself broken. All three now use fixtures declaring a genuinely vulnerable version.

Full suite: `go test ./... exit = 0`, 49 packages ok. That green now carries weight it did not before,
since the new guards were each shown to fail with their fix reverted.

Post-mortems are logged in `Lessons.md` (two 2026-08-21 entries); `mem:self_review_checklist` gained
rows 51 and 52, and row 17 was extended — it existed and did not catch N1.

> **One caveat on my own method.** A `--runtime=node` build failed once mid-session with
> `archive/tar: write too long` on a `.gz` sidecar. That was my error, not Pokkum's: I ran two builds
> concurrently against the same project directory, so precompressed sidecars were rewritten while the
> client layer's tar was being read. Serialised, the build succeeds. Concurrent builds sharing one
> project directory are not a supported configuration and this is **not** reported as a finding.
