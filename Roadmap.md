# Pokkum Roadmap

## v0.1 (Completed)

- [x] Reproducible layer timestamps: set every layer/config timestamp to `SOURCE_DATE_EPOCH` derived from the last git commit (`git log -1 --pretty=%ct`), not build time
- [x] Minimal `securityContext` in generated manifests: Default to `runAsNonRoot: true`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault`.
- [x] `.pokkumignore`: Explicit exclude list so `.env.local`, test fixtures, and source maps don't end up embedded
- [x] Dry-run mode (`--dry-run` / `--print-manifest`): show what would be built/pushed/applied without doing it - added to `pipeline.go`
- [x] Structured, leveled logging so CI logs are parseable
- [x] Idempotent registry pushes: skip push if the computed digest already exists remotely (`remote.Head` before `remote.Write`)
- [x] Basic config validation surfaced as clear CLI errors before any network call — fail fast, not mid-push

## v0.2 (In Progress)

- [x] Cosign signing
- [x] SLSA provenance attestation
- [ ] SBOM as OCI 1.1 referrer instead of `.sbom` tag convention
- [ ] `pokkum dev` — Hot-Reload Container Development (build + `--local` + run in one command)
- [ ] Zero-Config Auto-Injection: Auto-injecting the adapter and `SOURCE_DATE_EPOCH` pinning without manual `svelte.config.js` edits.
- [ ] Base Image Lockfile (`pokkum.lock`): Enable reproducible base image resolving (distroless/chainguard) to prevent drift.

## v0.3: Layer Caching & Core Architecture Shift

_Refactoring to replace the `exe` adapter with a hand-rolled adapter for layer caching, dramatically reducing per-commit image size._

- [ ] M1: Packager + runtime plumbing (Bun runtime resolution, pinned downloads).
- [ ] M2: Hand-rolled SvelteKit adapter + Phase-1 layering (separating app JS from runtime).
- [ ] M3: Vendor splitting (`bun build --splitting`) + native closure support (unblocking native `.node` addons).
- [ ] M4: Hardening (`readOnlyRootFilesystem`) & cutover from the old `exe` adapter.
- [ ] Image Optimization: Deduplication across layers, optional zstd layer compression.

## v0.4: Unified Telemetry & Developer Experience

- [ ] Unified Metrics & Telemetry: Zero-config injection of OpenTelemetry setup for traces and metrics (`pokkum metrics` and app-side).
- [ ] `pokkum init`: Project Configuration Wizard (interactive or `--defaults`).
- [ ] `pokkum doctor`: Environment preflight checks (Bun version, registry auth, `svelte.config.js` sanity).
- [ ] Interactive Failure Diagnostics: Automatic log dump and exit code analysis on local container failure.
- [ ] `--output=json`: Machine-readable build results schema for robust CI parsing.
- [ ] Diff & Explain: `pokkum diff`, `pokkum explain`, and `pokkum why` to trace layer changes and dependencies.

## v0.5: Reproducibility & Diagnosis

_Closing the loop on reproducibility with verifiable rebuilds and non-determinism bisection._

- [ ] M0: Provenance completeness (recording Go version, builder OS/arch, lockfile hashes in SLSA statement).
- [ ] M1/M2: Stage recorder, bisection core, and attestation-check mode (`pokkum verify --no-rebuild`).
- [ ] M3: `layerdiff` component, L3 explanation, and `pokkum repro doctor` diagnostics.
- [ ] M4: `pokkum verify --rebuild` (L1/L2 compare) with `--perturb` mode, K8s + CI ergonomics.

## v1.0: MVP Launch (Supply Chain Hardening & Production Readiness)

_The shippable minimum viable product for enterprise-grade adoption._

### Supply Chain

- [ ] Trusted-Builder Mode: SLSA L3 via an isolated CI job (GitHub Actions provenance generation).
- [ ] CVE Scanning Integration (`pokkum scan`): Run Grype/Trivy against the final image in CI and fail the pipeline above a severity threshold.
- [ ] Toolchain CVE Awareness: OSV.dev advisory lookups keyed on embedded Bun version.
- [ ] Base Image Signature Verification: Verify upstream Cosign signatures on distroless/Chainguard base images at pull time.
- [ ] Secret-Inlining Guard: Build-time entropy/pattern scan to prevent leaked `.env` values in layers.
- [ ] Base image digest pinning + automated update PRs (Renovate/Dependabot-style).
- [ ] Standard OCI Annotations: Auto-populate `org.opencontainers.image.*` from git metadata.

### Cluster-side hardening

- [ ] `NetworkPolicy` generation restricting egress and ingress limited to expected ports.
- [ ] Resource `requests`/`limits` and a `PodDisruptionBudget` by default in generated manifests.
- [ ] `readOnlyRootFilesystem: true` where feasible (supervisor + compiled binary).
- [ ] Readiness Drain on SIGTERM: Supervisor holds `/readyz` at 503 while the app drains.
- [ ] Secrets via `envFrom` referencing Kubernetes `Secret`/external-secrets, never baked into image layers.

### Build integrity

- [ ] Hermetic builds: No network egress during the compile step (SLSA L3 requirement).
- [ ] Multi-registry support with per-registry auth chains (ECR/GCR/ACR/self-hosted).
- [ ] Ephemeral test registry (`pkg/registry.New()`) wired into CI for integration tests.

### Operational maturity

- [ ] Version pinning of `pokkum` itself in generated manifest annotations.
- [ ] A GitHub Action wrapping the CLI (mirroring `setup-ko`).
- [ ] Rollback support (`pokkum rollback` reading from build history).
- [ ] Signed Self-Distribution (`pokkum upgrade`): Signature verification of release artifacts.

## Beyond v1.0 / Backlog

_Features demoted or planned for later iterations._

- [ ] `pokkum adopt` (Migration Codemod): Auto-convert Vercel/Node adapter projects.
- [ ] Runtime Env Contract: Declare required env vars in image annotations; validate at startup.
- [ ] Monorepo Affected-Detection: Git-diff input tracking per `pokkum://` app.
- [ ] Static/Prerendered Page Optimization: Extract prerendered pages to a slim layer, `--static` Nginx mode.
- [ ] Multi-Environment Management: Config templating and secret-manager integrations.
- [ ] Hooks System: Pre/post-build hooks (shell, Bun scripts, webhooks).
- [ ] Image Provenance Timeline: `pokkum history <image>` linking back to CI runs and PRs.
- [ ] Policy as Code: `pokkum policy check` with Rego/OPA.
- [ ] Service Mesh Integration: Auto-generate Istio/Linkerd configs and mTLS paths.
- [ ] Progressive Deployment Strategies: Canary/blue-green deployments (mostly handled by Argo/Flux).
- [ ] Asset Optimization Pipeline: Automatic WebP/AVIF variants, cache-busting, CDN-origin separation.
- [ ] Plugin System: Extensible via npm packages (high complexity/supply-chain risk).
