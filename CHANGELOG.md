# Changelog

All notable changes to Pokkum are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

Pokkum is in active development. Features in `docs/Roadmap.md` marked as shipped are included below.

### Added

- **Bun/supervisor layer diffID stability** — immutable-binary layers (Bun, `pokkum-init`, `pokkum-static`) now use a fixed epoch and disable Go's VCS stamping so their digest stops churning on every commit
- **Bun release checksum verification** — every downloaded Bun release archive is checksum-verified before extraction
- **Hermetic build mode** (`--hermetic`) — enforces real Linux network-namespace isolation during builds
- **Layered-strategy runtime hardening** — compiled entrypoint launcher and startup attestation for `--strategy=layered`
- **Zero-dependency multi-arch OCI compilation** — compiles SvelteKit projects directly to linux/amd64 and linux/arm64 OCI images without Docker daemon
- **Registry push throughput & composite remote-cache** — parallel HTTP/2 layer uploads, cross-repo blob mounting, `--tag` support
- **Rolling-deploy asset overlay** (`--asset-overlay`) — merges previous generations' immutable client assets into a separate registry-side overlay layer
- **Node runtime support** (`--runtime=node`) — targets `distroless-node` base with adapter-node output under `/nodejs/bin/node`
- **Static strategy** (`--strategy=static`) — compiles pure static SvelteKit sites onto `chainguard/static` with embedded pokkum-static file server as PID 1
- **pokkum adopt** — migrates SvelteKit projects off legacy adapters and Dockerfiles
- **pokkum config** — validates `.pokkum.yaml` schema and applies named build profiles
- **pokkum dev (container-parity mode)** — builds image, loads into Docker/Podman, rebuilds on source changes
- **pokkum dev --no-container** — runs project dev server directly on host for fastest iteration
- **pokkum doctor** — audits local Bun runtime, SvelteKit compatibility, `.pokkumignore`, registry credentials
- **Standard machine-readable output** (`--output=json`) — versioned JSON envelope across all commands
- **pokkum explain** — reads OCI image and reports per-layer digests, sizes, file origins; supports layer diffing
- **pokkum upgrade** — checks for new releases and verifies binary signature via Cosign
- **pokkum init** — guided interactive setup for `.pokkum.yaml` and `.pokkumignore`
- **Kubernetes cluster hardening** — injects secure `securityContext`, resource limits, `NetworkPolicy`, `PodDisruptionBudget`
- **pokkum resolve** — resolves `pokkum://` image URIs in Kubernetes YAML to immutable digest references
- **pokkum apply** — resolves manifests and applies directly to cluster via `kubectl apply -f -`
- **pokkum rollback** — rolls back image references using `pokkum.dev/image-history` annotations
- **Multi-registry authentication** (`--registry-config`) — shells out to `docker-credential-*` binaries with in-memory caching
- **Monorepo affected-detection** (`--since`) — diffs project tree against git ref, skips builds if unchanged with known prior digest
- **Kubernetes OTel Collector sidecar injection** (`--with-otel-sidecar`) — injects OTel Collector sidecar spec into workloads
- **OpenTelemetry SDK bootstrap** (`--telemetry`) — starts OTel NodeSDK + OTLP trace exporter before app runs
- **Base image CVE build gate** (`--fail-on-cve`) — queries OSV.dev against locked base digest, can break build on CVEs
- **Base image escrow/mirroring** (`--mirror-registry`) — mirrors base images and signatures to project-controlled registries
- **Base image lockfile** (`pokkum.lock`) — pins base image digests across multi-platform indexes, tracks scan metadata
- **Base image signature verification** — keyless Sigstore for stock bases (distroless, chainguard), static-key Cosign for custom
- **Composition-root verifier injection** — verifiers injected at construction sites, closing adapter-to-adapter import allowlist
- **Image signing with Cosign/DSSE** — builds signed via Cosign with fetch-back-and-reverify before reporting success
- **Multi-arch signature/attestation (dual-publish)** — signatures attach to both index and per-platform manifests
- **OpenVEX exemptions** — `.pokkum.yaml` vex_exemptions bypass `--fail-on-cve` threshold with real OpenVEX justification
- **Secret-inlining guard** — regex-based scan over pre-build source and packaged output for credential patterns
- **Sigstore TUF trust-root refresh** — embedded trust root regenerated from TUF-verified fetch, live refresh support
- **Toolchain (Bun) CVE awareness** — queries OSV.dev for advisories against exact embedded Bun version in SLSA provenance

### Fixed

- **--base accepts custom image reference** — `--base` now accepts free-form custom image references
- **cosign verify-attestation interop** — fixed missing annotation for compatibility with real cosign v3
- **pokkum-static If-None-Match handling** — now answers 304 Not Modified instead of resending full body on repeated requests

### Changed

- **Removed pokkum metrics** — `pokkum metrics` command removed; it never listened on advertised port

## [v1.0.1] — 2026-08-12

### Fixed

- **GoReleaser v2 compatibility** — updated `.goreleaser.yaml` to use `folder` → `directory` for GoReleaser v2

## [v1.0.0] — 2026-08-12

Pokkum v1.0 is the first public release.

### Core Features

- Zero-dependency OCI container image compiler for SvelteKit applications
- Multi-architecture (linux/amd64, linux/arm64) builds without Docker daemon
- Bit-for-bit reproducible builds via SOURCE_DATE_EPOCH
- SLSA 3 provenance generation and verification
- Cosign image signing and DSSE attestations
- SvelteKit build sandbox (zero mutation of user source files)
- Base image signature verification (Sigstore keyless + Cosign static-key)
- Hardened Kubernetes manifest generation
- Supply-chain CVE scanning and remediation
- Build-time secret leak detection
- OpenTelemetry instrumentation bootstrap

---

## Notes

- Pokkum is pre-2.0. See `docs/Roadmap.md` for planned features and `docs/Features.md` for known limitations.
- Features marked "shipped" in `docs/Shipped.md` are included in the current development version ([Unreleased]).
- See `paranoid-testing-guide.md` for step-by-step verification of core supply-chain claims.
