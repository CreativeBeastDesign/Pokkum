# Additional Features

## Decision Matrix (re-evaluated 2026-08)

| Feature                                     | DX (1-10) | Security (1-10) | Cost (1-10) | Expected cost (explicit)                                                         | External Dependencies             | Priority |
| ------------------------------------------- | --------- | --------------- | ----------- | -------------------------------------------------------------------------------- | --------------------------------- | -------- |
| Live Cluster Annotation Inspection          | 8         | 2               | 4           | `kubectl get` pre-flight query in `pokkum apply` to seed deployment history      | `kubectl`                         | Done     |
| Monorepo Affected-Detection                 | 7         | 1               | 4           | Git-diff input tracking per `pokkum://` app                                      | None                              | Done     |
| Static/Prerendered Page Optimization        | 8         | 1               | 4           | Shipped v1.0: prerendered slim layer (layered) + `--static` served by embedded Go `pokkum-static` (no nginx) | Embedded Go `pokkum-static`        | Done     |
| Multi-Environment Management                | 7         | 3               | 5           | Config templating; secret-manager integrations                                   | Vault / AWS Secrets               | Medium   |
| Hooks System                                | 6         | 2               | 5           | Small CLI size; cross-platform shell exec; defuses Plugin System demand          | Shell / Bun                       | Medium   |
| Policy as Code                              | 5         | 6               | 7           | +15MB CLI size (embedding OPA/Rego); high policy maintenance                     | OPA / Rego                        | Low      |
| Service Mesh Integration                    | 5         | 4               | 6           | Moderate maintenance (Istio/Linkerd API churn)                                   | Istio / Linkerd                   | Low      |
| Progressive Deployment Strategies           | 6         | 2               | 9           | Massive maintenance burden; Argo/Flux own this space                             | Kubernetes (Argo/Flux)            | Low      |
| Asset Optimization Pipeline                 | 8         | 1               | 8           | Massive build time increase; heavy deps (`libvips`)                              | `sharp`, `@sveltejs/enhanced-img` | Low      |
| Plugin System                               | 8         | 1               | 9           | Extreme complexity; npm supply-chain risk undercuts Pokkum's own hardening story | npm                               | Low      |
| Base Image CVE Build Gate                   | 8         | 9               | 3           | Build-time OSV.dev lookup & configurable `--fail-on-cve` threshold               | OSV.dev API                       | Done     |
| Base Image Escrow / Mirroring               | 7         | 8               | 4           | Remote registry copy + signature verification against upstream repo              | Registry                          | Done     |
| Registry Credential-Helper Invocation       | 8         | 6               | 3           | Execute `docker-credential-*` binaries via `credHelpers`/`credsStore`            | None                              | Done     |
| Multi-Generation Rollback History           | 8         | 4               | 3           | `pokkum.dev/image-history` annotation parsing and generation selection           | None                              | Done     |
| Secret-Inlining Guard                       | 6         | 9               | 2           | Slight build-time CPU (layer scan); forces `--env=disable` in bundling           | None                              | Done     |
| `pokkum verify --rebuild`                   | 6         | 10              | 5           | One full rebuild per verification; requires pinned toolchain                     | None (git + registry)             | Done     |
| CVE Scanning Integration (`pokkum scan`)    | 9         | 8               | 4           | Syft package cataloging + OSV.dev query batch API                                | Syft / OSV.dev                    | Done     |
| Base Image Signature Verification           | 4         | 8               | 2           | +1 registry roundtrip per build; sigstore libs already vendored (cosign adapter) | None                              | Done     |
| `pokkum repro doctor`                       | 9         | 6               | 4           | Double build + per-layer diff, on demand only                                    | None                              | Done     |
| `pokkum doctor` (preflight)                 | 9         | 3               | 2           | <1MB CLI; low maintenance                                                        | None                              | Done     |
| Readiness Drain on SIGTERM                  | 7         | 3               | 1           | ~20 LOC in supervisor                                                            | None                              | Done     |
| `--output=json` Build Results               | 8         | 2               | 1           | Negligible                                                                       | None                              | Done     |
| Standard OCI Annotations                    | 6         | 4               | 1           | Negligible (git metadata already read for SOURCE_DATE_EPOCH)                     | None                              | Done     |
| Diff & Explain (`diff`, `explain`, `why`)   | 9         | 4               | 4           | Layer pulls for remote diffs; near-free for own reproducible layered images      | None                              | **Not Done — mis-marked**, see 🚩 row below |
| Kubernetes (extended manifests/GitOps)      | 8         | 5               | 5           | Raw-YAML `pokkum://` resolution **only** — the "Helm/Kustomize templating" this row's own cost estimate named was never built | None                              | **Partial (corr. 2026-08-17)** |
| Image Optimization (zstd, deduplication)    | 7         | 1               | 4           | Build-time CPU (compression); largely covered by layer-caching concept §10       | None                              | Done     |
| `pokkum init` (Config Wizard)               | 8         | 2               | 3           | Negligible CLI size (<1MB); low maintenance                                      | None                              | Done     |
| `pokkum adopt` (Migration Codemod)          | 8         | 2               | 5           | Codemod maintenance across adapter ecosystems (Node/Vercel/Dockerfile)           | None                              | Done     |
| `pokkum dev` (Hot-Reload, full)             | 9         | 1               | 8           | +5MB CLI size; high cross-OS maintenance; lite variant already on Roadmap v0.2   | Docker / Podman                   | Done     |
| Interactive Failure Diagnostics             | 8         | 1               | 4           | Negligible CLI size (<1MB); low maintenance                                      | None                              | Done     |
| Runtime Env Contract (validation)           | 7         | 5               | 2           | Image annotation + supervisor startup check; drop build-time injection half      | None                              | Done     |
| Toolchain CVE Awareness                     | 5         | 7               | 3           | OSV.dev advisory lookups keyed on embedded Bun version                           | OSV.dev API                       | Done     |
| Signed Self-Distribution (`pokkum upgrade`) | 5         | 7               | 3           | Release signing infra; verify-on-update logic                                    | cosign / minisign                 | Done     |
| Log Aggregation (app-side, trace context)   | 7         | 2               | 3           | Trace-context JSON logging in adapter/runtime; CLI half already shipped          | None                              | **Not Done — mis-marked (corr. 2026-08-17)**: no `trace_id` correlation exists anywhere; folds into the OTel row below |
| Built-in Metrics Endpoint (supervisor)      | 7         | 2               | 3           | Supervisor-level `/metrics` only; app metrics already shipped via OTel           | None                              | **Not Done — mis-marked (corr. 2026-08-17)**: `supervisor/` has no `/metrics` handler; only `/livez`+`/readyz`. App-side OTel metrics and the `pokkum metrics` CLI *are* shipped |
| Image Provenance Timeline                   | 6         | 5               | 5           | Registry API reliance; partially subsumed by `verify` + OCI annotations          | Registry (SLSA lookup)            | Done     |
| Verify Base on Cache Hit                    | 3         | 7               | 2           | One Cosign/keyless verify per cache hit; opt-in strict; decoupled from `--cache-verify` | None (sigstore already vendored) | Backlog  |
| Layered-Strategy Runtime Hardening          | 5         | 8               | 4           | Option A compiled stub launcher (`--stub-launcher`) + Option C supervisor startup attestation (`POKKUM_ATTESTATION_DIGEST`) | None | Done     |
| Dedicated `chainguard-static` Preset (new 2026-08-16) | 4 | 5             | 2           | Small enum-driven change; one self-healing orphaned `pokkum.lock` entry per existing `--static` user | None | Low-Medium |

### Added 2026-08-17 (external review — see [audit](#external-review-audit-2026-08-17))

Priority column uses the `Roadmap.md` flag vocabulary: **🚩 Blocker** = do not publish without it, **⚠️ Risk** = fix or reword the claim before publishing.

| Feature                                     | DX (1-10) | Security (1-10) | Cost (1-10) | Expected cost (explicit)                                                         | External Dependencies             | Priority |
| ------------------------------------------- | --------- | --------------- | ----------- | -------------------------------------------------------------------------------- | --------------------------------- | -------- |
| Real `explain`/`why`/`diff` (de-stub)       | 9         | 3               | 5           | Remote layer pulls + tar walks; near-free for own reproducible layered images. Or delete the commands | None                              | 🚩 Blocker |
| Bun checksum coverage (all versions)        | 2         | 9               | 2           | Per-release SHA-256 table or fetch-and-verify against Bun's published `SHASUMS`; fail closed on unknown | Bun release assets                | 🚩 Blocker |
| `$env/static/*` Detection + `baked-env`     | 7         | 7               | 5           | Vite-build import analysis + OCI annotation; codemod half is optional             | None                              | 🚩 Blocker |
| `ORIGIN` / Proxy / Body-Limit Contract      | 9         | 5               | 3           | Flag plumbing to image env + one supervisor fail-fast check                       | None                              | 🚩 Blocker |
| `.pokkum/` Relative-Path Correctness        | 8         | 2               | 4           | Pin Vite `root`, rewrite `resolve.alias`, neutralize `__dirname`/`import.meta.url` | None                              | 🚩 Blocker |
| Pinned `klauspost` gzip for Layers          | 4         | 3               | 1           | One import swap + pinned encoder settings; dependency **already in `go.mod`**     | None (already vendored)           | ⚠️ Risk |
| Drop `.zst` from Layered Path               | 1         | 1               | 1           | Delete a call site; keeps `.zst` for `--strategy=static`                          | None                              | ⚠️ Risk |
| Real Readiness + `startupProbe`             | 6         | 4               | 4           | Supervisor HTTP proxy to app path + manifest probe generation                     | None                              | ⚠️ Risk |
| OTel Route-Template Span Naming             | 7         | 2               | 4           | `handle` hook reading `event.route.id`; `fetch` propagation; trace-correlated logs | None                              | ⚠️ Risk |
| OpenVEX Exemptions (expiry + justification) | 7         | 7               | 5           | OpenVEX parse/validate + attestation recording; refuse expired entries            | OpenVEX spec                      | ⚠️ Risk |
| Bun as SBOM Component                       | 3         | 6               | 2           | One synthetic component per SBOM format; provenance half already done             | None                              | ⚠️ Risk |
| `--referrers-mode=auto` Probe + Fallback    | 7         | 3               | 3           | One capability probe per push, cached; silent tag fallback                        | None                              | ⚠️ Risk |
| Rolling-Deploy Asset Overlay                | 9         | 2               | 6           | Pull prior generations' client layer by digest + merge; must join the cache key   | Registry                          | **High (moat)** |
| Sub-Second Cluster Dev Loop                 | 10        | 1               | 7           | 6a local-process mode is cheap; 6b in-pod sync needs K8s exec/cp plumbing         | Kubernetes API                    | **High (moat)** |
| `--runtime=node`                            | 7         | 4               | 6           | Second runtime resolver + base preset + lock key + CVE lookup; ~doubles reach     | Distroless Node base              | **High** |
| Supply-Chain Completions (KMS/OIDC/TUF/…)   | 4         | 8               | 6           | Six independent sub-items; 9a/9b are the two that change the SLSA claim           | Cloud KMS, Sigstore TUF, CI OIDC  | Medium |
| Registry & Runtime Ergonomics               | 8         | 3               | 6           | Unglamorous; per review ~⅓ of real-world failures live here                       | ECR/GAR/Harbor APIs               | Medium |
| Monorepo Shared Vendor Cache                | 6         | 1               | 5           | Extend `layercacheutils`; **verify the gap in code first** — asserted from docs   | None                              | Low-Medium |
| Source Maps as OCI Referrer                 | 7         | 4               | 3           | Strip + attach as artifact keyed to digest; enables Sentry release tagging        | Registry (referrers)              | Low-Medium |
| `.secretguardignore` / inline allow comment | 6         | 4               | 2           | Scoped annotation parsing; complements the existing blunt `--allow-secret-pattern` | None                              | Low-Medium |
| Helm Post-Renderer + Kustomize KRM          | 8         | 2               | 5           | Two thin entrypoints over existing `resolve`; unlocks the two dominant toolchains | Helm / kustomize                  | Low-Medium |
| Build-Time Test Gate (`--test`)             | 6         | 3               | 3           | Subprocess + exit-code gate; conflicts with `--hermetic` semantics                | None                              | Low |
| ~~Enforced Hermetic (netns/Landlock/seccomp)~~ | — | — | — | ✅ **Shipped (2026-08-17)** — real Linux network-namespace enforcement, see `Roadmap.md`'s PR-2 entry | Linux kernel features | Done |
| Edge / WASM Runtimes                        | 6         | 2               | 9           | Not an OCI image; a different product                                             | CF Workers / Deno / Vercel        | **Out of scope — state it** |

### Shipped (removed from matrix)

- **OpenTelemetry Auto-Instrumentation** — shipped with the unified OTel/metrics work (`--telemetry`, `--metrics-only`, `--with-otel-sidecar`; see `Meantime.md`).
- **Log Aggregation, CLI half** — `--log-format=json|text` and leveled logging shipped in v0.1; only app-side trace-context logging remains (row above).

## Feature list

### `pokkum dev` - Hot-Reload Container Development

- Runs the OCI image locally (Docker/Podman) with file-watching and live reload
- Mounts the source directory as a volume, recompiles and restarts the container on changes
- Mirrors the production environment exactly (same base image, supervisor, probes)
- Supports --debug flag to drop into a shell inside the distroless container for troubleshooting

### Interactive Failure Diagnostics

When the container exits with a non-zero status during local testing, automatically prints

- Container logs (even from distroless, by capturing stdout/stderr)
- Exit code analysis with common fixes (e.g., "Exit code 137: OOMKilled - consider increasing memory limits")
- Supervisor logs with timing breakdown (startup duration, shutdown sequence)

### `pokkum init` - Project Configuration Wizard

- Interactive or `--defaults` mode
- Detects existing adapters (Vercel, Cloudflare, Node) and suggests optimal settings
- Generates .pokkum.yaml with sensible defaults

### Environment Variable Injection & Validation

- `--env-file` to inject variables at build time (baked in) vs runtime (expected at deploy)
- `--validate-env` to check that all required variables are documented
- Automatic `.env.example` to Kubernetes ConfigMap/Secret generation

### Hooks System

- Pre-build hooks: `pokkum hook pre-build` for database migrations, asset compilation
- Post-build hooks: `pokkum hook post-build` for smoke tests against the new image
- Support for shell scripts, Bun scripts, or remote webhooks

### Static/Prerendered Page Optimization

- Extract prerendered pages to a separate slim OCI layer (`/app/prerendered`), served via the patched `handler.js` honoring `POKKUM_PRERENDERED_DIR` (layered strategy)
- Generate immutable Cache-Control headers on prerendered assets based on build output
- `--static` flag compiling a fully static site onto a minimal libc-free `chainguard/static` image served by the embedded Go `pokkum-static` PID-1 file server (zero JS/Bun runtime, no nginx)

### Asset Optimization Pipeline

- Automatic image optimization with sharp/@sveltejs/enhanced-img
- Generate WebP/AVIF variants and responsive srcsets at build time
- Cache-busting with content hashes built into the image layers
- Optional CDN-origin separation (assets to S3/R2, app to K8s)

### CVE Scanning Integration

- `pokkum scan` subcommand using Trivy/Grype on the built image
- Fail build on critical/high CVEs with `--fail-on=critical`
- Generate VEX (Vulnerability Exploitability eXchange) documents
- Diff mode: "3 new vulnerabilities introduced since last build"

> **Note, 2026-08-17.** Both external reviewers independently identified the missing VEX support as a top-tier gap — and the third bullet above shows it was **already recorded here and never built**. Grepping `internal/` and `cmd/` for `vex`/`exemption` returns nothing. The scanning and gating halves shipped; the exemption half did not, which is precisely the combination the first reviewer warns produces `--fail-on-cve=critical`, then `--allow-incomplete`, then nothing. Now tracked as **PR-6** / Roadmap item 8, upgraded from "generate VEX documents" to "*consume* OpenVEX with mandatory expiry, justification, and owner" — consuming is what makes the gate survivable; generating is a nice-to-have. Diff mode ("N new vulnerabilities since last build") also remains unbuilt.

### Policy as Code

- `pokkum policy check` with Rego/OPA policies
- Built-in policies for common compliance frameworks (PCI-DSS, SOC2)
- Custom policy support: "No images running as root", "Must include SBOM"

### Image Provenance Timeline

- `pokkum history <image>` to show full build chain
- Link back to GitHub Actions run, commit SHA, and PR
- Verify SLSA provenance against the source repository

### Progressive Deployment Strategies

- `pokkum deploy --canary=10%` for canary deployments
- `pokkum deploy --blue-green` with traffic shifting
- `--auto-rollback` to automatically rollback on health probe failure

### Multi-Environment Management

- `pokkum config env` for managing staging/production variants
- `--env=staging` to apply environment-specific overrides
- Secure secret injection from 1Password/Vault/AWS Secrets Manager

### Service Mesh Integration

- `pokkum mesh generate` to auto-generate Istio/Linkerd sidecar configurations
- `--mtls` to configure mTLS certificate paths and traffic policies
- `--telemetry` to add service mesh telemetry annotations

### OpenTelemetry Auto-Instrumentation

- `--with-otel` flag to inject OpenTelemetry SDK
- Auto-instrument HTTP requests, database queries, and fetch calls
- `--otel-export` to configure OTLP endpoint and telemetry hooks

### Built-in Metrics Endpoint

- `pokkum metrics` to expose Prometheus metrics on `/metrics`
- Request duration, error rates, GC pauses, event loop lag
- Custom metrics API for application-level telemetry

> **Status correction, 2026-08-17.** The app-side OTel metrics pipeline and the `pokkum metrics` CLI subcommand are real. The **supervisor-level `/metrics` endpoint this row was scoped to is not** — `supervisor/cmd/pokkum-init/probe.go` registers only `/livez` and `/readyz`. Neither Bun runtime metrics (event-loop lag, heap) nor cgroup limits are exported from PID 1; see Roadmap items 10d and the second reviewer's "instrument Bun itself" point.
>
> Separately, the first reviewer's ergonomic objection is fair and cheap to act on: `pokkum metrics` *reads* like a client-side scraper rather than a server the image exposes. Folding it into build flags and documenting the port contract would be clearer (Roadmap item 11).

### Log Aggregation

- Structured JSON logging with trace context injection
- `--log-format=json` for production, `--log-format=pretty` for development
- Direct integration with Grafana Loki/CloudWatch/ELK

> **Status correction, 2026-08-17.** The CLI half (`--log-format=json`, leveled logging) shipped in v0.1 and is real. The **app-side trace-context half was never built**: `trace_id`/`traceId` appears nowhere in `internal/` or `supervisor/` outside OTel sampler class names. Structured JSON logging correlated to `trace_id` is, per the first external review, "the thing people actually use" and is missing from `Feature-list.md` entirely. Tracked with the OTel work as **PR-5**.

### Diff & Explain

- `pokkum diff image:v1 image:v2` to show layer changes
- `pokkum explain image` to break down layer sizes and contents
- `pokkum why <dep>` to trace why a specific package is included

> **Status correction, 2026-08-17 — 🚩 PUBLISH-BLOCKER (PB-1).** All three commands exist and are wired into the CLI, but every one of them returns **hardcoded fabricated data**. `cmd/pokkum/explain.go:47` returns a literal `[]ports.LayerExplain` with `Digest: "sha256:base..."` and invented sizes; `:100` hardcodes `"layer_index": 3` for whatever path is passed to `why`; `:130` hardcodes `"modified": ["Layer #3 (App JS)"]` for `diff`. The 143-line file contains no `remote.`, no `Fetch`, no tar walk — it never touches an image. It also hardcodes a **5**-layer model while `Feature-list.md:12` documents **8**.
>
> Two valid resolutions, both acceptable; shipping the current state is not:
> 1. **Implement for real.** Cheap for Pokkum's own images — `layerdiffutils` and `comparator` already do genuine layer-by-layer tar diffing for `pokkum verify` L3, and `BuildDirectoryTreeLayerWithPruning` already emits per-layer file records. Most of the machinery exists; only the command wiring is fake.
> 2. **Delete the three commands** and remove `Feature-list.md:92`, deferring to item 11's core-vs-adjacent split.
>
> Resolution 1 also answers the second external reviewer's request for layer-churn visibility ("which of the 8 layers actually changed?"), which is the strongest item in that review — they assumed the command worked and asked for a sub-mode.

### Image Optimization

- `pokkum optimize` with deduplication across layers
- Shared base layer for monorepo builds
- `--slim` mode that removes dev dependencies, tests, docs

### Plugin System

- Extensible via npm packages (pokkum-plugin-*)
- Community plugins for custom adapters, preprocessors
- Plugin marketplace similar to esbuild/Vite

### Kubernetes (extended)

- `pokkum k8s generate` – Beyond basic Deployment/Service, e.g. Ingress generation, ConfigMap/Secret wiring, and ServiceAccount creation
- Rollout strategies: Add strategy: rollingUpdate with maxSurge/maxUnavailable defaults.
- Pod disruption budgets and HorizontalPodAutoscaler presets as an option
- GitOps integration: Instead of direct apply, allow exporting to a Helm chart or Kustomize overlay that can be used by ArgoCD/Flux.

> **Status correction, 2026-08-17.** The hardening, `NetworkPolicy`, PDB, resource-defaults, rollback, and `pokkum://` resolution work all shipped and is real. The **Helm/Kustomize GitOps bullet above did not** — `pokkum resolve` handles `pokkum://` in raw YAML only, which as the first reviewer notes covers Knative-style repos "and roughly nobody else." Most teams template with Helm or Kustomize and will therefore never reach `pokkum apply`. A `--post-renderer` mode and a KRM function are now tracked in the Long-term Backlog on `Roadmap.md`. `pokkum k8s generate`, Ingress/ConfigMap/ServiceAccount generation, and HPA presets also remain unbuilt.

### Performance & Size Optimizations

- Compression: Use zstd compression for OCI layers if the registry supports it (go-containerregistry does). Reduces image size and push time.
- Deduplication: If multiple images are built in parallel, share cached layers.
- Static asset handling: SvelteKit's build/client can be huge; ensure they are compressed (e.g., gzip/brotli) and marked with correct MIME types, and perhaps set immutable cache headers if served by your supervisor.
- Minify/obfuscate server-side code using Bun's minifier (though not always desirable; allow opt‑in).

### Additional Features beyond core

- `pokkum scan` – Wrapper around vulnerability scanners (Trivy, Grype) to run after build.
- `pokkum licenses` – Display license compliance of npm dependencies (using license-checker or license-webpack-plugin).
- `pokkum compare` – Compare two images (e.g., previous vs new) for size and dependencies diff.
- `pokkum serve` – Standalone "container runtime" that executes the image directly on the host (like podman), useful for testing without a daemon.
- `pokkum export sbom` – Export SBOM separately without building, useful for compliance pipelines.

### `pokkum verify --rebuild` — Reproducible Rebuild Verification

- Independently rebuild from a given git ref and confirm the digest matches what is in the registry or running in the cluster
- Turns reproducibility from an implementation detail into a verifiable security claim: "the deployed image provably came from this commit"
- Complements (does not replace) Cosign/SLSA: signatures prove _who_ built it, rebuild proves _what_ it was built from
- Requires pinned toolchain versions (Bun, Go/gzip caveat per README known limits) to rebuild faithfully

### Secret-Inlining Guard

- Bun's bundler can inline build-machine environment variables into output — a CI runner with cloud credentials exported can silently bake them into the image
- Force `--env=disable` during bundling, then run a pattern scan over final layer contents before push. **As actually shipped: five fixed regex patterns** (private key headers, AWS access keys, GitHub PATs, Google API keys, generic password/secret/token assignments) — not Shannon entropy analysis; a gitleaks-style entropy scan was the original design language here but was never built, and remains tracked as future work rather than implied covered.
- `.pokkumignore` protects files; this protects against the bundler and build environment themselves
- Since 2026-08-18, scans the actual packaged build output too, not just pre-build source (scoped per strategy; a compiled `exe` binary itself is a documented gap — text-scanning can't see inside it)
- Fail the build on findings, with `--allow-secret-pattern` escape hatch for false positives; a file too large/unreadable to scan also fails the build (`ErrSecretScanIncomplete`) rather than reporting a false-clean pass

### `pokkum repro doctor` — Non-Determinism Bisection

- README known-limit: an unpinned `kit.version.name` silently breaks reproducibility with no warning
- Build twice, compare digests; on mismatch, diff layer-by-layer and file-by-file, and explain the likely cause (version.json timestamp, unsorted output, embedded dates)
- One command turns the worst silent footgun into an actionable diagnosis

### Base Image Signature Verification

- Verify upstream Cosign signatures on distroless (Google) and Chainguard base images at pull time; fail on mismatch
- Pokkum signs its outputs but currently trusts its inputs — this completes the chain of custody
- Sigstore libraries are already vendored for the cosign adapter, so the cost is small

### Toolchain CVE Awareness

- Pokkum records which Bun version is embedded in every image it builds (SLSA resolvedDependencies)
- Query OSV.dev for advisories against that version: "images built with Bun ≤ X.Y are affected by CVE-Z" — without pulling or scanning any image
- Natural extension of `pokkum scan` diff mode

### Signed Self-Distribution

- `pokkum upgrade` with signature verification (cosign/minisign) of release artifacts
- Publish SLSA provenance for Pokkum's own releases — a build tool is itself a supply-chain target
- The GitHub Action should pin and verify the CLI it downloads

### `pokkum doctor` — Environment Preflight

- Proactive counterpart to Interactive Failure Diagnostics: check everything that currently fails mid-build or silently
- Bun version ≥ required, adapter installed, `svelte.config.js` sanity (version pin present!), registry auth works, target platform availability
- `--fix` mode for mechanical repairs (add version pin, generate `.pokkumignore`)

### `pokkum adopt` — Migration Codemod

- Detect adapter-node/Vercel/Cloudflare/Dockerfile projects and convert: rewrite `svelte.config.js`, generate `.pokkumignore`, optionally remove the Dockerfile
- Optimizes the first-five-minutes experience — where ko-alike tools win or lose adoption

### `--output=json` Build Results

- Machine-readable build _results_ (digest, per-layer sizes, SBOM path, attestation refs) as distinct from JSON _logs_
- CI pipelines currently must parse stdout for the digest; this removes that fragility
- Stable schema, versioned

### Standard OCI Annotations

- Auto-populate `org.opencontainers.image.{source,revision,created,licenses,version}` from git metadata already read for `SOURCE_DATE_EPOCH`
- Makes registry UIs, scanners, and Renovate-style tooling work correctly with Pokkum images for near-zero cost

### Readiness Drain on SIGTERM

- Supervisor flips `/readyz` to 503 immediately on SIGTERM and holds while the app drains, so Kubernetes removes the pod from endpoints before in-flight connections are killed
- Highest-value ~20 lines in the supervisor if not already implemented

### Runtime Env Contract

- Declare required env vars in an image annotation at build time; supervisor validates presence at startup and fails fast with the named missing list
- Replaces the build-time injection half of the old "Env Var Injection & Validation" item, which is a secret-baking footgun and should be dropped

### Monorepo Affected-Detection

- `pokkum resolve` on a manifest with several `pokkum://` refs git-diffs each app's input tree and skips unchanged apps entirely
- Stronger than digest-HEAD skipping: no build at all, not just no push

### Live Cluster Annotation Inspection for `pokkum apply`

- Performs a pre-flight `kubectl get` query on target workload resources before manifest resolution
- Reads currently deployed `pokkum.dev/image-history` or active container images from the cluster
- Seeds historical annotations so multi-generation rollback works reliably even when deploying from static, uncommitted `pokkum://` manifest templates across independent CLI runs

### Base Image CVE Build Gate

- Actively queries OSV.dev for vulnerabilities affecting the locked base image during `pokkum build`
- Enables breaking CI/CD pipelines on discovered CVEs with `--fail-on-cve=critical|high|medium|low` or `POKKUM_FAIL_ON_CVE`
- Fails closed on incomplete vulnerability database lookups (`--allow-incomplete` to opt out)
- Automatically persists `last_scanned_at`, `vulnerabilities_count`, and `max_severity` into `pokkum.lock`

### Base Image Escrow / Mirroring

- `--mirror-registry=<repo>` on `pokkum base update` mirrors base image indexes and Cosign `.sig` tags to a project-controlled registry
- Verifies Cosign signatures against canonical upstream repo references while pulling image blobs from mirrors
- Guarantees long-term reproducibility against `:latest` tag drift and upstream registry image pruning

### Verify Base on Cache Hit (`--verify-base-on-cache-hit`)

A confirmed remote-cache hit (`BuildResult{Cached:true}`) deliberately **skips** base-image signature
verification (`BaseImageResolver.VerifyBaseImage`) — nothing is built from the base image on a hit, and the
composite input hash already binds the base image *digest* (`base.Digest`, pinned via `pokkum.lock`) into the
cache key, so a hit can only match the exact base the verifier would have checked. The skip is disclosed with an
explicit audit log line on the hit path.

This flag adds an **opt-in, strict** escape hatch for supply-chain-audit environments that require the uniform
property "every emitted/promoted image traces to a verified base, hit or miss":

- When set and a cache hit is confirmed, run `VerifyBaseImage` (the *base* check) before accepting the hit.
- Closes the one narrow case the audit log cannot: a base whose pinned digest still matches (so cache hits keep
  firing) but whose signature was later revoked/rekeyed/withdrawn — only re-running verification would notice.
- **Structurally independent** of `--cache-verify`: that flag authenticates the cache-hit *output* image
  (anti-poisoning), whereas this flag authenticates the *base* image. The two must not couple.
- Tracking: Long-term Backlog on `Roadmap.md`. Deferred until a real consumer (an org with a strict
  SBOM/SLSA attestation requirement) asks for it, to avoid feature-creep in the already-dense verification
  surface. Opt-in so the sub-100ms fast path is preserved by default. (new flag: `--verify-base-on-cache-hit`
  on `build`)

### Layered-Strategy Runtime Hardening (Shipped)

- `--strategy=layered` (default since v0.3) ships stock `bun`, which exposes its full CLI via `BUN_BE_BUN=1` — an attacker with an existing exec primitive can run arbitrary scripts, unlike v0.1's sealed compiled-exe strategy
- Two composable mitigations shipped 2026-08-17:
  - **Option A (Compiled Stub Launcher)**: compiles a minimal non-foldable entrypoint launcher (`const p = "/app/server/" + "index.js"; await import(p);`) via `--stub-launcher` (`POKKUM_STUB_LAUNCHER`), cached per `(version, variant, platform)`.
  - **Option C (Supervisor Startup Attestation)**: packager computes a SHA-256 root digest over the authoritative `/app` tree (`POKKUM_ATTESTATION_DIGEST`) and `pokkum-init` re-derives and verifies it at startup, exit **125** on mismatch (126 is reserved for the pre-existing binary-exec-failure meaning; see `Vocabulary.md` §19).
- Tracking: Completed on `Roadmap.md` (see `concepts/archive/layered-strategy-runtime-hardening-concept.md` and `concepts/archive/pokkum-layer-caching-concept.md` §5.2)

### Dedicated `chainguard-static` Base Image Preset

- `--static`'s default base currently reuses the `BaseImageChainguard` preset (fixed 2026-08-16 from an original `BaseImageDistroless` misassignment that broke signature verification on every default `--static` build) — correct for signature identity, but leaves a narrow `pokkum.lock` collision between an explicit `--base cgr.dev/chainguard/glibc-dynamic` build and a `--static` build in the same project
- A dedicated `BaseImageChainguardStatic` preset would eliminate the collision entirely, at the cost of one new CLI-visible preset name and a self-healing orphaned-lockfile-entry migration note for existing `--static` users
- Tracking: Recommended Next Steps (Low-Medium Priority) on `Roadmap.md`; full design in `concepts/new-chainguard-static-preset-concept.md`

---

## SvelteKit Runtime Semantics (added 2026-08-17)

_The category both external reviews identified as the structural gap: everything currently shipped would apply equally to a Next.js or Remix packager. These four items are the places where SvelteKit is genuinely weird in production, and they are where the bug reports will come from._

### `$env/static/*` Detection & the `baked-env` Annotation (🚩 PB-3)

SvelteKit inlines `$env/static/private` and `$env/static/public` **at build time**. `PUBLIC_*` values are baked into client bundles and prerendered HTML; `$env/dynamic/*` is read at runtime. Nothing in Pokkum currently detects this (`grep -r 'env/static' internal/ cmd/` → zero hits).

Why this is worse than any CVE currently gated on:

- A project importing from `$env/static/private` produces an image **pinned to one environment**. Build-once-promote-dev→staging→prod is impossible, and *every* downstream guarantee — digest pinning, `pokkum resolve`, `pokkum rollback`, SLSA provenance — quietly becomes per-environment without saying so.
- Secrets from `$env/static/private` are baked into the server bundle. `secretguard` may catch some by its five fixed regex patterns (it does not do entropy analysis — see the Secret-Inlining Guard section below), but pattern matching is the wrong layer for this specific problem regardless: the correct response is "your architecture makes this image environment-specific," not "this string looks random" or "this string matches a known key shape."
- `PUBLIC_API_URL` baked into a client chunk means a carefully-signed immutable digest is environment-specific with **nothing in any annotation** revealing it. A user can promote to prod and silently ship staging's API URL.

Design:

| Piece | Behavior |
| ----- | -------- |
| Detection | Static-analyze imports from `$env/static/*` during the Vite build. Must run on the real build output, not a source-text guess — re-exports through a local module will otherwise be missed (the same limitation `EffectiveAdapterConfigured` already documents for adapters) |
| Annotation | `pokkum.dev/baked-env` listing every statically-inlined **key** (never values), so `pokkum history` can report "this image has `PUBLIC_API_URL` baked in" |
| `--env-mode=strict` | Fail the build on any `$env/static/private` import, with a codemod suggestion pointing at `$env/dynamic/private` |
| `--env-mode=warn` | **Default.** Log and annotate, do not fail |
| `--env-mode=off` | Escape hatch |
| `pokkum adopt` | Rewrite static→dynamic where trivially safe |

Interacts with the composite input hash: the set of baked keys is part of what makes an image what it is, so it belongs in the cache key. Otherwise a cache hit can serve an image baked against a different environment.

### `ORIGIN`, CSRF & Proxy Header Contract (🚩 PB-4)

The single most common SvelteKit-behind-an-ingress failure: form actions return **`403 Cross-site POST form submissions are forbidden`** because `ORIGIN` is unset and the app cannot reconstruct its public URL. adapter-node's documented contract is `ORIGIN`, `PROTOCOL_HEADER`, `HOST_HEADER`, `ADDRESS_HEADER`, `XFF_DEPTH`, and `BODY_SIZE_LIMIT`. **None of these six strings appears anywhere in the codebase** — `internal/`, `cmd/`, or `supervisor/`.

`--require-env` is a generic mechanism; what is needed here is *opinions*:

| Piece | Behavior |
| ----- | -------- |
| `--origin=https://example.com` | Stamps `ORIGIN` into the image env |
| `--trust-proxy` | Sets the header trio to sane ingress defaults (`x-forwarded-proto` / `x-forwarded-host`) and sets `XFF_DEPTH` to match the hop count implied by the generated manifests |
| `--body-size-limit=<n>` | adapter-node defaults to **512KB**. Users uploading files hit this and have no idea why. At minimum surface the default; ideally make it settable |
| Build-time warning | If the app has form actions and no `ORIGIN` strategy is configured, warn — this is detectable from the SvelteKit build output |
| Supervisor fail-fast | A POST-capable app booting with neither `ORIGIN` nor trusted-proxy headers configured should fail with a **readable** message, reusing the existing `--require-env` enforcement path |

### `.pokkum/` Relative-Path Correctness (🚩 PB-5)

`PrepareVirtualViteConfig` writes the transformed config to `<projectDir>/.pokkum/<viteConfigName>` and runs `bun x vite build --config .pokkum/<name>`. `rewriteRelativeImportSpecifiers` (`injector.go:416`) correctly prepends `../` to relative **import specifiers** — but that is the only relocation-aware rewrite, and it is not sufficient:

| Construct | Status | Failure |
| --------- | ------ | ------- |
| `import x from './foo.js'` | ✅ Handled | — |
| `path.resolve(__dirname, './src/lib')` | ❌ Not handled | Resolves to `<project>/.pokkum/src/lib`. **This is the near-universal alias idiom** |
| `import.meta.url`-derived paths | ❌ Not handled | Same failure mode |
| `resolve.alias` with bare relative strings | ❌ Not handled | Resolution point shifts |
| Vite `root` | ⚠️ Incidentally correct | Defaults to `process.cwd()`, and `cmd.Dir = req.ProjectDir`, so it currently lands right — but by accident, not by pin. Pin it explicitly so a future change to `cmd.Dir` cannot silently break it |
| `envDir`, `publicDir`, `build.outDir`, `css.postcss` | ❌ Unaudited | Each is a relative-path option with its own resolution base |

Zero-config injection is the headline DX feature and the thing a new user meets first. It must not break on the common config shape. The fix is not more regex whack-a-mole: pin `root` explicitly, and prepend a small preamble to the generated config that restores `__dirname`/`import.meta.url` to the *original* config's directory rather than `.pokkum/`.

**Note on provenance of this item:** the external reviewer arrived at it from a wrong premise (see the audit below) — they believed the adapter lives in `svelte.config.js` and that relocating a *Vite* config was the mistake. The premise is wrong; the path-resolution consequence they predicted is real. Worth recording as an instance of a correct finding reached by faulty reasoning, which is exactly the kind of claim that gets dismissed with its premise if not verified independently.

### Rolling-Deploy Asset Overlay (`--asset-overlay`) — ✅ Shipped 2026-08-18

SvelteKit's client polls `/_app/version.json`. During a rolling update across N replicas, a browser holding v1's HTML requests `/_app/immutable/chunks/<hash>.js`, gets routed to a v2 pod, and receives a **404 → white screen**. `updated.check()` improves the UX but does not close the 404 window, and it is worse with prerendered pages and long-lived tabs.

Nobody solves this well, and Pokkum is unusually positioned to because it already controls layer composition. **As shipped, this does NOT read `pokkum.dev/image-history`** as originally sketched below — that annotation is written and read exclusively by `internal/adapters/k8s` (`resolve`/`apply`/`rollback`), and `pokkum build` has no code path to parse a Kubernetes manifest at all, so a build-time flag structurally cannot depend on cluster state without either coupling `build` to Kubernetes or requiring every caller to already be using `resolve`/`apply`. The actual mechanism, entirely registry-side and Kubernetes-independent:

1. Every image pushed with `--asset-overlay` set stamps a new `pokkum.dev/predecessor` OCI manifest annotation naming the digest it replaced at the same push target.
2. `--asset-overlay=<n>` walks that chain backward via repeated `remote.Get` calls, up to N generations deep, self-bootstrapping (first-ever push to a target has no predecessor: 0 generations, not an error). `--asset-overlay-from=<ref1>,<ref2>,...` is an explicit override for hand-supplied refs.
3. Each resolved generation's `/app/client` layer is pulled by digest and only content under `_app/immutable/` is extracted.
4. Non-conflicting hashed files are merged into a **separate** overlay layer, appended to the current build.
5. Because the merged bytes are byte-identical to what the node already pulled, the layer dedupes at the registry and on the node.

Cost is near zero. Benefit is zero-downtime rolling deploys that actually work. Per the first reviewer, "a bigger differentiator than anything in your security section" — and given how dense the security surface already is, that judgment is worth weighing seriously.

Design questions — resolved:

- The overlay's source digests **do** join the composite input hash (`ports.RemoteCacheInputRequest.AssetOverlaySourceDigests`, sorted before hashing, mirrors `BaseImageDigest`'s treatment) — a cache hit that differs only in resolved predecessors now produces a different cache key.
- Conflict policy: same hashed path, different bytes, across generations is a **hard build failure** (`core.ErrAssetOverlayConflict`), never a silent pick, exactly as scoped.
- **Interaction with `pokkum verify --rebuild`: still genuinely unresolved, not silently glossed over.** `pokkum verify` has no `--asset-overlay`/`--asset-overlay-from` flags today, and its rebuild path does not attempt to reproduce the overlay layer at all — running `pokkum verify` against an image that was built with `--asset-overlay` will report the overlay layer's content as a digest mismatch against a plain rebuild, even though the image is legitimate. Closing this needs `verify` to either accept the same `--asset-overlay-from` refs the original build used, or read them back off the image's own `pokkum.dev/asset-overlay-sources` annotation and re-resolve them automatically before rebuilding. Tracked as a follow-up, not yet built.

---

## External Review Audit (2026-08-17)

Two independent external reviews of `Feature-list.md` were received and each claim verified against the code. Recording the verdicts here so that (a) confirmed gaps have a single source, and (b) **nobody spends time "fixing" the three claims that are wrong.**

### Claims verified WRONG — do not act on these

| Claim | Source | Why it's wrong |
| ----- | ------ | -------------- |
| "The `sveltekit()` Vite plugin doesn't take an adapter argument — the adapter is configured in `svelte.config.js` and the plugin reads it from there. Either your doc is imprecise or something's off in the implementation." | Review 1, Tier 1 #5 | **`Feature-list.md:15` is accurate and Pokkum's model is more sophisticated than the reviewer's.** Current `sv create` scaffolds emit **no `svelte.config.js` at all** and configure the adapter exclusively via `sveltekit({ adapter: adapter() })` in `vite.config.ts`. `project.go:152-183` documents this; `project_test.go:350-370` holds a real captured scaffold; and it was confirmed empirically by a real `bun install` + `bun run build` against a decoy `svelte.config.js`, with SvelteKit printing `svelte.config.js is ignored when options are passed via your Vite config`. This is already logged in `Lessons.md` (2026-08-17) and fixed in Roadmap §0. |
| "You already set `runAsNonRoot`, but not `readOnlyRootFilesystem: true` with explicit writable paths" | Review 2, Medium-Priority table | **Already implemented.** `internal/adapters/k8s/resolver.go:440` — `ensureBoolDefault(sc, "readOnlyRootFilesystem", true)`, regression-tested at `resolver_test.go:328`. Shipped in v0.3 M4 / v1.0 cluster-side hardening. The reviewer read the feature list, which omits it — which is itself worth fixing in `Feature-list.md`. |
| "Your runtime has no provenance … record the Bun binary's SHA-256 as a resolved dependency in your SLSA provenance" | Review 1, Tier 2 | **Two-thirds already done.** `slsa/generator.go:96-104` records Bun as a `ResourceDescriptor` (`pkg:generic/bun@<version>`) with its SHA-256, wired from `provenance/resolver.go:458`. The *real* residual gaps are narrower and are tracked as **PB-2** (checksum coverage) and **PR-7** (SBOM cataloguing). |

### Claims verified CORRECT

Every item below was confirmed absent or defective in the code, and is tracked on `Roadmap.md`:

| Claim | Verified at | Tracked as |
| ----- | ----------- | ---------- |
| No `$env/static/*` handling | zero hits, `internal/` + `cmd/` | 🚩 PB-3 |
| No `ORIGIN`/proxy/body-limit contract | zero hits, all six env names | 🚩 PB-4 |
| `/readyz` proves only a TCP listener; no `startupProbe` | `probe.go:62-77` | ⚠️ PR-4 |
| `.pokkum/` relocation breaks relative paths | `injector.go:416` | 🚩 PB-5 (premise wrong, conclusion right) |
| Bun binary largely unattested | `bunruntime/resolver.go:27` — 3 pinned entries, all `1.2.2` | 🚩 PB-2, ⚠️ PR-7 |
| No VEX / CVE exemptions | zero hits — *and already an unbuilt aspiration in this file* | ⚠️ PR-6, item 8 |
| `.zst` sidecars unusable in the default strategy | `packager.go:257` + `ports/packager.go:178` (adapter-node/sirv entrypoint) | ⚠️ PR-3 |
| Stdlib `compress/gzip` used for layers; `klauspost` already vendored | `layer.go:18` vs. `go.mod:12` | ⚠️ PR-1 |
| No route-template span naming; no trace-correlated logging | `telemetry.go:38-100` | ⚠️ PR-5 |
| No referrers capability probe / auto-fallback | `registry/sbom.go:63` | ⚠️ PR-8 |
| ~~`--hermetic` is advisory (env vars only)~~ | ✅ **Fixed (2026-08-17)** — real Linux network-namespace enforcement, `hermetic_linux.go` | PR-2 |
| No KMS signing, no TUF refresh, no CI OIDC | zero hits for `awskms`/`gcpkms`/`hashivault`/`pkcs11`/`tuf` | item 9a–9c |
| Multi-arch attestation subject undocumented/untested | no index-vs-manifest assertion found | item 9d |
| No ECR `--create-repository`; no resumable chunked upload | zero hits | item 10a–10b |
| No cgroup awareness in supervisor | zero hits for `cgroup`/`memory.max` | item 10d |
| No Helm post-renderer, no kustomize KRM, no `--to-oci-layout`, no kind/k3d load | zero hits | backlog + item 6c |
| No build-time test gate | zero hits | item 12d |
| Native prebuilt `.node` addons outside the integrity model | `nativeinspect`/`striputils` strip but do not hash/attest | item 9e |
| Layer-churn visibility missing | true, and *worse* than claimed — the commands are stubs | 🚩 PB-1 |

### Findings NEITHER review caught

The most serious finding in this whole exercise came from verifying the reviews rather than from the reviews themselves:

1. **🚩 `explain`/`why`/`diff` are hardcoded fakes** (PB-1). Review 2 asked for a layer-churn *sub-mode*, assuming the commands worked. They do not. `Feature-list.md:92` is the one untrue line in that document.
2. **Three further `[x]`/Done items are overstated** — supervisor `/metrics`, app-side trace-context logging, and Helm/Kustomize GitOps export. All three are corrected in the matrix and prose above. This is the *second* time an audit has found this class of drift in this project (see `fixes-to-v1.md`), which is a process signal, not a coincidence.
3. **`Feature-list.md:12` says 8 layers; `explain.go` says 5.** One of them is wrong in a user-facing string.

### Meta-observation worth keeping

The first review's closing judgment — *"you've built a supply-chain platform that happens to package SvelteKit, and the SvelteKit part is where the users actually live"* — is supported by the evidence, not just rhetoric: the supply-chain adapters are real, deep, and heavily tested, while the three SvelteKit-runtime-semantics features are absent and the three SvelteKit-facing introspection commands are stubs. That asymmetry is the thing to correct before publishing, and it is what the Pre-Publication Gate on `Roadmap.md` encodes.

