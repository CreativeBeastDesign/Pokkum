# Pokkum: SvelteKit to OCI in One Command

[![GitHub Actions Build Status](https://github.com/CreativeBeastDesign/pokkum/workflows/ci/badge.svg)](https://github.com/CreativeBeastDesign/pokkum/actions)
[![pkg.go.dev](https://pkg.go.dev/badge/github.com/CreativeBeastDesign/pokkum.svg)](https://pkg.go.dev/github.com/CreativeBeastDesign/pokkum)
[![Go Report Card](https://goreportcard.com/badge/github.com/CreativeBeastDesign/pokkum)](https://goreportcard.com/report/github.com/CreativeBeastDesign/pokkum)
[![SLSA 3](https://slsa.dev/images/gh-badge-level3.svg)](https://slsa.dev)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

**Pokkum** is a zero-dependency OCI container image compiler for SvelteKit applications. In a single command, Pokkum compiles your SvelteKit app into a zero-daemon, multi-layer cached OCI container image (the exact layer set and count depend on the strategy and what a given build actually produces — run `pokkum explain` for the real breakdown) complete with SBOMs, an opt-in OpenTelemetry SDK bootstrap, SLSA provenance, and hardened Kubernetes deployment manifests.

**A signing key is required to actually get a signed image.** `--sign` defaults on and generates a real SLSA statement either way, but Pokkum only produces a cryptographic Cosign signature and DSSE attestation — and self-verifies them against the registry before reporting success — when you configure `--signing-key`/`POKKUM_SIGNING_KEY`. Without a key, a signing-enabled build pushes **unsigned**, with a loud warning; pass `--require-signed` to make that a hard failure instead of a warning. The SLSA-3 badge above describes what the pipeline is capable of producing, not an unconditional guarantee for every build — see [Vocabulary.md](Vocabulary.md) for the flag reference.

Think of it as `ko` for SvelteKit: zero Dockerfile, zero Docker daemon required, and bit-for-bit reproducible builds out of the box.

---

## Example Usages

### 1. Quickest & Lightest (Standard One-Liner)

Zero-config build and push to your container registry:

```bash
POKKUM_DOCKER_REPO=ghcr.io/example/my-app pokkum build ./my-app
```

- **What this does**:
  - Automatically detects your SvelteKit project directory.
  - Compiles your app into an architecture-independent layout using the Bun runtime.
  - Assembles a multi-layer OCI container image on a Distroless glibc base (run `pokkum explain` for the real per-build layer breakdown).
  - Generates SPDX-JSON Software Bill of Materials (SBOMs).
  - Pushes the multi-architecture image (`linux/amd64`, `linux/arm64`) to `ghcr.io/example/my-app`.

---

### 2. Maximum Security (Enterprise Hermetic & Hardened)

Strict SLSA L3 hermetic build on a hardened Chainguard base, with Cosign signature checks, custom OCI annotations, and hardened Kubernetes manifest resolution:

**Build the Image:**

```bash
POKKUM_DOCKER_REPO=ghcr.io/example/my-app pokkum build ./my-app \
  --hardened \
  --hermetic \
  --image-label org.opencontainers.image.vendor="Acme Corp" \
  --allow-secret-pattern="(?i)PUBLIC_.*"
```

- **What this does**:
  - `--hardened`: Uses `cgr.dev/chainguard/glibc-dynamic:latest` base image.
  - `--hermetic`: Enforces strict zero-network egress during compilation, requiring pre-cached base images and pre-populated `node_modules/`.
  - **Secret Guard**: Scans both pre-build source and, since 2026-08-18, the actual packaged build output against five fixed regex patterns (private key headers, AWS/GitHub/Google API key shapes, generic password/secret/token assignments) — not entropy analysis; entropy-based detection of arbitrary high-randomness tokens is tracked as future work, not shipped.
  - **Base Image Verification**: Verifies both static-key Cosign signatures (for custom/self-signed bases) and keyless Sigstore signatures (Fulcio + Rekor, for stock `distroless`/`chainguard` presets by default).

**Resolve Hardened Kubernetes Manifests:**

```bash
POKKUM_DOCKER_REPO=ghcr.io/example/my-app pokkum resolve -f deployment.yaml \
  --security-context \
  --network-policy \
  --resource-defaults \
  --registry-config=~/.docker/config.json
```

- **What this does**:
  - Replaces `pokkum://` URIs in `deployment.yaml` with pinned immutable image digests (`repo@sha256:...`).
  - Injects hardened container `securityContext` (`runAsNonRoot: true`, `seccompProfile: RuntimeDefault`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`).
  - Generates restricted `NetworkPolicy` ingress/egress rules and injects CPU/memory `requests`/`limits` with a `PodDisruptionBudget`.

---

## Key Features

- **Zero-Mutation Build Sandbox**: No manual SvelteKit adapter configuration needed. Auto-injects virtual build sandbox configuration in `.pokkum/` workspace without mutating user source files.
- **Architecture-Independent Layer Caching**: the default `--strategy=layered` splits a build into independently-cached layers — a pinned Bun runtime layer (`/usr/local/bin/bun`), the PID-1 supervisor, the SvelteKit server/asset output, and, when a given build produces them, separate layers for the vendor `node_modules` tree, native `.node` addons, and prerendered pages. The exact layer set and count vary per build (a purely static site or a native-addon-free app produces fewer layers than one with both) — run `pokkum explain <image>` against a built image for its real, per-build breakdown rather than assuming a fixed number.
- **Bit-for-Bit OCI Reproducibility**: All timestamps and tar headers derive deterministically from `SOURCE_DATE_EPOCH` / git commit metadata.
- **Security Scanning & Guardrails**: Integrated `pokkum scan` vulnerability auditing with Syft OS package enumeration and OSV.dev batch queries (Debian, Ubuntu, Alpine, Wolfi, Chainguard), build-time secret leak prevention, and base image CVE reactivity.
- **Base Image Signature Verification & Escrow Mirroring**: Real keyless Sigstore verification (Fulcio certificate chain + Rekor transparency log) runs automatically against live `distroless`/`chainguard` base image signatures. Base image escrow mirroring (`pokkum base update --mirror-registry`) copies base images and Cosign `.sig` tags to project-controlled registries with automated lockfile fallback.
- **Optional OpenTelemetry Bootstrap**: `--telemetry` compiles a real OTel NodeSDK + OTLP trace exporter directly into the image, started at container boot. Automatic HTTP/framework instrumentation isn't possible under Bun's runtime today (a Bun limitation, not Pokkum's) — real, route-templated spans need one documented line added to your own `hooks.server.ts`; see [Vocabulary.md](Vocabulary.md) §3a. OTLP metrics export is not currently functional (a Bun bundler bug); `--metrics-only` warns rather than silently doing nothing.
- **Day-2 Lifecycle Management**: Annotation-based manifest rollbacks (`pokkum rollback`, no `--to` needed for the last change) and signed CLI self-upgrades (`pokkum upgrade`).

---

## Scope, Philosophy & Telemetry

**What Pokkum optimizes for.** Pokkum is designed for maximum security and bit-for-bit reproducibility, not the fastest possible path to a running deployment. Hermetic builds, SLSA provenance generation, keyless base-image verification, and reproducibility checks (`pokkum verify`, whose rebuild-and-compare path runs by default — `--no-rebuild` opts out) are all either on by default or one flag away — and there is no "just build it, verify later" fast path that skips them. Turning that provenance into a cryptographically signed, self-verified artifact needs one more thing from you: a signing key (`--signing-key`/`POKKUM_SIGNING_KEY`; `--require-signed` to enforce it in CI) — see above. If your priority is the shortest possible time-to-first-deploy over a verifiable supply chain, that trade-off is a deliberate design choice here, not an oversight; expect the defaults to feel stricter than a typical `docker build`.

**What Pokkum does not do.** Pokkum compiles SvelteKit applications into OCI container images — it does not target edge/isolate runtimes (Cloudflare Workers, Deno Deploy, Vercel Edge Functions). Those aren't OCI images; they're a fundamentally different deployment model, and SvelteKit's own `adapter-cloudflare`/`adapter-vercel`/`adapter-deno` already serve that use case directly. This is an explicit non-goal, not an unaddressed gap — if edge deployment is what you need, Pokkum isn't the tool for that half of your stack.

**Telemetry.** The `pokkum` CLI itself sends no telemetry, analytics, or usage data anywhere, ever — no phone-home, no update-check pings beyond an explicit `pokkum upgrade --check`. (This is unrelated to the `--telemetry` *build* flag, which configures OpenTelemetry instrumentation inside the SvelteKit application you're compiling — see [Vocabulary.md](Vocabulary.md) §3a. That feature is entirely opt-in, off by default, and exports only to the OTLP endpoint you configure.)

---

## Installation & Setup

Choose your preferred installation method:

### 1. Homebrew (macOS & Linux)

```bash
brew install CreativeBeastDesign/pokkum/pokkum
```

---

### 2. NPM / NPX (Zero-Install One-Liner)

Run directly via `npx` or `bunx` without manual installation:

```bash
npx @pokkum/cli build ./my-app
```

Or install globally:

```bash
npm install -g @pokkum/cli
```

---

### 3. Standalone Installer Script (Linux / macOS)

```bash
curl -fsSL https://raw.githubusercontent.com/CreativeBeastDesign/pokkum/main/install.sh | sh
```

---

### 4. GitHub Action for CI/CD Pipelines (`.github/workflows/ci.yml`)

```yaml
- name: Setup Pokkum
  uses: CreativeBeastDesign/pokkum/.github/actions/setup-pokkum@v1
  with:
    version: 'latest'

- name: Build & Push SvelteKit Container
  env:
    POKKUM_DOCKER_REPO: ghcr.io/${{ github.repository }}
  run: pokkum build ./my-app
```

---

## Runtime Contract

Every container image produced by Pokkum is supervised by an ultra-lightweight PID-1 init process:

```
/pokkum/init -- /usr/local/bin/bun /app/server/index.js
```

### Health Probes & Signals

- `/pokkum/init` reaps orphaned zombie processes and handles OS signals (`SIGTERM`, `SIGINT`).
- Exposes probe endpoints on `POKKUM_PROBE_PORT` (default `8081`):
  - `GET /healthz` → `200 OK` (supervisor liveness)
  - `GET /readyz` → `200 OK` (app readiness; returns `503 Service Unavailable` during graceful shutdown drain)
- Application listens on `PORT` (default `3000`).

---

## CLI Command Reference

| Command                    | Usage                                       | Description                                                                        |
| -------------------------- | ------------------------------------------- | ---------------------------------------------------------------------------------- |
| `pokkum build [dir]`       | `pokkum build ./my-app`                     | Compiles SvelteKit app into a multi-layer OCI container image.                     |
| `pokkum resolve -f <file>` | `pokkum resolve -f deploy.yaml`             | Resolves `pokkum://` URIs in K8s manifests to immutable image digests.             |
| `pokkum apply -f <file>`   | `pokkum apply -f deploy.yaml`               | Resolves manifests and pipes directly to `kubectl apply`.                          |
| `pokkum dev [dir]`         | `pokkum dev ./my-app`                       | Local development mode with hot-reloading file watcher and Docker daemon loading.  |
| `pokkum scan [target]`     | `pokkum scan ./my-app`                      | Security vulnerability scanner for directories, images, or tarballs.               |
| `pokkum doctor [dir]`      | `pokkum doctor ./my-app`                    | Diagnostic wizard for preflight checks and mechanical repairs.                     |
| `pokkum init [dir]`        | `pokkum init ./my-app`                      | Bootstraps project config and `.pokkumignore`.                                     |
| `pokkum explain [image]`   | `pokkum explain <ref>`                      | Inspects layer hierarchy, file origin tracing (`why`), and image diffing (`diff`). |
| `pokkum rollback`          | `pokkum rollback -f deploy.yaml`            | Rolls back to the previous image ref (`pokkum.dev/previous-image` annotation), or pass `--to=<ref>` explicitly. One hop deep. |
| `pokkum upgrade`           | `pokkum upgrade --check`                    | Checks for signed CLI release updates.                                             |

---

## Documentation & Deep Dive

- **[ARCHITECTURE.md](ARCHITECTURE.md)**: Architectural invariants, Hexagonal layer boundaries, and image layer layout.
- **[Vocabulary.md](Vocabulary.md)**: Complete CLI flag reference and configuration options.
- **[docs/Roadmap.md](docs/Roadmap.md)**: What's next — SvelteKit-specific DX, supply-chain completions, and ergonomics.
- **[docs/Features.md](docs/Features.md)**: Every shipped capability, with its known limitations stated alongside it.
- **[docs/Shipped.md](docs/Shipped.md)**: What landed, newest first. These three are generated from [docs/roadmap/*.yaml](docs/roadmap) — edit the YAML, not the markdown.
- **[docs/archive/](docs/archive)**: Retired historical documents, including [Roadmap-v1-Archive.md](docs/archive/Roadmap-v1-Archive.md) for the full v1.0 build history.
- **[docs/archive/fixes-to-v1.md](docs/archive/fixes-to-v1.md)**: Post-v1.0 audit findings and the fixes applied for each.
- **[docs/archive/for-users.md](docs/archive/for-users.md)**: User-visible behavior changes from that fix round and what they require of you.
- **[paranoid-testing-guide.md](paranoid-testing-guide.md)**: Step-by-step "believe nothing" verification guide for testing Pokkum against a real project — cross-checks every claim (build, signature, provenance, reproducibility) with an independent tool.

---

## License

Apache 2.0
