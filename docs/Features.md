<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Features

## Build & Packaging

### [Bun release checksum verification](items/bun-release-integrity.md)

Every downloaded Bun release archive is checksum-verified before extraction — pinned digests for common versions, Bun's own GPG-signed SHASUMS256.txt.asc for anything else — failing closed rather than silently installing an unverifiable download.

- Implementation:
  - [internal/adapters/bunruntime/resolver.go](../internal/adapters/bunruntime/resolver.go)

### [Hermetic build mode (--hermetic)](items/hermetic-build-mode.md)

Enforces real Linux network-namespace isolation for the build subprocess (no IP egress regardless of what a compromised dependency's build-time code tries), falling back to advisory-only isolation elsewhere.

- Flags: `--hermetic`, `--hermetic-mount-isolation`
- Implementation:
  - [internal/core/pipeline.go](../internal/core/pipeline.go)

### [Layered-strategy runtime hardening (stub launcher + startup attestation)](items/layered-runtime-hardening.md)

Two composable mitigations for stock Bun's full CLI attack surface in a layered image: a non-foldable compiled entrypoint launcher, and a supervisor-verified startup digest over the /app tree.

- Flags: `--stub-launcher`, `POKKUM_STUB_LAUNCHER`, `POKKUM_ATTESTATION_DIGEST`
- Implementation:
  - [internal/adapters/bunexec/compiler.go](../internal/adapters/bunexec/compiler.go)
  - [internal/adapters/packager/packager.go](../internal/adapters/packager/packager.go)

### [Zero-dependency multi-arch OCI compilation](items/multi-arch-oci-compilation.md)

Compiles a SvelteKit project straight into a multi-arch (linux/amd64, linux/arm64) OCI image with no Docker daemon or buildkit, using the project's configured adapter — or injecting one virtually into .pokkum/vite.config.ts when its two preconditions hold.

- Flags: `--strategy`, `--platform`
- Implementation:
  - [internal/core/pipeline.go](../internal/core/pipeline.go)
  - [internal/adapters/bunexec/compiler.go](../internal/adapters/bunexec/compiler.go)
  - [internal/adapters/packager/packager.go](../internal/adapters/packager/packager.go)

### [Registry push throughput, tagging, and composite remote-cache](items/registry-push-and-cache.md)

Parallel HTTP/2 layer uploads, cross-repo blob mounting, idempotent pushes, repeatable --tag support, and a composite input hash that skips a full rebuild in sub-100ms on a verified registry cache hit.

- Flags: `--tag`, `--push-concurrency`, `--cache-verify`, `--compression`
- Implementation:
  - [internal/adapters/registry/push.go](../internal/adapters/registry/push.go)
  - [internal/adapters/registry/mount.go](../internal/adapters/registry/mount.go)
  - [internal/adapters/layercacheutils/layercacheutils.go](../internal/adapters/layercacheutils/layercacheutils.go)

### [Rolling-deploy asset overlay (--asset-overlay)](items/rolling-deploy-asset-overlay.md)

Merges the last N generations' immutable /_app/immutable client assets into a separate overlay layer, registry-side, so a browser holding a prior generation's HTML never hits a 404 mid-rollout.

- Flags: `--asset-overlay`, `--asset-overlay-from`
- Implementation:
  - [internal/ports/assetoverlay.go](../internal/ports/assetoverlay.go)
  - [internal/adapters/assetoverlay](../internal/adapters/assetoverlay)
  - [internal/core/pipeline.go](../internal/core/pipeline.go)
  - [internal/adapters/packager/packager.go](../internal/adapters/packager/packager.go)
  - [tests/integration/asset_overlay_e2e_test.go](../tests/integration/asset_overlay_e2e_test.go)

### [--runtime=node, the second runtime dimension](items/runtime-node.md)

Targets a distroless-node base and execs adapter-node output directly under /nodejs/bin/node with no Bun layer at all, proven by a real Docker boot and, since e918c52, an automated smoke test.

- Flags: `--runtime=bun`, `--runtime=node`
- Implementation:
  - [internal/core/model.go](../internal/core/model.go)
  - [internal/core/pipeline.go](../internal/core/pipeline.go)
  - [internal/core/runtime_node_test.go](../internal/core/runtime_node_test.go)
  - [tests/integration/runtime_smoke_node_test.go](../tests/integration/runtime_smoke_node_test.go)

### [Scoped secret-allow annotations](items/scoped-secret-allow-annotations.md)

--allow-secret-pattern is a global regex; an inline pokkum:allow-secret comment gives a known-safe line the scoped exemption it actually needs.

- Flags: `--allow-secret-pattern`

### [--strategy=static](items/strategy-static.md)

Compiles a pure static SvelteKit site onto chainguard/static with an embedded pokkum-static Go file server as PID 1 — genuinely functional only since 2026-08-19, after six independent bugs were found by its first real boot test.

- Flags: `--strategy=static`, `--static`
- Implementation:
  - [internal/adapters/staticserver](../internal/adapters/staticserver)
  - [supervisor/cmd/pokkum-static/main.go](../supervisor/cmd/pokkum-static/main.go)
  - [supervisor/cmd/pokkum-static/server.go](../supervisor/cmd/pokkum-static/server.go)
  - [internal/adapters/bunexec/compiler.go](../internal/adapters/bunexec/compiler.go)
  - [testdata/fixtures/sveltekit-static](../testdata/fixtures/sveltekit-static)

## Developer Experience

### [pokkum adopt](items/adopt-codemod.md)

Migrates SvelteKit projects off `adapter-node`, `adapter-vercel`, `adapter-auto`, or a legacy Dockerfile onto Pokkum compilation defaults.

- Implementation:
  - [cmd/pokkum/adopt.go](../cmd/pokkum/adopt.go)

### [pokkum config view / validate, build profiles](items/config-management.md)

Inspects resolved build configuration, strictly validates `.pokkum.yaml` schema and profile consistency, and applies named profile overrides at build time.

- Flags: `--profile`, `-P`
- Implementation:
  - [cmd/pokkum/config.go](../cmd/pokkum/config.go)

### [pokkum dev (container-parity hot reload)](items/dev-hot-reload.md)

Builds the image, loads it into the local Docker/Podman daemon, and rebuilds on source changes so local iteration exercises the same runtime the production image ships.

- Flags: `--debug`, `--port`, `--watch`, `--env-file`, `--platform`, `--bun-binary`, `--bun-variant`, `--bun-version`
- Implementation:
  - [cmd/pokkum/dev.go](../cmd/pokkum/dev.go)

### [pokkum doctor](items/doctor-preflight.md)

Audits local Bun runtime, SvelteKit version compatibility, `.pokkumignore`, and registry credentials, with `--fix` for mechanical repairs.

- Flags: `--fix`
- Implementation:
  - [cmd/pokkum/doctor.go](../cmd/pokkum/doctor.go)

### [Standardized machine-readable output (--output=json)](items/json-output-envelope.md)

A global `--output=json` flag emits a versioned JSON envelope across every command, instead of callers parsing human-readable stdout.

- Flags: `--output`
- Implementation:
  - [cmd/pokkum/build.go](../cmd/pokkum/build.go)

### [pokkum explain / explain why / explain diff](items/layer-origin-tracing.md)

Reads a real OCI image and reports its actual per-layer digests, sizes, and file origins, and diffs two images layer-by-layer.

- Implementation:
  - [cmd/pokkum/explain.go](../cmd/pokkum/explain.go)

### [pokkum dev --no-container](items/no-container-dev-mode.md)

Runs the project's own dev server directly on the host, skipping image construction entirely, for the fastest possible local iteration loop.

- Flags: `--no-container`
- Implementation:
  - [cmd/pokkum/dev.go](../cmd/pokkum/dev.go)

### [pokkum upgrade](items/signed-self-update.md)

Checks for new releases and verifies the release binary's checksum signature via Cosign before self-replacing.

- Implementation:
  - [cmd/pokkum/upgrade.go](../cmd/pokkum/upgrade.go)

### [pokkum init](items/workspace-init-wizard.md)

Guided interactive setup for `.pokkum.yaml` and `.pokkumignore`, with a non-interactive `--defaults` mode.

- Flags: `--defaults`
- Implementation:
  - [cmd/pokkum/init.go](../cmd/pokkum/init.go)

## Kubernetes & Operations

### [Cluster hardening defaults](items/cluster-hardening-defaults.md)

Injects secure `securityContext`, resource requests/limits, `NetworkPolicy`/`PodDisruptionBudget` manifests, and probe defaults into resolved Kubernetes workloads.

- Flags: `--security-context`, `--no-security-context`, `--network-policy`, `--with-otel-sidecar`
- Implementation:
  - [internal/adapters/k8s/resolver.go](../internal/adapters/k8s/resolver.go)

### [pokkum apply](items/k8s-apply.md)

Resolves manifests and applies them directly to a Kubernetes cluster via `kubectl apply -f -`, seeding rollback history from live cluster state first.

- Implementation:
  - [cmd/pokkum/apply.go](../cmd/pokkum/apply.go)
  - [internal/ports/k8s.go](../internal/ports/k8s.go)

### [pokkum resolve](items/k8s-uri-resolution.md)

Resolves `pokkum://` image URIs embedded in Kubernetes YAML manifests to immutable `repo@sha256:...` digest references.

- Implementation:
  - [cmd/pokkum/resolve.go](../cmd/pokkum/resolve.go)
  - [internal/adapters/k8s/resolver.go](../internal/adapters/k8s/resolver.go)

### [Monorepo affected-detection (--since)](items/monorepo-affected-detection.md)

Diffs each project's tree against a git ref and skips builds entirely for projects with no changes and a known prior digest.

- Flags: `--since`
- Implementation:
  - [internal/adapters/gitutils/affected.go](../internal/adapters/gitutils/affected.go)
  - [cmd/pokkum/k8s.go](../cmd/pokkum/k8s.go)

### [pokkum rollback](items/multi-generation-rollback.md)

Rolls back image references in Kubernetes manifests using `pokkum.dev/image-history` annotations, with generation depth selection.

- Flags: `-g`, `--generation`, `--list`, `--to`
- Implementation:
  - [cmd/pokkum/rollback.go](../cmd/pokkum/rollback.go)

### [Multi-registry authentication (--registry-config)](items/multi-registry-auth.md)

Shells out to `docker-credential-*` binaries (ECR, GCR, OSXKeychain) with in-memory caching, falling back to static `auths` blocks.

- Flags: `--registry-config`
- Implementation:
  - [internal/adapters/registryutils/keychain.go](../internal/adapters/registryutils/keychain.go)

## Observability

### [Kubernetes OTel Collector sidecar injection (--with-otel-sidecar)](items/otel-collector-sidecar.md)

Injects an OpenTelemetry Collector sidecar spec (4317 gRPC, 4318 HTTP, 8889 metrics) directly into generated Kubernetes workload manifests.

- Flags: `--with-otel-sidecar`
- Implementation:
  - [cmd/pokkum/build.go](../cmd/pokkum/build.go)
  - [internal/adapters/k8s/resolver.go](../internal/adapters/k8s/resolver.go)

### [OpenTelemetry SDK bootstrap (--telemetry)](items/otel-sdk-bootstrap.md)

Starts a real OTel NodeSDK + OTLP trace exporter before the app runs, via a compile-entrypoint wrapper for `--strategy=exe` and a packaged `bun --preload` file for `--strategy=layered`.

- Flags: `--telemetry`, `--no-telemetry`, `--otel-export`, `--telemetry-env`, `--trace-sample-rate`, `--metrics-only`
- Implementation:
  - [internal/adapters/sveltekitutils/telemetry.go](../internal/adapters/sveltekitutils/telemetry.go)
  - [internal/core/pipeline.go](../internal/core/pipeline.go)

## Supply Chain & Attestation

### [Base image CVE build gate](items/base-image-cve-gate.md)

`pokkum build` actively queries OSV.dev against the locked base digest and can break the build on discovered CVEs by severity threshold.

- Flags: `--fail-on-cve`, `POKKUM_FAIL_ON_CVE`, `--allow-incomplete`
- Implementation:
  - [internal/adapters/scanner/adapter.go](../internal/adapters/scanner/adapter.go)
  - [internal/adapters/baseimage/resolver.go](../internal/adapters/baseimage/resolver.go)

### [Base image escrow / mirroring](items/base-image-escrow-mirroring.md)

`--mirror-registry` mirrors upstream base images and signatures to a project-controlled registry, with pulled bytes verified against pokkum.lock's pinned digest.

- Flags: `--mirror-registry`
- Implementation:
  - [internal/adapters/baseimage/resolver.go](../internal/adapters/baseimage/resolver.go)

### [Base image lockfile (pokkum.lock) and audit (pokkum base check)](items/base-image-lockfile.md)

pokkum.lock pins base image digests across multi-platform indexes and tracks scan metadata; pokkum base check audits that state without touching the network.

- Flags: `pokkum base check`, `pokkum base update`
- Implementation:
  - [internal/adapters/lockfileutils/lockfile.go](../internal/adapters/lockfileutils/lockfile.go)
  - [cmd/pokkum/base.go](../cmd/pokkum/base.go)

### [Base image signature verification](items/base-image-signature-verification.md)

Stock base presets are verified via keyless Sigstore by default; custom bases via static-key Cosign, completing the chain of custody Pokkum already applies to its own outputs.

- Flags: `--base-verify-mode`, `--base-verify-key`
- Implementation:
  - [internal/adapters/baseimage/resolver.go](../internal/adapters/baseimage/resolver.go)
  - [internal/adapters/sigstore/verifier.go](../internal/adapters/sigstore/verifier.go)

### [Composition-root refactor for verifier injection](items/composition-root-verifier-injection.md)

cmd/pokkum now injects verifiers at every construction site instead of adapters building their own defaults, closing an empty-by-construction adapter-to-adapter import allowlist.

- Implementation:
  - [internal/architecture_test.go](../internal/architecture_test.go)
  - [internal/adapters/provenance/resolver.go](../internal/adapters/provenance/resolver.go)

### [Per-ref pokkum.lock slot for custom --base images](items/custom-base-lock-slot.md)

Give every custom --base reference its own pokkum.lock slot instead of sharing one, since two custom bases in a project still evict each other today.

- Flags: `--base`
- Implementation:
  - [internal/adapters/baseimage/resolver.go](../internal/adapters/baseimage/resolver.go)
  - [internal/adapters/lockfileutils/lockfile.go](../internal/adapters/lockfileutils/lockfile.go)
  - [cmd/pokkum/base.go](../cmd/pokkum/base.go)

### [Embedded PID-1 binaries brought under CI attestation](items/embedded-pid1-attestation-coverage.md)

pokkum-init and pokkum-static are now built by CI/releases and freshness-checked, closing the gap where every image's PID 1 was a developer-laptop binary outside the attested pipeline.

- Implementation:
  - [internal/adapters/staticserver/blob_freshness_test.go](../internal/adapters/staticserver/blob_freshness_test.go)
  - [Makefile](../Makefile)

### [`--strategy=exe` secret-scanning gap](items/exe-secret-scan-gap.md)

The compiled exe strategy's single binary output has no post-build secret scan, unlike layered/static/asset-overlay.

- Implementation:
  - [internal/adapters/secretguard/guard.go](../internal/adapters/secretguard/guard.go)
  - [internal/core/pipeline.go](../internal/core/pipeline.go)

### [`--expect-source` requires verified provenance](items/expect-source-verified.md)

`--expect-source` now refuses to compare against unsigned source annotations unless the caller opts into the explicitly-marked-unverified escape hatch.

- Flags: `--expect-source`, `--allow-unverified-source`
- Implementation:
  - [internal/adapters/provenance/resolver.go](../internal/adapters/provenance/resolver.go)
  - [internal/adapters/slsa/generator.go](../internal/adapters/slsa/generator.go)

### [Image signing with Cosign/DSSE](items/image-signing.md)

Builds are signed via Cosign static-key or DSSE, with a fetch-back-and-reverify step before the build is allowed to report `Signed: true`.

- Flags: `--sign`, `--signing-key`, `POKKUM_SIGNING_KEY`, `--require-signed`
- Implementation:
  - [internal/adapters/cosign/signer.go](../internal/adapters/cosign/signer.go)
  - [internal/core/pipeline.go](../internal/core/pipeline.go)

### [Multi-arch signature/attestation subject (dual-publish)](items/multi-arch-attestation-subject.md)

Signatures and attestations attach to both the image index and every per-platform manifest digest, so any verifier agrees regardless of which digest it targets.

- Implementation:
  - [internal/core/pipeline.go](../internal/core/pipeline.go)
  - [internal/adapters/cosign/signer.go](../internal/adapters/cosign/signer.go)

### [OpenVEX exemptions for the CVE gate](items/openvex-exemptions.md)

`.pokkum.yaml`'s vex_exemptions lets a specific CVE bypass the --fail-on-cve threshold, but only with a real OpenVEX justification code, a mandatory expiry, and a mandatory owner.

- Flags: `--vex-output`
- Implementation:
  - [internal/core/model.go](../internal/core/model.go)
  - [internal/adapters/vexutils/document.go](../internal/adapters/vexutils/document.go)

### [Remove shared placeholder trust-anchor fallback](items/placeholder-pubkey-fallback-removed.md)

Deleted the single hardcoded placeholder public key that silently backstopped signing, base-image, and remote-cache verification when no key was configured.

- Implementation:
  - [internal/adapters/cosign/signer.go](../internal/adapters/cosign/signer.go)
  - [internal/adapters/baseimage/resolver.go](../internal/adapters/baseimage/resolver.go)

### [POKKUM_*_PUBKEY meant two different things](items/pubkey-env-var-divergence.md)

The same public-key environment variable was resolved as a file path in one place and as literal PEM in another, so its meaning depended on which code path read it.

- Flags: `--cache-verify-key`, `POKKUM_CACHE_PUBKEY`, `POKKUM_SIGNING_PUBKEY`, `POKKUM_BASE_IMAGE_PUBKEY`
- Implementation:
  - [internal/adapters/keymaterialutils/keymaterialutils.go](../internal/adapters/keymaterialutils/keymaterialutils.go)
  - [cmd/pokkum/build.go](../cmd/pokkum/build.go)
  - [internal/adapters/remotecacheutils/remotecacheutils.go](../internal/adapters/remotecacheutils/remotecacheutils.go)
  - [internal/adapters/provenance/resolver.go](../internal/adapters/provenance/resolver.go)
  - [internal/adapters/baseimage/resolver.go](../internal/adapters/baseimage/resolver.go)

### [Remote-cache verify key should inherit the signing key](items/remote-cache-verify-key-inheritance.md)

A build signed via --signing-key alone doesn't automatically make its own remote-cache entries verifiable, since the cache-verify key chain never reads the signing public key.

- Implementation:
  - [internal/ports/cache.go](../internal/ports/cache.go)
  - [internal/core/pipeline.go](../internal/core/pipeline.go)
  - [internal/adapters/remotecacheutils/remotecacheutils.go](../internal/adapters/remotecacheutils/remotecacheutils.go)

### [Secret-inlining guard (secretguard)](items/secret-inlining-guard.md)

Regex-based build-time scan over both pre-build source and packaged build output, catching secrets baked in by build-time dependencies as well as the project's own source.

- Flags: `--allow-secret-pattern`
- Implementation:
  - [internal/adapters/secretguard/guard.go](../internal/adapters/secretguard/guard.go)
  - [internal/core/pipeline.go](../internal/core/pipeline.go)

### [Sigstore TUF trust-root refresh](items/sigstore-tuf-refresh.md)

The embedded Sigstore trust root is regenerated from a TUF-verified fetch and can refresh live; a nightly CI job now catches it silently rotting again.

- Flags: `--sigstore-tuf-refresh`, `--sigstore-trusted-root`, `--hermetic`
- Implementation:
  - [internal/adapters/sigstore/tufrefresh.go](../internal/adapters/sigstore/tufrefresh.go)
  - [internal/adapters/sigstore/trustedroot.go](../internal/adapters/sigstore/trustedroot.go)
  - [internal/adapters/sigstore/trustedroot_freshness.go](../internal/adapters/sigstore/trustedroot_freshness.go)

### [Toolchain (Bun) CVE awareness](items/toolchain-cve-awareness.md)

Queries OSV.dev for advisories against the exact embedded Bun version recorded in SLSA provenance, without pulling or scanning any image.

- Implementation:
  - [internal/adapters/scanner/adapter.go](../internal/adapters/scanner/adapter.go)

### [TrustedRootPath should take bytes, not a file path](items/trusted-root-bytes.md)

Change the base-image trusted-root field from a file path to bytes so all three Sigstore trust-root consumers take the same shape.

- Implementation:
  - [internal/ports/baseimage.go](../internal/ports/baseimage.go)
  - [internal/adapters/baseimage/resolver.go](../internal/adapters/baseimage/resolver.go)
  - [cmd/pokkum/build.go](../cmd/pokkum/build.go)

## Known Limitations

### Build & Packaging

- Images whose only output is a local tarball carry no annotations at all, so this path cannot help them — see [Tarball output silently drops every OCI annotation](items/tarball-output-drops-annotations.md). ([pokkum verify doesn't reproduce the asset-overlay layer](items/asset-overlay-verify-gap.md))
- The fix was itself silently undermined until 81a6fb6: Go's default -buildvcs stamping made the pokkum-init/pokkum-static binaries' own content change every commit regardless of the tar-timestamp pin, so the two layers containing them kept churning anyway — the same failure class (build metadata leaking into a content-addressed artifact) as the first bug, a second independent source of it in the identical layers. Closed with -buildvcs=false on both embedded-binary build targets; the main CLI build deliberately keeps VCS stamping since it wants version reporting. ([Bun/supervisor layer diffID stability, pinned twice](items/bun-layer-diffid-stability.md))
- This was a real bug, not a missing assertion: writing the stability test found the diffID derived its tar timestamp from SOURCE_DATE_EPOCH, which changes every commit, actively inverting what was supposed to be the single biggest fleet-wide size lever (fixed 1675d4c). ([Bun/supervisor layer diffID stability, pinned twice](items/bun-layer-diffid-stability.md))
- PB-2's first-contact gap is a stated, permanent limitation, not an open TODO: the very first resolve of a genuinely new, unlisted (version, target) on a fresh cache has no independent trust anchor beyond the GPG-signed manifest itself — GitHub's Releases API shares the same trust root as the download host and exposes no per-asset digests, so it adds no real signal. Re-running scripts/pin-bun-checksums periodically narrows this; nothing closes it fully. ([Bun release checksum verification](items/bun-release-integrity.md))
- --hermetic-mount-isolation's docker.sock mask has an honest residual gap: the sandboxed process retains CAP_SYS_ADMIN in its own namespace (the capability that created the mask), so a sufficiently sophisticated dependency aware of the mechanism could in principle umount() it. Closing this needs capset(2) to drop the capability before the final exec — not attempted. ([Hermetic build mode (--hermetic)](items/hermetic-build-mode.md))
- Requires cached base image resolution, pre-populated node_modules/, and a pre-cached Bun binary — fails closed rather than downloading, so a cold cache cannot hermetic-build. ([Hermetic build mode (--hermetic)](items/hermetic-build-mode.md))
- Startup attestation only exists for --strategy=layered; --strategy=exe and --strategy=static don't attest. ([Layered-strategy runtime hardening (stub launcher + startup attestation)](items/layered-runtime-hardening.md))
- Zero-config injection writes a transformed vite config one directory deeper (.pokkum/<viteConfigName>); __dirname/import.meta.url-derived paths and Vite's root/envDir/publicDir/build.outDir are corrected for this, but any new relative-path construct added to a future SvelteKit/Vite release needs the same audit before it can be assumed safe. ([Zero-dependency multi-arch OCI compilation](items/multi-arch-oci-compilation.md))
- A remote-cache hit skips VerifyBaseImage/native inspection by design (the base digest is already bound into the cache key), but the cache-verify key chain never reads req.Signing.PublicKeyPEM, so a build signed via --signing-key alone doesn't automatically make its own cache entries independently verifiable — falls through to a full rebuild rather than failing fast with a clear story (tracked in Serena mem:open_decisions, not this file's scope). ([Registry push throughput, tagging, and composite remote-cache](items/registry-push-and-cache.md))
- Before b350ecb there was no way to tag an image at all — every build published latest unconditionally. Tags apply registry-side after the image is hashed, so the tag set never affects the digest. ([Registry push throughput, tagging, and composite remote-cache](items/registry-push-and-cache.md))
- Lineage discovery is registry-side via a pokkum.dev/predecessor manifest annotation, deliberately independent of Kubernetes' pokkum.dev/image-history — a build-time flag cannot depend on cluster state without coupling build to Kubernetes. The annotation is only stamped when --asset-overlay is actually in use, so auto-discovery can only find a chain that opted in from the start of a rollout sequence. ([Rolling-deploy asset overlay (--asset-overlay)](items/rolling-deploy-asset-overlay.md))
- Requires --output=push for auto-discovery (there is no current tag to inspect for --local/--tarball); --asset-overlay-from's explicit refs work regardless of output mode. ([Rolling-deploy asset overlay (--asset-overlay)](items/rolling-deploy-asset-overlay.md))
- pokkum verify's rebuild-and-compare path does not reproduce this layer — see [pokkum verify doesn't reproduce the asset-overlay layer](items/asset-overlay-verify-gap.md). ([Rolling-deploy asset overlay (--asset-overlay)](items/rolling-deploy-asset-overlay.md))
- --telemetry is rejected outright for node, not silently ignored: the layered strategy's OTel bootstrap is a bun --preload mechanism with no Node equivalent. ([--runtime=node, the second runtime dimension](items/runtime-node.md))
- Correction to the source docs: Roadmap.md and Feature-list.md both still read, at the time of this migration, as if no automated boot smoke test existed for this runtime. That is stale — TestRuntimeSmoke_NodeRuntime_BootsAndServes (tests/integration/runtime_smoke_node_test.go) shipped in e918c52 and also asserts the *absence* of a Bun layer two independent ways, so it is real coverage, not a manual one-off. ([--runtime=node, the second runtime dimension](items/runtime-node.md))
- Node-core CVEs are unqueryable: distroless-node ships Node outside dpkg, invisible to the OS-package scanner, and the zero-dependency scanner has no Node-core ecosystem entry to query against OSV.dev. ([--runtime=node, the second runtime dimension](items/runtime-node.md))
- pokkum dev, pokkum resolve/apply, and standalone pokkum scan have zero runtime awareness — verified: no RuntimeNode reference anywhere in cmd/pokkum/dev.go, k8s.go, or scan.go. ([--runtime=node, the second runtime dimension](items/runtime-node.md))
- Conditional GET (If-None-Match -> 304) was missing until 61fd873 (finding 12) — every request re-downloaded the full body even when the client already held the current copy. ([--strategy=static](items/strategy-static.md))
- Had never worked in any prior release before 2026-08-19: both HTTP servers bound no Addr and silently fell back to port 80 (finding 2, fixed 8306d37); Preflight rejected every real adapter-static project because it hard-coded a bun/node adapter check (finding 3); real prerendered output nests under pages/dependencies/data while the code assumed a flat tree (finding 4); there was no <path>.html fallback so every non-root prerendered route 404'd (finding 7); and the embedded pokkum-static blob was gitignored and absent from every CI job and released binary until 5693980 (finding 6). All fixed in 1c33509/5693980, proven against a real @sveltejs/adapter-static fixture rather than the synthetic mock that had been encoding the same wrong flat-tree assumption. ([--strategy=static](items/strategy-static.md))
- Its own Cache-Control contract (immutable /_app/immutable, no-cache version.json/prerendered HTML) is genuinely tested here (server_test.go, integration_test.go) — see [Cache-Control contract, tested](items/cache-control-contract.md) for why the layered/exe strategies don't have the equivalent. ([--strategy=static](items/strategy-static.md))

### Developer Experience

- Presets are tried first, and only a value containing `/`, `.`, `:`, or `@` is parsed as a reference — this ordering is load-bearing, since `name.ParseReference` would otherwise accept a typo'd preset (e.g. `distrolss`) as valid Docker Hub shorthand instead of surfacing a clear "unknown preset" error. ([--base accepts a custom image reference](items/base-flag-custom-reference.md))
- No supervisor, no startup attestation, no health/readiness probes, no base image, and no non-root user — a single startup warning states this explicitly and the default remains full container-parity mode so nobody debugs a production discrepancy against a mode never meant to model it. ([pokkum dev --no-container](items/no-container-dev-mode.md))
- `--debug`, `--platform`, `--bun-version`, and `--bun-variant` are rejected outright rather than silently ignored, since each describes a property of an image that is never built. ([pokkum dev --no-container](items/no-container-dev-mode.md))
- `--port` and `--watch` warn (rather than reject) when explicitly set, since the dev server picks its own port and hot reload is inherent rather than opt-in. ([pokkum dev --no-container](items/no-container-dev-mode.md))

### Kubernetes & Operations

- Handles raw-YAML `pokkum://` references only. Most teams template with Helm or Kustomize and will never reach this path today — see [Helm post-renderer and Kustomize KRM function](items/helm-kustomize-integration.md). ([pokkum resolve](items/k8s-uri-resolution.md))
- Operating on a static, untouched manifest file cannot accumulate multi-generation rollback history across independent CLI runs unless intermediate annotations are committed or seeded from live cluster state. ([pokkum resolve](items/k8s-uri-resolution.md))
- History accumulation depends on the annotation surviving across independent CLI runs — a static, untouched manifest template with no live cluster query has no other source for it. `pokkum apply`'s pre-flight cluster inspection closes this for the deploy path; a bare `pokkum resolve` run does not. ([pokkum rollback](items/multi-generation-rollback.md))

### Observability

- No automatic HTTP/framework instrumentation: `@opentelemetry/auto-instrumentations-node`'s module-patching approach does not take effect under Bun's runtime. Real spans require the documented `hooks.server.ts` snippet, never auto-injected. ([OpenTelemetry SDK bootstrap (--telemetry)](items/otel-sdk-bootstrap.md))
- Rejected outright for `--runtime=node` — the layered bootstrap's `bun --preload` mechanism is Bun-specific with no Node equivalent yet. ([OpenTelemetry SDK bootstrap (--telemetry)](items/otel-sdk-bootstrap.md))
- `--metrics-only` is non-functional: combining an OTLP metrics exporter with the SDK crashes once compiled via `bun build --compile` — a real Bun bundler bug, not a Pokkum bug. It warns at runtime rather than silently doing nothing. ([OpenTelemetry SDK bootstrap (--telemetry)](items/otel-sdk-bootstrap.md))

### Supply Chain & Attestation

- Fails closed on an incomplete vulnerability database lookup by default (`--allow-incomplete` opts out) rather than silently reporting a clean scan. ([Base image CVE build gate](items/base-image-cve-gate.md))
- Escrow-mirror pulls are digest-pinned against pokkum.lock's recorded digest; a mirror tag retargeted to different content fails closed rather than silently serving stale-pin content. ([Base image escrow / mirroring](items/base-image-escrow-mirroring.md))
- Every custom `--base` reference currently shares one lockfile slot rather than getting its own — see [Per-ref pokkum.lock slot for custom --base images](items/custom-base-lock-slot.md). ([Base image lockfile (pokkum.lock) and audit (pokkum base check)](items/base-image-lockfile.md))
- Keyless verification requires the operator to supply `--keyless-identity`/`--keyless-issuer` explicitly and refuses outright before any network I/O if keyless material is present with no configured identity — it does not trust anything derived from the certificate under verification (a prior version did, and that path was dead code). ([Base image signature verification](items/base-image-signature-verification.md))
- Deleting the implicit defaults exposed two latent fail-opens in provenance verification (see [Remove shared placeholder trust-anchor fallback](items/placeholder-pubkey-fallback-removed.md)) that were previously unreachable, not previously safe. ([Composition-root refactor for verifier injection](items/composition-root-verifier-injection.md))
- The bug was an interop assumption about cosign's own wire format encoded in a code comment and never checked against cosign's actual source — the attestation layer now writes `dev.cosignproject.cosign/signature: ""` to match cosign's convention exactly. ([cosign verify-attestation interop fix](items/cosign-attestation-interop.md))
- The two embedded blobs are gitignored build artifacts (only `.gitkeep` is tracked), so `make check-embedded-blobs` guards local working-tree staleness specifically — CI itself is structurally safe since it always rebuilds both blobs from the checked-out commit before any test runs. ([Embedded PID-1 binaries brought under CI attestation](items/embedded-pid1-attestation-coverage.md))
- This closes the gap described in the finding, not a hypothetical: for a supply-chain tool, the one component that had been running as PID 1 in every produced image, outside the CLI's own SLSA-attested build, was the sharpest edge found during that run. ([Embedded PID-1 binaries brought under CI attestation](items/embedded-pid1-attestation-coverage.md))
- Covered by unit-level tests through real `core.Build` with the real secretguard adapter, but not yet by a real `bun build --compile` run; that empirical test class has caught nearly every severe bug in Lessons.md and remains the highest-value follow-up here. ([`--strategy=exe` secret-scanning gap](items/exe-secret-scan-gap.md))
- exe is **not** at parity with layered/static: a secret injected by the `bun build --compile` step itself — a `bunfig.toml` preload plugin, a `with { type: "macro" }` import — is present in neither scanned tree. ([`--strategy=exe` secret-scanning gap](items/exe-secret-scan-gap.md))
- Breaking change: CI using `--expect-source` on unsigned images now fails until it signs or passes `--allow-unverified-source`. ([`--expect-source` requires verified provenance](items/expect-source-verified.md))
- Static-key signing only — there is no keyless (Fulcio/OIDC) signing path. Keyless Sigstore exists only on the verification side (base images, `pokkum verify`). ([Image signing with Cosign/DSSE](items/image-signing.md))
- The placeholder trust-anchor fallback was removed; an unconfigured key now hard-fails instead of silently no-op signing (a breaking change for anyone who relied on the old default). ([Image signing with Cosign/DSSE](items/image-signing.md))
- Interop with `cosign verify-attestation` in tag-fallback mode required a follow-up fix (see [cosign verify-attestation interop fix](items/cosign-attestation-interop.md)) — dual-publish alone did not guarantee third-party tool agreement. ([Multi-arch signature/attestation subject (dual-publish)](items/multi-arch-attestation-subject.md))
- An unjustified or already-expired exemption entry is rejected outright at config-parse time, not silently honored — this was flagged externally as a gap (both reviewers named missing VEX support as a top-tier concern) before this shipped. ([OpenVEX exemptions for the CVE gate](items/openvex-exemptions.md))
- Diff mode ("N new vulnerabilities since last build") from the original CVE-scanning concept remains unbuilt; this item covers exemption consumption only, not vulnerability-diffing. ([OpenVEX exemptions for the CVE gate](items/openvex-exemptions.md))
- Breaking change: static-key verification now requires an explicitly configured key rather than silently trusting an undocumented shared fallback that nobody's private key ever matched. ([Remove shared placeholder trust-anchor fallback](items/placeholder-pubkey-fallback-removed.md))
- The fallback removal exposed two latent fail-opens in `internal/adapters/provenance/resolver.go` (a nil-tolerant signer check and a bare `false` for a nil DSSE signer) — both now refuse via `ErrVerifierNotInjected` instead of silently skipping verification. ([Remove shared placeholder trust-anchor fallback](items/placeholder-pubkey-fallback-removed.md))
- A file too large or unreadable to scan fails the build (ErrSecretScanIncomplete) rather than silently reporting a clean pass. ([Secret-inlining guard (secretguard)](items/secret-inlining-guard.md))
- Five fixed regex patterns only (private key headers, AWS access keys, GitHub PATs, Google API keys, generic password/secret/token assignments) — not Shannon-entropy analysis. An entropy-based scan for arbitrary high-randomness strings was the original design language but was never built. ([Secret-inlining guard (secretguard)](items/secret-inlining-guard.md))
- See [--strategy=exe secret-scanning gap](items/exe-secret-scan-gap.md) for the one strategy this does not cover. ([Secret-inlining guard (secretguard)](items/secret-inlining-guard.md))
- An explicit `--sigstore-trusted-root` always wins and skips the refresh branch entirely; `pokkum base update`/`base check` never set `VerifySignature`, so the flag is deliberately absent there. ([Sigstore TUF trust-root refresh](items/sigstore-tuf-refresh.md))
- The embedded snapshot was not merely stale when found — it was already actively rejecting valid signatures on the `log2025-1` Rekor shard (live since 2025-09-23) as forgeries, indistinguishable from a real attack from the verifier's own error text. ([Sigstore TUF trust-root refresh](items/sigstore-tuf-refresh.md))
- `--sigstore-tuf-refresh`'s `Offline` mode is bound to `--hermetic` on `pokkum build`; `pokkum verify` has no hermetic concept, so it always allows the refresh attempt and falls back to the embedded snapshot with a warning on failure. ([Sigstore TUF trust-root refresh](items/sigstore-tuf-refresh.md))
- Node-core CVEs remain unqueryable: distroless ships Node outside dpkg, invisible to both the OS-package scanner and the zero-dependency toolchain scanner, which has no Node-core ecosystem entry. Tracked as an open decision — see [Node-core CVE lookup](items/node-cve-lookup.md). ([Toolchain (Bun) CVE awareness](items/toolchain-cve-awareness.md))

### Testing & Infrastructure

- Found and fixed on first run: POKKUM_LOG_LEVEL (read by both PID-1 binaries) was undocumented, --write-config on adopt was undocumented, and Vocabulary.md claimed a verify --rebuild flag that does not exist (the real behavior is rebuild-by-default with --no-rebuild to opt out). ([CLI/docs drift as a mechanical test failure](items/cli-docs-invariant-tests.md))
- The same commit closed six merged-but-unvalidated .pokkum.yaml config fields, including profiles.<name>.output, which nothing had validated before. ([CLI/docs drift as a mechanical test failure](items/cli-docs-invariant-tests.md))
- Deliberately not added to the PR-gate CI job — CI always rebuilds both blobs from the checked-out commit before any test runs, so this specific staleness is structurally impossible there. It exists for the local working-tree hazard, which is exactly where it was first found live (concurrent commits had moved HEAD past the last local rebuild). ([Embedded PID-1 binary freshness guard](items/embedded-blob-freshness-guard.md))
- Must run with -count=1: go test's result cache cannot see through an exec'd go build into another package's source, so a stale cached result would otherwise report clean. ([Embedded PID-1 binary freshness guard](items/embedded-blob-freshness-guard.md))
- Read-only tests were deliberately left untouched, but only after confirming — by reading the actual production code, not assuming — that nothing in their dependency chain (sbom.Generator, packager.Packager, mockCompiler's StrategyExe branch) ever calls os.WriteFile/os.MkdirAll against ProjectDir. ([Real-build tests copy their fixture into t.TempDir() first](items/fixture-isolation.md))
- Stale-claim correction: overnight-findings.md's finding 11 recorded this as 'not fixed — deliberately,' calling it a broader test-hygiene change than belonged at the end of that queue. It was, in fact, done the same day. Serena's mem:state, as read at the start of this migration, still described it as 'known fragility, deliberately not fixed yet' — also stale. Commit 20ba1ec generalized the t.TempDir()-copy pattern tests/integration/runtime_smoke_test.go had already established to all five affected real-build tests, moved the shared helper into harness_test.go, and proved order-independence empirically rather than asserting it. ([Real-build tests copy their fixture into t.TempDir() first](items/fixture-isolation.md))
- bunexec cannot import the tests/integration helper (a separate package, and adapter-to-adapter imports are architecturally forbidden), so it carries a small, deliberate duplicate that — unlike the shared helper — does not skip .svelte-kit, since that test's precondition is a pre-prepared fixture. ([Real-build tests copy their fixture into t.TempDir() first](items/fixture-isolation.md))
- -race is deliberately scoped to registry/core/packager/supervisor rather than the full ./... tree, to keep the added CI cost (~6s) proportionate to where concurrency actually lives. ([Race detector + enforced coverage floor](items/race-detector-and-coverage-floor.md))
- Landed in the same change as fixing a structural CI blind spot: CI never installed Bun before this, so every genuinely-real-build e2e test silently skipped and CI's 'e2e' job was entirely mock-compiler. A separate e2e-real-build job now installs Bun, kept apart so the fast hermetic gate stays fast. ([Race detector + enforced coverage floor](items/race-detector-and-coverage-floor.md))
- --strategy=static has its own separate fixture-driven boot test rather than this harness — see [Real @sveltejs/adapter-static test fixture](items/static-strategy-real-fixture.md). ([Real Docker boot smoke tests](items/runtime-boot-smoke-tests.md))
- Every existing layer-structure/determinism/golden-manifest test had proven the packaged bytes were correct and stable, never that the result actually runs — that gap is exactly what let every layered image ship without a working entrypoint for this codebase's entire prior history (see Lessons.md's 2026-08-18 entry on the missing /app/server/index.js). ([Real Docker boot smoke tests](items/runtime-boot-smoke-tests.md))
- Gated on -short/bun/docker/network, each skipping cleanly rather than failing when unavailable. ([Real Docker boot smoke tests](items/runtime-boot-smoke-tests.md))
- This is the sharpest instance of a recurring lesson in this codebase: a mock encoding the same wrong assumption as the code it tests can never detect the mismatch. TestFixtureDrivenE2E_Static had passed throughout, because both the mock and the production code shared the same incorrect belief that prerendered output is a flat tree — see [--strategy=static](items/strategy-static.md) in build-packaging.yaml for the bugs this found (findings 2, 3, 4, 6, 7). ([Real @sveltejs/adapter-static test fixture replaces a fictional mock](items/static-strategy-real-fixture.md))

