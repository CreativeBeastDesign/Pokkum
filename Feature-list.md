# Pokkum — Feature List & Known Limitations

This document provides a comprehensive inventory of all implemented features, capabilities, and architectural invariants across Pokkum, followed by explicit documentation of known limitations and design boundaries.

---

## 1. Feature List

### Core Compilation & Container Packaging
- **Zero-Dependency OCI Image Compilation**: Compiles SvelteKit applications directly into multi-arch OCI container images (`linux/amd64`, `linux/arm64`) without Docker daemon or buildkit dependencies.
- **Three Packaging Strategies**:
  - `--strategy=layered` (Default): 8-layer OCI layout (Base image, Bun runtime or compiled stub launcher, Supervisor, App server, Client assets, Vendor dependencies, Native addons, Prerendered pages).
  - `--strategy=static` (`--static` shorthand): Pure zero-JS static site compilation onto `chainguard/static` with embedded `pokkum-static` Go web server (PID 1), SPA fallback support, Range requests, strong ETags, and precompressed `.gz/.br/.zst` sidecars.
  - `--strategy=exe` (Legacy): Compiles self-contained single-file executable using Bun compiler (`bun build --compile`).
- **Zero-Config Virtual Auto-Injection**: Surgically transforms `vite.config.ts/js` to inject/override the target adapter inside `sveltekit({ adapter: adapter() })` while preserving runes, compiler options, and other plugins. `bunexec.Compiler.Prepare` generates `.pokkum/<viteConfigName>` and invokes `bun x vite build --config .pokkum/<viteConfigName>` without mutating user-authored files, with fallback to Option C (`checkEffectiveAdapter` fail-fast validation).
- **Layered Runtime Hardening & Startup Attestation**:
  - Option A Compiled Stub Launcher (`--stub-launcher`): Replaces stock Bun CLI with a minimal non-foldable entrypoint launcher (`const p = "/app/server/" + "index.js"; await import(p);`) eliminating the `BUN_BE_BUN` / `bunx` attack surface.
  - Option C Supervisor Startup Attestation (`POKKUM_ATTESTATION_DIGEST`): Computes a deterministic SHA-256 root digest over authoritative post-processing `/app` artifacts, verified by `/pokkum/init` prior to execution (exit 126 on mismatch).
- **Composite Remote OCI Input Caching & Anti-Poisoning**: Computes deterministic composite input hashes (`sha256(source + lockfile + baseDigest + bunVersion + platforms + flags)`) to skip builds in sub-100ms on registry cache hits, with mandatory pre-promotion Cosign/Sigstore signature verification (`--cache-verify`).
- **Static Asset Pre-Compression**: Build-time `.gz`, `.br`, and `.zst` sidecar compression for `/app/client` web assets.
- **Vendor Layer Pruning & ELF Stripping**: Automatic build-time removal of non-runtime files (`*.d.ts`, `*.map`, `tsconfig.json`, `README*`, tests) from `/app/vendor` and stripping of debug symbols from native `.node` addons.
- **Configurable Layer Compression**: Supports `--compression=gzip` (default) and `--compression=zstd` for high-throughput registry pushes.
- **Embedded PID-1 Process Supervisor (`/pokkum/init`)**:
  - Sub-process management: Reaps orphaned zombie child processes and forwards OS signals (`SIGTERM`, `SIGINT`, `SIGHUP`).
  - Graceful Shutdown & Readiness Drain: Instantly sets readiness probes (`/readyz`) to HTTP 503 on `SIGTERM` while holding open application draining before sending `SIGKILL` after `--shutdown-timeout` (default 30s).
  - Built-in Probes: Exposes lightweight health endpoints (`/livez`, `/readyz`) on dedicated probe port (default 8081).
- **Hermetic Build Mode**: `--hermetic` enforces zero network egress (`BUN_OFFLINE=1`), cached base image resolution, and pre-populated `node_modules/` verification.

### Supply Chain Security & Attestation
- **SLSA v1.0 Provenance Attestation**: Generates and attaches SLSA v1.0 provenance attestations (`<repo>:sha256-<hex>.att`) capturing builder metadata, invocation parameters, and cryptographic dependency digests.
- **Cosign & DSSE Cryptographic Signing**: Automatically generates Cosign signatures (`<repo>:sha256-<hex>.sig`) and DSSE envelopes using local private keys (`POKKUM_SIGNING_KEY`) or Sigstore keyless infrastructure.
- **Software Bill of Materials (SBOM)**:
  - Generates comprehensive SBOMs (SPDX JSON or CycloneDX JSON) cataloging OS and npm dependencies.
  - Attaches SBOMs via OCI 1.1 Referrers API (`--sbom-attach=referrer`, default) or legacy tags (`--sbom-attach=tag`).
- **Base Image Signature Verification**:
  - Keyless Sigstore verification (Fulcio OIDC + Rekor transparency logs) against embedded trust roots for stock presets (`distroless`, `chainguard`).
  - Static-key Cosign verification for custom base images (`--base-verify-mode=static-key`, `--base-verify-key=<path>`).
- **Secret-Inlining Guard (`secretguard`)**: Build-time Shannon entropy and regex scanner that prevents bundler secret leaks into OCI layer tarballs (`--allow-secret-pattern` bypass).
- **Runtime Environment Contract**: `--require-env=KEY1,KEY2` stamps runtime variable requirements into OCI annotations (`pokkum.dev/required-env`) and container environment, verified by PID-1 supervisor on boot to fail-fast if absent.

### Base Image Security & Escrow Management
- **Base Image Lockfile (`pokkum.lock`)**: Resolves and pins base image SHA256 digests across multi-platform indexes, tracking `last_scanned_at`, `vulnerabilities_count`, and `max_severity`.
- **Base Image Escrow / Mirroring**:
  - `pokkum base update --mirror-registry=<repo>` mirrors upstream base images and Cosign `.sig` tags to a project-controlled registry with automatic fallback on upstream unavailability.
  - Verifies Cosign signature claims (`docker-reference`) against the canonical upstream repository in `pokkum.lock` while pulling bytes from the escrow mirror.
- **Base Image CVE Build Gate**:
  - `pokkum build` actively queries OSV.dev for vulnerabilities against the locked base digest.
  - Supports `--fail-on-cve=critical|high|medium|low` (or `POKKUM_FAIL_ON_CVE`) to break builds on vulnerable base digests.
  - Enforces fail-closed handling on incomplete vulnerability database scans (`--allow-incomplete` to opt out).

### Security Vulnerability Auditing (`pokkum scan`)
- **Real OS & Dependency Scanning**: Analyzes container images, local projects, and tarballs by extracting packages via Syft and querying OSV.dev batch API (`/v1/querybatch`).
- **Toolchain Vulnerability Awareness**: `--toolchain` checks embedded Bun and SvelteKit runtime versions against published security advisories.
- **Vulnerability Threshold Enforcement**: `--fail-on=low|medium|high|critical` (default `critical`) with JSON/text reporting.

### Reproducibility & Verification (`pokkum verify`, `pokkum repro doctor`)
- **Cryptographic & Semantic Rebuild Verification**:
  - `pokkum verify <ref>` pulls remote manifests and verifies Cosign signatures, DSSE envelopes, SLSA provenance, and `--expect-source` assertions.
  - Multi-tier comparison (`--against <tarball>` or automatic rebuild):
    - **L1**: Exact bit-for-bit manifest digest match.
    - **L2**: Semantic uncompressed RootFS DiffID and container configuration match (diagnosing gzip framing skew).
    - **L3**: Layer-by-layer file diffs with root cause diagnostics.
- **Non-Determinism Bisection (`pokkum repro doctor`)**: Double-builds project to bisect non-deterministic build inputs (unpinned `kit.version.name`, timestamps, unsorted files).

### Kubernetes Integration & Day-2 Operations
- **Declarative URI Resolution (`pokkum resolve`)**: Resolves `pokkum://` image URIs in Kubernetes YAML manifests to immutable image digests (`repo@sha256:...`).
- **Direct Cluster Deployment (`pokkum apply`)**: Resolves manifests and applies them directly to Kubernetes clusters (`kubectl apply -f -`).
- **Monorepo Affected-Detection (`--since=<git-ref>`)**: Diffs each project tree against a base ref and skips builds for unaffected projects that already have a known prior digest (`ports.Reference.Skipped`).
- **Cluster Hardening Defaults**: Injects secure `securityContext` (`runAsNonRoot: true`, `seccompProfile: RuntimeDefault`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`), resource requests/limits, and automatic `NetworkPolicy` and `PodDisruptionBudget` manifests.
- **Multi-Generation Rollback (`pokkum rollback`)**: Rolls back image references in Kubernetes manifests using `pokkum.dev/image-history` annotations with depth selection (`-g`, `--generation=<n>`, `--list`, `--to`).
- **Multi-Registry Authentication (`--registry-config`)**: Shells out to dynamic `docker-credential-*` binaries (ECR, GCR, OSXKeychain) with in-memory caching and fallback to static `auths` blocks.

### Developer Experience & Diagnostics
- **Hot-Reload Development (`pokkum dev`)**: Builds, loads into Docker/Podman daemon, and watches source directories for rapid live reload with `--debug` shell inspection.
- **Preflight Checks & Mechanical Repairs (`pokkum doctor`)**: Audits local Bun runtime, SvelteKit version compatibility, `.pokkumignore`, and registry credentials, offering automatic fixes (`--fix`).
- **Layer & Origin Tracing (`pokkum explain`, `pokkum why`, `pokkum diff`)**: Breaks down layer compositions, tracks dependency origins, and visualizes diffs between images.
- **Project Adoption Codemod (`pokkum adopt`)**: Automatically migrates SvelteKit projects from `@sveltejs/adapter-node`, `adapter-vercel`, `adapter-auto`, or legacy Dockerfiles to Pokkum compilation defaults.
- **Signed Self-Updates (`pokkum upgrade`)**: Checks releases and verifies release binary checksum signatures via Cosign.
- **Standardized Machine-Readable Output**: Global `--output=json` across all commands emitting versioned `ports.JSONEnvelope` payloads.

---

## 2. Known Limitations & Architectural Boundaries

1. **`pokkum history <image>` vs `pokkum verify <ref>`**:
   - `pokkum history` is an inspection tool that extracts and formats published standard OCI annotations (`org.opencontainers.image.*`). It deliberately does **not** cryptographically verify signatures or SLSA provenance. Users requiring cryptographic authenticity verification must use `pokkum verify`.
2. **Rollback History Accumulation from Static Manifest Templates**:
   - `pokkum resolve` operating in isolation on a static, untouched `pokkum://` manifest file cannot accumulate multi-generation history across independent CLI runs unless intermediate annotations are committed or retrieved from live cluster state (`kubectl get`).
3. **Reproducibility Gzip Framing & Skew (L1 vs L2)**:
   - While Pokkum guarantees bit-for-bit determinism across identical toolchains, rebuilding on different OS/arch or differing Go stdlib `compress/gzip` versions may produce differing gzip compression headers while maintaining 100% semantic identity (L2 match). `pokkum verify` accurately categorizes and diagnoses this skew.
4. **Bun CPU Architecture Variants on x86-64**:
   - The default `standard` Bun binary requires AVX2 CPU instructions on `linux/amd64`. For legacy x86-64 hardware or older virtualization hypervisors lacking AVX2, `--bun-variant=baseline` must be specified.
5. **Zero Clock Access & `SOURCE_DATE_EPOCH`**:
   - Pokkum strictly forbids reading host system clocks during image compilation. All timestamps and tar headers derive from `req.SourceDateEpoch` (resolved from git commit timestamp or explicit environment variable). Outside of a git repo and without `SOURCE_DATE_EPOCH`, Pokkum falls back to a deterministic fallback epoch.
