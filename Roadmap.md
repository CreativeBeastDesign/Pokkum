# Pokkum Roadmap

See [Vocabulary.md](Vocabulary.md) for the full CLI flag reference, naming
conventions, and the rationale behind each new flag noted below.

See [fixes-to-v1.md](fixes-to-v1.md) for a post-v1.0 audit that found
several `[x]` items below overstated what they actually did, and the fixes
applied for each; [for-users.md](for-users.md) for what changed as a
result.

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
- [x] Zero-Config Auto-Injection: Auto-injecting the adapter and `SOURCE_DATE_EPOCH` pinning without manual `svelte.config.js` edits (supported via `--inject` and `--no-inject` flags). (see [pokkum-injection-concept.md](pokkum-injection-concept.md))
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
- [x] M1/M2: Stage recorder, bisection core, and attestation-check mode (`pokkum verify --no-rebuild`). (see [pokkum-verify-concept.md](pokkum-verify-concept.md)) — **caveat found this round, not yet fixed:** `pokkum verify`'s provenance summary (git repo/commit, signer identity, `SignatureValid`, `HasProvenance`) is sourced from `internal/adapters/provenance/resolver.go`, a resolver explicitly labeled `// Mock/stub extraction of SLSA provenance & toolchain inspection for M1` that returns identical hardcoded values regardless of the image passed in. This was found (and only worked around, not fixed, since it's out of scope for that command) while fixing `pokkum history`'s use of the same stub — see [fixes-to-v1.md](fixes-to-v1.md). `pokkum verify --no-rebuild`'s attestation-check verdict should not be trusted today; `--rebuild` mode (M4, below) does a real digest comparison and is unaffected. This is now the single highest-priority item in Recommended Next Steps below — a supply-chain tool's own verification command silently returning fake data is more severe than anything else still open.
- [x] M3: `layerdiff` component, L3 explanation, and `pokkum repro doctor` diagnostics. (new flag: `--fast` for static-checks-only, no build) (see [pokkum-verify-concept.md](pokkum-verify-concept.md) & [pokkum-repro-doctor-concept.md](pokkum-repro-doctor-concept.md))
- [x] M4: `pokkum verify --rebuild` (L1/L2 compare) with `--perturb` mode, K8s + CI ergonomics. (new flags: `--against <path>`, `--expect-source <repo>@<ref>`, `--all-platforms`) (see [pokkum-verify-concept.md](pokkum-verify-concept.md) & [pokkum-repro-doctor-concept.md](pokkum-repro-doctor-concept.md))

## v1.0: MVP Launch (Supply Chain Hardening & Production Readiness)

_The shippable minimum viable product for enterprise-grade adoption._

### Supply Chain

- [x] Trusted-Builder Mode: SLSA L3 via an isolated CI job (GitHub Actions provenance generation). (no new flag — a CI workflow concern, not a CLI flag)
- [x] CVE Scanning Integration (`pokkum scan`): Run vulnerability scanning against image, tarball, or directory and fail pipeline above severity threshold. (new flag: `--fail-on=critical`)
- [x] Toolchain CVE Awareness: OSV.dev advisory lookups keyed on embedded Bun and SvelteKit versions. (new flag: `pokkum scan --toolchain`, extending `scan`)
- [x] Base Image Signature Verification: Two modes — static-key Cosign verification for custom/self-signed bases (`POKKUM_BASE_IMAGE_PUBKEY`), and keyless Sigstore verification (Fulcio + Rekor) for stock `distroless`/`chainguard` presets by default. (new flags: `--base-verify-mode {auto|keyless|static-key}`, `--base-keyless-identity`, `--base-keyless-issuer`, `--sigstore-trusted-root`, `--no-verify-base` opt-out)
- [x] Secret-Inlining Guard: Build-time entropy/pattern scan to prevent leaked secrets in layers. (new flag: `--allow-secret-pattern` escape hatch for false positives)
- [x] Base image digest pinning + automated update PRs (Renovate/Dependabot-style). (reuses `pokkum base update --preset <name>` from the v0.2 lockfile item; update-PR half is a bot, not a flag) (see [pokkum-lock-concept.md](pokkum-lock-concept.md))
- [x] Standard OCI Annotations: `org.opencontainers.image.revision`/`.source`/`.version`/`.created` auto-populate on every build by default — no flag needed, and no opt-out flag exists. `revision`/`source`/`version` come from git (commit SHA, remote URL, `git describe`) or CI env vars; `created` is set to the exact same `SOURCE_DATE_EPOCH`-resolved timestamp used for layer mtimes and the image config elsewhere in the build (not resolved independently, so it can't disagree with what's actually in the image) and is left unset — not fabricated from `time.Now()` — when that timestamp can't be determined. `--image-label key=value` still works for explicit overrides (checked first, so an explicit value always wins) and any other annotation. Outside a git repository (or without `git` on `PATH`), `revision`/`source`/`version` are silently absent — no warning is printed. (new flag: `--image-label key=value`, repeatable — matches `ko build --image-label`)

### Cluster-side hardening

- [x] `NetworkPolicy` generation restricting egress and ingress limited to expected ports, `podSelector` scoped to the workload's own Pod-template labels. (new flags: `--network-policy` / `--no-network-policy` on `resolve`/`apply`)
- [x] Resource `requests`/`limits` and a `PodDisruptionBudget` by default in generated manifests, selector-scoped to the workload rather than the whole namespace — skipped entirely, not emitted unscoped, when no workload labels can be found. (new flags: `--resource-defaults` / `--no-resource-defaults` on `resolve`/`apply`)
- [x] `readOnlyRootFilesystem: true` where feasible (supervisor + compiled binary). (no new flag — folds into `--security-context`)
- [x] Readiness Drain on SIGTERM: Supervisor holds `/readyz` at 503 while the app drains. (no new flag — bounded by the existing `POKKUM_SHUTDOWN_TIMEOUT` env var)
- [x] Secrets via `envFrom` referencing Kubernetes `Secret`/external-secrets, never baked into image layers. (no new flag — expressed in the manifest itself)

### Build integrity

- [x] Hermetic builds: No network egress during the compile step (SLSA L3 requirement). (new flag: `--hermetic`, opt-in)
- [x] Multi-registry support with per-registry auth chains via a `docker config.json`-style file keyed by registry hostname, merged ahead of the default keychain (self-hosted and any registry with static credentials in the file). No ECR/GCR/ACR-specific credential-helper invocation by design — Pokkum stays zero-dependency rather than vendoring cloud-provider SDKs; the standard `docker config.json` format is the deliberate boundary. (new flag: `--registry-config=<path>`)
- [x] Ephemeral test registry (`pkg/registry.NewServer()`) wired into CI for integration tests. (no new flag — test infra only)

### Operational maturity

- [x] Version pinning of `pokkum` itself in generated manifest annotations. (no new flag — folds into `--image-label`/auto-annotations above)
- [x] A GitHub Action wrapping the CLI (mirroring `setup-ko`). (no new `pokkum` flag — Action inputs like `version`/`token`, mirroring `setup-ko`)
- [x] Rollback support (`pokkum rollback` reading from declarative manifest annotations `pokkum.dev/previous-image`). (new subcommand: `pokkum rollback -f <manifest> [--to=<ref>]`, reusing `-f`/`--file` from `resolve`/`apply`; `--to` is optional and defaults to `pokkum.dev/previous-image` annotation)
- [x] Signed Self-Distribution (`pokkum upgrade`): Signature verification of release artifacts and binary self-updates via `ports.ReleaseVerifier` and `cosign`. (new subcommand: `pokkum upgrade`, new flags: `--check`, `--version`, `--offline`, `--key`)

## Recommended Next Steps (Post-v1.0)

v1.0's supply-chain claims are now genuinely backed by working code (see
[fixes-to-v1.md](fixes-to-v1.md)) rather than partially aspirational. The
question that matters now isn't "does what's built work" but "what's the
highest-leverage thing to build next for someone actually adopting this
tool." Prioritized:

### Tier 0 — Fix `pokkum verify`'s fake provenance stub (new, highest priority)

Found this round, not yet fixed: `pokkum verify --no-rebuild`'s entire
provenance/signature summary comes from a hardcoded stub (see the v0.5
section above for the exact file/finding). This ranks above every item
below it — a scan gap or a missing mirror is an absent feature; a
verification command that reports a confident, specific, *wrong* verdict
(signer identity, signature validity, SLSA presence) regardless of the
real image is actively misleading, in the one command whose entire job is
to be trusted more than a visual check. Real fix needs the same real
registry-read `pokkum history` now does, extended to actually verify (not
just report) the signature and SLSA attestation via the cosign/sigstore
verifiers already wired elsewhere in this codebase — a bigger, riskier
change than `history`'s fix, since `verify` is a more heavily relied-upon
command; scope and test it deliberately rather than rushing it in
alongside unrelated work.

### Tier 1 — Close the CVE-detection gap — mostly done

This tier existed because of a real, concrete incident: a CRITICAL `libssl3`
CVE in `gcr.io/distroless/nodejs24-debian12:nonroot` was the actual reason
Pokkum got picked up for evaluation in the first place.

1. **Real image/OS-package vulnerability scanning for `pokkum scan` — done,
   verified live.** `pokkum scan` now genuinely pulls and enumerates image
   contents via `syft` and queries OSV.dev for real OS-package advisories,
   not a hardcoded list — confirmed by live-testing against
   `gcr.io/distroless/nodejs24-debian12:nonroot` (correctly enumerated
   `libssl3` and 10 other real packages) and against a directory with a
   known-vulnerable `lodash` version (correctly failed with real CVEs from
   OSV.dev). One gap found and closed in the same round: an OSV.dev lookup
   failure previously degraded silently to "0 vulnerabilities found" —
   `pokkum scan`/`doctor` now fail closed on an incomplete scan by default
   (`--allow-incomplete` opts back into best-effort). See
   [fixes-to-v1.md](fixes-to-v1.md).
2. **Base image CVE reactivity — still open.**
   `.github/workflows/update-base-lock.yml` runs on a Monday-only weekly
   cron. A critical CVE disclosed Tuesday sits unactioned until the
   following week. Now that (1) is real, the natural next step is for
   `pokkum build`/`pokkum doctor` to actively warn (or fail, above a
   threshold) when the *currently locked* base digest has a known CVE —
   turning a passive weekly bot into an active gate, not just a faster
   cron. `doctor`'s new "Base Image Security & CVEs" check is a start
   (queries locked bases via the now-real scanner) but only runs on-demand,
   not as part of `build` itself.
3. **Base Image Escrow / Mirroring — done, verified.** Fail-closed mirror write error propagation and full test suite covering multi-arch index and single-image escrow, Cosign signature tag mirroring, lockfile integrity, mirror fallback, and air-gapped resolution. See the dedicated note below.

### Tier 2 — Close `--registry-config`'s cloud-provider gap — done, verified

`--registry-config` was deliberately a generic `docker config.json` reader,
not an ECR/GCR/ACR SDK integration. It now genuinely shells out to
`docker-credential-*` helper binaries per registry — the same
`credHelpers`/`credsStore` mechanism `docker login` itself uses — via
`internal/adapters/registryutils/keychain.go`. Verified: no new heavyweight
dependency was added (`go.mod`/`go.sum` diff for that commit is empty; only
`github.com/docker/docker-credential-helpers`, already present
transitively, is now a direct import), a missing helper binary degrades
gracefully to the next keychain rather than hanging, and both
`internal/adapters/registry` and `internal/adapters/baseimage` route
through it. Real cloud-provider support, zero new dependencies, the
zero-dependency design philosophy held.

### Tier 3 — Adoption-lowering features — done, with real bugs found and fixed

4. **`pokkum adopt` — done, but shipped with two real gaps, now fixed.** It
   initially had no SvelteKit detection at all (would "successfully" adopt
   a plain Express app) and permanently rewrote `svelte.config.js` by
   default despite that not being necessary (`pokkum build` already injects
   the adapter virtually at build time) — both fixed, see
   [fixes-to-v1.md](fixes-to-v1.md).
5. **Multi-Generation Rollback History — done, but the resolver-side writer
   was dead code until this round** (a structurally-always-false condition
   meant `resolve`/`apply` never actually recorded history in the normal
   deploy workflow) — fixed; see [fixes-to-v1.md](fixes-to-v1.md). One
   real, still-open limitation: `resolve` alone cannot accumulate history
   across independent CLI invocations against an untouched,
   permanently-`pokkum://`-templated source (Pokkum's own recommended
   workflow) — only a caller that persists annotations across calls can.
   Closing this for real needs `pokkum apply` to read the *live cluster's*
   currently-deployed annotations (a `kubectl get` round-trip) before
   resolving, so "what changed" has something durable to compare against —
   not attempted this round; scoped as its own follow-up given the added
   complexity and failure modes (first deploy, namespace resolution,
   cluster access) a live round-trip introduces.
   **Image Provenance Timeline (`pokkum history`) — done, but was fully
   fake as first written** (hardcoded stub data regardless of the image
   passed in) — now reads the image's real OCI annotations instead;
   explicitly does not claim to verify signatures or SLSA provenance
   (`pokkum verify` remains the tool for that). See
   [fixes-to-v1.md](fixes-to-v1.md).
6. **Runtime Env Contract — done, verified clean.** Full chain traced from
   `--require-env` through to the supervisor's real fail-fast startup
   check; no gaps found.

### Base Image Escrow / Mirroring (Chainguard `:latest`-drift)

Real, structural risk, not a hypothetical: free/anonymous access to
Chainguard Images is generally limited to the `:latest` tag — historical
digests routinely become unpullable once Chainguard rebuilds and the
registry stops serving blobs no live tag references, even though the
digest itself is technically immutable content-addressed data. This is a
widely-reported pain point across the container ecosystem, not specific to
Pokkum. For a tool whose central promise is bit-for-bit reproducibility,
that's a gap `pokkum.lock` alone doesn't close: pinning a digest guarantees
*what* to reproduce, not that reproducing it will still be *possible*
months later if the only copy lived at an upstream registry that already
moved on.

Recommended pattern — **Base Image Escrow**: when `pokkum base update`
locks a new digest, optionally copy that exact image (by digest, using the
copy primitives `go-containerregistry` already provides — no new
dependency) into a registry the project controls (GHCR under the same org
is the natural zero-new-infrastructure choice, and matches this project's
existing GHCR-centric examples). Record both the upstream ref and the
mirror ref in `pokkum.lock`; resolve upstream first, mirror as fallback (or
the reverse, configurable) — giving durable reproducibility independent of
any single upstream registry's retention policy, at the cost of one small
image copy per lock update (already-compressed layers, so cheap) and a
modest `pokkum.lock` schema extension. This is exactly the class of problem
`pokkum.lock` already exists to solve (see
[pokkum-lock-concept.md](pokkum-lock-concept.md)) — escrow closes the
remaining gap between "pinned" and "durably fetchable."

**Status: done, verified.** All mirror write errors (`remote.WriteIndex`, `remote.Write`, and Cosign `.sig` tag writes) are classified and fail-closed (`core.ErrPushFailed`/`core.ErrRegistryAuth`), preventing unwritten `MirrorRef` entries from being saved to `pokkum.lock`. Full test coverage in `internal/adapters/baseimage/resolver_test.go` verifies multi-platform indexes, single images, signature tag escrow, fail-closed write errors, graceful fallback to `PinnedRef` on mirror outage, air-gapped resolution from mirror, and stale mirror clearing on digest updates.

## Beyond v1.0 / Backlog

_Features demoted or planned for later iterations._

- [x] Real Image/OS Vulnerability Scanning: `pokkum scan <image>`/`<tarball>` now catalogs OS and toolchain packages using Syft and queries OSV.dev via batch API (`/v1/querybatch`) for ecosystem-aware CVE lookup with CVSS severity ranking and fixed-version extraction. (no new flag — fixes the existing `pokkum scan [target]` contract to match its own documented behavior)
- [x] Base Image Escrow / Mirroring: `--mirror-registry=<repo>` mirrors base images/indexes and their Cosign `.sig` tags to a project-controlled registry, saving `mirror_ref` in `pokkum.lock` with automatic fallback. Mirror write errors are classified (`core.ErrPushFailed`/`core.ErrRegistryAuth`) and fail-closed so `pokkum.lock` never records unwritten mirror refs. (new flag: `--mirror-registry=<repo>` on `base update`)
- [x] Registry Credential-Helper Invocation: `--registry-config` dynamically resolves credentials via `credHelpers` and `credsStore` by executing `docker-credential-*` binaries (e.g., ECR, GCR, OSXKeychain) with in-memory caching and fallback to static `auths` blocks, supporting cloud registries with zero new external SDK dependencies. (no new flag — extends existing `--registry-config=<path>` behavior)
- [x] `pokkum adopt` (Migration Codemod): Auto-converts Vercel/Node/Cloudflare/Auto adapter projects to native Pokkum compilation defaults with AST/regex config rewrites, `.pokkumignore` bootstrapping, and optional legacy Dockerfile removal. (new subcommand: `pokkum adopt [dir]`, new flags: `--dry-run`, `--remove-dockerfile`)
- [x] Runtime Env Contract: Declares required runtime environment variables in OCI image annotations (`pokkum.dev/required-env`) and embeds contract into runtime config, enforced by PID-1 supervisor (`/pokkum/init`) to fail-fast on startup if any are missing. (new flag: `--require-env=KEY1,KEY2` on `build`)
- [ ] Monorepo Affected-Detection: Git-diff input tracking per `pokkum://` app. (new flag: `--since=<git-ref>` on `resolve`)
- [ ] Static/Prerendered Page Optimization: Extract prerendered pages to a slim layer, `--static` Nginx mode. (new flag: `--static` on `build`)
- [ ] Multi-Environment Management: Config templating and secret-manager integrations. (new flag: `--target-env=<name>` on `build`/`resolve`/`apply` — named to avoid colliding with `build`'s existing `--telemetry-env`)
- [ ] Hooks System: Pre/post-build hooks (shell, Bun scripts, webhooks). (new subcommands: `pokkum hook pre-build`, `pokkum hook post-build`; new flag: `--skip-hooks` on `build`)
- [x] Image Provenance Timeline: `pokkum history <image>` inspects SLSA provenance attestations, Cosign signatures, builder metadata, base images, and CI workflow / pull request links. (new subcommand: `pokkum history <image>`, reuses `--output=json`)
- [x] Multi-Generation Rollback History: `pokkum rollback` supports arbitrary historical rollback depths via `pokkum.dev/image-history` manifest annotations with timeline inspection (`--list`) and generation selection (`--generation=<n>`, `-g <n>`). (new flags: `--generation=<n>`, `-g <n>`, `--list` on `rollback`)
- [ ] Policy as Code: `pokkum policy check` with Rego/OPA. (new subcommand: `pokkum policy check`, new flag: `--policy=<path>`)
- [ ] Service Mesh Integration: Auto-generate Istio/Linkerd configs and mTLS paths. (new subcommand: `pokkum mesh generate`, new flags: `--mtls`, `--mesh-telemetry` — named to avoid colliding with `build`'s OTel `--telemetry`)
- [ ] Progressive Deployment Strategies: Canary/blue-green deployments (mostly handled by Argo/Flux). (new subcommand: `pokkum deploy`, new flags: `--canary=<percent>`, `--blue-green`, `--auto-rollback`)
- [ ] Asset Optimization Pipeline: Automatic WebP/AVIF variants, cache-busting, CDN-origin separation. (new flag: `--optimize-assets` on `build`, opt-in given its build-time cost)
- [ ] Plugin System: Extensible via npm packages (high complexity/supply-chain risk). (new subcommands: `pokkum plugin add|list|remove <name>`)
