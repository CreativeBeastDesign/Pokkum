# Pokkum Implementation Strategy & Feature Bundles

## 📦 Bundle 1: Supply Chain & Reproducibility Lock (v0.2 & v0.5 M0)
- Base Image Lockfile (pokkum.lock) & Provenance Completeness M0
- Focus: Lock base image digests and capture build parameters in signed SLSA attestations.

## 📦 Bundle 2: Layer Caching & Architecture Shift (v0.3 M1-M4)
- Bun Runtime Plumbing, Hand-rolled SvelteKit Adapter, Vendor Splitting, Hardening & Cutover, Image Optimization.
- Focus: Shift from single-binary builds to 5 architecture-independent JS/asset layers.

## 📦 Bundle 3: Telemetry & Machine-Readable DX (v0.4)
- --output=json Schema Standard, Unified Metrics & Telemetry, Developer Experience Wizards (pokkum init, doctor, explain).
- Focus: Standardize structured JSON schemas and zero-config OpenTelemetry observability.

## 📦 Bundle 4: Verification & Non-Determinism Diagnosis (v0.5 M1-M4)
- Shared layerdiff Engine, pokkum repro doctor, pokkum verify --rebuild.
- Focus: Independent rebuild verification and stage-level non-determinism bisection.