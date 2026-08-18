# Pokkum Roadmap

**Status: v1.0 shipped, Pre-Publication Gate closed (2026-08-18).** Every blocker and overclaim risk two independent external reviews found was verified against the code and fixed — see [Roadmap-v1-Archive.md](Roadmap-v1-Archive.md) for that full history, root causes, and fixes in detail. This document starts from where that gate left off.

See [Vocabulary.md](Vocabulary.md) for the full CLI flag reference, naming conventions, and the rationale behind each flag below, and [AdditionalFeatures.md](AdditionalFeatures.md) for the underlying decision matrix (DX/Security/Cost scoring) this roadmap draws its priorities from.

## The thesis this roadmap is built around

Both external reviews that drove the v1.0 gate converged on one structural judgment, quoted verbatim because it's still the right frame:

> "You've built a supply-chain platform that happens to package SvelteKit, and the SvelteKit part is where the users actually live."

That was true when written. It's less true now — `$env/static/*` detection, the `ORIGIN`/proxy-header contract, and `.pokkum/` relative-path correctness (the three concrete SvelteKit-runtime gaps the reviews found) all shipped during the gate. But the underlying asymmetry the judgment describes hasn't fully closed: the OCI/supply-chain plumbing remains deep, tested, and expert-level; the features that would make a SvelteKit team *choose* Pokkum over a generic `ko`-style builder — instead of merely tolerating it — are still thin. This roadmap is ordered accordingly: **SvelteKit-specific DX first**, because that's the moat and the adoption lever; supply-chain completions and ergonomics second, because they're real but not differentiating; scope discipline throughout, because `AdditionalFeatures.md`'s own reviewers already flagged the surface-area risk of building everything a Kubernetes shop could conceivably want.

## Status flag legend

| Flag | Meaning |
| ---- | ------- |
| 🎯 **Moat** | A capability no competitor in this space solves, and one only Pokkum's specific architecture (layer control + build-time SvelteKit knowledge) can solve well. The adoption lever. |
| ⚙️ **Foundation** | Real, valuable, but not differentiating — closes a supply-chain or ergonomics gap a careful team would eventually ask about, not something that gets a team to switch tools. |
| 🧹 **Polish** | Small, cheap, and disproportionately annoying if left undone. |
| ⏸ **Deferred** | Considered and explicitly not prioritized, with the reason stated — not a silent omission. |
| 🚫 **Non-goal** | Will not be built, and why, stated plainly so it reads as a decision rather than a gap. |

---

## Tier 1 — SvelteKit DX Moat (Highest Priority)

The three items in this tier are the ones `AdditionalFeatures.md` scores "High (moat)" and are the direct answer to the thesis above. Nothing else on this roadmap should take priority over these while they're unstarted.

### 1.1 🎯 Rolling-Deploy Asset Overlay (`--asset-overlay`) — ✅ Shipped 2026-08-18

**Problem.** SvelteKit's client polls `/_app/version.json`. During a rolling update across N replicas, a browser holding v1's HTML requests `/_app/immutable/chunks/<hash>.js`, gets routed to a v2 pod, and gets a **404 → white screen**. `updated.check()` improves the UX but does not close the 404 window — it's worse with prerendered pages and long-lived tabs. Per the original review: "a bigger differentiator than anything in your security section," and given how dense that security surface already is, that judgment is worth taking seriously.

**Why Pokkum specifically can solve this.** It already controls layer composition. As built, lineage discovery does **not** read `pokkum.dev/image-history` as originally sketched below — that annotation is written and read exclusively by `internal/adapters/k8s` (`resolve`/`apply`/`rollback`), and `pokkum build` has no code path to parse a Kubernetes manifest, so a build-time flag structurally cannot depend on it without coupling `build` to Kubernetes. Instead, every image pushed with `--asset-overlay` set carries a new, Kubernetes-independent `pokkum.dev/predecessor` OCI manifest annotation naming the digest it replaced at the same push target; `--asset-overlay=<n>` walks that chain backward, entirely registry-side, up to N generations (self-bootstrapping — the first push to a target has no predecessor and degrades to "0 generations found," not an error). `--asset-overlay-from=<ref1>,<ref2>,...` is an explicit override for callers who want to hand-supply the chain instead. Each resolved generation's `/app/client` layer is pulled by digest, only content under `_app/immutable/` is extracted (the content-hashed, safe-forever build output — never non-hashed files like `index.html`), and non-conflicting files are merged into a **separate overlay layer** appended to the current build.

**Design — resolved:**
- The overlay's source digests **do** join the composite input hash (`ports.RemoteCacheInputRequest.AssetOverlaySourceDigests`, sorted before hashing) — a cache hit differing only in resolved predecessors now produces a different cache key, so it can never silently serve a stale overlay.
- Conflict policy: same hashed path, different bytes, across generations is a **hard build failure** (`core.ErrAssetOverlayConflict`), never a silent pick, exactly as scoped.
- Interaction with `pokkum verify --rebuild`: **still an open gap, not resolved.** `pokkum verify` has no `--asset-overlay`/`--asset-overlay-from` flags and its rebuild path does not reproduce the overlay layer — verifying an image that was built with `--asset-overlay` will currently report the overlay layer as a digest mismatch. Tracked in `AdditionalFeatures.md`; closing it means teaching `verify` to accept the same predecessor references (or read them back off the image's own `pokkum.dev/asset-overlay-sources` annotation) before rebuilding.
- The predecessor annotation is stamped **only** when `--asset-overlay` is actually in use on that push, not unconditionally — avoids taxing every ordinary push with an extra registry round-trip. Consequence, stated plainly: a later build's auto-discovery can only find a predecessor chain that itself opted in from the start of a rollout sequence.

**Flags/Interface:** `--asset-overlay=<n>` (default `0` = off, preserving current behavior) and `--asset-overlay-from=<ref1>,<ref2>,...` on `pokkum build`. Auto-discovery requires `--output=push` (there is no "current tag" to inspect for `--local`/`--tarball` output); `--asset-overlay-from`'s explicit refs work regardless of output mode.

**Implementation:** `internal/ports/assetoverlay.go` (port), `internal/adapters/assetoverlay/` (resolver + extraction/merge), wired into `internal/core/pipeline.go` Stage 4.4 and `internal/adapters/packager/packager.go`'s `appendAssetOverlayLayer` (its records are folded into the packager's attestation digest — omitting that would make every `--asset-overlay` image fail `pokkum-init`'s startup check). Empirically proven end-to-end in `tests/integration/asset_overlay_e2e_test.go` against a real two-generation build+push+extract cycle — see `Lessons.md`'s 2026-08-18 entry for a real path-handling bug this exact test caught before it shipped.

### 1.2 🎯 Sub-Second Cluster Dev Loop

**Problem.** `pokkum dev` goes through full image construction and a Docker/Podman daemon today. That's table stakes — it is not what makes anyone switch tools. Both external reviews independently flagged the dev loop as the weakest part of the day-to-day experience.

**Solution, in ascending order of value and descending order of urgency:**
- **6a (cheap, do first):** a no-container mode that skips image creation entirely and runs a hot-reloading Bun process locally, with an opt-in container-parity mode for when a real environment check matters. This is what most SvelteKit developers actually want on a normal day.
- **6b (the actual differentiator):** `pokkum dev --cluster` — watch → rebuild → sync `/app/server` + `/app/client` into a running pod via the Kubernetes API → restart the Bun process. No registry round-trip. This is the SvelteKit analog of hot-swapping a Go binary, and would put Pokkum on Skaffold/Tilt's own turf with a narrower, SvelteKit-specific implementation.
- **6c (adjacent, cheap):** `--to-oci-layout=<path>` output plus direct `kind`/`k3d`/`minikube` load, for contributors and CI environments with no daemon at all.

**Flags/Interface:** `--no-container` (or `--local-process`) on `dev`; `--cluster` + `--namespace`/`--selector` on `dev`; `--to-oci-layout=<path>` on `build`.

### 1.3 🎯 `--runtime=node`

**Problem.** Bun-only is the single largest cap on addressable users. Many teams cannot run Bun in production for policy reasons, Node-compat risk (`AsyncLocalStorage`, `worker_threads`, N-API gaps), or plain conservatism. Today, a user who hits a Bun compatibility bug has **no escape hatch** inside Pokkum — they abandon the tool entirely, not just the flag that broke.

**Solution.** `--runtime=node` targeting a distroless Node base. Architecturally cheap relative to its reach: the layered strategy already separates runtime/server/vendor/client, and `adapter-node` is already the layered strategy's target adapter — the runtime swap is substitution, not redesign. Roughly doubles the addressable audience and converts a dead end into a fallback.

**Flags/Interface:** `--runtime=bun|node` (default `bun`). Must join the composite input hash, `pokkum.lock` keying, and the toolchain CVE lookup — treat it exactly like a second dimension on everything `--bun-version` already touches, not a bolt-on.

**Note:** this is a *strategy* decision as much as a feature — see Tier 4's core-vs-adjacent scope discussion before starting; a second runtime is exactly the kind of surface-area growth that section is about.

---

## Tier 2 — Supply-Chain Completions (⚙️ Foundation)

Real gaps, individually small, collectively the difference between a defensible SLSA story and an asserted one. None of these are moat features — they're what a careful adopting team will eventually ask about, and it's cheaper to have already answered.

| # | Item | What's missing | Why it matters |
| - | ---- | --------------- | --------------- |
| 2a | KMS-backed signing | **Raw-key signing is now implemented (2026-08-18)** — `--signing-key`/`POKKUM_SIGNING_KEY` (PEM text or a file path, ECDSA P-256/Ed25519), self-verified against the registry post-push, `--require-signed` as a CI gate. What's still missing is exactly what this item was originally about even though its framing implied there was no key input at all to critique: `awskms://`, `gcpkms://`, `azurekms://`, `hashivault://`, and PKCS#11 URIs, so the private key never has to be a literal env-var value at all. | Raw-key-in-env-var is real now (it wasn't before — there was no signing key input of any kind), and remains the exact pattern a security review flags first. |
| 2b | CI OIDC identity | Provenance generated by a CLI on a developer laptop is SLSA L1/L2 in substance regardless of what the attestation claims. Add first-class GitHub Actions / GitLab / Buildkite OIDC so the certificate identity is issuer-attested, not self-asserted, and state the build environment explicitly in the attestation. | This is what actually separates a real SLSA L3 claim from an asserted one. |
| 2c | Sigstore TUF refresh | Embedded trust roots go stale as the Sigstore root rotates via TUF. Ship a TUF client or a documented refresh path, or keyless verification silently breaks for anyone on an older binary. | Silent breakage on a security control is the worst failure mode available. |
| 2d | Multi-arch attestation subject | **Answered (2026-08-18)**: `.sig`/`.att` attach to both the **index** digest and every **per-platform manifest** digest (dual-publish), so `cosign verify`/`cosign verify-attestation` and a Kyverno-style per-manifest policy check agree regardless of which one they target. `pokkum verify` and remote-cache-hit verification both read the same tag convention. | Was previously unstated; now documented in `ARCHITECTURE.md` §6 and `Vocabulary.md`. |
| 2e | Native addon provenance | `sharp`, `better-sqlite3`, etc. ship prebuilt `.node` binaries fetched **outside** the npm tarball integrity model; Syft will not meaningfully catalog them. Pokkum already strips their symbols — it should also hash them, record them in provenance, and flag any addon whose bytes aren't covered by a lockfile tarball. | The one class of build artifact the existing SBOM/SLSA story currently can't see. |
| 2f | Lifecycle-script provenance | Bun blocks `postinstall` for untrusted dependencies and requires `trustedDependencies`. Emitting which packages actually executed lifecycle scripts during the build is a cheap provenance field nobody else publishes. | Cheap, and directly answers "what build-time code actually ran." |
| 2g | Build-environment capture | Record the Go version Pokkum itself was built with, plus the resolved SvelteKit version, alongside the already-recorded Bun version. | Rounds out the toolchain-provenance story started by the Bun SBOM/SLSA work. |
| 2h | Placeholder public-key fallback, split per domain | One hardcoded placeholder public key (`cosign.DefaultPublicKeyPEM`, byte-identical to `baseimage.DefaultBaseImagePublicKeyPEM`) is the shared last-resort trust anchor across signing verification, base-image verification, AND remote-cache verification whenever the relevant `POKKUM_*_PUBKEY` env var is unset. Fails closed today (nothing was ever signed with its private half), but it is an undocumented shared trust assumption across three independent domains, not a per-domain secret. | Splitting it (or hard-failing instead of falling back when no real key is configured) closes a real, if currently-safe, gap. |
| 2i | `--strategy=exe`'s compiled binary is not secret-scanned | `secretguard`'s post-build scan (added 2026-08-18) covers `layered`/`static`/asset-overlay output, but a compiled `exe` binary is opaque to text scanning — only its pre-compile input tree is covered, a documented gap, not implied covered. | A secret baked into the compiled binary via a build-time dependency currently ships with zero detection for this one strategy. |
| 2j | `--expect-source` compares against unsigned annotations when no verified provenance exists | `pokkum verify --expect-source` validates against `PinnedInputs.Repo`/`Commit`, seeded from the image's own `org.opencontainers.image.source`/`.revision` annotations *unless* a verified SLSA statement overwrites them first. On an image with no verified attestation at all, this compares against attacker-controlled strings — exact matching (fixed 2026-08-18) made the comparison tighter, not sound. A real residual fail-open, deliberately not fixed this round. Fix needs verified provenance plus an `--allow-unverified-source` escape hatch (precedent: `--allow-incomplete`), which is a breaking change for anyone currently relying on the unverified comparison. | Silently comparing against unsigned data and reporting a match/mismatch either way reads as a real security check when it currently isn't one. |

**Also tracked here — real, honestly-scoped residual gaps from work already shipped, not new discoveries:**

- **`pokkum verify`'s keyless verification is now genuinely functional (2026-08-18)** — previously it derived its expected signer identity from the certificate under verification (`Issuer.CommonName`, the CA's own name), making it dead code that failed identically for a real and a forged signature; see `Lessons.md`. It now requires the operator to supply `--keyless-identity`/`--keyless-issuer` (refusing outright, before any network I/O, if keyless material is present with no configured identity) rather than trusting anything derived from the artifact itself.
- **Composition-root refactor for adapter→adapter import edges**: `internal/architecture_test.go`'s new adapters-must-not-import-each-other invariant (added 2026-08-18, see `ARCHITECTURE.md` §1) allowlists a handful of real, pre-existing edges with an inline per-edge justification each — a documented gap, not a sanctioned pattern, and one actively being reduced in parallel with this note (check the test file for the current count rather than citing a fixed number here). Closing the remainder means moving cross-adapter calls behind a shared port or up into `cmd/pokkum`'s composition root instead of one adapter importing another directly.
- **PB-2's first-contact-MITM gap** (Bun release checksum verification): TOFU pinning plus a static pin table covering the most recent releases now protects every resolve of a *previously-seen* `(version, target)`, and the compiled-in pin table protects the versions it explicitly lists — but the very first resolve of a genuinely new, unlisted version on a fresh cache has no independent trust anchor to check against (researched directly: GitHub's Releases API shares the exact same trust root as the release-download host and doesn't expose per-asset digests, so it adds no real signal). There is no further code fix available here without a fundamentally different distribution channel — this is a stated, permanent, accepted limitation of GPG-signed-manifest-over-HTTPS distribution, not an open TODO. Re-running `scripts/pin-bun-checksums` periodically narrows it further; nothing closes it fully.
- **`--hermetic-mount-isolation`'s capability self-undo gap**: the sandboxed build process retains `CAP_SYS_ADMIN` within its own mount namespace — the same capability Pokkum's own code uses to mask `docker.sock` — so a sufficiently sophisticated dependency that specifically knows this mechanism exists could in principle `umount()` the mask. Closing this needs dropping the capability via `capset(2)` (raw syscall, version-specific ABI) after masking but before the final exec into the untrusted process. Real, scoped, not yet attempted.
- **Dedicated `chainguard-static` base image preset**: `--strategy=static`'s default base reuses the `BaseImageChainguard` preset (correct signature identity, proven safe) but leaves a narrow `pokkum.lock` collision between an explicit `--base cgr.dev/chainguard/glibc-dynamic` build and a `--static` build in the same project sharing one lock slot. A dedicated `BaseImageChainguardStatic` preset (own default ref, own lock key) closes it. Full design in `concepts/new-chainguard-static-preset-concept.md`.

---

## Tier 3 — Registry & Runtime Ergonomics (⚙️ Foundation)

Unglamorous, and per the original external review, roughly a third of real-world failures live here — not exotic edge cases, the ordinary daily friction of running this against real registries and real clusters.

| # | Item | Detail |
| - | ---- | ------ |
| 3a | ECR repository auto-create | ECR requires the repository to exist before push. `--create-repository` closes a first-push failure every ECR user hits once. |
| 3b | Resumable chunked upload | Backoff on 429/5xx during a ~90MB layer push, instead of failing the whole push on a transient registry hiccup. |
| 3c | Registry-specific error surfacing | GAR/Harbor project-path semantics; Docker Hub anonymous base-pull rate limits surfaced as a readable, specific error rather than a generic push/pull failure. |
| 3d | Supervisor cgroup awareness | JSC (Bun's engine) does not read cgroup limits, so a Bun app in a 512Mi container OOMKills in ways that look random from the outside. Read `/sys/fs/cgroup/memory.max`, export it, and warn when the limit is below a sane floor. |
| 3e | Source maps as an OCI referrer | Strip from the shipped image, attach as an artifact keyed to the digest, so `handleError` + Sentry release tagging works without shipping maps to production. |
| 3f | Bun layer diffID stability, tested | Assert and test that the Bun runtime layer's diffID is **globally stable** for a given `(version × variant × platform)`, so an entire fleet pulls that ~90MB exactly once. It's the single biggest size lever and it's invisible unless enforced by a test. |
| 3g | Cache-Control contract, tested | State it as a tested invariant, not documentation prose: `/_app/immutable/*` → `public, max-age=31536000, immutable`; `/_app/version.json` → `no-cache` (it's polled); `service-worker.js` → `no-cache` at root scope; prerendered HTML → `no-cache` or a short TTL. Getting one wrong either breaks `updated.check()` or serves stale HTML forever. |

---

## Tier 4 — Scope Discipline & Polish (🧹 Polish)

`ko`'s power is that it's three seconds and one concept. Pokkum now has `build`, `resolve`, `apply`, `dev`, `scan`, `rollback`, `base update --mirror-registry`, NetworkPolicy/PDB generation, `upgrade`, `history`, `explain` (with `why`/`diff`), `doctor`, `repro doctor`, `adopt`, `init`, `config`, and `metrics`. Each is individually defensible; collectively it's a large maintenance surface and a lot of `--help`. This is a decision to make deliberately, not a task to complete:

- **Core (only Pokkum can build these):** `build`, `resolve`, `verify`, `dev`, `explain`, plus Tier 1's three moat items and `$env/static` detection. This is the actual product.
- **Adjacent (strong dedicated tools already exist):** registry mirroring, `NetworkPolicy` generation, vulnerability scanning — maintenance spent competing with Trivy, Grype, Kyverno, and Renovate without differentiating. Not necessarily wrong to keep, but worth periodically asking whether Pokkum should own them or integrate with the dedicated tool instead.
- **Reconsider:** `pokkum metrics` reads like a client-side scraper rather than a server the image exposes. Folding it into build flags and documenting the port contract would be clearer — and would also force an honest look at what the command actually does today versus what its `Long` description claims, the same code-first verification discipline the whole Pre-Publication Gate was built on.

**Small, cheap, currently missing:**
- JSON Schema for `.pokkum.yaml` with editor completion.
- `pokkum config view` showing value **provenance** (flag vs. profile vs. env vs. default), not just the resolved value.
- A documented exit-code table — `125`/`126` are already used meaningfully and are undocumented.
- A stable Go library API, if Skaffold/Tilt integration is ever wanted.

*(The "no telemetry" statement and the security-vs-speed / edge-runtime scope statements this section used to call for are now in `README.md`'s "Scope, Philosophy & Telemetry" section — done, not tracked here anymore.)*

---

## Tier 5 — Monorepo & Config Drift

- **5a — Shared vendor cache across a monorepo invocation:** `--since` avoids builds *between* projects but does nothing *within* a build when many packages share dependencies. Extend `layercacheutils` into a content-addressable vendor-layer cache keyed by `package.json` + lockfile subtree, shared across projects in one invocation — analogous to how `ko` leans on Go's build cache. **Verify first whether the existing layer cache already covers this** — this gap was originally asserted from the feature list, not the code, and deserves the same code-first verification this whole gate was built on.
- **5b — `pokkum doctor` drift check:** nothing currently validates that a `.pokkum.yaml`'s configured adapter, base image, and telemetry settings are still coherent after a SvelteKit or Bun upgrade. Add a check: adapter still installed and still the effective one, base preset still resolvable, telemetry flags still valid for the detected SvelteKit version.
- **5c — Scoped secret-allow annotations:** `.secretguardignore` / inline `// pokkum:allow-secret` comments. `--allow-secret-pattern` exists, but a global regex is a blunt instrument for one known-safe JWT-shaped string in a test fixture; a scoped annotation is the right granularity.
- **5d — Build-time test gate (`--test`):** condition image creation on the project's own test suite passing. Note the interaction with `--hermetic` semantics needs an explicit answer (a test suite that needs network access conflicts with hermetic-by-default) before this ships, not after.

---

## ⏸ Deferred (considered, not prioritized — reasons stated)

| Item | Why deferred |
| ---- | ------------ |
| Multi-Environment Management (`pokkum config env`, secret-manager integration) | Real, but Vault/AWS Secrets Manager integration is a large surface for a feature most teams solve at the CI/CD layer already. Revisit if repeatedly requested. |
| Hooks System (pre/post-build shell hooks) | Defuses plugin-system demand cheaply, but is still new maintenance surface for a capability CI pipelines already provide natively. |
| Policy as Code (OPA/Rego, `pokkum policy check`) | +15MB CLI size for embedded OPA, high policy-maintenance burden, and CI-level policy gates (Kyverno, Conftest) already own this space well. Revisit only if a real adopting team specifically asks. |
| `--verify-base-on-cache-hit` | Closes a narrow case (a pinned-digest base whose signature was later revoked/rekeyed) that only matters under a strict SBOM/SLSA attestation requirement. Deferred until a real consumer asks, to avoid feature-creep in an already-dense verification surface; opt-in by design so the sub-100ms cache-hit fast path stays fast by default. |
| Helm Post-Renderer + Kustomize KRM function | Real gap — `pokkum resolve` only handles raw-YAML `pokkum://` refs today, which covers Knative-style repos and roughly nobody else, since most teams template with Helm or Kustomize. Deferred behind Tier 1's dev-loop and asset-overlay work because it's ergonomics, not differentiation — but it's the *ergonomics* item most likely to be the actual reason a team can't adopt Pokkum at all, so revisit this first if Tier 1 stalls. |

## 🚫 Non-goals (stated, not omissions)

An unstated non-goal reads as a gap; a stated one reads as focus. All of the below are now stated in `README.md`'s "Scope, Philosophy & Telemetry" section or here, not left as apparent omissions:

- **Edge/WASM runtimes** (Cloudflare Workers, Deno Deploy, Vercel Edge Functions). Not an OCI image; a fundamentally different deployment model. SvelteKit's own `adapter-cloudflare`/`adapter-vercel`/`adapter-deno` already serve this directly. See `README.md`.
- **Progressive deployment strategies** (canary, blue-green, auto-rollback). Argo Rollouts and Flagger already own this space with Kubernetes-native primitives Pokkum has no reason to reimplement; `pokkum rollback`'s one-hop-deep annotation-based rollback covers the case Pokkum is actually positioned to solve (fast, no extra controller).
- **A plugin system** (npm-distributed `pokkum-plugin-*` packages). The DX case is real, but an npm-based extension model directly undercuts Pokkum's own hardening story — this is a supply-chain-security tool that would be inviting the exact npm supply-chain risk its own `secretguard`/hermetic-build/CVE-gate features exist to mitigate.
- **A built-in asset optimization pipeline** (`sharp`, WebP/AVIF variant generation). Heavy native dependencies and a real build-time cost increase, for a concern `@sveltejs/enhanced-img` and CDN-side transforms already solve without Pokkum needing to own it — and pulling in `sharp` conflicts with the zero-dependency ethos the rest of this tool is built around.
- **Service mesh integration** (Istio/Linkerd sidecar config generation). Real but narrow demand, real and ongoing API churn to track, and mesh-specific tooling already exists; not a place Pokkum's SvelteKit-specific knowledge adds anything a generic Kubernetes tool wouldn't.

---

## Beyond this roadmap

For the full v0.1–v1.0 build history, every root-caused bug, and the exact fix applied to each Pre-Publication Gate item — see [Roadmap-v1-Archive.md](Roadmap-v1-Archive.md). For the underlying feature-by-feature decision matrix (DX/Security/Cost scoring) this document's priorities are drawn from, see [AdditionalFeatures.md](AdditionalFeatures.md). For architectural invariants and layer-boundary rules that any of the above must respect, see [ARCHITECTURE.md](ARCHITECTURE.md).
