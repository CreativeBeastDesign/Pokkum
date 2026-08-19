<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Shipped

## backlog

### Build & Packaging

| Title | Summary | Kind | Status | Commits |
| --- | --- | --- | --- | --- |
| [Scoped secret-allow annotations](items/scoped-secret-allow-annotations.md) | --allow-secret-pattern is a global regex; an inline pokkum:allow-secret comment gives a known-safe line the scoped exemption it actually needs. | feature | shipped |  |

### Supply Chain & Attestation

| Title | Summary | Kind | Status | Commits |
| --- | --- | --- | --- | --- |
| [`--strategy=exe` secret-scanning gap](items/exe-secret-scan-gap.md) | The compiled exe strategy's single binary output has no post-build secret scan, unlike layered/static/asset-overlay. | hardening | shipped |  |
| [Remote-cache verify key should inherit the signing key](items/remote-cache-verify-key-inheritance.md) | A build signed via --signing-key alone doesn't automatically make its own remote-cache entries verifiable, since the cache-verify key chain never reads the signing public key. | hardening | shipped |  |

## v1.2

### Observability

| Title | Summary | Kind | Status | Commits |
| --- | --- | --- | --- | --- |
| [Reconsider pokkum metrics' shape](items/metrics-command-shape.md) | Decided by removal: pokkum metrics claimed to run a metrics server, never listened on anything, and is deleted, since it neither runs a server nor exposes anything the way its own text claims. | dx | shipped |  |

## v1.1

### Build & Packaging

| Title | Summary | Kind | Status | Commits |
| --- | --- | --- | --- | --- |
| [pokkum verify doesn't reproduce the asset-overlay layer](items/asset-overlay-verify-gap.md) | Verifying an image built with --asset-overlay reports a false-positive digest mismatch, because verify's rebuild path has no way to re-resolve the same predecessor chain. | fix | shipped |  |

### Developer Experience

| Title | Summary | Kind | Status | Commits |
| --- | --- | --- | --- | --- |
| [Human-readable console output for build logs](items/console-log-rendering.md) | Build progress rendered with level glyphs and aligned attribute blocks on a terminal, while piped and CI output stays byte-identical logfmt. | dx | shipped |  |
| [pokkum init wrote a config pokkum build refused](items/init-generates-invalid-config.md) | Every generated .pokkum.yaml carried an invalid sbom.attach value, so the first two commands a new user runs did not work together. | fix | shipped |  |
| [pokkum init recommended a command it had guaranteed could not work](items/init-recommends-a-failing-command.md) | init always ended with "you can now run pokkum build", which fails immediately for the local-only setup its own first prompt invites. | fix | shipped |  |
| [Zero-config adapter injection declined silently, with undocumented preconditions](items/injection-preconditions-undocumented.md) | Injection is advertised as automatic but engages only under two conditions, and when it declined it said nothing, so the failure read as the feature being broken. | fix | shipped |  |

### Supply Chain & Attestation

| Title | Summary | Kind | Status | Commits |
| --- | --- | --- | --- | --- |
| [Per-ref pokkum.lock slot for custom --base images](items/custom-base-lock-slot.md) | Give every custom --base reference its own pokkum.lock slot instead of sharing one, since two custom bases in a project still evict each other today. | hardening | shipped | `69914ac` |
| [POKKUM_*_PUBKEY meant two different things](items/pubkey-env-var-divergence.md) | The same public-key environment variable was resolved as a file path in one place and as literal PEM in another, so its meaning depended on which code path read it. | hardening | shipped |  |
| [Secret findings gave no usable location in minified output, and build artifacts were scanned as source](items/secret-findings-navigable-in-minified-output.md) | A finding in a 44 KB single-line chunk reported only line 3, and that chunk was a generated build artifact being scanned during the pre-build source stage. | fix | shipped |  |
| [Secret-guard findings reported a count with no locations](items/secretguard-reports-locations.md) | A failing build said how many secrets it found and nothing about where, so the finding could not be acted on. | fix | shipped |  |
| [TrustedRootPath should take bytes, not a file path](items/trusted-root-bytes.md) | Change the base-image trusted-root field from a file path to bytes so all three Sigstore trust-root consumers take the same shape. | hardening | shipped |  |

### Testing & Infrastructure

| Title | Summary | Kind | Status | Commits |
| --- | --- | --- | --- | --- |
| [Multi-command CLI workflow tests](items/cli-workflow-tests.md) | Run the real binary through command sequences in one project directory, covering whether commands work together rather than only in isolation. | infra | shipped |  |

## Unscheduled

### Build & Packaging

| Title | Summary | Kind | Status | Commits |
| --- | --- | --- | --- | --- |
| [Bun/supervisor layer diffID stability, pinned twice](items/bun-layer-diffid-stability.md) | Immutable-binary layers (Bun, pokkum-init, pokkum-static) now use a fixed epoch and disable Go's VCS stamping, so their ~90MB digest stops churning on every commit for content that never actually changed. | fix | shipped | `1675d4c`, `81a6fb6` |
| [Bun release checksum verification](items/bun-release-integrity.md) | Every downloaded Bun release archive is checksum-verified before extraction — pinned digests for common versions, Bun's own GPG-signed SHASUMS256.txt.asc for anything else — failing closed rather than silently installing an unverifiable download. | hardening | shipped | `0e8ae5e`, `4d8ba1b` |
| [Hermetic build mode (--hermetic)](items/hermetic-build-mode.md) | Enforces real Linux network-namespace isolation for the build subprocess (no IP egress regardless of what a compromised dependency's build-time code tries), falling back to advisory-only isolation elsewhere. | hardening | shipped | `28582b3`, `00f791d` |
| [Layered-strategy runtime hardening (stub launcher + startup attestation)](items/layered-runtime-hardening.md) | Two composable mitigations for stock Bun's full CLI attack surface in a layered image: a non-foldable compiled entrypoint launcher, and a supervisor-verified startup digest over the /app tree. | hardening | shipped | `fb23335` |
| [Zero-dependency multi-arch OCI compilation](items/multi-arch-oci-compilation.md) | Compiles a SvelteKit project straight into a multi-arch (linux/amd64, linux/arm64) OCI image with no Docker daemon or buildkit, using the project's configured adapter — or injecting one virtually into .pokkum/vite.config.ts when its two preconditions hold. | feature | shipped | `5693980` |
| [Registry push throughput, tagging, and composite remote-cache](items/registry-push-and-cache.md) | Parallel HTTP/2 layer uploads, cross-repo blob mounting, idempotent pushes, repeatable --tag support, and a composite input hash that skips a full rebuild in sub-100ms on a verified registry cache hit. | feature | shipped | `b350ecb` |
| [Rolling-deploy asset overlay (--asset-overlay)](items/rolling-deploy-asset-overlay.md) | Merges the last N generations' immutable /_app/immutable client assets into a separate overlay layer, registry-side, so a browser holding a prior generation's HTML never hits a 404 mid-rollout. | feature | shipped | `f9c2f1d` |
| [--runtime=node, the second runtime dimension](items/runtime-node.md) | Targets a distroless-node base and execs adapter-node output directly under /nodejs/bin/node with no Bun layer at all, proven by a real Docker boot and, since e918c52, an automated smoke test. | feature | shipped | `f5229c3`, `e918c52` |
| [--strategy=static](items/strategy-static.md) | Compiles a pure static SvelteKit site onto chainguard/static with an embedded pokkum-static Go file server as PID 1 — genuinely functional only since 2026-08-19, after six independent bugs were found by its first real boot test. | feature | shipped | `8306d37`, `1c33509`, `5693980`, `61fd873` |

### Developer Experience

| Title | Summary | Kind | Status | Commits |
| --- | --- | --- | --- | --- |
| [pokkum adopt](items/adopt-codemod.md) | Migrates SvelteKit projects off `adapter-node`, `adapter-vercel`, `adapter-auto`, or a legacy Dockerfile onto Pokkum compilation defaults. | feature | shipped |  |
| [--base accepts a custom image reference](items/base-flag-custom-reference.md) | `--base` now accepts a free-form custom image reference, closing a docs/CLI mismatch where the help text promised it and no code path accepted one. | fix | shipped | `69914ac` |
| [pokkum config view / validate, build profiles](items/config-management.md) | Inspects resolved build configuration, strictly validates `.pokkum.yaml` schema and profile consistency, and applies named profile overrides at build time. | feature | shipped |  |
| [pokkum dev (container-parity hot reload)](items/dev-hot-reload.md) | Builds the image, loads it into the local Docker/Podman daemon, and rebuilds on source changes so local iteration exercises the same runtime the production image ships. | feature | shipped | `1f8e5bf` |
| [pokkum doctor](items/doctor-preflight.md) | Audits local Bun runtime, SvelteKit version compatibility, `.pokkumignore`, and registry credentials, with `--fix` for mechanical repairs. | feature | shipped |  |
| [Standardized machine-readable output (--output=json)](items/json-output-envelope.md) | A global `--output=json` flag emits a versioned JSON envelope across every command, instead of callers parsing human-readable stdout. | feature | shipped |  |
| [pokkum explain / explain why / explain diff](items/layer-origin-tracing.md) | Reads a real OCI image and reports its actual per-layer digests, sizes, and file origins, and diffs two images layer-by-layer. | feature | shipped |  |
| [pokkum dev --no-container](items/no-container-dev-mode.md) | Runs the project's own dev server directly on the host, skipping image construction entirely, for the fastest possible local iteration loop. | feature | shipped | `18f056c` |
| [pokkum upgrade](items/signed-self-update.md) | Checks for new releases and verifies the release binary's checksum signature via Cosign before self-replacing. | feature | shipped |  |
| [pokkum init](items/workspace-init-wizard.md) | Guided interactive setup for `.pokkum.yaml` and `.pokkumignore`, with a non-interactive `--defaults` mode. | feature | shipped |  |

### Kubernetes & Operations

| Title | Summary | Kind | Status | Commits |
| --- | --- | --- | --- | --- |
| [Cluster hardening defaults](items/cluster-hardening-defaults.md) | Injects secure `securityContext`, resource requests/limits, `NetworkPolicy`/`PodDisruptionBudget` manifests, and probe defaults into resolved Kubernetes workloads. | feature | shipped |  |
| [pokkum apply](items/k8s-apply.md) | Resolves manifests and applies them directly to a Kubernetes cluster via `kubectl apply -f -`, seeding rollback history from live cluster state first. | feature | shipped |  |
| [pokkum resolve](items/k8s-uri-resolution.md) | Resolves `pokkum://` image URIs embedded in Kubernetes YAML manifests to immutable `repo@sha256:...` digest references. | feature | shipped |  |
| [Monorepo affected-detection (--since)](items/monorepo-affected-detection.md) | Diffs each project's tree against a git ref and skips builds entirely for projects with no changes and a known prior digest. | feature | shipped |  |
| [pokkum rollback](items/multi-generation-rollback.md) | Rolls back image references in Kubernetes manifests using `pokkum.dev/image-history` annotations, with generation depth selection. | feature | shipped |  |
| [Multi-registry authentication (--registry-config)](items/multi-registry-auth.md) | Shells out to `docker-credential-*` binaries (ECR, GCR, OSXKeychain) with in-memory caching, falling back to static `auths` blocks. | feature | shipped |  |

### Observability

| Title | Summary | Kind | Status | Commits |
| --- | --- | --- | --- | --- |
| [Kubernetes OTel Collector sidecar injection (--with-otel-sidecar)](items/otel-collector-sidecar.md) | Injects an OpenTelemetry Collector sidecar spec (4317 gRPC, 4318 HTTP, 8889 metrics) directly into generated Kubernetes workload manifests. | feature | shipped |  |
| [OpenTelemetry SDK bootstrap (--telemetry)](items/otel-sdk-bootstrap.md) | Starts a real OTel NodeSDK + OTLP trace exporter before the app runs, via a compile-entrypoint wrapper for `--strategy=exe` and a packaged `bun --preload` file for `--strategy=layered`. | feature | shipped |  |

### Supply Chain & Attestation

| Title | Summary | Kind | Status | Commits |
| --- | --- | --- | --- | --- |
| [Base image CVE build gate](items/base-image-cve-gate.md) | `pokkum build` actively queries OSV.dev against the locked base digest and can break the build on discovered CVEs by severity threshold. | feature | shipped |  |
| [Base image escrow / mirroring](items/base-image-escrow-mirroring.md) | `--mirror-registry` mirrors upstream base images and signatures to a project-controlled registry, with pulled bytes verified against pokkum.lock's pinned digest. | feature | shipped | `a149b28` |
| [Base image lockfile (pokkum.lock) and audit (pokkum base check)](items/base-image-lockfile.md) | pokkum.lock pins base image digests across multi-platform indexes and tracks scan metadata; pokkum base check audits that state without touching the network. | feature | shipped |  |
| [Base image signature verification](items/base-image-signature-verification.md) | Stock base presets are verified via keyless Sigstore by default; custom bases via static-key Cosign, completing the chain of custody Pokkum already applies to its own outputs. | feature | shipped | `efc1743` |
| [Composition-root refactor for verifier injection](items/composition-root-verifier-injection.md) | cmd/pokkum now injects verifiers at every construction site instead of adapters building their own defaults, closing an empty-by-construction adapter-to-adapter import allowlist. | hardening | shipped | `e5dd73c` |
| [cosign verify-attestation interop fix](items/cosign-attestation-interop.md) | Fixed a missing annotation key that made real cosign v3 reject Pokkum's own valid DSSE attestations in tag-fallback mode. | fix | shipped | `e918c52` |
| [Embedded PID-1 binaries brought under CI attestation](items/embedded-pid1-attestation-coverage.md) | pokkum-init and pokkum-static are now built by CI/releases and freshness-checked, closing the gap where every image's PID 1 was a developer-laptop binary outside the attested pipeline. | hardening | shipped | `5693980`, `a86baa3`, `81a6fb6` |
| [`--expect-source` requires verified provenance](items/expect-source-verified.md) | `--expect-source` now refuses to compare against unsigned source annotations unless the caller opts into the explicitly-marked-unverified escape hatch. | hardening | shipped | `91dc3cd` |
| [Image signing with Cosign/DSSE](items/image-signing.md) | Builds are signed via Cosign static-key or DSSE, with a fetch-back-and-reverify step before the build is allowed to report `Signed: true`. | feature | shipped | `2f03609` |
| [Multi-arch signature/attestation subject (dual-publish)](items/multi-arch-attestation-subject.md) | Signatures and attestations attach to both the image index and every per-platform manifest digest, so any verifier agrees regardless of which digest it targets. | feature | shipped | `2f03609` |
| [OpenVEX exemptions for the CVE gate](items/openvex-exemptions.md) | `.pokkum.yaml`'s vex_exemptions lets a specific CVE bypass the --fail-on-cve threshold, but only with a real OpenVEX justification code, a mandatory expiry, and a mandatory owner. | feature | shipped |  |
| [Remove shared placeholder trust-anchor fallback](items/placeholder-pubkey-fallback-removed.md) | Deleted the single hardcoded placeholder public key that silently backstopped signing, base-image, and remote-cache verification when no key was configured. | hardening | shipped | `a149b28` |
| [Secret-inlining guard (secretguard)](items/secret-inlining-guard.md) | Regex-based build-time scan over both pre-build source and packaged build output, catching secrets baked in by build-time dependencies as well as the project's own source. | feature | shipped |  |
| [Sigstore TUF trust-root refresh](items/sigstore-tuf-refresh.md) | The embedded Sigstore trust root is regenerated from a TUF-verified fetch and can refresh live; a nightly CI job now catches it silently rotting again. | hardening | shipped | `9188d56`, `eeaa83a`, `a86baa3` |
| [Toolchain (Bun) CVE awareness](items/toolchain-cve-awareness.md) | Queries OSV.dev for advisories against the exact embedded Bun version recorded in SLSA provenance, without pulling or scanning any image. | feature | shipped |  |

### Testing & Infrastructure

| Title | Summary | Kind | Status | Commits |
| --- | --- | --- | --- | --- |
| [CLI/docs drift as a mechanical test failure](items/cli-docs-invariant-tests.md) | Five new tests check Vocabulary.md and action.yml against the CLI's real flag/env-var surface in both directions, in the spirit of internal/architecture_test.go, and found three genuine drifts on their first run. | infra | shipped | `548c0e1` |
| [Embedded PID-1 binary freshness guard](items/embedded-blob-freshness-guard.md) | make check-embedded-blobs rebuilds pokkum-init and pokkum-static fresh from source with the exact Makefile flags and byte-compares them against what is actually embedded, catching local staleness a from-scratch CI build structurally cannot hit. | infra | shipped | `a86baa3` |
| [Real-build tests copy their fixture into t.TempDir() first](items/fixture-isolation.md) | Every mutating real-build test now copies its checked-in fixture into a fresh t.TempDir() before building, closing an order-dependence that had already caused three separate incidents, proven at -count=3 -shuffle=on. | infra | shipped | `ac5dc89`, `20ba1ec` |
| [Fuzz targets across parsers and codecs](items/fuzz-targets.md) | Fuzz targets now exercise DSSE PAE encoding, ignore-pattern matching, Cosign payloads, asset-overlay merge logic, Bun SHASUMS parsing, scanner version comparison, and the static file server — none of these existed before. | infra | shipped | `e6e4746` |
| [Race detector + enforced coverage floor](items/race-detector-and-coverage-floor.md) | go test -race now runs in CI over the packages where concurrency actually lives, and coverage is measured and enforced at a 73% floor as a ratchet off the real baseline, not an aspirational number. | infra | shipped | `e6e4746` |
| [Real Docker boot smoke tests](items/runtime-boot-smoke-tests.md) | The first tests in this repo to actually boot a produced image and poll it, for both the default layered/Bun strategy and --runtime=node, instead of only proving the packaged bytes are right and stable. | infra | shipped | `fc53968`, `e918c52` |
| [Real @sveltejs/adapter-static test fixture replaces a fictional mock](items/static-strategy-real-fixture.md) | A genuine, scaffolded-and-built @sveltejs/adapter-static project replaced a synthetic mock that had been fabricating a flat prerendered/index.html, immediately exposing four bugs the mock's own wrong assumption had been hiding. | infra | shipped | `0ef9dd0`, `1c33509` |

