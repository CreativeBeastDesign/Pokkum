# Pokkum — Full Code Review

_Multi-agent review, 2026-08-18. 3 mapping agents + 8 domain reviewers (Haiku/Sonnet/Opus) + an adversarial Opus verification pass + a deep supply-chain crypto audit. Every finding below was traced to specific code lines and checked against `Roadmap.md`, `Roadmap-v1-Archive.md`, `Lessons.md`, and `fixes-to-v1.md` so that only genuinely un-flagged issues are reported as new. Severities are the adversarial pass's calibrated ones, not the intake claims._

---

## TL;DR

The engineering is genuinely strong: clean hexagonal boundaries, deep determinism/reproducibility discipline, a solid PID-1 runtime, no TODO/stub markers in production, and a runtime attack surface (path traversal, Range parser, signal/reap) that survived a dedicated bypass attempt. **But there is one Critical, un-flagged finding that blocks publication: the product signs nothing it builds.** `--sign` defaults on, the signers are validated as required — and then never called. Every pushed image is unsigned while the README carries a SLSA-3 badge and "signed provenance, on by default." Two consequences fall straight out of it: `pokkum verify` fails on Pokkum's own images, and the remote build cache (verify-on-by-default) can never hit. This is the same "shipped feature is a stub" class the Pre-Publication Gate already caught once (`Lessons.md`), recurring.

After that, a credible Medium tier (secret scan misses the shipped bundle, escrow-mirror digest-pin bypass, a live goroutine orphan, `dev --watch` dies after one rebuild, unbounded reads) and a set of real optimization and test gaps. Nothing else is Critical.

---

## 1. Weaknesses not already flagged in the roadmaps

### 🔴 Critical

**C1 — Output image signing is entirely unwired. Images are pushed unsigned despite `--sign=true` and SLSA-3 claims.**
`--sign` defaults true (`cmd/pokkum/build.go:204`, resolved `:682`). The pipeline _requires_ non-nil `CosignSigner` + `DSSESigner` when signing (`internal/core/pipeline.go:209,212`) and wires real ones (`build.go:445-446`). But the signing block (`pipeline.go:1028-1081`) only calls `SLSAGenerator.Generate(...)` and then **logs** the statement — no DSSE wrap, no signature, no attach, no push. `CosignSigner.Sign`/`DSSESigner.Sign` have **zero callers** in `internal/core` (verified by grep; only the ed25519/ecdsa primitives inside the adapters call `.Sign`). The `Registry` port exposes only `Push` + `AttachSBOM` (`internal/ports/registry.go:189-198`) — there is **no signature/attestation attach method anywhere in the codebase**. There is also no way to supply a signing key: `POKKUM_SIGNING_KEY` has zero code references; `cosign.Signer.Sign` hard-requires `req.KeyPEM`, which nothing populates.

_Consequences (each verified):_

- `cosign verify <img>` and `cosign verify-attestation` fail on every Pokkum image (no `.sig`/`.att` tag exists).
- **`pokkum verify --no-rebuild` exits 2 on Pokkum's own output** (`verify.go:87-96`): both `HasProvenance` and `SignatureValid` are structurally unsatisfiable.
- **The remote build cache is silently dead in the default config.** Cache-hit verification defaults on (`build.go:884`); `verifyCandidate` requires a `<repo>:<alg>-<hex>.sig` (`remotecacheutils.go:440`) that nothing ever creates, so every cache check is a guaranteed miss (`:449-458` → `:384-388`). A performance feature disabled by a security feature that verifies a producer that doesn't exist.
- Kyverno / Ratify / any Sigstore admission policy rejects every Pokkum image.

_Overclaim inventory:_ `README.md:6,9,36,85`, `Vocabulary.md:85`, `ARCHITECTURE.md:184-187`, `Roadmap-v1-Archive.md:42-43`, `Supply Chain Hardening v1.md:11`, `.serena/memories/core.md:33`, `paranoid-testing-guide.md:189-201` all assert signing happens. `Roadmap.md:79` (item 2d) _presupposes_ attachment exists. Un-flagged in every roadmap/Lessons file. The CLI _release binary_ is genuinely signed (goreleaser + cosign, verified in `upgrade.go`) — but none of that applies to user images.

> The single most valuable fix in the whole review: wire signing, then **read the signature back from the registry and verify it before reporting success**. A post-push self-verify stage would have made C1 unshippable.

### 🟠 Medium (credible, verified, un-flagged)

| ID                 | Finding                                                                                                                    | Evidence                                                                                                                                                                                        | Failure scenario                                                                                                                                                                                                                                            |
| ------------------ | -------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **M-secret**       | secretguard scans only pre-build **source**, never the built bundle that ships                                             | `pipeline.go:544` scans `req.ProjectDir` _before_ the SvelteKit build; packaging ships `prep.OutputDir` (`:1314-1348`), never re-scanned                                                        | A secret inlined by Vite `define`, `$env/static/private`, or a malicious build dep ships in the image, uncaught. The feature's own name implies the shipped artifact is checked.                                                                            |
| **M-mirror**       | Escrow mirror pulled by a **mutable tag**; served digest never checked against the locked `entry.Digest` → signed rollback | `baseimage/resolver.go:276-290` (`ref = entry.MirrorRef`, the `sha256-<hex>` tag), `:325` (`out.Digest = pull.digest`), `:1231` compares served-vs-served                                       | A compromised project-owned escrow mirror serves a _different but genuinely upstream-signed_ older/vulnerable image of the same repo; all checks pass. `fixes-to-v1.md:166,235` claims the locked digest is always re-verified — untrue on the mirror path. |
| **M-k8s**          | k8s resolver goroutine orphan: `AffectedDetector` error on a **non-first** path returns before `g.Wait()`                  | `k8s/resolver.go:556-559` vs `:584`; plain `errgroup.Group`, outer ctx not cancelled                                                                                                            | The exact bug class checklist rows 1/4 exist for, still live and not logged as fixed. Orphaned `req.Build` (full image builds) run to completion, results discarded; `os.Exit(1)` may kill one mid-push.                                                    |
| **M-dev**          | `pokkum dev --watch` dies after exactly one rebuild (stale buffered channel)                                               | `dev.go:184-233`: `cmdErrChan` (cap 1) reused across rebuild generations; killed old container's error is read next iteration, outer `ctx.Err()==nil`, loop logs "container exited" and returns | The flagship dev loop silently stops rebuilding after the first file save. No test covers `watchAndRunDevContainer` at all.                                                                                                                                 |
| **M-upgrade-read** | `pokkum upgrade` slurps the whole release archive via unbounded `io.ReadAll` **before** checksum verification              | `upgrade.go:92` → `:298`, verify only at `:314`; no `io.LimitReader` in the file                                                                                                                | Pre-auth memory exhaustion on a build host. Inconsistent with the codebase's own caps (bunruntime 512MB).                                                                                                                                                   |

Deeper crypto-audit items in the same tier (single-agent, high-confidence, worth confirming during the fix): **`pokkum verify`'s keyless path can never succeed** — it derives the expected identity from the cert's own `Issuer.CommonName` (`provenance/resolver.go:308-315`), which never matches the OIDC issuer, so it always fails; worse, a naive "fix" that reads the OIDC extension would make it accept _any_ Fulcio identity (the empty-identity guard at `sigstore/verifier.go:156` exists precisely to stop this). And **`cosign.Signer.Verify` rejects signatures from the real cosign CLI** (`signer.go:122` accepts only the `atomic container signature` type; the other two copies accept both) — which matters because hand-signing with cosign is the only workaround for C1 until it's fixed.

### 🟡 Low (real, un-flagged, not blockers)

- **Bun cache-hit trusts a self-written sidecar** — `verifiedCacheHit` (`bunruntime/resolver.go:723`) compares the binary against its own `.sha256` sidecar with no cross-check against the compiled-in pin table or GPG; a local attacker who can write the cache dir plants `bun` + sidecar and owns every subsequent build (recorded in SBOM/provenance as authentic). `mem:core` documents this as "fixed" — the stated property holds, but the security goal doesn't. Requires local write access; `os.TempDir()` fallback (`:137`) widens it when `$HOME`/`$XDG_CACHE_HOME` are unset (routine in `docker run -u 1000` / distroless / k8s Jobs).
- **`--sign` silently no-ops for `--output=local`/`--tarball`** with no warning, despite defaulting true (`pipeline.go:1028`). Contrast `--asset-overlay`, which warns.
- **`comparator.go:151`** indexes `remoteDiffIDs[i]`/`localDiffIDs[i]` unguarded in the `Uncompressed`-failed branch → `pokkum explain diff` panics on a degraded image.
- **Scanner offline CVE gate uses lexicographic version compare** — `isVersionOlderThan` (`scanner/adapter.go:574`) does string `<`, so `1.2.0 < 1.10.0` is false. Currently only produces false _positives_ (embedded FixedVersions are single-digit `1.1.0`/`2.3.0`); becomes a real gate bypass the moment any FixedVersion gains a two-digit segment. Latent.
- **`upgrade.go replaceBinary`** removes the target then retries an identical rename (`:411-416`) — harmless in the normal path (same-filesystem), but the pattern is wrong and loses mode/ownership (hardcoded `0755`).
- **Config can disable base-image verification silently** — `.pokkum.yaml`'s `security.verify_base: false` sets `NoVerifyBase` with only an info log, invisible in the command line a reviewer reads.
- **One hardcoded placeholder pubkey** is the last-resort trust anchor for three domains (base-image, cache, provenance verify); byte-identical across `cosign/signer.go:31` and `baseimage/resolver.go:718`. Fails closed today (no private half in the repo), but it's an undocumented trust assumption; `cosign/signer.go` carries no provenance note.
- **`embeddedbinaryutils`** is the only package with zero tests and holds the only production `panic` (`:27`) — though it's build-time (packager), not PID-1 runtime, and the panic is effectively dead (`zstd.NewReader(nil)` can't error with no options).
- **`--hermetic-mount-isolation` masks only `docker.sock`** — omits containerd/podman/CRI-O/BuildKit sockets, which netns does _not_ cover (filesystem-path Unix sockets stay reachable). Opt-in + a mounted socket required; the code comment already calls the allowlist non-exhaustive, so this is incremental hardening, not a new hole — but the specific sockets deserve enumeration.

### Verified SOUND (attempted and refuted — worth stating)

pokkum-static path-traversal chain (double-encode, null-byte, symlink, `%2f` all blocked); Range/`If-Range` parsing (no overflow/OOM, multi-range sidesteps amplification); `fileETagCache` growth (bounded by real files, 404 before cache); PID-1 signal/reap (no deadlock, correct SIGCHLD coalescing, `Pdeathsig`); in-image hardening (uid/gid 65532, mode 0555, no setuid/caps, `readOnlyRootFilesystem`-ready); `sigstore` keyless verifier (strict, empty-identity refused, tlog+SCT+timestamp required); `dsse` PAE (spec-correct); `upgrade` trust chain (checksum verified before extraction, offline release key); `bunruntime` zip handling (no zip-slip, 512MB cap, per-entry limit, truncation caught); cosign base64 `if err == nil` (fail-closed, backstopped). The recent `--asset-overlay` hardening (path traversal, cross-repo, attestation dedup, DoS caps) is present and consistent in current code.

---

## 2. Optimization potential

### Image size

| Item                                                                                          | Detail                                                                                                                                                                                                                                                | Impact                                                                                                   | Cost                                                                                                                                        |
| --------------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- |
| **Bun layer digest churns every commit** (elevate Roadmap 3f — it's a bug, not just untested) | Immutable-binary layers' tar `ModTime` + layercache key derive from `SOURCE_DATE_EPOCH`, which defaults to `git log -1 %ct` (`config.go:449`, `packager.go:252`, `layer.go:586,665`). Byte-identical ~90MB Bun binary → different digest every commit | Busts local layer cache **and** registry fleet dedup — the single biggest size lever, currently inverted | **Low** — pin immutable-binary layers to a constant ModTime keyed on (path, content-hash, platform, compression); still fully deterministic |
| Precompression ships gzip + brotli (+zstd) sidecars, default-on                               | `packager.go:295-300`; brotli is preferred by all modern clients, so the `.gz` sidecar is mostly dead weight (~+130-150% of client-bundle bytes)                                                                                                      | Moderate on client-heavy apps                                                                            | Low-med — add `--precompress=br-only`, or make gzip opt-in                                                                                  |
| No source-map strip/referrer outside vendor                                                   | server/client/prerendered pass `NoPrune:true` (`packager.go:264,301`); if a project emits `.map`, it ships                                                                                                                                            | Already tracked (Roadmap 3e)                                                                             | —                                                                                                                                           |

### Build speed

| Item                                                  | Detail                                                                                                                                      | Impact                                                         | Cost                                      |
| ----------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------- | ----------------------------------------- |
| Bun binary SHA-256'd twice for platform[0]            | `pipeline.go:882` pre-resolves for SBOM, then `fanOut` (`:1201`) re-`Resolve`s every platform incl. [0]; each hashes the whole ~90MB binary | Full-file hash pass, twice, every build                        | Low — thread `bunToolchain` into `fanOut` |
| No content-addressed vendor/server/client layer cache | `BuildDirectoryTreeLayerWithPruning` has zero caching; every build re-walks, re-strips, re-precompresses, re-compresses                     | Confirms Roadmap 5a is real; B1's fix (stable key) unblocks it | Med (tracked)                             |
| Serial ELF stripping                                  | `striputils` runs one `exec` per `.node`/`.so` sequentially                                                                                 | Moderate on native-heavy trees (sharp, better-sqlite3)         | Low — bounded worker pool                 |
| Serial asset-overlay generation pulls                 | `assetoverlay.BuildOverlayDir` pulls each prior generation one at a time                                                                    | Low-moderate, scales with `n`                                  | Low — bounded concurrent fetch            |

### Security / hardening (beyond fixing §1)

Post-push self-verify; attach attestations to **both** index and per-platform manifest (Roadmap 2d, blocked by C1); a `--require-signed` policy gate that fails the build if attach didn't happen; content-address trust anchors (key the Bun cache on the pin-table digest, not a writable sidecar; key trusted-root by content not path); one shared `checkSimpleSigningClaims` + payload-type policy (three divergent copies caused the cosign-type mismatch); a `golangci-lint` rule banning bare `io.ReadAll` on `remote`-derived readers (the codebase caps in 3 places, forgets in 3); drop `CAP_SYS_ADMIN` via `capset(2)` after masking (already tracked); escrow digest enforcement; `pokkum upgrade` rollback protection (semver ordering + `--allow-downgrade`).

---

## 3. Tests that would assure quality

Current strengths: strong determinism/golden/reproducibility tests, the hexagonal purity test, the build-vs-runtime attest parity test. Gaps below; full prioritized plan (P0/P1/P2) with file names available. Biggest systemic gaps: **no `-race` anywhere**, **no coverage measurement**, **no fuzzing**, **CI never installs Bun** (so all 4 real-build e2e tests silently skip — CI's "e2e" is entirely mock-compiler), **no signing→push→verify round-trip**, **no test ever runs a produced image**, **no CLI-flag↔docs consistency test**, and port mocks triplicated with no `var _ ports.X` conformance asserts.

**P0 — invariant/meta tests (architecture-test style):**

- `TestEveryPushPathAttachesSignature` — instrument the pipeline so `Push` after `Sign` is asserted when `req.Sign`; directly guards C1.
- `TestVerify_PokkumOwnSignedOutput` — build `--sign` → push to in-memory registry → `pokkum verify` must pass. (Would have caught C1 on day one.)
- `TestCLIFlagsCoveredByVocabulary` + `TestActionYMLFlagsExist` — AST-walk every `cmd.Flags()` vs docs and `action.yml` (catches the broken `--repo`/`--tag` in the Action).
- `TestAdaptersDoNotCrossImport` + `TestMockConformance` (`var _ ports.X = (*mockX)(nil)` in all 4 mock files).

**P0 — regression tests for the confirmed bugs:** signing (above), k8s non-first-path goroutine leak (with `goleak`), `isVersionOlderThan` multi-digit table, `dev --watch` multi-rebuild, secretguard-scans-shipped-output, git `--since` leading-dash rejection.

**P0 — fuzz targets** (stdlib `testing.F`): `extractImmutableAssets`/`safeJoinOverlayPath` (tar traversal), `cosign` Simple-Signing payload, `provenance` DSSE/SLSA parse, `.pokkumignore` glob, `parseSingleRange` (PID-1 Range header), `isVersionOlderThan` differential.

**P0 — CI/tooling:** add `-race`; add coverage with a "must not decrease" floor (~65% start, measure first); install Bun in CI; wire `scripts/check-build-flags.sh` into CI; a real-image runtime smoke test (`docker load` a tarball build, curl `/healthz`/`/readyz`).

**P0 — fixtures:** an `adapter-static` fixture and a monorepo fixture (both are first-class supported configs tested today only through synthetic mocks).

**Architecture-test extensions specifically:** the purity test today checks only `core→adapters` and `ports→*`. Add adapters→adapters cross-import bans, utils-never-import-core, and enum-switch exhaustiveness (every `switch` over `Strategy`/`SBOMFormat`/`BaseImageVerifyMode`/`OutputMode` has an erroring `default`).

---

## 4. Roadmap evaluation

### Per-tier verdict

**Tier 1 (SvelteKit DX moat) — keep all; this is the product.**

- `--asset-overlay` (done): genuinely differentiating, well-built.
- Cluster dev loop: 6a (no-container hot reload) is high-value and cheap; 6b (`--cluster` sync) is the real differentiator but highest complexity — sequence it after the correctness fixes. Note **M-dev**: the _existing_ `dev --watch` is broken today, so fix that before extending.
- **`--runtime=node` — the single highest-leverage adoption item on the whole roadmap.** It converts a dead end (a Bun-compat bug = abandon the tool) into a fallback and roughly doubles addressable users, at low architectural cost. Arguably promote above 6b.

**Tier 2 (supply-chain completions) — re-frame around C1.** These read as "polish an existing signing story." There is no signing story yet. Once C1 is fixed: 2b (CI OIDC identity) is what makes a _real_ vs _asserted_ SLSA L3 — near-required; 2c (TUF root refresh) is required to stop keyless verification silently breaking (the embedded root is the pre-2023 snapshot); 2d (multi-arch attestation subject) is blocked by C1. 2a (KMS) matters but its framing ("`POKKUM_SIGNING_KEY` in an env var is a smell") conceals that there is no key input at all. 2e-2g are nice.

**Tier 3 (registry/runtime ergonomics) — keep; mostly real friction.** Elevate **3f** — it's the Bun-layer bug (§2), not merely untested. 3a-3c (ECR auto-create, resumable upload, registry error surfacing) are exactly the "a third of real failures live here" items and cheap adoption wins. 3d (cgroup awareness for Bun/JSC) is a good real-world fix.

**Tier 4 (scope discipline) — agree with the instinct; act on it.** The reconsider-`metrics` call is right. The "adjacent" items (scan, NetworkPolicy, mirror) compete with Trivy/Grype/Kyverno/Renovate — keep them as thin, honest, integrate-first features, don't invest in matching those tools. The JSON-schema / provenance-in-`config view` / exit-code-table polish items are cheap and worth doing before public.

**Tier 5 (monorepo) — 5a confirmed real** (no content-addressed vendor cache exists). 5b/5c/5d are reasonable, lower urgency.

### Features missing from the roadmap

- Post-push **self-verification** of the signature Pokkum just wrote (would have prevented C1's class permanently).
- A **`--require-signed`** / policy-gate flag — any control whose absence is indistinguishable from success will eventually be absent.
- **Scan of the built output** for secrets (M-secret), not just source.
- **Escrow digest enforcement** (M-mirror) and **upgrade rollback protection**.
- **Fuzzing + `-race` + coverage** infrastructure (§3) — for a supply-chain tool these are table stakes, currently absent.
- Fixing the **GitHub Action** (`action.yml` passes non-existent `--repo`/`--tag`) — the advertised primary integration fails on first use.

### The security tiers: secure / waterproof / bombproof

A concrete ladder mapped onto Pokkum's actual state.

**Secure — what a supply-chain tool must do to meet normal expectations.** Reproducible builds ✅, base-image digest pinning + signature verification ✅, deterministic SBOM ✅, non-root/read-only image ✅, hermetic network isolation ✅, CVE gating ✅, secret scanning ⚠️ (source only), **signed provenance ❌ (C1)**. _Pokkum is one fix — C1 — away from genuinely "secure." Until then it does not meet its own headline claim._

**Waterproof — more than usually required.** Everything in Secure, plus: sign-then-**read-back-and-verify** before success; attestations on both index and per-platform manifest; identity-pinned `pokkum verify` (fix the keyless path + add `--keyless-identity`); escrow-mirror digest enforcement; **CAP_SYS_ADMIN dropped** after masking (tracked); entropy-based secret detection that also scans the built bundle; unbounded-read caps everywhere; Sigstore **TUF root refresh** (2c); CI-OIDC-attested identity for real SLSA L3 (2b). _Pokkum has the architecture for this; it's ~6-8 well-scoped items away._

**Bombproof — more than reasonable.** Everything in Waterproof, plus: signing keys in **KMS/HSM**, never in-process, with a policy/two-person gate; per-trust-domain **distinct** keys (not one shared anchor); **content-addressed** trust anchors throughout; an **independent rebuilder** cross-checking reproducibility as an external attestation; **fuzzed** parsers on every untrusted input (tar/OCI/SBOM/DSSE/glob/headers); runtime attestation covering the **supervisor itself**; signed **version manifests** so upgrade _selection_ is as protected as the artifact; a SLSA-L4-style two-party review gate. _This is a deliberate destination, not a v1 requirement — worth stating as the north star so the roadmap's ordering serves it._

### How far down the "SvelteKit part" is reasonable

The roadmap's own thesis ("the SvelteKit part is where users live") is correct, and the depth so far is well-judged: `$env/static` detection, the reverse-proxy/`ORIGIN` contract, `.pokkum/` relative-path correctness, SPA fallback, and asset-overlay are all things _only_ a SvelteKit-aware builder can do — that's the moat, and going deep there is right. The reasonable line is **build-time SvelteKit knowledge = go deep; Kubernetes-runtime concerns = integrate, don't reimplement.** `--runtime=node` is the highest-value next step down that path. The risk is not "too deep on SvelteKit" — it's breadth _elsewhere_ (Tier 4's metrics/scan/rollback competing with dedicated tools). The stated non-goals (edge/WASM, canary/blue-green, plugin system, asset optimization) are all correctly excluded — SvelteKit's own adapters or Argo/Flagger already own those, and reproducing them would dilute the moat.

---

## 5. Go-public gate

### Required before public (do not ship without these)

| #   | Item                                                                                                                                                                                       | Why it's a blocker                                                                                                                                                                           |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| R1  | **Fix C1** — actually sign + attach, _or_ strip every signing/SLSA-3/"signed provenance" claim from docs and default `--sign=false`                                                        | Shipping a supply-chain-security tool that signs nothing while displaying a SLSA-3 badge is a credibility and trust failure the moment anyone runs `cosign verify`. This is the whole pitch. |
| R2  | **Fix the GitHub Action** — `action.yml` passes non-existent `--repo`/`--tag` flags                                                                                                        | The advertised primary CI integration fails on first use.                                                                                                                                    |
| R3  | **Reconcile doc overclaims** — `POKKUM_SIGNING_KEY` (doesn't exist), entropy secret detection (doesn't exist), base image `ghcr.io` vs actual `cgr.dev`, "signed provenance on by default" | First-day users hit each of these; they read as dishonesty even where the underlying tool is good.                                                                                           |
| R4  | **Resolve the dead remote cache** — if cache-verify defaults on and nothing signs, the cache never hits                                                                                    | Either wire signing (R1) or change the default; otherwise the advertised sub-100ms cache is unreachable out of the box.                                                                      |
| R5  | **Fix or honestly document M-secret** — secretguard must scan the shipped bundle, or the docs must stop implying it does                                                                   | A secret-scanning feature that misses the artifact that ships is worse than none (false assurance).                                                                                          |

### Recommended before public

M-mirror (escrow digest enforcement) · M-k8s (goroutine orphan fix + non-first-path test) · M-dev (`dev --watch` fix — it's the flagship loop) · M-upgrade-read + the other unbounded `io.ReadAll` caps · git `--since` `--` terminator · scanner semver comparison · `pokkum verify` keyless path (can't succeed today) · cosign-CLI signature-type acceptance · `-race` + coverage floor + Bun-in-CI + real-image smoke test · signing round-trip + flag↔docs invariant tests · Bun-layer diffID stability (3f) · config-validate field-coverage · the Tier 4 cheap polish (exit-code table, JSON schema, `config view` provenance).

### Nice-to-have

Fuzz corpus · `adapter-static` + monorepo + arm64 fixtures · enum-exhaustiveness + adapters-cross-import architecture tests · the size/speed optimizations in §2 · macOS CI job.

---

_Prepared by a multi-agent review. The Critical finding (C1) and the k8s goroutine orphan were additionally verified by hand against source. The deeper crypto-audit items flagged "worth confirming" (keyless-verify-can-never-succeed, cosign-type mismatch, Bun-sidecar trust) came from a single deep pass and are high-confidence but not double-verified — treat them as strong leads to confirm during the fix, not as settled._
