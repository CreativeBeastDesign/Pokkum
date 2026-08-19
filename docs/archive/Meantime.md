# What changed

## `internal/adapters/nativeinspect` & `internal/ports/native.go`

Added a dedicated native module inspection port and dual adapter strategy to detect and handle native C++ Node addons and computed dynamic `import()` calls:

- **`internal/ports/native.go`**: Declares `ports.NativeInspector` interface and `NativeInspectionResult` domain struct.
- **`internal/adapters/nativeinspect/strict.go` (`StrictNativeAdapter`)**: Performs fast pre-flight inspection. If native C++ addons (`.node` binaries, `better-sqlite3`, `sharp`, `canvas`, etc.) or untraceable dynamic imports (`import(variable)`) are found, it halts the build with a hard error (`core.ErrNativeModulesUnsupported`).
- **`internal/adapters/nativeinspect/closured.go` (`ClosuredNativeAdapter`)**: Uses pure Go `debug/elf` to parse target Linux ELF `.node` binaries, discovers dynamic library dependencies (`DT_NEEDED`), filters out base image libraries (`distroless/cc-debian12`), checks `GLIBC` symbol versions (`<= 2.36`), and prepares metadata for `.so` closure layer injection (`LD_LIBRARY_PATH=/app/lib`).

```go
// internal/core/errors.go
// ErrNativeModulesUnsupported reports that native Node C++ addons or untraceable
// dynamic import expressions were detected in a project configured for strict preflight.
ErrNativeModulesUnsupported = errors.New("unsupported native Node modules or untraceable dynamic imports detected")
```

## `internal/adapters/sveltekit` & `pokkum-injection-concept.md`

Provides zero-config project inspection, native dependency scanning, dynamic import auditing, and virtual build config auto-injection:

- **Project Inspection & Native Scanners**:
  - `project.go`: Evaluates `package.json` dependencies and `svelte.config.js` target options.
  - `native.go`: Scans `package.json` and `node_modules/` for native C++ addons (`.node` files).
  - `dynamic_import.go`: Statically parses JS/TS files for untraceable dynamic `import(...)` expressions.
- **Auto-Injection Engine (`injector.go` & `pokkum-injection-concept.md`)**:
  - Implements zero-config virtual config transformation (`PrepareVirtualConfig`), writing temporary build configs to `.pokkum/svelte.config.js`.
  - Automatically injects `@jesterkit/exe-sveltekit` adapter if missing and pins `kit.version.name` to `process.env.SOURCE_DATE_EPOCH` for reproducible builds—without mutating repository source files on disk.

## SLSA provenance attestation, Cosign & DSSE signing

Implemented standalone SLSA v1.0 Provenance Attestation (`internal/adapters/slsa`), Cosign container image signing (`internal/adapters/cosign`), and DSSE envelope signing (`internal/adapters/dsse`) adapters alongside supply chain ports (`internal/ports/supplychain.go`):

- **`internal/ports/supplychain.go`**: Declares `SLSAStatement`, `SLSAPredicate`, `SLSABuildDefinition`, `SLSARunDetails`, `ResourceDescriptor`, `CosignSimpleSigningPayload`, `CosignSignRequest`, `CosignSignatureBundle`, `DSSEEnvelope`, `DSSESignature`, `DSSESignRequest`, `SigstoreBundle`, `ports.SLSAGenerator`, `ports.CosignSigner`, and `ports.DSSESigner` interfaces.
- **`internal/adapters/slsa/generator.go` (`Generator`)**: Constructs in-toto SLSA v1.0 statements capturing subject digests, external/internal parameters, resolved dependencies (base image pinned digest, git commit SHA, lockfiles, toolchain versions), and builder metadata.
- **`internal/adapters/slsa/lockfile.go`**: Scans project lockfiles (`bun.lock`, `bun.lockb`, `package-lock.json`, `pnpm-lock.yaml`, `yarn.lock`) and computes their cryptographic SHA256 checksums to verify pre-fetched dependency integrity.
- **`internal/adapters/cosign/payload.go` & `signer.go` (`Signer`)**: Builds canonical Red Hat Simple Signing JSON payloads (`atomic container signature`) and signs image manifest digests with ECDSA P-256 / Ed25519 keypairs.
- **`internal/adapters/dsse/pae.go` & `signer.go` (`Signer`)**: Implements standard Pre-Authentication Encoding ($\text{PAE}(\text{type}, \text{payload})$) and DSSE envelope signing (`application/vnd.dsse.envelope.v1+json`) for signing in-toto SLSA provenance statements and SBOM attestations (`application/vnd.in-toto+json`).

## Unified OpenTelemetry & Metrics Auto-Instrumentation

Unified metrics and auto-instrumentation into a zero-config OpenTelemetry pipeline leveraging SvelteKit 2.31+ native observability (`pokkum-metrics-otel-concept.md`):

- **SvelteKit Version Inspector**: Added `CheckTelemetrySupported` and `IsVersionAtLeast` in `internal/adapters/sveltekit/project.go` to parse `@sveltejs/kit` version from `package.json` and skip telemetry if < 2.31.0.
- **Single-Pass Config Transformer**: Extended `TransformConfig` in `internal/adapters/sveltekit/injector.go` to perform adapter swapping, `SOURCE_DATE_EPOCH` version pinning, and `kit.experimental.tracing/instrumentation` flag injection in one unified pass.
- **Strict Precedence & Virtual Instrumentation**: Implemented `InstrumentationExists`, `GenerateInstrumentationServer`, and `PrepareVirtualInstrumentation` in `internal/adapters/sveltekit/telemetry.go`. Preserves existing `src/instrumentation.server.ts|js|mjs` files if present; generates virtual instrumentation code with lazy SDK initialization, OTLP exporters, trace sampling rate control (`--trace-sample-rate`), and metrics-only mode (`--metrics-only`).
- **Kubernetes OTEL Sidecar Generator**: Updated `internal/adapters/k8s/resolver.go` with `injectOTELCollectorSidecar` and `WithOTELSidecar` support to automatically inject OpenTelemetry Collector sidecar specs into generated Pod manifests when `--with-otel-sidecar` is set.
- **CLI Options**: Added `--telemetry`, `--no-telemetry`, `--otel-export`, `--telemetry-env`, `--trace-sample-rate`, `--metrics-only`, and `--with-otel-sidecar` flags to `cmd/pokkum/build.go`.

## Documentation & concepts

- new `action.yml` (official Pokkum GitHub Composite Action)
- new `docs/GITHUB_ACTION.md` (comprehensive GitHub Action usage & options guide)
- new `ARCHITECTURE.md`
- modified `README.md`
- new `pokkum-lock-concept.md`
- new `Supply Chain Hardening v1.md`
- new `pokkum-injection-concept.md`
- new `pokkum-metrics-otel-concept.md`

