# Pokkum — Feature List & Known Limitations

This document provides a comprehensive inventory of all implemented features, capabilities, and architectural invariants across Pokkum, followed by explicit documentation of known limitations and design boundaries.

---

## 1. Feature List

### Core Compilation & Container Packaging
- **Zero-Dependency OCI Image Compilation**: Compiles SvelteKit applications directly into multi-arch OCI container images (`linux/amd64`, `linux/arm64`) without Docker daemon or buildkit dependencies.
- **Three Packaging Strategies**:
  - `--strategy=layered` (Default): multi-layer OCI layout — up to Base image, Bun runtime or compiled stub launcher, Supervisor, App server, Client assets, Vendor dependencies, Native addons, and Prerendered pages, though the exact set and count depend on what a given build actually produces (client/vendor/native/prerendered layers are only added when present). Run `pokkum explain` on a built image for its real per-layer breakdown.
  - `--strategy=static` (`--static` shorthand): Pure zero-JS static site compilation onto `chainguard/static` with embedded `pokkum-static` Go web server (PID 1), SPA fallback support, Range requests, strong ETags, and precompressed `.gz/.br/.zst` sidecars.
  - `--strategy=exe` (Legacy): Compiles self-contained single-file executable using Bun compiler (`bun build --compile`).
- **Zero-Config Virtual Auto-Injection**: Surgically transforms `vite.config.ts/js` to inject/override the target adapter inside `sveltekit({ adapter: adapter() })` while preserving runes, compiler options, and other plugins. `bunexec.Compiler.Prepare` generates `.pokkum/<viteConfigName>` and invokes `bun x vite build --config .pokkum/<viteConfigName>` without mutating user-authored files, with fallback to Option C (`checkEffectiveAdapter` fail-fast validation). Because the virtual config executes one directory deeper than the real one, `__dirname`/`__filename`/`import.meta.url` are corrected to the real project directory (Bun injects `__dirname` as an ambient global even under ESM, so the near-universal `path.resolve(__dirname, './src/lib')` pattern used to build `resolve.alias` entries would otherwise silently resolve into `.pokkum/` instead — verified against the real Bun runtime).
- **Layered Runtime Hardening & Startup Attestation**:
  - Option A Compiled Stub Launcher (`--stub-launcher`): Replaces stock Bun CLI with a minimal non-foldable entrypoint launcher (`const p = "/app/server/" + "index.js"; await import(p);`) eliminating the `BUN_BE_BUN` / `bunx` attack surface.
  - Option C Supervisor Startup Attestation (`POKKUM_ATTESTATION_DIGEST`): Computes a deterministic SHA-256 root digest over authoritative post-processing `/app` artifacts, verified by `/pokkum/init` prior to execution (exit 125 on mismatch, reserving exit 126 for binary exec failures).
- **Bun Release Integrity Verification**: Every downloaded Bun release archive is checksum-verified before extraction — a small set of common versions against statically pinned SHA256 digests (no network round trip), and any other `--bun-version` against Bun's own GPG-signed `SHASUMS256.txt.asc` release manifest, verified against an embedded release-signing public key. Fails closed: a download whose trusted checksum can't be established (network failure, invalid signature, no matching manifest entry) or doesn't match is rejected, never silently installed.
- **Composite Remote OCI Input Caching & Anti-Poisoning**: Computes deterministic composite input hashes (`sha256(source + lockfile + baseDigest + bunVersion + platforms + flags)`) to skip builds in sub-100ms on registry cache hits, with mandatory pre-promotion Cosign/Sigstore signature verification (`--cache-verify`).
- **Registry Push Throughput & Zero-Egress Optimizations**:
  - Parallel layer uploads over HTTP/2 with configurable concurrency (`--push-concurrency`, defaulting to 4 workers).
  - Cross-repository blob mounting (`mount=`/`from=`) for zero-egress base layer reuse when targeting same-host registries.
  - Idempotent registry pushes skipping unchanged digests (`remote.Head`).
- **Rolling-Deploy Asset Overlay (`--asset-overlay=<n>` / `--asset-overlay-from=<refs>`)**: Closes the mid-rollout 404 a browser holding a prior generation's HTML hits when it requests an old hashed `_app/immutable/` chunk from a pod already running the new generation. Resolves the last N generations pushed to the same `repo:tag` — registry-side and Kubernetes-independent, via each push's own `pokkum.dev/predecessor` manifest annotation, not `pokkum.dev/image-history` — pulls each one's immutable client assets by digest, and merges non-conflicting files into a new, separate OCI layer (same-path/different-bytes across generations hard-fails the build). Off by default (`0`); resolved source digests join the composite cache-input hash so a cache hit can never serve a stale overlay. Known gap: `pokkum verify --rebuild` does not yet reproduce this layer — see `Vocabulary.md` §13.
- **Static Asset Pre-Compression**: Build-time `.gz`/`.br` sidecar compression for `/app/client` web assets under the layered strategy (adapter-node's bundled `sirv` server never negotiates zstd); `--strategy=static` additionally generates `.zst`, which `pokkum-static` serves.
- **Vendor Layer Pruning & ELF Stripping**: Automatic build-time removal of non-runtime files (`*.d.ts`, `*.map`, `tsconfig.json`, `README*`, tests) from `/app/vendor` and stripping of debug symbols from native `.node` addons.
- **Configurable Layer Compression**: Supports `--compression=gzip` (default) and `--compression=zstd` for high-throughput registry pushes.
- **Embedded PID-1 Process Supervisor (`/pokkum/init`)**:
  - Sub-process management: Reaps orphaned zombie child processes and forwards OS signals (`SIGTERM`, `SIGINT`, `SIGHUP`).
  - Graceful Shutdown & Readiness Drain: Instantly sets readiness probes (`/readyz`) to HTTP 503 on `SIGTERM` while holding open application draining before sending `SIGKILL` after `--shutdown-timeout` (default 30s).
  - Built-in Probes: Exposes lightweight health endpoints (`/livez`, `/readyz`) on dedicated probe port (default 8081).
- **Hermetic Build Mode**: `--hermetic` enforces real, kernel-level network isolation on Linux (an unprivileged network namespace — no IP egress possible for the build subprocess regardless of what a compromised dependency's build-time code tries), falling back to advisory-only (`BUN_OFFLINE=1` and friends) on other platforms with a clear warning. Also strips socket-bearing environment variables (`SSH_AUTH_SOCK` and similar) from the build subprocess, closing SSH-agent-forwarding as an escape vector. Also requires cached base image resolution, pre-populated `node_modules/`, and a pre-cached Bun runtime binary (fails closed rather than downloading). The separate opt-in `--hermetic-mount-isolation` flag additionally blocks a *pathname* Unix domain socket reachable by hardcoded/conventional path (e.g. a bind-mounted `/var/run/docker.sock`) via a `/proc/self/exe` reexec into a fresh mount namespace with that path bind-masked — real and empirically verified, but with an honestly-documented residual limitation (the sandboxed process retains enough capability to self-undo the mask if it specifically knows to try) — see `Roadmap.md`.

### OpenTelemetry & Observability
- **OpenTelemetry SDK Bootstrap** (real as of 2026-08-18 for both `--strategy=exe` and `--strategy=layered`, see `Roadmap.md`'s PR-5 and `Vocabulary.md` §3a for the full picture): `--telemetry` starts a real `@opentelemetry/sdk-node` NodeSDK + OTLP trace exporter before the real app runs, via two mechanisms depending on strategy — a compile-entrypoint wrapper for `--strategy=exe` (`sveltekitutils.PrepareVirtualTelemetryEntry`) and a packaged `bun --preload`'d file for `--strategy=layered` (`sveltekitutils.PrepareLayeredTelemetryBootstrap`), both wired through `pipeline.go` → `bunexec.Compiler.Prepare` → `internal/adapters/packager`. Verified end-to-end for real for both strategies: a real `bun build --compile` + run for `exe`, a real interpreted `bun --preload <path> index.js` run (no compile step) for `layered` — each with a real OTLP export reaching a fake collector, not just unit-tested. **Two further real limitations, both empirically confirmed, not assumed**: no automatic HTTP/framework instrumentation (`@opentelemetry/auto-instrumentations-node`'s module-patching approach does not take effect under Bun's runtime — real spans require the documented `hooks.server.ts` snippet in `Vocabulary.md` §3a); no metrics export (`--metrics-only` is currently non-functional — combining an OTLP metrics exporter with the SDK crashes once compiled, a real Bun bundler bug, not a Pokkum bug).
- **Unified Metrics & Tracing Controls**: Build-time configuration via `--telemetry`, `--no-telemetry`, `--otel-export`, `--telemetry-env` (`dev`, `preview`, `production`), `--trace-sample-rate` (`0.0`–`1.0`), and `--metrics-only` — all parse, validate, and reach a running build; `--metrics-only` specifically warns at runtime rather than doing anything, per the limitation above.
- **Application Metrics Endpoint (`pokkum metrics`)**: Exposes dedicated metrics scraping server via `pokkum metrics` (`--metrics-port`, default 8889).
- **Kubernetes Collector Sidecar Injection**: `--with-otel-sidecar` injects OpenTelemetry Collector sidecar specs (`4317` gRPC, `4318` HTTP, `8889` metrics) directly into generated Kubernetes workload manifests.

### Supply Chain Security & Attestation
- **SLSA v1.0 Provenance Attestation**: Generates and attaches SLSA v1.0 provenance attestations (`<repo>:sha256-<hex>.att`) capturing builder metadata, invocation parameters, and cryptographic dependency digests.
- **Cosign & DSSE Cryptographic Signing**: Automatically generates Cosign signatures (`<repo>:sha256-<hex>.sig`) and DSSE envelopes using local private keys (`POKKUM_SIGNING_KEY`) or Sigstore keyless infrastructure.
- **Software Bill of Materials (SBOM)**:
  - Generates comprehensive SBOMs (SPDX JSON or CycloneDX JSON) cataloging npm dependencies and the embedded Bun runtime (`pkg:generic/bun@<version>`, with its SHA-256 when resolved — the same value recorded in SLSA provenance) for `--strategy=layered`/`--strategy=exe` builds. Does not catalog OS packages.
  - Attaches SBOMs via OCI 1.1 Referrers API (`--sbom-attach=referrer`), legacy tags (`--sbom-attach=tag`), or `--sbom-attach=auto` (default): tries the Referrers API first and falls back to the tag convention on registries that don't support it (ECR, older Harbor, older Artifactory), so a real, discoverable SBOM attachment doesn't require the caller to know their registry's capability in advance.
- **Base Image Signature Verification**:
  - Keyless Sigstore verification (Fulcio OIDC + Rekor transparency logs) against embedded trust roots for stock presets (`distroless`, `chainguard`).
  - Static-key Cosign verification for custom base images (`--base-verify-mode=static-key`, `--base-verify-key=<path>`).
- **Secret-Inlining Guard (`secretguard`)**: Build-time Shannon entropy and regex scanner that prevents bundler secret leaks into OCI layer tarballs (`--allow-secret-pattern` bypass).
- **Runtime Environment Contract**: `--require-env=KEY1,KEY2` stamps runtime variable requirements into OCI annotations (`pokkum.dev/required-env`) and container environment, verified by PID-1 supervisor on boot to fail-fast if absent.
- **adapter-node Reverse-Proxy Contract**: First-class support for `ORIGIN`, `PROTOCOL_HEADER`, `HOST_HEADER`, `ADDRESS_HEADER`, `XFF_DEPTH`, and `BODY_SIZE_LIMIT` via `--origin`/`--protocol-header`/`--host-header`/`--address-header`/`--xff-depth`/`--body-size-limit` (or matching `.pokkum.yaml` `image.*` keys). Closes the most common first-deploy failure for an app behind a reverse proxy/ingress (`403 Cross-site POST form submissions are forbidden`) — `pokkum build` proactively warns when `ORIGIN` is unset for a layered/exe build.
- **Build-Time Environment Baking Detection**: Scans the project's `src/` tree (including `.svelte` components) for `$env/static/public`/`$env/static/private` imports — values SvelteKit inlines as literal build output, pinning the resulting image to whatever environment built it. A build-time warning names the exact bindings found, and they're stamped as a durable `pokkum.dev/env-baked` manifest annotation so the "this image is pinned to one environment" fact survives past the build log. `$env/dynamic/*` (read at container startup, never baked) is correctly excluded.
- **Image Provenance Timeline (`pokkum history <image>`)**: Inspects published OCI manifests and extracts standard `org.opencontainers.image.*` provenance annotations (git commit, repository, build timestamp, version) with `--expect-source` validation.

### Base Image Security & Escrow Management
- **Base Image Lockfile (`pokkum.lock`)**: Resolves and pins base image SHA256 digests across multi-platform indexes, tracking `last_scanned_at`, `vulnerabilities_count`, and `max_severity`.
- **Lockfile Audit & Verification (`pokkum base check`)**: Inspects lockfile status, pinned digests, and vulnerability summaries without triggering image updates.
- **Base Image Escrow / Mirroring**:
  - `pokkum base update --mirror-registry=<repo>` mirrors upstream base images and Cosign `.sig` tags to a project-controlled registry with automatic fallback on upstream unavailability.
  - Verifies Cosign signature claims (`docker-reference`) against the canonical upstream repository in `pokkum.lock` while pulling bytes from the escrow mirror.
- **Base Image CVE Build Gate**:
  - `pokkum build` actively queries OSV.dev for vulnerabilities against the locked base digest.
  - Supports `--fail-on-cve=critical|high|medium|low` (or `POKKUM_FAIL_ON_CVE`) to break builds on vulnerable base digests.
  - Enforces fail-closed handling on incomplete vulnerability database scans (`--allow-incomplete` to opt out).
  - **OpenVEX Exemptions**: `.pokkum.yaml`'s `security.vex_exemptions` lets a specific CVE be excluded from the `--fail-on-cve` threshold, each entry requiring a real OpenVEX justification code, a mandatory expiry, and a mandatory owner (an unjustified or already-expired entry is rejected outright, not silently honored). Exempted CVEs are named in the build warning, stamped onto the image (`dev.pokkum.vex-exemptions` label / `pokkum.dev/vex-exemptions` annotation), and can be exported as a real OpenVEX v0.2.0 JSON document via `--vex-output=<path>` for consumption by Trivy/Grype/Kyverno.

### Security Vulnerability Auditing (`pokkum scan`)
- **Real OS & Dependency Scanning**: Analyzes container images, local projects, and tarballs by extracting packages via Syft and querying OSV.dev batch API (`/v1/querybatch`).
- **Toolchain Vulnerability Awareness**: `--toolchain` checks embedded Bun and SvelteKit runtime versions against published security advisories.
- **Vulnerability Threshold Enforcement**: `--fail-on=low|medium|high|critical` (default `critical`) with JSON/text reporting.

### Reproducibility & Verification (`pokkum verify`, `pokkum repro doctor`)
- **Cryptographic & Semantic Rebuild Verification**:
  - `pokkum verify <ref>` pulls remote manifests and verifies Cosign signatures, DSSE envelopes, SLSA provenance, and `--expect-source` assertions.
  - Multi-tier comparison (`--against <tarball>` or automatic rebuild):
    - **L1**: Exact bit-for-bit manifest digest match.
    - **L2**: Semantic uncompressed RootFS DiffID and container configuration match — content-identical even if the compressed layer bytes differ (e.g. across a `klauspost/compress` version bump between two Pokkum releases).
    - **L3**: Layer-by-layer file diffs with root cause diagnostics.
- **Non-Determinism Bisection (`pokkum repro doctor`)**: Double-builds project to bisect non-deterministic build inputs (unpinned `kit.version.name`, timestamps, unsorted files).

### Kubernetes Integration & Day-2 Operations
- **Declarative URI Resolution (`pokkum resolve`)**: Resolves `pokkum://` image URIs in Kubernetes YAML manifests to immutable image digests (`repo@sha256:...`).
- **Direct Cluster Deployment (`pokkum apply`)**: Resolves manifests and applies them directly to Kubernetes clusters (`kubectl apply -f -`).
- **Monorepo Affected-Detection (`--since=<git-ref>`)**: Diffs each project tree against a base ref and skips builds for unaffected projects that already have a known prior digest (`ports.Reference.Skipped`).
- **Cluster Hardening Defaults**: Injects secure `securityContext` (`runAsNonRoot: true`, `seccompProfile: RuntimeDefault`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`), resource requests/limits, automatic `NetworkPolicy` and `PodDisruptionBudget` manifests, and `readinessProbe`/`livenessProbe`/`startupProbe` defaults against the supervisor's `/readyz`/`/healthz` (checked independently per probe type, so an existing custom probe of one type doesn't block the other two).
- **Multi-Generation Rollback (`pokkum rollback`)**: Rolls back image references in Kubernetes manifests using `pokkum.dev/image-history` annotations with depth selection (`-g`, `--generation=<n>`, `--list`, `--to`).
- **Multi-Registry Authentication (`--registry-config`)**: Shells out to dynamic `docker-credential-*` binaries (ECR, GCR, OSXKeychain) with in-memory caching and fallback to static `auths` blocks.

### Developer Experience & Diagnostics
- **Workspace Initialization (`pokkum init`)**: Guided interactive setup questionnaire for `.pokkum.yaml` (registries, profiles, presets, CVE policy) and `.pokkumignore` (`--defaults` non-interactive mode).
- **Configuration Management (`pokkum config`, `--profile`)**:
  - Build profiles: `pokkum build --profile <name>` (`-P`) evaluates named profile overrides.
  - Inspection & Validation: `pokkum config view` displays resolved configurations and `pokkum config validate` strictly checks schema structure, known fields, and profile consistency.
- **Hot-Reload Development (`pokkum dev`)**: Builds, loads into Docker/Podman daemon, and watches source directories for rapid live reload with `--debug` shell inspection.
- **Preflight Checks & Mechanical Repairs (`pokkum doctor`)**: Audits local Bun runtime, SvelteKit version compatibility, `.pokkumignore`, and registry credentials, offering automatic fixes (`--fix`).
- **Layer & Origin Tracing (`pokkum explain`, `pokkum explain why`, `pokkum explain diff`)**: Reads a real OCI image (registry ref or local tarball) and reports its actual per-layer digests, sizes, file counts, and purposes; traces which real layer a specific file came from, was deleted in, or is absent from entirely; and diffs real layer/file-level changes between two images.
- **Project Adoption Codemod (`pokkum adopt`)**: Automatically migrates SvelteKit projects from `@sveltejs/adapter-node`, `adapter-vercel`, `adapter-auto`, or legacy Dockerfiles to Pokkum compilation defaults.
- **Signed Self-Updates (`pokkum upgrade`)**: Checks releases and verifies release binary checksum signatures via Cosign.
- **Standardized Machine-Readable Output**: Global `--output=json` across all commands emitting versioned `ports.JSONEnvelope` payloads.

---

## 2. Known Limitations & Architectural Boundaries

1. **`pokkum history <image>` vs `pokkum verify <ref>`**:
   - `pokkum history` is an inspection tool that extracts and formats published standard OCI annotations (`org.opencontainers.image.*`). It deliberately does **not** cryptographically verify signatures or SLSA provenance. Users requiring cryptographic authenticity verification must use `pokkum verify`.
2. **Rollback History Accumulation from Static Manifest Templates**:
   - `pokkum resolve` operating in isolation on a static, untouched `pokkum://` manifest file cannot accumulate multi-generation history across independent CLI runs unless intermediate annotations are committed or retrieved from live cluster state (`kubectl get`).
3. **Bun CPU Architecture Variants on x86-64**:
   - The default `standard` Bun binary requires AVX2 CPU instructions on `linux/amd64`. For legacy x86-64 hardware or older virtualization hypervisors lacking AVX2, `--bun-variant=baseline` must be specified.
4. **Zero Clock Access & `SOURCE_DATE_EPOCH`**:
   - Pokkum strictly forbids reading host system clocks during image compilation. All timestamps and tar headers derive from `req.SourceDateEpoch` (resolved from git commit timestamp or explicit environment variable). Outside of a git repo and without `SOURCE_DATE_EPOCH`, Pokkum falls back to a deterministic fallback epoch.
