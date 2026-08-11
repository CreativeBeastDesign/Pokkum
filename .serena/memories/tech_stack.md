# Technology Stack & Dependencies

- **Language**: Go 1.22+
- **Application Framework**: SvelteKit 2.31+ (`@sveltejs/kit`)
- **Runtime Compiler & Resolver**: Bun ≥ 1.2.0 (`internal/adapters/bunruntime` resolver, `--bun-binary`, `--bun-variant=standard|baseline`)
- **Packaging Strategy & Adapter**: `--strategy=layered` (default, 5-layer arch-independent layout via `@sveltejs/adapter-node`) / `--strategy=exe` (legacy 2-layer single executable)
- **OCI Container Engine**: `github.com/google/go-containerregistry` (no Docker daemon required)
- **Base Images**: Distroless (`gcr.io/distroless/cc-debian12:nonroot`) or Chainguard (`ghcr.io/chainguard-images/glibc-dynamic:latest`)
- **Observability**: Native OpenTelemetry Node SDK (`@opentelemetry/sdk-node`, `@opentelemetry/api`)
- **Supply Chain Security**: Syft (SBOM), Cosign (ECDSA/Ed25519 image signing), SLSA v1.0 (in-toto provenance), DSSE envelopes.