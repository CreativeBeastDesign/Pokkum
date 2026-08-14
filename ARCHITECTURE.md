# Pokkum Architecture & Technical Deep-Dive

**Pokkum** is a Go-based zero-dependency OCI image builder and deployment pipeline tool specifically designed for SvelteKit applications (using `--strategy=layered` N-layer layout by default, or `--strategy=exe`).

It builds multi-architecture OCI images (`linux/amd64`, `linux/arm64`), embeds a PID-1 supervisor, generates Software Bills of Materials (SBOMs), and pushes reproducible container images directly to OCI registries — **without requiring Docker or a Docker daemon**.

See also [fixes-to-v1.md](fixes-to-v1.md) for a post-v1.0 audit and the
fixes it produced.

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
   - `build.go`: Parses flags (`--platform`, `--base`, `--sbom`, `--sbom-attach`, `--local`, `--tarball`, `--update-base`, `--offline`, `--bun-binary`, `--bun-variant`, `--image-label`, `--base-verify-mode`, `--base-keyless-identity`, `--base-keyless-issuer`, `--sigstore-trusted-root`, `--no-verify-base`, `--allow-secret-pattern`, `--hermetic`, `--registry-config`) and invokes the core build pipeline. Resolves `SOURCE_DATE_EPOCH` first, then unconditionally calls `git_metadata.go`'s `discoverGitMetadata` to auto-populate `org.opencontainers.image.revision`/`.source`/`.version` (from `git`/CI env vars) and `.created` (set to the already-resolved build timestamp, never independently re-derived) before any explicit `--image-label` values are merged in (explicit values win).
   - `scan.go`: Implements `pokkum scan [target]` for security vulnerability scanning, OSV advisory lookups (`--toolchain`), threshold enforcement (`--fail-on`), and JSON reporting (`--output=json`).
   - `dev.go`: Implements `pokkum dev [dir]` subcommand for local container development, supporting `--debug` interactive shell debugging, local Docker daemon loading, and hot-reload source watching.
   - `doctor.go`: Implements `pokkum doctor [dir]` for environment preflight checks (Bun runtime, registry credentials, SvelteKit version compatibility, `.pokkumignore` sanity) and mechanical repairs (`--fix`).
   - `init.go`: Implements `pokkum init [dir]` subcommand to bootstrap project configuration and `.pokkumignore`.
   - `explain.go`: Implements `pokkum explain`, `pokkum why`, and `pokkum diff` subcommands for layer composition breakdown, file origin tracing, and image diffing.
   - `metrics.go`: Implements `pokkum metrics` subcommand to manage and monitor the OpenTelemetry metrics collector endpoint.
   - `verify.go`: Implements `pokkum verify <ref>` subcommand for rebuild verification and SLSA provenance attestation validation (`--no-rebuild`, `--expect-source`, `--against`).
   - `repro_doctor.go`: Implements `pokkum repro doctor [dir]` for stage-level non-determinism bisection (`--fast`, `--perturb`).
   - `base.go`: Implements `pokkum base update` and `pokkum base check` subcommands to query remote base image digests and manage `pokkum.lock`. Neither runs signature verification — digests are pinned trust-on-first-use; `pokkum build` re-verifies the locked digest against the live signature at build time regardless.
   - `resolve.go`: Scans Kubernetes YAML manifests for `pokkum://` image URIs, triggers automated builds, and resolves them to immutable image digests (`repo@sha256:...`), supporting `--registry-config`.
   - `apply.go`: Resolves `pokkum://` manifests and pipes the output directly into `kubectl apply -f -`, supporting `--registry-config`.
   - `rollback.go`: Implements `pokkum rollback -f <manifest> [--to=<ref>]` for rolling back container image references in Kubernetes manifests. `--to` is optional — omitting it reads the `pokkum.dev/previous-image` annotation that `resolve`/`apply` (and `rollback` itself) write into the manifest, so it self-toggles between the two most recent refs. One hop deep only — see [Roadmap.md](Roadmap.md)'s Backlog for multi-generation history.
   - `upgrade.go`: Implements `pokkum upgrade` for release checking (`--check`), signature verification, and self-updating.
   - `k8s.go`: Shared manifest parsing and URI replacement engine.
   - `version.go`: Displays git version, commit, and build timestamp metadata.

5. **Public Packages (`pkg/`)**:
   - `registry`: Ephemeral in-memory OCI 1.1 test registry server utility (`pkg/registry.NewServer()`) for integration testing and local development.

2. **Domain Core (`internal/core/`)**:
   - `pipeline.go`: Orchestrates the execution flow across compilers, base image resolvers, packagers, and registries, supporting `PinnedBuildInputs` override parameters.
   - `model.go`: Defines domain models (`BuildRequest`, `Platform`, `RuntimeConfig`, `ImageRef`, `BunRuntimeOptions`, etc.).
   - `errors.go`: Defines standardized domain error types (`ErrPackageFailed`, `ErrUnsupportedPlatform`, `ErrBunResolutionFailed`, etc.).

3. **Abstraction Ports (`internal/ports/`)**:
   - Interfaces decoupling core logic from external adapters: `Compiler`, `Packager`, `Registry`, `BaseImageResolver`, `BunRuntimeResolver`, `SBOMGenerator`, `SupervisorProvider`, `K8sResolver`, `Signer`, `Attestor`, `BinaryInspector`, `ProvenanceResolver`, `ImageComparator`, `ReleaseVerifier`, `SecretGuard`, and structured output envelopes (`JSONEnvelope`, `OutputFormat`).

4. **Adapter Implementations (`internal/adapters/`)**:
   - Every concrete adapter package enforces explicit compile-time interface assertions (e.g., `var _ ports.BaseImageResolver = (*Resolver)(nil)`) to guarantee immediate compile-time detection of port contract drift.
   - `bunexec`: Wraps host `bun build --compile` for cross-compiling single executables.
   - `bunruntime`: Resolves, downloads, SHA256-verifies, and caches official Bun runtime binaries (`~/.cache/pokkum/bun`) for runtime layer assembly (`ports.BunRuntimeResolver`).
   - `packager`: Constructs reproducible OCI tarballs, custom single-binary layers (`BuildCustomFileLayer`), directory tree layers (`BuildDirectoryTreeLayer`), and multi-arch index manifests using `github.com/google/go-containerregistry`.
   - `baseimage`: Resolves base image layers (`gcr.io/distroless/cc-debian12:nonroot` or Chainguard `glibc-dynamic`), verifies base image signatures (static-key via `ports.CosignSigner` for custom/self-signed bases, or keyless Sigstore via `ports.KeylessVerifier` for stock presets), and maintains `pokkum.lock` digest locks. The resolver picks verification mode from the preset/flag before fetching any signature material — never inferred from wire data, to prevent downgrade attacks.
   - `sigstore`: Implements `ports.KeylessVerifier` to verify Sigstore keyless signatures (Fulcio certificate chain + Rekor transparency log inclusion) using `github.com/sigstore/sigstore-go`, against an embedded public-good trust root snapshot. Fully offline (no live TUF fetch), enabling verification in `--offline`/`--hermetic` builds.
   - `lockfileutils`: Utility package for loading, parsing, and saving `pokkum.lock` base image lockfiles.
   - `jsonutils`: Utility package for structured, versioned JSON response formatting (`--output=json`).
   - `diagnosticsutils`: Utility package for container exit failure analysis and log tracing.
   - `layerdiffutils`: Utility package for entry-by-entry tar merge-walking and header/content diff heuristics.
   - `provenance`: Adapter resolving remote SLSA provenance statements and Cosign attestations.
   - `comparator`: Adapter performing L1/L2/L3 multi-level image digest and file diff comparisons.
   - `registry`: Handles OCI registry authentication (including per-registry auth chains from a `docker config.json`-style file via `--registry-config`), blob uploads, and index pushes.
   - `sbom`: Generates SPDX or CycloneDX SBOMs using `github.com/anchore/syft`.
   - `supervisor`: Embedded supervisor binary assets (`/pokkum/init`).
   - `k8s`: Kubernetes manifest inspection, document rewriting, and `pokkum://` schema resolution.
   - `sveltekit`: Checks `@jesterkit/exe-sveltekit` adapter installation in target projects.
   - `cosign`: Signs OCI images, attaches Cosign signatures to OCI registries, and implements `ports.ReleaseVerifier` for release signature and SHA-256 checksum verification.
   - `secretguard`: Build-time entropy and pattern scanner for detecting hardcoded secrets in source files before image packaging (`ports.SecretGuard`).
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
   - Injects default container resource `requests` (`cpu: 50m`, `memory: 64Mi`) and `limits` (`memory: 256Mi`) unless `--no-resource-defaults` is provided.
   - Appends a `PodDisruptionBudget` document (`minAvailable: 1`) unless `--no-resource-defaults` is provided, with `selector.matchLabels` scoped to the workload's own Pod-template labels (read from `spec.template.metadata.labels`, or `metadata.labels` for a bare Pod) rather than matching every Pod in the namespace. Skipped entirely — no document emitted — when no labels can be found on the workload, since a namespace-wide selector would be actively wrong rather than merely imprecise.
   - Appends a `NetworkPolicy` document restricting ingress to actual workload container ports (`containerPort`, defaulting to 3000 and 8081) and egress to expected infrastructure ports (DNS 53, HTTPS 443, OTLP 4317/4318/8889) unless `--no-network-policy` is provided. `podSelector` is scoped to the same workload labels as the PodDisruptionBudget above when available, falling back to an unscoped selector (`{}`) only when no labels can be found.
   - Replaces `pokkum://./my-app` with the immutable digest:
     ```yaml
     image: ghcr.io/example/my-app@sha256:123456789abcdef...
     ```
   - When the value being replaced was already a concrete image reference (not a `pokkum://` URI — i.e. this is a re-resolve of a previously-resolved manifest), records it in a `pokkum.dev/previous-image` annotation before overwriting, so a later `pokkum rollback -f manifest.yaml` (no `--to` needed) can undo this one change.
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

---

## 9. Diagnostic Wizards, Structured JSON Schemas & Machine-Readable DX (`v0.4`)

Pokkum provides a machine-readable developer experience tailored for CI/CD automation and developer diagnostics:

* **Structured JSON Schema Standard (`--output=json`)**: Standardizes CLI stdout responses across commands using a versioned JSON envelope (`ports.JSONEnvelope`, `schema_version: "1.0"`). Formatted by `internal/adapters/jsonutils`. Non-JSON logs are consistently routed to `stderr`.
* **Environment Preflight Diagnostics (`pokkum doctor`)**: Performs automated environment checks (`bun` binary, registry auth credentials, SvelteKit version compatibility, `.pokkumignore` sanity) with optional mechanical auto-repairs (`--fix`).
* **Project Initializer (`pokkum init`)**: Bootstraps workspace configuration and `.pokkumignore` defaults.
* **Layer Composition & Origin Tracing (`pokkum explain`, `pokkum why`, `pokkum diff`)**: Inspects container layer hierarchies, traces file origins to specific build outputs, and computes size diffs between images.
* **Interactive Container Failure Diagnostics (`internal/adapters/diagnosticsutils`)**: Automatically analyzes container crash exit codes (127 loader failures, 137 OOMKilled, port conflicts) and provides actionable remediation suggestions.

---

## 10. Rebuild Verification & Non-Determinism Diagnosis (`v0.5`)

Pokkum provides bit-for-bit rebuild verification and stage-level non-determinism bisection:

* **Attestation & Provenance Validation (`pokkum verify --no-rebuild`)**: Validates SLSA v1.0 attestations and Cosign signatures via `internal/adapters/provenance`, inspecting `PinnedBuildInputs` to predict toolchain compatibility.
* **Shared `layerdiff` Tar Engine (`internal/adapters/layerdiffutils`)**: Performs entry-by-entry merge-walking over uncompressed layer tarballs, diffing headers (mtime, mode, size) and SHA256 content hashes to report root cause heuristics (timestamp drift, host path embedding, unsorted maps).
* **Pipeline Stage Bisection (`pokkum repro doctor`)**: Runs static non-determinism checks (`--fast`) and dual pipeline builds in perturbed environments (`--perturb`) to pinpoint non-deterministic build steps.
* **Multi-Level Rebuild Verification (`pokkum verify --rebuild`)**: Rebuilds the image from source in a clean temporary git worktree (`git worktree add`), compares remote vs local tarball across L1 (exact OCI index match), L2 (uncompressed `diffIDs` match), and L3 (file-level `layerdiff` report).





