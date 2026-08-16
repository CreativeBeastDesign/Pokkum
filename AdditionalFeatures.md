# Additional Features

## Decision Matrix (re-evaluated 2026-08)

| Feature                                     | DX (1-10) | Security (1-10) | Cost (1-10) | Expected cost (explicit)                                                         | External Dependencies             | Priority |
| ------------------------------------------- | --------- | --------------- | ----------- | -------------------------------------------------------------------------------- | --------------------------------- | -------- |
| Live Cluster Annotation Inspection          | 8         | 2               | 4           | `kubectl get` pre-flight query in `pokkum apply` to seed deployment history      | `kubectl`                         | Done     |
| Monorepo Affected-Detection                 | 7         | 1               | 4           | Git-diff input tracking per `pokkum://` app                                      | None                              | Medium   |
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
| Diff & Explain (`diff`, `explain`, `why`)   | 9         | 4               | 4           | Layer pulls for remote diffs; near-free for own reproducible layered images      | None                              | Done     |
| Kubernetes (extended manifests/GitOps)      | 8         | 5               | 5           | YAML/Helm/Kustomize templating; moderate maintenance                             | None                              | Done     |
| Image Optimization (zstd, deduplication)    | 7         | 1               | 4           | Build-time CPU (compression); largely covered by layer-caching concept §10       | None                              | Done     |
| `pokkum init` (Config Wizard)               | 8         | 2               | 3           | Negligible CLI size (<1MB); low maintenance                                      | None                              | Done     |
| `pokkum adopt` (Migration Codemod)          | 8         | 2               | 5           | Codemod maintenance across adapter ecosystems (Node/Vercel/Dockerfile)           | None                              | Done     |
| `pokkum dev` (Hot-Reload, full)             | 9         | 1               | 8           | +5MB CLI size; high cross-OS maintenance; lite variant already on Roadmap v0.2   | Docker / Podman                   | Done     |
| Interactive Failure Diagnostics             | 8         | 1               | 4           | Negligible CLI size (<1MB); low maintenance                                      | None                              | Done     |
| Runtime Env Contract (validation)           | 7         | 5               | 2           | Image annotation + supervisor startup check; drop build-time injection half      | None                              | Done     |
| Toolchain CVE Awareness                     | 5         | 7               | 3           | OSV.dev advisory lookups keyed on embedded Bun version                           | OSV.dev API                       | Done     |
| Signed Self-Distribution (`pokkum upgrade`) | 5         | 7               | 3           | Release signing infra; verify-on-update logic                                    | cosign / minisign                 | Done     |
| Log Aggregation (app-side, trace context)   | 7         | 2               | 3           | Trace-context JSON logging in adapter/runtime; CLI half already shipped          | None                              | Done     |
| Built-in Metrics Endpoint (supervisor)      | 7         | 2               | 3           | Supervisor-level `/metrics` only; app metrics already shipped via OTel           | None                              | Done     |
| Image Provenance Timeline                   | 6         | 5               | 5           | Registry API reliance; partially subsumed by `verify` + OCI annotations          | Registry (SLSA lookup)            | Done     |
| Verify Base on Cache Hit                    | 3         | 7               | 2           | One Cosign/keyless verify per cache hit; opt-in strict; decoupled from `--cache-verify` | None (sigstore already vendored) | Backlog  |
| Layered-Strategy Runtime Hardening (new 2026-08-16) | 5 | 8               | 5           | Compiled stub launcher (needs a spike: verify runtime-import of external files) + ~100 LOC supervisor startup attestation | None | Medium-High |
| Dedicated `chainguard-static` Preset (new 2026-08-16) | 4 | 5             | 2           | Small enum-driven change; one self-healing orphaned `pokkum.lock` entry per existing `--static` user | None | Low-Medium |

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
- Complements (does not replace) Cosign/SLSA: signatures prove _who_ built it, rebuild proves _what_ it was built from
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

### Layered-Strategy Runtime Hardening

- `--strategy=layered` (default since v0.3) ships stock `bun`, which exposes its full CLI via `BUN_BE_BUN=1` — an attacker with an existing exec primitive can run arbitrary scripts, unlike v0.1's sealed compiled-exe strategy
- Two composable mitigations, both from the original v0.3 hardening analysis but never built: a compiled stub launcher (Option A — closes the `BUN_BE_BUN`/`bunx` surface entirely) and supervisor startup attestation (Option C — a SHA-256 manifest check before `pokkum-init` execs the app, restoring tamper-evidence without depending on cluster-level readonly-fs policy)
- Unlike most items in this document, this closes a gap in the **default** build path, not an opt-in feature — see `Roadmap.md`'s "Recommended Next Steps" and `concepts/archive/pokkum-layer-caching-concept.md` §5.2
- Tracking: Recommended Next Steps (Medium-High Priority) on `Roadmap.md`

### Dedicated `chainguard-static` Base Image Preset

- `--static`'s default base currently reuses the `BaseImageChainguard` preset (fixed 2026-08-16 from an original `BaseImageDistroless` misassignment that broke signature verification on every default `--static` build) — correct for signature identity, but leaves a narrow `pokkum.lock` collision between an explicit `--base cgr.dev/chainguard/glibc-dynamic` build and a `--static` build in the same project
- A dedicated `BaseImageChainguardStatic` preset would eliminate the collision entirely, at the cost of one new CLI-visible preset name and a self-healing orphaned-lockfile-entry migration note for existing `--static` users
- Tracking: Recommended Next Steps (Low-Medium Priority) on `Roadmap.md`; full design in `concepts/new-chainguard-static-preset-concept.md`

