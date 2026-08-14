# Pokkum Core Architecture

Pokkum is a Go-based zero-dependency OCI container image compiler for SvelteKit applications. It compiles multi-arch images (`linux/amd64`, `linux/arm64`) using `--strategy=layered` (N-layer layout with pinned Bun runtime by default) or `--strategy=exe`, embedding a PID-1 supervisor (`/pokkum/init`), generating native deterministic SPDX/CycloneDX SBOMs, and pushing directly to registries without a Docker daemon.

## Major Domains
- Architecture & Ports: `mem:conventions`
- Tech Stack & Dependencies: `mem:tech_stack`
- OpenTelemetry & Metrics Pipeline: `mem:telemetry`
- Verification & Test Commands: `mem:task_completion`

## Invariants
- Hexagonal architecture: `internal/ports` is the leaf dependency graph node; core never imports adapters.
- Bit-for-Bit reproducibility: Timestamps, tar headers, base image digests (`pokkum.lock`), and `kit.version.name` are pinned to `SOURCE_DATE_EPOCH`.
- Zero-config auto-injection: `svelte.config.js` and `instrumentation.server.ts` transforms happen in virtual memory sandboxes without mutating disk sources.
- `sync.Pool` Buffer Recycling: `internal/adapters/poolutils` recycles 64KB I/O copy buffers and bounded `bytes.Buffer` instances across tarball archiving (`packager`), layer caching (`layercacheutils`), input hashing (`remotecacheutils`), and static asset pre-compression (`precompressutils`), eliminating GC allocation thrashing.
- Composite Remote OCI Input Caching: `internal/adapters/remotecacheutils` implements `ports.RemoteCacher`, computing deterministic composite input hashes (`sha256(source + lockfile + baseDigest + bunVersion + platforms + flags)`) to skip builds in sub-100ms when matching cache tags (`<repo>:cache-<hash>`) exist in the remote registry (opt-out via `--no-cache`).
- Static Asset Pre-Compression: `internal/adapters/precompressutils` automatically pre-compresses `/app/client` web assets with `.gz` (Gzip), `.br` (Brotli), and `.zst` (Zstandard) sidecars within the build sandbox, inheriting `SOURCE_DATE_EPOCH` (configurable via `--no-precompress`).
- ELF Native Addon Stripping: `internal/adapters/striputils` automatically strips unneeded debug symbols from native `.node` addons and `.so` libraries in `/app/native` and `/app/vendor` within the build sandbox (`--no-strip`).
- Single-Pass Layer Hashing & Streaming: `internal/adapters/packager` computes uncompressed `DiffID` and compressed `Digest` simultaneously via `buildSinglePassLayer`, eliminating multiple disk read passes and compressing once per layer.
- Base Image Lockfile & Escrow: `pokkum.lock` automatically resolves and pins base image digests to guarantee reproducible builds across machines and time (`--update-base`, `--offline`, `pokkum base update`). Supports escrow mirroring (`--mirror-registry`) of base images and Cosign `.sig` tags to project registries with automatic lockfile fallback.
- Vulnerability Scanning & Reactivity Gate: `pokkum scan` performs lightweight zero-dependency targeted OS package (`dpkg/status`, `apk/db/installed`) and toolchain CVE audits against OSV.dev batch API (`internal/adapters/scannerutils`); `pokkum build` actively scans base images on resolve, logging warnings by default and actively failing on vulnerable base image digests (`--fail-on-cve`, `POKKUM_FAIL_ON_CVE`) with fail-closed incomplete scan handling (`--allow-incomplete`) and automatic audit recording in `pokkum.lock` (`last_scanned_at`, `vulnerabilities_count`, `max_severity`).
- Vendor Layer Pruning: Automatic build-time pruning (`internal/adapters/pruneutils`) strips junk files (`*.d.ts`, `*.map`, `tsconfig.json`, `README*`, tests) from `/app/vendor`, saving 15–35MB per container image without mutating local user `node_modules` (configurable via `--no-prune` and `--keep-vendor`).
- Pre-Calculated & Cached Immutable Layers: `internal/adapters/layercacheutils` caches compressed immutable layer blobs (`/usr/local/bin/bun`, `/pokkum/init`) on disk (`~/.cache/pokkum/layers/`), skipping ~100MB of tarring and gzip/zstd compression per build.
- Supply chain security: SLSA v1.0 provenance, Cosign digests, and DSSE envelopes are automatically generated and signed during `pokkum build` pipeline unless `--no-sign` is provided.
- Kubernetes Integration & Live History: `pokkum resolve` and `pokkum apply` resolve `pokkum://` URIs, inject hardened security defaults, and track multi-generation deployment history. `pokkum apply` performs pre-flight live cluster annotation queries (`--cluster-inspect`, `--no-cluster-inspect`) to seed deployment history across independent CLI runs on static manifest templates.
- SBOM Attachment: Native deterministic SBOMs (SPDX 2.3 / CycloneDX 1.5) are attached by default as OCI 1.1 referrers (`--sbom-attach=referrer`), with legacy `.sbom` tag fallback (`--sbom-attach=tag`).
- Keyless Sigstore Base Verification: Stock presets (`distroless`/`chainguard`) use keyless Sigstore verification (Fulcio+Rekor) against embedded trust roots by default, while custom bases use static-key Cosign verification (`--base-verify-mode`).
- Hot-Reload Dev Environment: `pokkum dev` builds, loads into Docker daemon, runs, and watches source files for auto-rebuilding with interactive shell debugging (`--debug`).