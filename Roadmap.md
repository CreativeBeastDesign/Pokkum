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
- [x] M1/M2: Stage recorder, bisection core, and attestation-check mode (`pokkum verify --no-rebuild`). (see [pokkum-verify-concept.md](pokkum-verify-concept.md))
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

### Tier 1 — Close the CVE-detection gap

This tier exists because of a real, concrete incident: a CRITICAL `libssl3`
CVE in `gcr.io/distroless/nodejs24-debian12:nonroot` was the actual reason
Pokkum got picked up for evaluation in the first place — moving off a
Node.js-runtime base image entirely (which Pokkum's Bun-compiled,
`distroless/cc`-or-`chainguard`-glibc-dynamic architecture already does) is
exactly the kind of problem this tool exists to solve. But the CVE-visibility
half of that story isn't there yet:

1. **Real image/OS-package vulnerability scanning for `pokkum scan`.**
   Verified fact: `pokkum scan <image-ref-or-tarball>` today does not pull
   or inspect the target at all — `internal/adapters/scanner/adapter.go`'s
   `Scan()` only recognizes a local directory target (for toolchain/OSV
   checks); anything else falls through to a hardcoded 2-entry advisory
   list (`embeddedAdvisories`), regardless of what image or tarball was
   actually passed. This directly contradicts the command's own help text
   ("Scan inspects a project directory, container image, or toolchain
   dependencies..."). A `libssl3` CVE in a resolved base image would not be
   caught by `pokkum scan` today. Natural implementation: the SBOM
   generation this project already has (`internal/adapters/sbom`, via
   `syft`) already enumerates OS packages — feed those into the existing
   `queryOSV` plumbing in `scanner/adapter.go` (OSV.dev has ecosystem
   coverage for Debian/Alpine packages, not just language-package
   ecosystems), instead of only checking embedded Bun/SvelteKit versions.
   Most of the pieces already exist; the missing part is wiring OS
   packages into the same lookup path.
2. **Base image CVE reactivity.** `.github/workflows/update-base-lock.yml`
   runs on a Monday-only weekly cron. A critical CVE disclosed Tuesday sits
   unactioned until the following week. Once (1) above exists, the
   natural fix is for `pokkum build`/`pokkum doctor` to actively warn (or
   fail, above a threshold) when the *currently locked* base digest has a
   known CVE — turning a passive weekly bot into an active gate, not just
   a faster cron.
3. **Base Image Escrow / Mirroring** — see the dedicated note below; this
   is both a CVE-reactivity concern (an upstream base with no accessible
   older digest can't be rolled back to) and a reproducibility concern.

### Tier 2 — Close `--registry-config`'s cloud-provider gap without abandoning zero-dependency

`--registry-config` is deliberately a generic `docker config.json` reader,
not an ECR/GCR/ACR SDK integration — see [Vocabulary.md](Vocabulary.md) §3
for the confirmed rationale (Pokkum stays zero-dependency rather than
vendoring cloud SDKs). That boundary doesn't have to mean no cloud-provider
support: `docker config.json`'s own `credHelpers`/`credsStore` keys already
name which `docker-credential-*` binary to shell out to per registry — this
is literally how `docker login` against ECR/GCR/ACR already works today,
and those binaries are typically already on `PATH` in any CI environment
that authenticates to those registries by other means. Pokkum's
`--registry-config` resolver could invoke the same mechanism (shell out to
the named credential helper, exactly as Docker does) instead of reading
only the static `auths` block it handles today. Real cloud-provider
support, zero new dependencies, no violation of the stated design
philosophy — this closes the gap the philosophy itself creates room for.

### Tier 3 — Adoption-lowering features (this is what makes it a product, not a demo)

4. **`pokkum adopt`** (already backlogged below) is the single
   highest-leverage feature for getting other developers to actually use
   this: an automated Vercel/Node-adapter-to-Pokkum codemod removes the
   single biggest switching-cost objection ("I'd have to rewrite my
   Dockerfile/deploy setup"). Every other feature in this roadmap benefits
   nobody who never adopts the tool.
5. **Multi-Generation Rollback History** and **Image Provenance Timeline**
   (already backlogged, natural pairing — see below) turn "day-2 lifecycle
   management" from a nice-to-have into something an SRE team can actually
   depend on during an incident.
6. **Runtime Env Contract** (already backlogged) is cheap and well-scoped:
   closes a real footgun (a missing required env var surfacing as a
   confusing runtime crash instead of a build/deploy-time error).

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

## Beyond v1.0 / Backlog

_Features demoted or planned for later iterations._

- [x] Real Image/OS Vulnerability Scanning: `pokkum scan <image>`/`<tarball>` now catalogs OS and toolchain packages using Syft and queries OSV.dev via batch API (`/v1/querybatch`) for ecosystem-aware CVE lookup with CVSS severity ranking and fixed-version extraction. (no new flag — fixes the existing `pokkum scan [target]` contract to match its own documented behavior)
- [x] Base Image Escrow / Mirroring: copies each `pokkum.lock`-pinned base image digest (and its associated Cosign `.sig` signature tag) into a project-controlled mirror registry (e.g. GHCR) at lock-update time, recording both refs with automated fallback in `pokkum.lock`. (new flag: `--mirror-registry=<repo>` on `pokkum base update`, extends `pokkum.lock` schema)
- [x] Registry Credential-Helper Invocation: `--registry-config` dynamically resolves credentials via `credHelpers` and `credsStore` by executing `docker-credential-*` binaries (e.g., ECR, GCR, OSXKeychain) with in-memory caching and fallback to static `auths` blocks, supporting cloud registries with zero new external SDK dependencies. (no new flag — extends existing `--registry-config=<path>` behavior)
- [ ] `pokkum adopt` (Migration Codemod): Auto-convert Vercel/Node adapter projects. (new subcommand: `pokkum adopt [dir]`, new flags: `--dry-run` (reusing `build`'s semantics), `--remove-dockerfile`)
- [ ] Runtime Env Contract: Declare required env vars in image annotations; validate at startup. (new flag: `--require-env=KEY1,KEY2` on `build`; supersedes the old build-time `--env-file` injection idea, dropped as a secret-baking footgun)
- [ ] Monorepo Affected-Detection: Git-diff input tracking per `pokkum://` app. (new flag: `--since=<git-ref>` on `resolve`)
- [ ] Static/Prerendered Page Optimization: Extract prerendered pages to a slim layer, `--static` Nginx mode. (new flag: `--static` on `build`)
- [ ] Multi-Environment Management: Config templating and secret-manager integrations. (new flag: `--target-env=<name>` on `build`/`resolve`/`apply` — named to avoid colliding with `build`'s existing `--telemetry-env`)
- [ ] Hooks System: Pre/post-build hooks (shell, Bun scripts, webhooks). (new subcommands: `pokkum hook pre-build`, `pokkum hook post-build`; new flag: `--skip-hooks` on `build`)
- [ ] Image Provenance Timeline: `pokkum history <image>` linking back to CI runs and PRs. (new subcommand: `pokkum history <image>`, reuses `--output=json`)
- [ ] Multi-Generation Rollback History: `pokkum rollback` without `--to` now works (v1.0, via the `pokkum.dev/previous-image` manifest annotation) but is one hop deep — a second rollback just toggles back to what was just replaced, it can't reach an arbitrary earlier generation. Reaching N generations back needs a real build-history store — natural to build on top of the Image Provenance Timeline item above rather than duplicate its tracking.
- [ ] Policy as Code: `pokkum policy check` with Rego/OPA. (new subcommand: `pokkum policy check`, new flag: `--policy=<path>`)
- [ ] Service Mesh Integration: Auto-generate Istio/Linkerd configs and mTLS paths. (new subcommand: `pokkum mesh generate`, new flags: `--mtls`, `--mesh-telemetry` — named to avoid colliding with `build`'s OTel `--telemetry`)
- [ ] Progressive Deployment Strategies: Canary/blue-green deployments (mostly handled by Argo/Flux). (new subcommand: `pokkum deploy`, new flags: `--canary=<percent>`, `--blue-green`, `--auto-rollback`)
- [ ] Asset Optimization Pipeline: Automatic WebP/AVIF variants, cache-busting, CDN-origin separation. (new flag: `--optimize-assets` on `build`, opt-in given its build-time cost)
- [ ] Plugin System: Extensible via npm packages (high complexity/supply-chain risk). (new subcommands: `pokkum plugin add|list|remove <name>`)
