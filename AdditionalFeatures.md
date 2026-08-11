# Additional Features

## Decision Matrix (re-evaluated 2026-08)

Changes vs. the previous matrix:

- **Added a Security column (1-10)** — DX alone hid the fact that several cheap features are primarily security wins (base-image signature verification scored poorly on DX but is a top-tier item).
- **Moved shipped features out** — OpenTelemetry Auto-Instrumentation and CLI structured logging shipped with the v0.2 OTel work (see `Meantime.md`); keeping them in a decision matrix distorts prioritization.
- **Re-scored against the layered-image concept** (`pokkum-layer-caching-concept.md`): Static/Prerendered optimization and Diff & Explain got dramatically cheaper (separate layers + reproducible digests do most of the work); zstd was already adopted there.
- **Re-scored against Roadmap v1.0** — items the roadmap already commits to (vuln gating, base pinning) keep their rows only where the feature exceeds the roadmap scope.
- **Demotions:** `pokkum dev` full hot-reload (the roadmap's build+`--local`+run lite variant captures most value for a fraction of the cost); Progressive Deployment (Argo/Flux own that space; DX 8→6).
- **Promotions:** Diff & Explain (cost 6→4, unique fit with reproducible layered images); Hooks (cheap way to defuse Plugin System demand).

| Feature                                     | DX (1-10) | Security (1-10) | Cost (1-10) | Expected cost (explicit)                                                        | External Dependencies             | Priority |
| ------------------------------------------- | --------- | --------------- | ----------- | -------------------------------------------------------------------------------- | --------------------------------- | -------- |
| Secret-Inlining Guard                       | 6         | 9               | 2           | Slight build-time CPU (layer scan); forces `--env=disable` in bundling           | None                              | High     |
| `pokkum verify --rebuild`                   | 6         | 10              | 5           | One full rebuild per verification; requires pinned toolchain                     | None (git + registry)             | High     |
| CVE Scanning Integration (`pokkum scan`)    | 9         | 8               | 4           | Shell out to local Trivy/Grype (do NOT embed → no +20-30MB CLI); CI scan time    | Trivy / Grype                     | High     |
| Base Image Signature Verification           | 4         | 8               | 2           | +1 registry roundtrip per build; sigstore libs already vendored (cosign adapter) | None                              | High     |
| `pokkum repro doctor`                       | 9         | 6               | 4           | Double build + per-layer diff, on demand only                                    | None                              | High     |
| `pokkum doctor` (preflight)                 | 9         | 3               | 2           | <1MB CLI; low maintenance                                                        | None                              | High     |
| Readiness Drain on SIGTERM                  | 7         | 3               | 1           | ~20 LOC in supervisor                                                            | None                              | High     |
| `--output=json` Build Results               | 8         | 2               | 1           | Negligible                                                                       | None                              | High     |
| Standard OCI Annotations                    | 6         | 4               | 1           | Negligible (git metadata already read for SOURCE_DATE_EPOCH)                     | None                              | High     |
| Diff & Explain (`diff`, `explain`, `why`)   | 9         | 4               | 4           | Layer pulls for remote diffs; near-free for own reproducible layered images      | None                              | High     |
| Kubernetes (extended manifests/GitOps)      | 8         | 5               | 5           | YAML/Helm/Kustomize templating; moderate maintenance                             | None                              | High     |
| Image Optimization (zstd, deduplication)    | 7         | 1               | 4           | Build-time CPU (compression); largely covered by layer-caching concept §10       | None                              | High     |
| `pokkum init` (Config Wizard)               | 8         | 2               | 3           | Negligible CLI size (<1MB); low maintenance                                      | None                              | High     |
| `pokkum adopt` (Migration Codemod)          | 8         | 2               | 5           | Codemod maintenance across adapter ecosystems (Node/Vercel/Dockerfile)           | None                              | Medium   |
| `pokkum dev` (Hot-Reload, full)             | 9         | 1               | 8           | +5MB CLI size; high cross-OS maintenance; lite variant already on Roadmap v0.2   | Docker / Podman                   | Medium   |
| Interactive Failure Diagnostics             | 8         | 1               | 4           | Negligible CLI size (<1MB); low maintenance                                      | None                              | Medium   |
| Runtime Env Contract (validation)           | 7         | 5               | 2           | Image annotation + supervisor startup check; drop build-time injection half     | None                              | Medium   |
| Toolchain CVE Awareness                     | 5         | 7               | 3           | OSV.dev advisory lookups keyed on embedded Bun version                           | OSV.dev API                       | Medium   |
| Signed Self-Distribution (`pokkum upgrade`) | 5         | 7               | 3           | Release signing infra; verify-on-update logic                                    | cosign / minisign                 | Medium   |
| Monorepo Affected-Detection                 | 7         | 1               | 4           | Git-diff input tracking per `pokkum://` app                                      | None                              | Medium   |
| Static/Prerendered Page Optimization        | 8         | 1               | 4           | Mostly free under layered design; `--static` nginx mode is separate & costlier   | Nginx (only for `--static`)       | Medium   |
| Log Aggregation (app-side, trace context)   | 7         | 2               | 3           | Trace-context JSON logging in adapter/runtime; CLI half already shipped          | None                              | Medium   |
| Multi-Environment Management                | 7         | 3               | 5           | Config templating; secret-manager integrations                                   | Vault / AWS Secrets               | Medium   |
| Built-in Metrics Endpoint (supervisor)      | 7         | 2               | 3           | Supervisor-level `/metrics` only; app metrics already shipped via OTel           | None                              | Medium   |
| Hooks System                                | 6         | 2               | 5           | Small CLI size; cross-platform shell exec; defuses Plugin System demand          | Shell / Bun                       | Medium   |
| Image Provenance Timeline                   | 6         | 5               | 5           | Registry API reliance; partially subsumed by `verify` + OCI annotations          | Registry (SLSA lookup)            | Low      |
| Policy as Code                              | 5         | 6               | 7           | +15MB CLI size (embedding OPA/Rego); high policy maintenance                     | OPA / Rego                        | Low      |
| Service Mesh Integration                    | 5         | 4               | 6           | Moderate maintenance (Istio/Linkerd API churn)                                   | Istio / Linkerd                   | Low      |
| Progressive Deployment Strategies           | 6         | 2               | 9           | Massive maintenance burden; Argo/Flux own this space                             | Kubernetes (Argo/Flux)            | Low      |
| Asset Optimization Pipeline                 | 8         | 1               | 8           | Massive build time increase; heavy deps (`libvips`)                              | `sharp`, `@sveltejs/enhanced-img` | Low      |
| Plugin System                               | 8         | 1               | 9           | Extreme complexity; npm supply-chain risk undercuts Pokkum's own hardening story | npm                               | Low      |

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

- Extract prerendered pages to a separate slim layer
- Generate immutable Cache-Control headers based on build output
- `--static` flag to build a fully static site to an Nginx-alpine OCI image (zero JS runtime)

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

### Log Aggregation

- Structured JSON logging with trace context injection
- `--log-format=json` for production, `--log-format=pretty` for development
- Direct integration with Grafana Loki/CloudWatch/ELK

### Diff & Explain

- `pokkum diff image:v1 image:v2` to show layer changes
- `pokkum explain image` to break down layer sizes and contents
- `pokkum why <dep>` to trace why a specific package is included

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
- Complements (does not replace) Cosign/SLSA: signatures prove *who* built it, rebuild proves *what* it was built from
- Requires pinned toolchain versions (Bun, Go/gzip caveat per README known limits) to rebuild faithfully

### Secret-Inlining Guard

- Bun's bundler can inline build-machine environment variables into output — a CI runner with cloud credentials exported can silently bake them into the image
- Force `--env=disable` during bundling, then run an entropy/pattern scan (gitleaks-style) over final layer contents before push
- `.pokkumignore` protects files; this protects against the bundler and build environment themselves
- Fail the build on findings, with `--allow-secret-pattern` escape hatch for false positives

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

- Machine-readable build *results* (digest, per-layer sizes, SBOM path, attestation refs) as distinct from JSON *logs*
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
