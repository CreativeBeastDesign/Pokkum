# Pokkum Architecture & Technical Deep-Dive

**Pokkum** is a Go-based zero-dependency OCI image builder and deployment pipeline tool specifically designed for SvelteKit applications adapted with `@jesterkit/exe-sveltekit`.

It builds multi-architecture OCI images (`linux/amd64`, `linux/arm64`), embeds a PID-1 supervisor, generates Software Bills of Materials (SBOMs), and pushes reproducible container images directly to OCI registries — **without requiring Docker or a Docker daemon**.

---

## 1. High-Level System Architecture

Pokkum is structured using **Hexagonal Architecture (Ports and Adapters)** to decouple core domain logic from external tools, operating systems, and network registries.

```
                    ┌────────────────────────┐
                    │     Cobra/Viper CLI    │
                    │  (cmd/pokkum/*.go)     │
                    └───────────┬────────────┘
                                │
                                ▼
                    ┌────────────────────────┐
                    │      Domain Core       │
                    │   (internal/core/)     │
                    └───────────┬────────────┘
                                │
        ┌───────────────────────┼───────────────────────┐
        ▼                       ▼                       ▼
┌──────────────┐        ┌──────────────┐        ┌──────────────┐
│ Ports (Ifaces)│        │ Ports (Ifaces)│        │ Ports (Ifaces)│
│ Compiler     │        │ Packager     │        │ Registry     │
└───────┬──────┘        └───────┬──────┘        └───────┬──────┘
        ▼                       ▼                       ▼
┌──────────────┐        ┌──────────────┐        ┌──────────────┐
│ bunexec      │        │ packager     │        │ registry     │
│ (Bun Adapter)│        │ (OCI Adapter)│        │ (go-container│
└──────────────┘        └──────────────┘        │   registry)  │
                                                └──────────────┘
```

### Core Layers

1. **CLI Layer (`cmd/pokkum/`)**:
   - `build.go`: Parses flags (`--platform`, `--base`, `--sbom`, `--sbom-attach`, `--local`, `--tarball`, `--update-base`, `--offline`, `--bun-binary`, `--bun-variant`) and invokes the core build pipeline.
   - `dev.go`: Implements `pokkum dev [dir]` subcommand for local container development, supporting `--debug` interactive shell debugging, local Docker daemon loading, and hot-reload source watching.
   - `base.go`: Implements `pokkum base update` and `pokkum base check` subcommands to query remote base image digests and manage `pokkum.lock`.
   - `resolve.go`: Scans Kubernetes YAML manifests for `pokkum://` image URIs, triggers automated builds, and resolves them to immutable image digests (`repo@sha256:...`).
   - `apply.go`: Resolves `pokkum://` manifests and pipes the output directly into `kubectl apply -f -`.
   - `k8s.go`: Shared manifest parsing and URI replacement engine.
   - `version.go`: Displays git version, commit, and build timestamp metadata.

2. **Domain Core (`internal/core/`)**:
   - `pipeline.go`: Orchestrates the execution flow across compilers, base image resolvers, packagers, and registries.
   - `model.go`: Defines domain models (`BuildRequest`, `Platform`, `RuntimeConfig`, `ImageRef`, `BunRuntimeOptions`, etc.).
   - `errors.go`: Defines standardized domain error types (`ErrPackageFailed`, `ErrUnsupportedPlatform`, `ErrBunResolutionFailed`, etc.).

3. **Abstraction Ports (`internal/ports/`)**:
   - Interfaces decoupling core logic from external adapters: `Compiler`, `Packager`, `Registry`, `BaseImageResolver`, `BunRuntimeResolver`, `SBOMGenerator`, `SupervisorProvider`, `K8sResolver`, `Signer`, `Attestor`, `BinaryInspector`.

4. **Adapter Implementations (`internal/adapters/`)**:
   - `bunexec`: Wraps host `bun build --compile` for cross-compiling single executables.
   - `bunruntime`: Resolves, downloads, SHA256-verifies, and caches official Bun runtime binaries (`~/.cache/pokkum/bun`) for runtime layer assembly (`ports.BunRuntimeResolver`).
   - `packager`: Constructs reproducible OCI tarballs, custom single-binary layers (`BuildCustomFileLayer`), directory tree layers (`BuildDirectoryTreeLayer`), and multi-arch index manifests using `github.com/google/go-containerregistry`.
   - `baseimage`: Resolves base image layers (`gcr.io/distroless/cc-debian12:nonroot` or Chainguard `glibc-dynamic`) and maintains `pokkum.lock` digest locks.
   - `lockfileutils`: Utility package for loading, parsing, and saving `pokkum.lock` base image lockfiles.
   - `registry`: Handles OCI registry authentication, blob uploads, and index pushes.
   - `sbom`: Generates SPDX or CycloneDX SBOMs using `github.com/anchore/syft`.
   - `supervisor`: Embedded supervisor binary assets (`/pokkum/init`).
   - `k8s`: Kubernetes manifest inspection, document rewriting, and `pokkum://` schema resolution.
   - `sveltekit`: Checks `@jesterkit/exe-sveltekit` adapter installation in target projects.
   - `cosign`: Signs OCI images and attaches Cosign signatures to OCI registries.
   - `slsa`: Generates SLSA v1.0 provenance predicate statements.
   - `dsse`: Wraps attestations in Dead Simple Signing Envelopes (DSSE).
   - `config`: Environment variable and CLI flag parsing/validation.
   - `ignore`: Reads `.pokkumignore` patterns to exclude unwanted files (`.env.local`, source maps, fixtures).
   - `nativeinspect`: Inspects compiled binaries (`DT_NEEDED`, glibc symbols) to ensure base image compatibility.


5. **PID-1 Supervisor Subproject (`supervisor/cmd/pokkum-init`)**:
   - A standalone Go program cross-compiled to `linux/amd64` and `linux/arm64` and embedded in Pokkum.
   - Acts as `ENTRYPOINT ["/pokkum/init", "--", "/app/server"]`.
   - Handles PID-1 duties (reaping zombie sub-processes, forwarding OS signals like SIGTERM/SIGINT).
   - Serves HTTP health endpoints (`/healthz`, `/readyz`) on `POKKUM_PROBE_PORT` (default: 8081).

---

## 2. End-to-End Execution Flow (`pokkum build`)

```
   1. Discover & Validate SvelteKit App
      (check @jesterkit/exe-sveltekit & .pokkumignore)
                  │
                  ▼
     2. Build SvelteKit App JS Entry
      (bun run build via exe adapter)
                  │
                  ▼
    3. Fix Non-Deterministic Artifacts
       (Sort assets.generated.ts)
                  │
                  ▼
   4. Cross-Compile Standalone Binaries
      (bun build --compile per platform)
                  │
                  ▼
   5. Inspect Binary Dependencies
      (nativeinspect DT_NEEDED check)
                  │
                  ▼
      6. Construct OCI Image Layers
     (Base + /pokkum/init + /app/server)
                  │
                  ▼
     7. Assemble Multi-Arch OCI Index
                  │
                  ▼
   8. Supply Chain: SBOM, SLSA, DSSE & Cosign
                  │
                  ▼
     9. Push to Registry / Export Image
```

### Detailed Pipeline Steps

1. **Project Validation**:
   - Checks that target directory contains `package.json` and `@jesterkit/exe-sveltekit` in dependencies.
   - Parses `.pokkumignore` to establish exclusion patterns.
2. **SvelteKit Adapter Run**:
   - Invokes `bun run build` inside the app directory. The `@jesterkit/exe-sveltekit` adapter builds the SvelteKit application and generates a standalone TypeScript server entrypoint along with embedded static assets.
3. **Determinism Stabilization**:
   - Reads `SOURCE_DATE_EPOCH` (or git commit timestamp) to pin all file timestamps (`mtime`).
   - Fixes known non-deterministic outputs (e.g. sorting asset manifests).
4. **Cross-Compilation (`bunexec`)**:
   - For each target platform (`linux/amd64`, `linux/arm64`), runs `bun build --compile --target=<platform>`.
   - Produces a single binary (~90 MB) embedding all JS code, Node/Bun polyfills, and static assets.
5. **Binary & Base Image Compatibility Inspection (`nativeinspect`)**:
   - Reads ELF `DT_NEEDED` dynamic links on built binaries to verify compatibility with glibc in the target base image.
6. **OCI Layer Assembly (`packager`)**:
   - Downloads/resolves the requested base image (`gcr.io/distroless/cc-debian12:nonroot` or Chainguard).
   - Appends **Supervisor Layer**: Adds `/pokkum/init` executable with pinned USTAR tar headers (`uid=65532`, `gid=65532`, `mode=0555`).
   - Appends **Application Layer**: Adds `/app/server` binary with matching pinned headers.
7. **OCI Manifest & Index Generation**:
   - Generates OCI Schema 1 Manifest for each architecture.
   - Combines single-arch manifests into a multi-arch OCI Image Index.
8. **Supply Chain Attestation & Signing**:
   - Generates SPDX/CycloneDX SBOM via `syft`.
   - Generates SLSA v1.0 provenance attestations wrapped in DSSE envelopes.
   - Signs images using Cosign if configured.
9. **Publishing / Exporting**:
   - Pushes layers, index, signatures, and attestations to `POKKUM_DOCKER_REPO` via `google/go-containerregistry`.
   - If `--local` is specified, loads the image into local Docker daemon.
   - If `--tarball` is specified, writes an OCI tar archive.

---

## 3. How Determinism & Reproducibility Are Achieved

Pokkum guarantees that **identical source code produces identical image digests (`sha256:...`)**.

To achieve this without a Docker daemon:

| Source of Non-Determinism | How Pokkum Pins / Neutralizes It |
|---|---|
| **Host System Clocks** | All tar entry timestamps (`mtime`), OCI image creation timestamps, and history records use `SOURCE_DATE_EPOCH` (derived from git commit timestamp). Nanoseconds are truncated. |
| **Tar Header Metadata** | Uid/Gid are forced to `65532:65532` (`nonroot`), Uname/Gname are wiped, permissions are fixed to `0555`, and format is pinned to `PAX/USTAR`. |
| **Directory Iteration Order** | Go map keys and file listings are explicitly sorted before generating archive entries or config annotations. |
| **Gzip Compression Header** | `tarball.LayerFromOpener` uses gzip with zeroed headers (no filename, zero mtime) so compressed layer digests are 100% stable. |
| **SvelteKit Versioning** | SvelteKit defaults `kit.version.name` to `Date.now()`. Pokkum passes `SOURCE_DATE_EPOCH` to the build environment so `version.json` inside the app binary remains constant. |
| **Base Image Upstream Drift** | `pokkum.lock` records exact SHA256 digests of base images (`distroless`, `chainguard`). Subsequent builds reuse locked digests without registry queries unless `--update-base` or `pokkum base update` is run. |
| **Bun Runtime Toolchain Drift** | `bunruntime` resolver pins version, CPU variant (`standard`/`baseline`), and SHA256 checksums of release binaries. Cached executables (`~/.cache/pokkum/bun`) ensure bit-identical runtime layers. |


---

## 4. Supervisor Contract (`/pokkum/init`)

Because Pokkum container images run without Docker or a full OS init system, the embedded `/pokkum/init` binary handles container runtime requirements:

* **Entrypoint**: `/pokkum/init -- /app/server`
* **Signal Forwarding**: Receives `SIGTERM` / `SIGINT` from Kubernetes/Docker and forwards them to `/app/server`.
* **Zombie Reaping**: Automatically reaps orphaned child processes using `unix.Wait4`.
* **Graceful Shutdown**: Waits up to `POKKUM_SHUTDOWN_TIMEOUT` (default `30s`) for `/app/server` to exit before sending `SIGKILL`.
* **Probes**: Exposes `/healthz` (supervisor status) and `/readyz` (app status) on `POKKUM_PROBE_PORT` (default `8081`).

---

## 5. Kubernetes Integration (`pokkum://` Resolver)

Pokkum includes a native Kubernetes resolver (`pokkum resolve` / `pokkum apply`).

### Workflow

1. In Kubernetes YAML manifests, images can be specified as:
   ```yaml
   image: pokkum://./my-app
   ```
2. Running `pokkum resolve -f manifest.yaml`:
   - Scans the YAML for `pokkum://<path>` references.
   - Automatically builds the SvelteKit app located at `./my-app`.
   - Pushes the built multi-arch image to `POKKUM_DOCKER_REPO`.
   - Injects secure `securityContext` defaults (`runAsNonRoot: true`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault`) unless `--no-security-context` is provided.
   - Replaces `pokkum://./my-app` with the immutable digest:
     ```yaml
     image: ghcr.io/example/my-app@sha256:123456789abcdef...
     ```
3. Running `pokkum apply -f manifest.yaml`:
   - Resolves all references and pipes the resulting manifest directly into `kubectl apply -f -`.

---

## 6. Supply Chain Security & Attestation Architecture

Pokkum embeds supply chain security directly into the image publishing lifecycle:

* **Cosign Image Signing (`internal/adapters/cosign`)**: Signs image manifests using Cosign key pairs or keyless OIDC identity signatures, pushing signature artifacts directly to the target registry.
* **SLSA Provenance Attestations (`internal/adapters/slsa`)**: Produces SLSA v1.0 predicate attestations linking the built image hash back to source git repositories and build commit digests. Includes complete M0 toolchain metadata: Go version, builder OS/architecture (`GOOS/GOARCH`), Bun binary digest, Pokkum commit ID, and `pokkum.lock` SHA256 hashes in `resolvedDependencies`.
* **DSSE Envelope Formatting (`internal/adapters/dsse`)**: Wraps attestations in Dead Simple Signing Envelopes (DSSE) before payload attachment.
* **SBOM Attachments (`internal/adapters/sbom`)**: Generates SPDX or CycloneDX vulnerability bill of materials attached directly as OCI 1.1 referrers by default (`--sbom-attach=referrer`), or under legacy tag conventions (`--sbom-attach=tag`).

---

## 7. Unified Observability & OpenTelemetry Architecture

Pokkum unifies trace spans and metrics into a native, zero-config OpenTelemetry pipeline built on SvelteKit 2.31+ native observability (`kit.experimental.tracing.server` and `kit.experimental.instrumentation.server`):

* **SvelteKit Version Inspection (`internal/adapters/sveltekit/project.go`)**: Inspects `@sveltejs/kit` in `package.json`. If < 2.31.0, telemetry injection is gracefully skipped.
* **Single-Pass Virtual Config Transformer (`internal/adapters/sveltekit/injector.go`)**: Patches `svelte.config.js` in a single virtual pass to enable adapter swapping, version pinning, and experimental tracing/instrumentation flags without mutating source files on disk.
* **Strict Precedence & Virtual Instrumentation (`internal/adapters/sveltekit/telemetry.go`)**: Checks for existing `src/instrumentation.server.ts|js|mjs`. If present, user setup is preserved. If missing, Pokkum generates a virtual instrumentation file configured with OTLP trace & metric exporters, probability sampling (`--trace-sample-rate`), lazy SDK initialization, and metrics-only mode (`--metrics-only`).
* **OTEL Collector Sidecar Injection (`internal/adapters/k8s`)**: When `--with-otel-sidecar` is set, `pokkum resolve` and `pokkum apply` automatically attach an OpenTelemetry Collector sidecar container specification exposing OTLP ports (`4317` gRPC, `4318` HTTP) and Prometheus scraping endpoints (`8889`/`9090`) to the generated Pod specs.

---

## 8. Layer Caching & Packaging Strategies

Pokkum supports two image compilation strategies controlled via `--strategy=layered|exe` (default: `layered`):

### N-Layer Arch-Independent Layout (`--strategy=layered`)
1. **Base Image Layer (Layer 0)**: Distroless Linux runtime (`distroless/cc-debian12:nonroot`).
2. **Bun Runtime Layer (Layer 1)**: Pinned Bun executable (`/usr/local/bin/bun`, per platform) fetched and cached deterministically by `bunruntime.Resolver`.
3. **Supervisor Layer (Layer 2)**: Pokkum supervisor binary (`/pokkum/init`, per platform).
4. **App Server Layer (Layer 3)**: Application JavaScript server bundle (`/app/server/**`, architecture-independent).
5. **App Client Layer (Layer 4)**: Static client assets (`/app/client/**`, architecture-independent).
6. **App Vendor Layer (Layer 5)**: Split dependency JS vendor chunks (`/app/vendor/**`, architecture-independent).
7. **Native Addon Layer (Layer 6)**: Native `.node` binaries and dynamic `.so` closure (`/app/native/**`, platform-specific), inspected and verified by `ClosuredNativeAdapter`.

### Single Executable Strategy (`--strategy=exe`)
Combines supervisor and standalone compiled Bun binary into a 2-layer image (`/pokkum/init` and `/app/server`).



