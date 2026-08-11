# Technology Stack & Dependencies

- **Language**: Go 1.22+
- **Application Framework**: SvelteKit 2.31+ (`@sveltejs/kit`)
- **Runtime Compiler & Resolver**: Bun ≥ 1.2.0 (`internal/adapters/bunruntime` resolver, `--bun-binary`, `--bun-variant=standard|baseline`)
- **Packaging Strategy & Adapter**: `--strategy=layered` (default, arch-independent layout via `@sveltejs/adapter-node`) / `--strategy=exe` (deprecated 2-layer single executable)
- **Layer Compression**: `--compression=gzip|zstd` (default: `gzip`, `application/vnd.oci.image.layer.v1.tar+zstd` when `zstd`)
- **Native Addon Closure & Splitting**: `ClosuredNativeAdapter` (`internal/adapters/nativeinspect/closured.go`), ELF `.node` addon inspection, `/app/native` layer, and vendor chunking (`/app/vendor`).
- **Security & Hardening**: Container `securityContext` defaults inject `runAsNonRoot: true`, `allowPrivilegeEscalation: false`, `readOnlyRootFilesystem: true`, `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault`.
- **OCI Container Engine**: `github.com/google/go-containerregistry` (no Docker daemon required for builds)
- **Local Dev Engine**: `pokkum dev` (`cmd/pokkum/dev.go`) for local Docker daemon load, `--debug` interactive shell, and file watching.
- **Base Images**: Distroless (`gcr.io/distroless/cc-debian12:nonroot`) or Chainguard (`ghcr.io/chainguard-images/glibc-dynamic:latest`)
- **Observability**: Native OpenTelemetry Node SDK (`@opentelemetry/sdk-node`, `@opentelemetry/api`)
- **Supply Chain Security**: Syft (SBOM attached via OCI 1.1 referrers by default `--sbom-attach=referrer` or tag `--sbom-attach=tag`), Cosign (ECDSA/Ed25519 image signing), SLSA v1.0 (in-toto provenance), DSSE envelopes.