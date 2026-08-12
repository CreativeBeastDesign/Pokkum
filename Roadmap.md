# Pokkum Roadmap

See [Vocabulary.md](Vocabulary.md) for the full CLI flag reference, naming
conventions, and the rationale behind each new flag noted below.

See [fixes-to-v1.md](fixes-to-v1.md) for a post-v1.0 audit that found
several `[x]` items below overstated what they actually did, and the fixes
applied for each; [for-users.md](for-users.md) for what changed as a
result; and [unfixed-limitation.md](unfixed-limitation.md) for the one gap
still open (base image signature verification, noted inline below).

## Implementation Strategy & Feature Bundles

Open roadmap items are organized into **4 cohesive implementation bundles**, structured by technical dependencies and architectural boundaries:

### 📦 Bundle 1: Supply Chain & Reproducibility Lock
* **Target Release**: `v0.2` & `v0.5`
* **Focus**: Lock base image digests and capture complete build toolchain parameters in signed attestations.
* **Included Items**:
  1. **Base Image Lockfile (`pokkum.lock`)** (see [pokkum-lock-concept.md](pokkum-lock-concept.md)) — `pokkum base update` and offline base resolving.
  2. **Provenance Completeness M0** (see [pokkum-verify-concept.md](pokkum-verify-concept.md)) — Extends `slsa/generator.go` to capture Go runtime version, builder OS/arch, Bun binary hash, base digest, and lockfile hashes.
* **Why Bundled**: `pokkum.lock` locks the inputs; Provenance M0 records them in the SLSA statement. Implementing them together ensures every build produces complete, future-proof attestations.

---

### 📦 Bundle 2: Layer Caching & Architecture Shift
* **Target Release**: `v0.3` (Milestones M1–M4)
* **Focus**: Shift from monolithic single-binary builds to a 5-layer arch-independent image layout.
* **Included Items**:
  1. **M1 (Bun Runtime Plumbing)** (see [pokkum-layer-caching-concept.md](pokkum-layer-caching-concept.md)) — `bunruntime` resolver/downloader and packager directory-tree support.
  2. **M2 (Hand-rolled Adapter & Phase-1 Layering)** — SvelteKit adapter emitting split JS/asset layers (`--strategy=layered`).
  3. **M3 (Vendor Splitting & Native Closure)** — `bun build --splitting` and ELF `.node` addon support.
  4. **M4 (Hardening & Cutover)** — `readOnlyRootFilesystem` default security context and `exe` strategy deprecation.
  5. **Image Optimization** — Layer deduplication and zstd layer compression (`--compression=gzip|zstd`).
* **Why Bundled**: Cohesive core refactoring that replaces the packaging engine end-to-end. `bunruntime` from M1 also provides the pinned toolchain fetcher needed by future `pokkum verify` runs.

---

### 📦 Bundle 3: Telemetry & Machine-Readable DX
* **Target Release**: `v0.4`
* **Focus**: Standardize structured JSON schemas, zero-config OpenTelemetry, and diagnostic wizards.
* **Included Items**:
  1. **`--output=json` Schema Standard** — Machine-readable build and inspection output schema family.
  2. **Unified Metrics & Telemetry** (see [pokkum-metrics-otel-concept.md](pokkum-metrics-otel-concept.md)) — Experimental SvelteKit tracing/metrics injection and OTEL Collector K8s sidecar generation.
  3. **Developer Experience Wizards** — `pokkum init`, `pokkum doctor`, and `pokkum explain`/`why`.
* **Why Bundled**: `--output=json` provides the common JSON schema. Building Telemetry, Diagnostics, and `pokkum explain` together guarantees all developer tools emit structured JSON natively from day one.

---

### 📦 Bundle 4: Verification & Non-Determinism Diagnosis
* **Target Release**: `v0.5` (Milestones M1–M4)
* **Focus**: Independent rebuild verification and stage-level non-determinism bisection.
* **Included Items**:
  1. **Shared `layerdiff` Engine** (see [pokkum-verify-concept.md](pokkum-verify-concept.md) & [pokkum-repro-doctor-concept.md](pokkum-repro-doctor-concept.md)) — Entry-by-entry tar merge-walking and header vs. content diffing.
  2. **`pokkum repro doctor`** — Stage-level pipeline bisection, static checks (`--fast`), cause heuristics, and `--perturb` environment testing.
  3. **`pokkum verify --rebuild`** — Rebuild pipeline, L1/L2 digest comparison, and L3 mismatch explanation.
* **Why Bundled**: `layerdiff` is ~80% shared machinery between `repro doctor` and `verify`. Building them together prevents code duplication and completes Pokkum's core reproducibility promise.



## v0.1 (Completed)

- [x] Reproducible layer timestamps: set every layer/config timestamp to `SOURCE_DATE_EPOCH` derived from the last git commit (`git log -1 --pretty=%ct`), not build time
- [x] Minimal `securityContext` in generated manifests: Default to `runAsNonRoot: true`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault`.
- [x] `.pokkumignore`: Explicit exclude list so `.env.local`, test fixtures, and source maps don't end up embedded
- [x] Dry-run mode (`--dry-run` / `--print-manifest`): show what would be built/pushed/applied without doing it - added to `pipeline.go`
- [x] Structured, leveled logging so CI logs are parseable
- [x] Idempotent registry pushes: skip push if the computed digest already exists remotely (`remote.Head` before `remote.Write`)
- [x] Basic config validation surfaced as clear CLI errors before any network call — fail fast, not mid-push

## v0.2 (Completed)

- [x] Cosign signing
- [x] SLSA provenance attestation
- [x] SBOM as OCI 1.1 referrer instead of `.sbom` tag convention (new flag: `--sbom-attach=referrer|tag`, default `referrer`)
- [x] `pokkum dev` — Hot-Reload Container Development (build + `--local` + run in one command; new flag: `--debug` to drop into a shell inside the container)
- [x] Zero-Config Auto-Injection: Auto-injecting the adapter and `SOURCE_DATE_EPOCH` pinning without manual `svelte.config.js` edits. (Note: `--no-inject` escape hatch flag still pending) (see [pokkum-injection-concept.md](pokkum-injection-concept.md))
- [x] Base Image Lockfile (`pokkum.lock`): Enable reproducible base image resolving (distroless/chainguard) to prevent drift. (new flags: `--update-base`, `--offline`; new subcommand `pokkum base update --preset <name>`) (see [pokkum-lock-concept.md](pokkum-lock-concept.md))

## v0.3: Layer Caching & Core Architecture Shift

_Refactoring to replace the `exe` adapter with a hand-rolled adapter for layer caching, dramatically reducing per-commit image size._ (see [pokkum-layer-caching-concept.md](pokkum-layer-caching-concept.md))

- [x] M1: Packager + runtime plumbing (Bun runtime resolution, pinned downloads). (new flags: `--bun-binary=<path>` offline escape hatch, `--bun-variant=baseline` for pre-AVX2 hosts) (see [pokkum-layer-caching-concept.md](pokkum-layer-caching-concept.md))
- [x] M2: Hand-rolled SvelteKit adapter + Phase-1 layering (separating app JS from runtime). (new flag: `--strategy=layered|exe`, `layered` default) (see [pokkum-layer-caching-concept.md](pokkum-layer-caching-concept.md))
- [x] M3: Vendor splitting (`bun build --splitting`) + native closure support (unblocking native `.node` addons). (no new flag — internal to `--strategy=layered`) (see [pokkum-layer-caching-concept.md](pokkum-layer-caching-concept.md))
- [x] M4: Hardening (`readOnlyRootFilesystem`) & cutover from the old `exe` adapter. (no new flag — folds into `--security-context`'s hardened-defaults bundle) (see [pokkum-layer-caching-concept.md](pokkum-layer-caching-concept.md))
- [x] Image Optimization: Deduplication across layers, optional zstd layer compression. (new flag: `--compression=gzip|zstd`, default `gzip`) (see [pokkum-layer-caching-concept.md](pokkum-layer-caching-concept.md))

## v0.4: Unified Telemetry & Developer Experience

- [x] Unified Metrics & Telemetry: Zero-config injection of OpenTelemetry setup for traces and metrics (`pokkum metrics` and app-side). (new flag: `--metrics-port` on the `pokkum metrics` subcommand; build-time half reuses existing `--telemetry`/`--otel-export` family) (see [pokkum-metrics-otel-concept.md](pokkum-metrics-otel-concept.md))
- [x] `pokkum init`: Project Configuration Wizard (interactive or `--defaults`).
- [x] `pokkum doctor`: Environment preflight checks (Bun version, registry auth, `svelte.config.js` sanity). (new flag: `--fix` for mechanical repairs)
- [x] Interactive Failure Diagnostics: Automatic log dump and exit code analysis on local container failure. (new flag: `--no-diagnostics` opt-out for CI)
- [x] `--output=json`: Machine-readable build results schema for robust CI parsing.
- [x] Diff & Explain: `pokkum diff`, `pokkum explain`, and `pokkum why` to trace layer changes and dependencies. (reuses `--output=json` for machine-readable output; no new flag)

## v0.5: Reproducibility & Diagnosis

_Closing the loop on reproducibility with verifiable rebuilds and non-determinism bisection._

- [x] M0: Provenance completeness (recording Go version, builder OS/arch, lockfile hashes in SLSA statement). (no new flag — recorded automatically) (see [pokkum-verify-concept.md](pokkum-verify-concept.md))
- [x] M1/M2: Stage recorder, bisection core, and attestation-check mode (`pokkum verify --no-rebuild`). (see [pokkum-verify-concept.md](pokkum-verify-concept.md))
- [x] M3: `layerdiff` component, L3 explanation, and `pokkum repro doctor` diagnostics. (new flag: `--fast` for static-checks-only, no build) (see [pokkum-verify-concept.md](pokkum-verify-concept.md) & [pokkum-repro-doctor-concept.md](pokkum-repro-doctor-concept.md))
- [x] M4: `pokkum verify --rebuild` (L1/L2 compare) with `--perturb` mode, K8s + CI ergonomics. (new flags: `--against <path>`, `--expect-source <repo>@<ref>`, `--all-platforms`) (see [pokkum-verify-concept.md](pokkum-verify-concept.md) & [pokkum-repro-doctor-concept.md](pokkum-repro-doctor-concept.md))

## v1.0: MVP Launch (Supply Chain Hardening & Production Readiness)

_The shippable minimum viable product for enterprise-grade adoption._

### Supply Chain

- [x] Trusted-Builder Mode: SLSA L3 via an isolated CI job (GitHub Actions provenance generation). (no new flag — a CI workflow concern, not a CLI flag)
- [x] CVE Scanning Integration (`pokkum scan`): Run vulnerability scanning against image, tarball, or directory and fail pipeline above severity threshold. (new flag: `--fail-on=critical`)
- [x] Toolchain CVE Awareness: OSV.dev advisory lookups keyed on embedded Bun and SvelteKit versions. (new flag: `pokkum scan --toolchain`, extending `scan`)
- [x] Base Image Signature Verification: Verifies Cosign signatures against a static public key (`POKKUM_BASE_IMAGE_PUBKEY`) at pull time — real protection for a custom or self-signed base image. Does **not** yet cover the stock `distroless`/`chainguard` presets' actual signatures, which use keyless Sigstore signing (Fulcio + Rekor) with no fixed key to check against; see [unfixed-limitation.md](unfixed-limitation.md). (new flag: `--no-verify-base` opt-out)
- [x] Secret-Inlining Guard: Build-time entropy/pattern scan to prevent leaked secrets in layers. (new flag: `--allow-secret-pattern` escape hatch for false positives)
- [x] Base image digest pinning + automated update PRs (Renovate/Dependabot-style). (reuses `pokkum base update --preset <name>` from the v0.2 lockfile item; update-PR half is a bot, not a flag) (see [pokkum-lock-concept.md](pokkum-lock-concept.md))
- [x] Standard OCI Annotations: `--image-label key=value` (repeatable) mirrors user-supplied labels onto the matching `org.opencontainers.image.*` annotation keys. No git-metadata auto-population exists yet — annotations only appear if you pass them explicitly. (new flag: `--image-label key=value`, repeatable — matches `ko build --image-label`)

### Cluster-side hardening

- [x] `NetworkPolicy` generation restricting egress and ingress limited to expected ports, `podSelector` scoped to the workload's own Pod-template labels. (new flags: `--network-policy` / `--no-network-policy` on `resolve`/`apply`)
- [x] Resource `requests`/`limits` and a `PodDisruptionBudget` by default in generated manifests, selector-scoped to the workload rather than the whole namespace — skipped entirely, not emitted unscoped, when no workload labels can be found. (new flags: `--resource-defaults` / `--no-resource-defaults` on `resolve`/`apply`)
- [x] `readOnlyRootFilesystem: true` where feasible (supervisor + compiled binary). (no new flag — folds into `--security-context`)
- [x] Readiness Drain on SIGTERM: Supervisor holds `/readyz` at 503 while the app drains. (no new flag — bounded by the existing `POKKUM_SHUTDOWN_TIMEOUT` env var)
- [x] Secrets via `envFrom` referencing Kubernetes `Secret`/external-secrets, never baked into image layers. (no new flag — expressed in the manifest itself)

### Build integrity

- [x] Hermetic builds: No network egress during the compile step (SLSA L3 requirement). (new flag: `--hermetic`, opt-in)
- [x] Multi-registry support with per-registry auth chains via a `docker config.json`-style file keyed by registry hostname, merged ahead of the default keychain (self-hosted and any registry with static credentials in the file). No ECR/GCR/ACR-specific credential-helper invocation. (new flag: `--registry-config=<path>`)
- [x] Ephemeral test registry (`pkg/registry.NewServer()`) wired into CI for integration tests. (no new flag — test infra only)

### Operational maturity

- [x] Version pinning of `pokkum` itself in generated manifest annotations. (no new flag — folds into `--image-label`/auto-annotations above)
- [x] A GitHub Action wrapping the CLI (mirroring `setup-ko`). (no new `pokkum` flag — Action inputs like `version`/`token`, mirroring `setup-ko`)
- [x] Rollback support (`pokkum rollback` rewrites a manifest's `image:` reference to a user-supplied tag/digest). (new subcommand: `pokkum rollback -f <manifest> --to=<ref>`, reusing `-f`/`--file` from `resolve`/`apply`; `--to` is required — there is no build-history store to auto-discover a prior version from, see Backlog)
- [x] Signed Self-Distribution (`pokkum upgrade`): Signature verification of release artifacts and binary self-updates via `ports.ReleaseVerifier` and `cosign`. (new subcommand: `pokkum upgrade`, new flags: `--check`, `--version`, `--offline`, `--key`)


## Beyond v1.0 / Backlog

_Features demoted or planned for later iterations._

- [ ] `pokkum adopt` (Migration Codemod): Auto-convert Vercel/Node adapter projects. (new subcommand: `pokkum adopt [dir]`, new flags: `--dry-run` (reusing `build`'s semantics), `--remove-dockerfile`)
- [ ] Runtime Env Contract: Declare required env vars in image annotations; validate at startup. (new flag: `--require-env=KEY1,KEY2` on `build`; supersedes the old build-time `--env-file` injection idea, dropped as a secret-baking footgun)
- [ ] Monorepo Affected-Detection: Git-diff input tracking per `pokkum://` app. (new flag: `--since=<git-ref>` on `resolve`)
- [ ] Static/Prerendered Page Optimization: Extract prerendered pages to a slim layer, `--static` Nginx mode. (new flag: `--static` on `build`)
- [ ] Multi-Environment Management: Config templating and secret-manager integrations. (new flag: `--target-env=<name>` on `build`/`resolve`/`apply` — named to avoid colliding with `build`'s existing `--telemetry-env`)
- [ ] Hooks System: Pre/post-build hooks (shell, Bun scripts, webhooks). (new subcommands: `pokkum hook pre-build`, `pokkum hook post-build`; new flag: `--skip-hooks` on `build`)
- [ ] Image Provenance Timeline: `pokkum history <image>` linking back to CI runs and PRs. (new subcommand: `pokkum history <image>`, reuses `--output=json`)
- [ ] History-Aware Rollback: `pokkum rollback` without `--to` auto-discovers the previous deployed ref instead of requiring the caller to already know it. Needs a build-history store — natural to build on top of the Image Provenance Timeline item above rather than duplicate its tracking.
- [ ] Policy as Code: `pokkum policy check` with Rego/OPA. (new subcommand: `pokkum policy check`, new flag: `--policy=<path>`)
- [ ] Service Mesh Integration: Auto-generate Istio/Linkerd configs and mTLS paths. (new subcommand: `pokkum mesh generate`, new flags: `--mtls`, `--mesh-telemetry` — named to avoid colliding with `build`'s OTel `--telemetry`)
- [ ] Progressive Deployment Strategies: Canary/blue-green deployments (mostly handled by Argo/Flux). (new subcommand: `pokkum deploy`, new flags: `--canary=<percent>`, `--blue-green`, `--auto-rollback`)
- [ ] Asset Optimization Pipeline: Automatic WebP/AVIF variants, cache-busting, CDN-origin separation. (new flag: `--optimize-assets` on `build`, opt-in given its build-time cost)
- [ ] Plugin System: Extensible via npm packages (high complexity/supply-chain risk). (new subcommands: `pokkum plugin add|list|remove <name>`)
