# Additional Features

## Decision Matrix

| Feature                                   | Benefit (DX; 1-10) | Cost (performance, size; 1-10) | Expected cost (explicit)                                                   | External Dependencies             | Priority |
| ----------------------------------------- | ------------------ | ------------------------------ | -------------------------------------------------------------------------- | --------------------------------- | -------- |
| Built-in Metrics Endpoint                 | 8                  | 4                              | +1-2MB container size (Prometheus client); Negligible runtime cost         | None                              | High     |
| Kubernetes (extended manifests/GitOps)    | 8                  | 6                              | Negligible cost (YAML templating)                                          | None                              | High     |
| `pokkum dev` (Hot-Reload)                 | 9                  | 8                              | +5MB CLI size; High maintenance for cross-OS mounts & restarts             | Docker / Podman                   | High     |
| Image Optimization (zstd, deduplication)  | 7                  | 5                              | Noticeable build-time CPU overhead (compression); Smaller final images     | None                              | High     |
| `pokkum init` (Config Wizard)             | 8                  | 3                              | Negligible CLI size (<1MB); Low maintenance                                | None                              | High     |
| CVE Scanning Integration (`pokkum scan`)  | 9                  | 5                              | +20-30MB CLI size (if embedding engine) or assumes local install           | Trivy / Grype                     | High     |
| Interactive Failure Diagnostics           | 8                  | 4                              | Negligible CLI size (<1MB); Low maintenance                                | None                              | Medium   |
| Env Var Injection & Validation            | 7                  | 4                              | Negligible CLI size; Slight build-time CPU usage                           | None                              | Medium   |
| Multi-Environment Management              | 7                  | 5                              | Negligible cost (YAML/Config templating)                                   | Vault / AWS Secrets               | Medium   |
| Static/Prerendered Page Optimization      | 8                  | 7                              | +10-15MB container size (needs static file server); Complex build pipeline | Nginx (for pure static)           | Medium   |
| OpenTelemetry Auto-Instrumentation        | 9                  | 7                              | Noticeable runtime memory/latency overhead in deployed app                 | OTEL SDK                          | Medium   |
| Diff & Explain (`diff`, `explain`, `why`) | 9                  | 6                              | Moderate CLI disk/network usage (pulling layers to diff)                   | None                              | Medium   |
| Image Provenance Timeline                 | 6                  | 5                              | Minor CLI size increase; Relies on external registry APIs                  | Registry (SLSA lookup)            | Low      |
| Progressive Deployment Strategies         | 8                  | 9                              | Massive maintenance burden (requires complex K8s state management)         | Kubernetes (Argo/Flux)            | Low      |
| Service Mesh Integration                  | 6                  | 6                              | Moderate maintenance (keeping up with Istio/Linkerd API changes)           | Istio / Linkerd                   | Low      |
| Policy as Code                            | 5                  | 7                              | +15MB CLI size (embedding OPA/Rego); High policy maintenance               | OPA / Rego                        | Low      |
| Log Aggregation (JSON/Pretty)             | 7                  | 3                              | Negligible cost                                                            | None                              | High     |
| Plugin System                             | 8                  | 9                              | Extreme architectural complexity; High security & maintenance burden       | npm                               | Low      |
| Hooks System                              | 6                  | 5                              | Small CLI size; High maintenance (cross-platform shell execution)          | Shell / Bun                       | Low      |
| Asset Optimization Pipeline               | 8                  | 8                              | Massive build time increase; Heavy CLI/Container dependencies (`libvips`)  | `sharp`, `@sveltejs/enhanced-img` | Low      |

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
