# Pokkum: SvelteKit to OCI in One Command

[![GitHub Actions Build Status](https://github.com/CreativeBeastDesign/pokkum/workflows/ci/badge.svg)](https://github.com/CreativeBeastDesign/pokkum/actions)
[![pkg.go.dev](https://pkg.go.dev/badge/github.com/CreativeBeastDesign/pokkum.svg)](https://pkg.go.dev/github.com/CreativeBeastDesign/pokkum)
[![Go Report Card](https://goreportcard.com/badge/github.com/CreativeBeastDesign/pokkum)](https://goreportcard.com/report/github.com/CreativeBeastDesign/pokkum)
[![SLSA 3](https://slsa.dev/images/gh-badge-level3.svg)](https://slsa.dev)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

**Pokkum** is a zero-dependency OCI container image compiler for SvelteKit applications. In a single command, Pokkum compiles your SvelteKit app into a zero-daemon, multi-layer cached OCI container image (the exact layer set and count depend on the strategy and what a given build actually produces — run `pokkum explain` for the real breakdown) complete with SBOMs, OpenTelemetry auto-instrumentation, signed SLSA provenance, and hardened Kubernetes deployment manifests.

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
  - `--hardened`: Uses `ghcr.io/chainguard-images/glibc-dynamic:latest` base image.
  - `--hermetic`: Enforces strict zero-network egress during compilation, requiring pre-cached base images and pre-populated `node_modules/`.
  - **Secret Guard**: Scans project source files for accidentally inlined secrets or high-entropy tokens before packaging layers.
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
- **5-Layer Architecture-Independent Caching**:
  1. Base Image Layer (`distroless` or `chainguard`)
  2. Bun Runtime Layer (`/pokkum/bun`)
  3. Closured Native Addon Layer (`/app/native` for `.node` binaries)
  4. Vendor `node_modules` Layer
  5. SvelteKit App & Asset Layer
- **Bit-for-Bit OCI Reproducibility**: All timestamps and tar headers derive deterministically from `SOURCE_DATE_EPOCH` / git commit metadata.
- **Security Scanning & Guardrails**: Integrated `pokkum scan` vulnerability auditing with Syft OS package enumeration and OSV.dev batch queries (Debian, Ubuntu, Alpine, Wolfi, Chainguard), build-time secret leak prevention, and base image CVE reactivity.
- **Base Image Signature Verification & Escrow Mirroring**: Real keyless Sigstore verification (Fulcio certificate chain + Rekor transparency log) runs automatically against live `distroless`/`chainguard` base image signatures. Base image escrow mirroring (`pokkum base update --mirror-registry`) copies base images and Cosign `.sig` tags to project-controlled registries with automated lockfile fallback.
- **Built-In Observability**: Zero-config OpenTelemetry tracing and Prometheus metrics auto-instrumentation.
- **Day-2 Lifecycle Management**: Annotation-based manifest rollbacks (`pokkum rollback`, no `--to` needed for the last change) and signed CLI self-upgrades (`pokkum upgrade`).

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
/pokkum/init -- /pokkum/bun /app/index.js
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
- **[Roadmap.md](Roadmap.md)**: Implementation progress and future feature backlog.
- **[fixes-to-v1.md](fixes-to-v1.md)**: Post-v1.0 audit findings and the fixes applied for each.
- **[for-users.md](for-users.md)**: User-visible behavior changes from that fix round and what they require of you.
- **[paranoid-testing-guide.md](paranoid-testing-guide.md)**: Step-by-step "believe nothing" verification guide for testing Pokkum against a real project — cross-checks every claim (build, signature, provenance, reproducibility) with an independent tool.

---

## License

Apache 2.0
