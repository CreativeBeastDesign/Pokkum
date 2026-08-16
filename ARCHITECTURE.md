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
   - `build.go`: Parses flags (`--platform`, `--base`, `--sbom`, `--sbom-attach`, `--local`, `--tarball`, `--update-base`, `--offline`, `--bun-binary`, `--bun-variant`, `--image-label`, `--base-verify-mode`, `--base-keyless-identity`, `--base-keyless-issuer`, `--sigstore-trusted-root`, `--no-verify-base`, `--allow-secret-pattern`, `--require-env`, `--fail-on-cve`, `--allow-incomplete`, `--hermetic`, `--registry-config`) and invokes the core build pipeline. Resolves `SOURCE_DATE_EPOCH` first, then unconditionally calls `git_metadata.go`'s `discoverGitMetadata` to auto-populate `org.opencontainers.image.revision`/`.source`/`.version` (from `git`/CI env vars) and `.created` (set to the already-resolved build timestamp, never independently re-derived) before any explicit `--image-label` values are merged in (explicit values win).
   - `scan.go`: Implements `pokkum scan [target]` for security vulnerability scanning, OSV advisory lookups (`--toolchain`), threshold enforcement (`--fail-on`), offline scanning (`--offline`), incomplete scan handling (`--allow-incomplete`), and JSON reporting (`--output=json`).
   - `dev.go`: Implements `pokkum dev [dir]` subcommand for local container development, supporting `--debug` interactive shell debugging, local Docker daemon loading, and hot-reload source watching.
   - `doctor.go`: Implements `pokkum doctor [dir]` for environment preflight checks (Bun runtime, registry credentials, SvelteKit version compatibility, `.pokkumignore` sanity, base image security) and mechanical repairs (`--fix`).
   - `init.go`: Implements `pokkum init [dir]` subcommand to bootstrap project configuration and `.pokkumignore`.
   - `explain.go`: Implements `pokkum explain`, `pokkum why`, and `pokkum diff` subcommands for layer composition breakdown, file origin tracing, and image diffing.
   - `metrics.go`: Implements `pokkum metrics` subcommand to manage and monitor the OpenTelemetry metrics collector endpoint.
   - `verify.go`: Implements `pokkum verify <ref>` subcommand for rebuild verification and SLSA provenance attestation validation (`--no-rebuild`, `--expect-source`, `--against`, `--registry-config`).
   - `repro_doctor.go`: Implements `pokkum repro doctor [dir]` for stage-level non-determinism bisection (`--fast`, `--perturb`).
   - `base.go`: Implements `pokkum base update` and `pokkum base check` subcommands to query remote base image digests, manage `pokkum.lock`, and escrow mirror base images and Cosign `.sig` tags (`--mirror-registry`).
   - `adopt.go`: Implements `pokkum adopt [dir]` migration codemod to convert existing SvelteKit apps (`adapter-node`, `adapter-vercel`, `adapter-auto`, `adapter-cloudflare`) to Pokkum compilation defaults, rewriting `package.json`/`svelte.config.js`, bootstrapping `.pokkumignore`, and optionally deleting legacy Dockerfiles (`--dry-run`, `--remove-dockerfile`).
   - `history.go`: Implements `pokkum history <image>` subcommand for inspecting image provenance timelines, SLSA attestations, Cosign signatures, builder metadata, and CI workflow run links (`--expect-source`, `--output=json`).
   - `resolve.go`: Scans Kubernetes YAML manifests for `pokkum://` image URIs, triggers automated builds, and resolves them to immutable image digests (`repo@sha256:...`), supporting `--registry-config`.
   - `apply.go`: Resolves `pokkum://` manifests and pipes the output directly into `kubectl apply -f -`, supporting pre-flight live cluster annotation inspection (`--cluster-inspect`, `--no-cluster-inspect`) and `--registry-config`.
   - `rollback.go`: Implements `pokkum rollback -f <manifest> [--to=<ref>]` for rolling back container image references in Kubernetes manifests across multiple generations using `pokkum.dev/image-history` annotations (`-g`, `--generation=<n>`, `--list`) or an explicit target reference (`--to`).
   - `upgrade.go`: Implements `pokkum upgrade` for release checking (`--check`), signature verification via `ports.ReleaseVerifier`, and self-updating.
   - `k8s.go`: Shared manifest parsing, security context/network policy injection, and URI replacement engine.
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
    - `bunexec`: Manages SvelteKit preparation and compilation. Supports Option B zero-config virtual Vite config injection (`.pokkum/vite.config.ts`), invoking `bunx vite build --config .pokkum/vite.config.ts` without mutating user-authored files, with fallback to Option C `checkEffectiveAdapter` fail-fast validation.
    - `bunruntime`: Resolves, downloads, SHA256-verifies, and caches official Bun runtime binaries (`~/.cache/pokkum/bun`) or compiles and caches hardened minimal stub entrypoint launchers (`stubs/<version>/<variant>/<platform>/bun`) when `StubLauncher` (`--stub-launcher`) is enabled. Supports local custom binary bypass (`--bun-binary`).
    - `packager`: Constructs reproducible OCI tarballs, custom single-binary layers (`BuildCustomFileLayer`), directory tree layers (`BuildDirectoryTreeLayer`), and multi-arch index manifests using `github.com/google/go-containerregistry`.
   - `baseimage`: Resolves base image layers (`gcr.io/distroless/cc-debian12:nonroot` or Chainguard `glibc-dynamic`), verifies base image signatures (static-key via `ports.CosignSigner` for custom/self-signed bases, or keyless Sigstore via `ports.KeylessVerifier` for stock presets), and maintains `pokkum.lock` digest locks. Supports base image escrow mirroring via `--mirror-registry` on `pokkum base update`, copying base images/indexes and their associated Cosign `.sig` tags with automated `MirrorRef` fallback in `pokkum.lock`.
   - `sigstore`: Implements `ports.KeylessVerifier` to verify Sigstore keyless signatures (Fulcio certificate chain + Rekor transparency log inclusion) using `github.com/sigstore/sigstore-go`, against an embedded public-good trust root snapshot. Fully offline (no live TUF fetch), enabling verification in `--offline`/`--hermetic` builds.
   - `scanner`: Implements `ports.Scanner` for security vulnerability scanning (`pokkum scan`). Catalogs container images, tarballs, and project workspaces using lightweight zero-dependency native parsers (`scannerutils`), queries OSV.dev in high-performance batches (`/v1/querybatch`) for ecosystem-aware OS package CVE lookup (Debian, Ubuntu, Alpine, Wolfi, Chainguard, npm), and enforces severity thresholds (`--fail-on`).
   - `scannerutils`: Utility package providing zero-dependency native parsers for Debian `dpkg/status`, Alpine `apk/db/installed`, `os-release`, and Node.js lockfiles (`bun.lock`, `package-lock.json`, `pnpm-lock.yaml`).
   - `pruneutils`: Utility package providing junk-file blocklisting and directory pruning for `/app/vendor` layer optimization (`*.d.ts`, `*.map`, `tsconfig.json`, `README*`, tests).
   - `precompressutils`: Utility package providing build-time static asset pre-compression (`.gz`, `.br`, `.zst`) for `/app/client` web assets.
   - `striputils`: Utility package providing build-time stripping of unneeded debug symbols from native `.node` ELF addons and `.so` shared libraries in `/app/native` and `/app/vendor`.
    - `attestutils`: Utility package defining the shared startup-attestation record/digest algorithm (`Record`, `RootDigest` — sha256 over `<rel>\x00<sha>\n` records globally sorted by rel). Used by the packager (computes the expected digest while building layers) and mirrored verbatim by `pokkum-init` (re-derives it from the live `/app` tree); parity is pinned by `attestutils_test.go`'s mirrored-walk tests.
   - `remotecacheutils`: Utility package implementing `ports.RemoteCacher` for composite build input hashing (`sha256(source + lockfile + baseDigest + bunVersion + platforms + flags)`), sub-100ms remote registry cache querying, pre-promotion cryptographic Cosign/Sigstore signature verification, and tag reconciliation.
   - `poolutils`: Utility package managing `sync.Pool` allocation recycling for 64KB I/O copy buffers and bounded `bytes.Buffer` instances to eliminate GC thrashing during tar archiving, layer streaming, hashing, and pre-compression.
   - `layercacheutils`: Utility package managing local on-disk caching (`~/.cache/pokkum/layers/`) for immutable layer blobs (Bun runtime, `pokkum-init` supervisor) to skip tarring and compression.
   - `lockfileutils`: Utility package for loading, parsing, and saving `pokkum.lock` base image lockfiles, tracking `pinned_ref`, `mirror_ref`, `last_scanned_at`, `vulnerabilities_count`, and `max_severity`.
   - `jsonutils`: Utility package for structured, versioned JSON response formatting (`--output=json`).
   - `diagnosticsutils`: Utility package for container exit failure analysis and log tracing.
   - `layerdiffutils`: Utility package for entry-by-entry tar merge-walking and header/content diff heuristics.
   - `provenance`: Adapter resolving remote SLSA provenance statements and Cosign attestations.
   - `comparator`: Adapter performing L1/L2/L3 multi-level image digest and file diff comparisons.
   - `registry`: Handles OCI registry authentication (including per-registry auth chains from a `docker config.json`-style file via `--registry-config`), blob uploads, and index pushes.
   - `registryutils`: Utility package for resolving Docker `config.json` auth keychains, executing dynamic credential helpers (`credHelpers`, `credsStore`), and caching credentials in-memory.
   - `sbom`: Generates deterministic SPDX 2.3 or CycloneDX 1.5 SBOMs natively without external cataloger dependencies.
   - `supervisor`: Embedded `pokkum-init` supervisor binary assets (`/pokkum/init`), stored zstd-compressed to shrink the CLI footprint and decompressed on-the-fly to the raw ELF by `ports.SupervisorProvider`.
    - `k8s`: Kubernetes manifest inspection, document rewriting, and `pokkum://` schema resolution.
    - `gitutils`: Utility package for git-based monorepo affected-project detection (`ports.AffectedDetector`, `--since` on `resolve`/`apply`), declaring `const IsUtilityPackage = true`. It diffs each project tree against a base ref (`git diff --name-only <ref> -- .`) plus a `git status --porcelain` check for untracked/staged/deleted changes, validating the ref via `rev-parse --verify <ref>^{commit}` and failing closed on git errors.
   - `sveltekit`: Checks `@jesterkit/exe-sveltekit` adapter installation in target projects.
   - `cosign`: Signs OCI images, attaches Cosign signatures to OCI registries, and implements `ports.ReleaseVerifier` for release signature and SHA-256 checksum verification.
   - `secretguard`: Build-time entropy and pattern scanner for detecting hardcoded secrets in source files before image packaging (`ports.SecretGuard`).
   - `slsa`: Generates SLSA v1.0 provenance predicate statements.
   - `dsse`: Wraps attestations in Dead Simple Signing Envelopes (DSSE).
   - `config`: Implements `ports.ConfigManager` for reading, writing, and validating `.pokkum.yaml` project configurations, evaluating build profiles (e.g. `local`, `production`), and enforcing precedence: explicit CLI flags > environment variables > profile overrides > top-level config defaults.
   - `ignore`: Reads `.pokkumignore` patterns to exclude unwanted files (`.env.local`, source maps, fixtures).
   - `nativeinspect`: Inspects compiled binaries (`DT_NEEDED`, glibc symbols) to ensure base image compatibility.



5. **PID-1 Supervisor Subproject (`supervisor/cmd/pokkum-init`)**:
   - A standalone Go program cross-compiled to `linux/amd64` and `linux/arm64` and embedded in Pokkum.
   - Acts as `ENTRYPOINT ["/pokkum/init", "--", "/app/server"]`.
   - Handles PID-1 duties (reaping zombie sub-processes, forwarding OS signals like SIGTERM/SIGINT).
   - Serves HTTP health endpoints (`/healthz`, `/readyz`) on `POKKUM_PROBE_PORT` (default: 8081).
   - **Fast startup overlap**: `main` resolves the child executable up front (`resolveChildPath` → `New(..., WithChildPath(...))`), so `start()` forks immediately with no `exec.LookPath` on the fork path; the probe listener binds `/healthz` on its own goroutine concurrently with the Bun fork/exec rather than serializing after it. Guarded by `startup_test.go`.

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
   - Resolves the requested base image (`gcr.io/distroless/cc-debian12:nonroot` or Chainguard).
   - Employs **Single-Pass Layer Hashing & Streaming** (`buildSinglePassLayer`): Simultaneously calculates uncompressed `DiffID` and compressed `Digest` while streaming tar archives into compressed temporary layers, eliminating multiple disk passes and answering `v1.Layer` queries in `O(1)` time.
   - Appends **Supervisor Layer**: Adds `/pokkum/init` executable with pinned USTAR tar headers (`uid=65532`, `gid=65532`, `mode=0555`), leveraging `layercacheutils` on-disk caching.
   - Appends **Application Layers**: Adds `/app/server`, `/app/client`, `/app/vendor` (with `pruneutils` junk stripping), `/app/native`, and `/usr/local/bin/bun`.
7. **OCI Manifest & Index Generation**:
   - Generates OCI Schema 1 Manifest for each architecture.
   - Combines single-arch manifests into a multi-arch OCI Image Index.
8. **Supply Chain Attestation & Signing**:
   - Generates deterministic SPDX/CycloneDX SBOM natively via `sbom` adapter.
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

The Pokkum **CLI binary itself** is built reproducibly and size-optimized in every release path with the compiler build optimization flags (Roadmap "Compiler Build Optimization Flags"): `-trimpath` (strips absolute source paths, improving reproducibility and discarding path leakage) plus `-ldflags="-s -w"` (strips the DWARF symbol table / debug info) for a significant binary size reduction. This is enforced across the `Makefile` `build`/`supervisor` targets, `.goreleaser.yaml` (the official `pokkum upgrade` release pipeline), and `.github/workflows/slsa-builder.yml` (the SLSA L3 / trusted-builder path); all three also inject `-X main.version/commit/buildDate` so `pokkum version` carries real release metadata. `scripts/check-build-flags.sh` (run as Step 0 of `make verify`) guards against any of the three paths silently dropping the flags.


---

## 4. Supervisor Contract (`/pokkum/init`)

Because Pokkum container images run without Docker or a full OS init system, the embedded `/pokkum/init` binary handles container runtime requirements:

* **Entrypoint**: `/pokkum/init -- /app/server`
* **Signal Forwarding**: Receives `SIGTERM` / `SIGINT` from Kubernetes/Docker and forwards them to `/app/server`.
* **Zombie Reaping**: Automatically reaps orphaned child processes using `unix.Wait4`.
* **Graceful Shutdown**: Waits up to `POKKUM_SHUTDOWN_TIMEOUT` (default `30s`) for `/app/server` to exit before sending `SIGKILL`.
* **Probes**: Exposes `/healthz` (supervisor status) and `/readyz` (app status) on `POKKUM_PROBE_PORT` (default `8081`).

**Compressed embedding**: The `pokkum-init` binaries are cross-compiled by `make supervisor` into the `internal/adapters/supervisor/bin` directory and embedded zstd-compressed (`.zst`) into the `pokkum` CLI, cutting the embedded footprint from ~12 MB to ~4.7 MB. `ports.SupervisorProvider.Binary` decompresses the blob on-the-fly so the bytes written to `/pokkum/init` remain the bit-identical raw ELF — image digests, layer cache keys, and supervisor version labels are unaffected.

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
   - **Monorepo affected-detection (`--since=<git-ref>`)**: as an optimization, each `pokkum://` project's source tree is diffed against a base git ref (`internal/adapters/gitutils`). A project that has **not** changed since that ref, and for which a prior digest is known — from its manifest `pokkum.dev/current-image` annotation or from live cluster state when inspecting — **skips compilation and packaging entirely** and reuses that digest in the emitted manifest (logged `reused unaffected image reference`, surfaced via `ports.Reference.Skipped`). If no prior digest is known, or the project is affected, it is built normally, so every emitted reference is a real `repo@sha256:…`. `--since` is fail-closed: an unknown ref or a git error fails the resolve rather than silently building or silently skipping.
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

Pokkum supports three image compilation strategies controlled via `--strategy=layered|exe|static` (default: `layered`; `--static` is the shorthand for `--strategy=static`):

### N-Layer Arch-Independent Layout (`--strategy=layered`)
1. **Base Image Layer (Layer 0)**: Distroless Linux runtime (`distroless/cc-debian12:nonroot`).
2. **Bun Runtime Layer (Layer 1)**: Pinned Bun executable (`/usr/local/bin/bun`, per platform) fetched and cached deterministically by `bunruntime.Resolver`.
3. **Supervisor Layer (Layer 2)**: Pokkum supervisor binary (`/pokkum/init`, per platform).
4. **App Server Layer (Layer 3)**: Application JavaScript server bundle (`/app/server/**`, architecture-independent).
5. **App Client Layer (Layer 4)**: Static client assets (`/app/client/**`, architecture-independent).
6. **App Vendor Layer (Layer 5)**: Split dependency JS vendor chunks (`/app/vendor/**`, architecture-independent).
7. **Native Addon Layer (Layer 6)**: Native `.node` binaries and dynamic `.so` closure (`/app/native/**`, platform-specific), inspected and verified by `ClosuredNativeAdapter`.
8. **Prerendered Layer (Layer 7)**: Prerendered static pages (`/app/prerendered/**`, architecture-independent). The generated `handler.js` is patched at build time to resolve this tree via the `POKKUM_PRERENDERED_DIR` env, which the packager sets to `/app/prerendered`, so prerendered pages serve from their own slim layer instead of being dropped.

**Layered startup attestation (hardening Option C)**: while assembling the layered layers above, the packager also computes a deterministic SHA-256 root digest over the authoritative post-processing `/app` tree (server/client/vendor/native/prerendered — records emitted per layer by `BuildDirectoryTreeLayerWithPruning`, so pruning, precompression sidecars and stripping are already applied; shared algorithm in `internal/adapters/attestutils`) and stamps it as the `POKKUM_ATTESTATION_DIGEST` env in the image config. At container startup, `/pokkum/init` mirrors the algorithm (`supervisor/cmd/pokkum-init/attest.go`), re-derives the digest from the live `/app` tree before exec, and refuses to start (exit 126) on mismatch — restoring the tamper-evidence property of a sealed artifact without depending on cluster-level `readOnlyRootFilesystem`. Delivering the expected value via image config env (rather than baking a file into the supervisor layer) keeps the immutable supervisor-layer cache intact. Only the layered strategy attests; exe (sealed binary) and static (no supervisor) do not.

### Single Executable Strategy (`--strategy=exe`)
Combines supervisor and standalone compiled Bun binary into a 2-layer image (`/pokkum/init` and `/app/server`).

### Static Strategy (`--strategy=static`)
Compiles a purely static site (all routes prerendered) onto a minimal libc-free `cgr.dev/chainguard/static` base. There is **no Bun runtime, no server JS, and no separate supervisor**: the embedded statically-linked `pokkum-static` binary (`ports.StaticServerProvider`) is PID 1 at `/pokkum/static`, acting as both entrypoint and probe server. It serves the `/app/client` and `/app/prerendered` trees (the `POKKUM_STATIC_ROOTS` env), performing file serving with Range requests, strong ETags, and Content-Encoding negotiation against the `.gz`/`.br`/`.zst` sidecars `precompressutils` generates. **Opt-in SPA fallback**: when the source project configures an `@sveltejs/adapter-static` `fallback` page, the packager stages it in the client layer and stamps `POKKUM_STATIC_FALLBACK` (an in-image path), so `pokkum-static` serves that shell with `200` on unmatched `GET`/`HEAD` routes; the file is never silently dropped (a configured-but-unemitted fallback fails the build). Because the strategy feeds `RemoteCacheInputRequest.Strategy`, static builds get their own remote-cache key space, distinct from layered/exe. Source is `@sveltejs/adapter-static` staged at `.svelte-kit/output`.

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





