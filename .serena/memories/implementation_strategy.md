# Pokkum Implementation Strategy & Feature Bundles

## 📦 Bundle 1: Supply Chain & Reproducibility Lock (v0.2 & v0.5 M0) - COMPLETED
- Base Image Lockfile (pokkum.lock) & Provenance Completeness M0
- Focus: Lock base image digests and capture build parameters in signed SLSA attestations.

## 📦 Bundle 2: Layer Caching & Architecture Shift (v0.3 M1-M4) - COMPLETED
- Bun Runtime Plumbing, Hand-rolled SvelteKit Adapter, Vendor Splitting, Hardening & Cutover, Image Optimization.
- Focus: Shift from single-binary builds to 5 architecture-independent JS/asset layers.

## 📦 Bundle 3: Telemetry & Machine-Readable DX (v0.4) - COMPLETED
- --output=json Schema Standard, Unified Metrics & Telemetry, Developer Experience Wizards (pokkum init, doctor, explain).
- Focus: Standardize structured JSON schemas and zero-config OpenTelemetry observability.

## 📦 Bundle 4: Verification & Non-Determinism Diagnosis (v0.5 M1-M4) - COMPLETED
- Shared layerdiff Engine, pokkum repro doctor, pokkum verify --rebuild.
- Focus: Independent rebuild verification and stage-level non-determinism bisection.

## 📦 Bundle 5: Cluster Hardening & Annotations (v1.0 MVP) - COMPLETED
- Custom & Standard OCI Image Annotations (`--image-label`), NetworkPolicy Generation (`--network-policy`/`--no-network-policy`), Resource Defaults & PDB Injection (`--resource-defaults`/`--no-resource-defaults`), readOnlyRootFilesystem & SIGTERM /readyz 503 drain verification.
- Focus: Production-grade Kubernetes deployment manifest hardening and standard OCI image labeling.

## 📦 Bundle 6: Security Scanning & Guardrails (v1.0 MVP) - PLANNED
- CVE Scanning (`pokkum scan`), Toolchain CVE Awareness (`scan --toolchain`), Base Image Signature Verification (`--no-verify-base`), Secret-Inlining Guard (`--allow-secret-pattern`).
- Focus: Active vulnerability scanning and leak prevention in build pipeline.

## 📦 Bundle 7: Enterprise Build Integrity (v1.0 MVP) - PLANNED
- Hermetic builds (`--hermetic`), Multi-registry auth chains (`--registry-config`), Ephemeral test registry (`pkg/registry.New()`).
- Focus: Supporting strict enterprise build environments and multi-registry authentication.

## 📦 Bundle 8: Day-2 Operations & CI/CD Ecosystem (v1.0 MVP) - PLANNED
- Trusted-Builder Mode (GitHub Actions), Rollback support (`pokkum rollback`), Signed Self-Distribution (`pokkum upgrade`), GitHub Action CLI wrapper, automated update PRs.
- Focus: CI/CD automation and full lifecycle management.