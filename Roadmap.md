# Pokkum Roadmap

See [Vocabulary.md](Vocabulary.md) for the full CLI flag reference, naming
conventions, and the rationale behind each new flag noted below.

See [fixes-to-v1.md](fixes-to-v1.md) for a post-v1.0 audit that found
several `[x]` items below overstated what they actually did, and the fixes
applied for each; [for-users.md](for-users.md) for what changed as a
result.

## v0.1 (Completed)

- [x] Reproducible layer timestamps: set every layer/config timestamp to `SOURCE_DATE_EPOCH` derived from the last git commit (`git log -1 --pretty=%ct`), not build time
- [x] Minimal `securityContext` in generated manifests: Default to `runAsNonRoot: true`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault`.
- [x] `.pokkumignore`: Explicit exclude list so `.env.local`, test fixtures, and source maps don't end up embedded
- [x] Dry-run mode (`--dry-run` / `--print-manifest`): show what would be built/pushed/applied without doing it - added to `pipeline.go`
- [x] Structured, leveled logging so CI logs are parseable
- [x] Idempotent registry pushes: skip push if the computed digest already exists remotely (`remote.Head` before `remote.Write`)
- [x] Basic config validation surfaced as clear CLI errors before any network call — fail fast, not mid-push

## v0.2 (Completed)

- [x] Cosign signing
- [x] SLSA provenance attestation
- [x] SBOM as OCI 1.1 referrer instead of `.sbom` tag convention (new flag: `--sbom-attach=referrer|tag`, default `referrer`)
- [x] `pokkum dev` — Hot-Reload Container Development (build + `--local` + run in one command; new flag: `--debug` to drop into a shell inside the container)
- [x] Zero-Config Auto-Injection: Auto-injecting the adapter and `SOURCE_DATE_EPOCH` pinning without manual `svelte.config.js` edits (supported via `--inject` and `--no-inject` flags). (see [pokkum-injection-concept.md](pokkum-injection-concept.md))
- [x] Base Image Lockfile (`pokkum.lock`): Enable reproducible base image resolving (distroless/chainguard) to prevent drift. (new flags: `--update-base`, `--offline`; new subcommand `pokkum base update --preset <name>`) (see [pokkum-lock-concept.md](pokkum-lock-concept.md))

## v0.3: Layer Caching & Core Architecture Shift

_Refactoring to replace the `exe` adapter with a hand-rolled adapter for layer caching, dramatically reducing per-commit image size._ (see [pokkum-layer-caching-concept.md](pokkum-layer-caching-concept.md))

- [x] M1: Packager + runtime plumbing (Bun runtime resolution, pinned downloads). (new flags: `--bun-binary=<path>` offline escape hatch, `--bun-variant=baseline` for pre-AVX2 hosts) (see [pokkum-layer-caching-concept.md](pokkum-layer-caching-concept.md))
- [x] M2: Hand-rolled SvelteKit adapter + Phase-1 layering (separating app JS from runtime). (new flag: `--strategy=layered|exe`, `layered` default) (see [pokkum-layer-caching-concept.md](pokkum-layer-caching-concept.md))
- [x] M3: Vendor splitting (`bun build --splitting`) + native closure support (unblocking native `.node` addons). (no new flag — internal to `--strategy=layered`) (see [pokkum-layer-caching-concept.md](pokkum-layer-caching-concept.md))
- [x] M4: Hardening (`readOnlyRootFilesystem`) & cutover from the old `exe` adapter. (no new flag — folds into `--security-context`'s hardened-defaults bundle) (see [pokkum-layer-caching-concept.md](pokkum-layer-caching-concept.md))
- [x] Image Optimization: Deduplication across layers, optional zstd layer compression. (new flag: `--compression=gzip|zstd`, default `gzip`) (see [pokkum-layer-caching-concept.md](pokkum-layer-caching-concept.md))

## v0.4: Unified Telemetry & Developer Experience

- [x] Unified Metrics & Telemetry: Zero-config injection of OpenTelemetry setup for traces and metrics (`pokkum metrics` and app-side). (new flag: `--metrics-port` on the `pokkum metrics` subcommand; build-time half reuses existing `--telemetry`/`--otel-export` family) (see [pokkum-metrics-otel-concept.md](pokkum-metrics-otel-concept.md))
- [x] `pokkum init`: Project Configuration Wizard (interactive or `--defaults`).
- [x] `pokkum doctor`: Environment preflight checks (Bun version, registry auth, `svelte.config.js` sanity). (new flag: `--fix` for mechanical repairs)
- [x] Interactive Failure Diagnostics: Automatic log dump and exit code analysis on local container failure. (new flag: `--no-diagnostics` opt-out for CI)
- [x] `--output=json`: Machine-readable build results schema for robust CI parsing.
- [x] Diff & Explain: `pokkum diff`, `pokkum explain`, and `pokkum why` to trace layer changes and dependencies. (reuses `--output=json` for machine-readable output; no new flag)

## v0.5: Reproducibility & Diagnosis

_Closing the loop on reproducibility with verifiable rebuilds and non-determinism bisection._

- [x] M0: Provenance completeness (recording Go version, builder OS/arch, lockfile hashes in SLSA statement). (no new flag — recorded automatically) (see [pokkum-verify-concept.md](pokkum-verify-concept.md))
- [x] M1/M2: Stage recorder, bisection core, and attestation-check mode (`pokkum verify --no-rebuild`). Provenance resolution and Cosign/Sigstore signature and SLSA attestation validation are fully functional and tested against real in-memory OCI registries (`internal/adapters/provenance/resolver.go`). (see [pokkum-verify-concept.md](pokkum-verify-concept.md))
- [x] M3: `layerdiff` component, L3 explanation, and `pokkum repro doctor` diagnostics. (new flag: `--fast` for static-checks-only, no build) (see [pokkum-verify-concept.md](pokkum-verify-concept.md) & [pokkum-repro-doctor-concept.md](pokkum-repro-doctor-concept.md))
- [x] M4: `pokkum verify --rebuild` (L1/L2 compare) with `--perturb` mode, K8s + CI ergonomics. (new flags: `--against <path>`, `--expect-source <repo>@<ref>`, `--all-platforms`) (see [pokkum-verify-concept.md](pokkum-verify-concept.md) & [pokkum-repro-doctor-concept.md](pokkum-repro-doctor-concept.md))

## v1.0: MVP Launch (Supply Chain Hardening & Production Readiness)

_The shippable minimum viable product for enterprise-grade adoption._

### Supply Chain

- [x] Trusted-Builder Mode: SLSA L3 via an isolated CI job (GitHub Actions provenance generation). (no new flag — a CI workflow concern, not a CLI flag)
- [x] CVE Scanning Integration (`pokkum scan`): Run vulnerability scanning against image, tarball, or directory and fail pipeline above severity threshold. (new flag: `--fail-on=critical`)
- [x] Toolchain CVE Awareness: OSV.dev advisory lookups keyed on embedded Bun and SvelteKit versions. (new flag: `pokkum scan --toolchain`, extending `scan`)
- [x] Base Image Signature Verification: Two modes — static-key Cosign verification for custom/self-signed bases (`POKKUM_BASE_IMAGE_PUBKEY`), and keyless Sigstore verification (Fulcio + Rekor) for stock `distroless`/`chainguard` presets by default. (new flags: `--base-verify-mode {auto|keyless|static-key}`, `--base-keyless-identity`, `--base-keyless-issuer`, `--sigstore-trusted-root`, `--no-verify-base` opt-out)
- [x] Secret-Inlining Guard: Build-time entropy/pattern scan to prevent leaked secrets in layers. (new flag: `--allow-secret-pattern` escape hatch for false positives)
- [x] Base image digest pinning + automated update PRs (Renovate/Dependabot-style). (reuses `pokkum base update --preset <name>` from the v0.2 lockfile item; update-PR half is a bot, not a flag) (see [pokkum-lock-concept.md](pokkum-lock-concept.md))
- [x] Standard OCI Annotations: `org.opencontainers.image.revision`/`.source`/`.version`/`.created` auto-populate on every build by default — no flag needed, and no opt-out flag exists. `revision`/`source`/`version` come from git (commit SHA, remote URL, `git describe`) or CI env vars; `created` is set to the exact same `SOURCE_DATE_EPOCH`-resolved timestamp used for layer mtimes and the image config elsewhere in the build (not resolved independently, so it can't disagree with what's actually in the image) and is left unset — not fabricated from `time.Now()` — when that timestamp can't be determined. `--image-label key=value` still works for explicit overrides (checked first, so an explicit value always wins) and any other annotation. Outside a git repository (or without `git` on `PATH`), `revision`/`source`/`version` are silently absent — no warning is printed. (new flag: `--image-label key=value`, repeatable — matches `ko build --image-label`)

### Cluster-side hardening

- [x] `NetworkPolicy` generation restricting egress and ingress limited to expected ports, `podSelector` scoped to the workload's own Pod-template labels. (new flags: `--network-policy` / `--no-network-policy` on `resolve`/`apply`)
- [x] Resource `requests`/`limits` and a `PodDisruptionBudget` by default in generated manifests, selector-scoped to the workload rather than the whole namespace — skipped entirely, not emitted unscoped, when no workload labels can be found. (new flags: `--resource-defaults` / `--no-resource-defaults` on `resolve`/`apply`)
- [x] `readOnlyRootFilesystem: true` where feasible (supervisor + compiled binary). (no new flag — folds into `--security-context`)
- [x] Readiness Drain on SIGTERM: Supervisor holds `/readyz` at 503 while the app drains. (no new flag — bounded by the existing `POKKUM_SHUTDOWN_TIMEOUT` env var)
- [x] Secrets via `envFrom` referencing Kubernetes `Secret`/external-secrets, never baked into image layers. (no new flag — expressed in the manifest itself)

### Build integrity

- [x] Hermetic builds: No network egress during the compile step (SLSA L3 requirement). (new flag: `--hermetic`, opt-in)
- [x] Multi-registry support with per-registry auth chains via a `docker config.json`-style file keyed by registry hostname, merged ahead of the default keychain (self-hosted and any registry with static credentials in the file). No ECR/GCR/ACR-specific credential-helper invocation by design — Pokkum stays zero-dependency rather than vendoring cloud-provider SDKs; the standard `docker config.json` format is the deliberate boundary. (new flag: `--registry-config=<path>`)
- [x] Ephemeral test registry (`pkg/registry.NewServer()`) wired into CI for integration tests. (no new flag — test infra only)

### Operational maturity

- [x] Version pinning of `pokkum` itself in generated manifest annotations. (no new flag — folds into `--image-label`/auto-annotations above)
- [x] A GitHub Action wrapping the CLI (mirroring `setup-ko`). (no new `pokkum` flag — Action inputs like `version`/`token`, mirroring `setup-ko`)
- [x] Rollback support (`pokkum rollback` reading from declarative manifest annotations `pokkum.dev/previous-image`). (new subcommand: `pokkum rollback -f <manifest> [--to=<ref>]`, reusing `-f`/`--file` from `resolve`/`apply`; `--to` is optional and defaults to `pokkum.dev/previous-image` annotation)
- [x] Signed Self-Distribution (`pokkum upgrade`): Signature verification of release artifacts and binary self-updates via `ports.ReleaseVerifier` and `cosign`. (new subcommand: `pokkum upgrade`, new flags: `--check`, `--version`, `--offline`, `--key`)

## Post-v1.0 Enhancements (Completed & Shipped)

The post-v1.0 milestones addressed critical supply chain verification, CVE gating, registry interoperability, and developer adoption capabilities:

### Supply Chain & Verification Rigor (Tier 0)
- [x] **Real Provenance & DSSE Envelope Verification**: `internal/adapters/provenance/resolver.go` pulls real manifests, verifies Cosign signatures (`<repo>:sha256-<hex>.sig`) via static-key or keyless Sigstore, extracts and verifies in-toto DSSE envelopes and SLSA v1.0 statements (`<repo>:sha256-<hex>.att`), enforces `--expect-source` assertions, and conducts toolchain skew analysis.
- [x] **Bit-for-Bit Image Comparator**: `internal/adapters/comparator/comparator.go` performs genuine L1 (exact manifest hash), L2 (semantic uncompressed DiffIDs and config), and L3 (layer-by-layer tar stream file diffs with root cause analysis) comparisons between remote images and local rebuild tarballs.
- [x] **Attestation & Verification CLI**: `cmd/pokkum/verify.go` supports `--registry-config`, `--no-rebuild`, and `--against <tarball>`, reporting accurate cryptographic verdicts and exit codes.

### Security & Base Image CVE Build Gate (Tier 1)
- [x] **Real OS & Toolchain Vulnerability Scanning**: `pokkum scan` pulls and enumerates image contents via `syft` and queries OSV.dev batch API (`/v1/querybatch`) for real OS-package and ecosystem advisories. Fails closed on incomplete scans by default (`--allow-incomplete` opt-out).
- [x] **Base Image CVE Reactivity & Build Gate**: `pokkum build` actively scans the resolved base image digest against OSV.dev, logging warnings by default and failing builds when configured with `--fail-on-cve=<severity>` (or `POKKUM_FAIL_ON_CVE`). Persists `last_scanned_at`, `vulnerabilities_count`, and `max_severity` into `pokkum.lock`. An active gate combined with `--offline`/`--hermetic` used to silently skip the scan with zero log output — fixed to fail closed (reusing the `--allow-incomplete` escape hatch) unless the operator explicitly acknowledges the scan can't run offline; see `TestPipeline_BaseImageCVE_Gating`'s offline/hermetic subtests.
- [x] **Base Image Escrow / Mirroring**: `pokkum base update --mirror-registry=<repo>` mirrors base images/indexes and their Cosign `.sig` tags to a project-controlled registry with automatic fallback. Resolving with `--mirror-registry` and `VerifySignature: true` verifies `docker-reference` claims against the upstream repo recorded in `pokkum.lock` (`entry.Ref`), preserving signature authenticity.

### Enterprise Registry Interoperability (Tier 2)
- [x] **Credential-Helper Invocation**: `--registry-config` dynamically resolves credentials via `credHelpers` and `credsStore` by executing `docker-credential-*` binaries (e.g., ECR, GCR, OSXKeychain) with in-memory caching and fallback to static `auths` blocks, supporting cloud registries with zero new external SDK dependencies.

### Adoption & Runtime Safety (Tier 3)
- [x] **Migration Codemod (`pokkum adopt`)**: Auto-converts `@sveltejs/adapter-node`, `adapter-vercel`, `adapter-auto`, and `adapter-cloudflare` projects to native Pokkum compilation defaults with AST/regex config rewrites, `.pokkumignore` bootstrapping, and optional legacy Dockerfile removal (`--dry-run`, `--remove-dockerfile`).
- [x] **Multi-Generation Rollback History**: `pokkum rollback` supports arbitrary historical rollback depths via `pokkum.dev/image-history` manifest annotations with timeline inspection (`--list`) and generation selection (`--generation=<n>`, `-g <n>`).
- [x] **Image Provenance Timeline (`pokkum history`)**: Inspects published OCI manifests and extracts standard `org.opencontainers.image.*` provenance annotations (git commit, repository, build timestamp, version).
- [x] **Live Cluster Annotation Inspection**: `pokkum apply` queries live cluster state (`kubectl get <kind>/<name> -n <ns> -o json`) before manifest resolution to seed `pokkum.dev/image-history` and active images, enabling multi-generation history accumulation across independent CLI runs on static `pokkum://` templates (`--cluster-inspect`, `--no-cluster-inspect`). Every kubectl failure (unreachable cluster, RBAC denial, malformed kubeconfig) used to be folded into the same "workload not found" empty state at Debug-only log level, silently resetting rollback history with no operator-visible signal. Fixed: only a genuine `(NotFound)` StatusReason is treated as fresh; every other failure surfaces as a Warn-level log line via `ResolveResult.ClusterInspectionWarnings`.
- [x] **Runtime Environment Contract**: Declares required runtime environment variables in OCI image annotations (`pokkum.dev/required-env`) and embeds contract into runtime config, enforced by PID-1 supervisor (`/pokkum/init`) to fail-fast on startup if any are missing (`--require-env=KEY1,KEY2` on `build`).

---

## Architectural & Performance Optimizations

*Focusing on build speed, CLI footprint, and container image size reduction.*

### 1. Build Speed & Throughput
- [x] **Pre-Calculated Immutable Layers**: Cache pre-compressed `bun` and `pokkum-init` blobs locally to skip ~100MB of tarring and compression per build.
- [x] **Single-Pass Layer Hashing & Streaming**: Compute uncompressed DiffID and compressed Digest simultaneously using multi-threaded compression (`pgzip` or `zstd`) to significantly speed up layer generation. The digest/diffID computation itself was independently adversarially verified as correct, deterministic, and bit-reproducible across GOMAXPROCS settings (byte-identical to the old fully-buffered path). Two real resource leaks were found and fixed: intermediate temp files relied solely on a GC finalizer that a CLI process normally exits before running (hundreds of MB/build orphaned in `/tmp`), and the zstd decoder was never released due to an `io.Closer` type-assertion mismatch — both fixed with deterministic cleanup (see `TestBuildSinglePassLayer_TempFileCleanup`, `TestUncompressed_ZstdDecoderClosed`).
- [x] **Overlapped Async Pipeline Stages**: Run base image resolution, signature verification, and module inspection in parallel background goroutines during the SvelteKit build phase. Base image *digest* resolution stays ahead of the remote-cache check (the cache key needs it), but signature verification and native-module inspection now run concurrently with `Compiler.Prepare` via `errgroup.WithContext`, gated on a confirmed cache miss — a cache hit skips both entirely since neither has anything left to gate once nothing is being built from source or from the base image. A failure in either concurrent check cancels the in-flight SvelteKit build (tearing down the `bun` subprocess tree, not just abandoning a goroutine) before publishing is ever reached. `--dry-run` keeps synchronously verifying the signature and inspecting native modules first, matching pre-change fail-fast behavior.
- [x] **Registry Push Optimizations**: Parallel layer uploads over HTTP/2 (`remote.WithJobs`, wired via `remoteConfig.Jobs`) and cross-repository blob mounting (`mount=`/`from=` query parameters) for zero-egress base layer copies — `internal/adapters/registry/mount.go`'s `mountObserver` sniffs the wire (never the response body) to classify each blob's outcome as mounted/streamed/already-present without any production-code hook into go-containerregistry's unexported writer internals, and `Push` logs `mount_layers_mounted`/`_declined`/`_already_present` on every successful push. Proven end-to-end in `internal/adapters/registry/push_test.go` against a repo-scoped mount-emulating test registry (`mount_test.go`): same-host mount success with zero bytes re-uploaded (`TestPush_CrossRepoMount_ZeroEgress`), graceful fallback when the claimed source blob is genuinely absent (`TestPush_CrossRepoMount_FallbackOnAbsentBlob`), graceful fallback across two independent registry hosts — the case that actually proves the optimization degrades safely rather than breaking (`TestPush_CrossRepoMount_CrossRegistryRejected`) — and correct handling of an explicit registry-side mount rejection exercising go-containerregistry's retry-without-mount path with nothing double-uploaded (`TestPush_CrossRepoMount_RegistryRejectsMount`). Along the way, fixed a real bug in the test harness itself (not production code): upstream's own repo-path arithmetic (`pkg/registry/blobs.go`) computes a different repo string for chunked-upload PATCH/PUT requests than for reads, so the harness's repo-scoped blob store stored real pushed blobs under `"<repo>/blobs"` while reads looked them up under plain `"<repo>"` — every non-mounted blob would appear to vanish. Fixed with `normalizeBlobRepo`; see `TestMountAwareTestRegistry_RealWriteThenReadAgreeOnRepo` and `Lessons.md`. The user-facing `--push-concurrency` flag (default 0, defers to adapter's hardcoded 4) is wired through `cmd/pokkum/build.go` → `core.BuildRequest.PushConcurrency` → `ports.PushRequest.Concurrency` → `remoteConfig.Jobs`; documented in `Vocabulary.md` section 3. Fixed HTTP/2 negotiation for `--insecure` pushes against self-signed-TLS registries by cloning from `remote.DefaultTransport` (preserving `ForceAttemptHTTP2`, proxy support, connection pooling) instead of bare `&http.Transport{}` literals in `internal/adapters/registry/registry.go`'s `defaultTransport`/`insecureTransport` — plain `http://` insecure targets still don't get HTTP/2 (a net/http limitation, not something this fixes). Confirmed pre-existing correct behavior: base images resolved via `--mirror-registry`/`pokkum.lock`'s `MirrorRef` produce mountable layers pointing at the mirror repo (not upstream), so mount-based zero-egress pushes engage the most common production case; separately, go-containerregistry intentionally suppresses cross-repo mount attempts when the push target is Docker Hub and source isn't also Docker Hub (documented upstream API workaround), so `mount_layers_mounted` staying low/zero on Docker Hub pushes is expected, not a defect — verified in `TestResolve_MirrorRef_LayersAreMountableFromMirrorRepo` and `TestPush_DockerHubMountSuppression_IsRealAndAppliesToOurPushPath`. Two pre-existing anti-patterns (bare `&http.Transport{}` literals) were identified but deliberately left out-of-scope: `internal/adapters/baseimage/resolver.go:92` and three sites in `internal/adapters/remotecacheutils/remotecacheutils.go` (~lines 432, 725, 766) — flagged for follow-up, see `Lessons.md`.

### 2. CLI Binary & Footprint Reduction
- [x] **Targeted Scanner vs Monolithic Syft**: Replace the monolithic `anchore/syft` dependency with lightweight, zero-dependency parsers for `dpkg`, `apk`, and `package.json` to cut CLI binary size by ~50MB. *(⚠️ Requires long-term maintenance: Must track changes in Debian/Alpine package database formats and NPM ecosystem lockfile specs)* Image/tarball scanning (`pokkum scan <image>`) had a confirmed bug: it read only the `dependencies`/`devDependencies` maps out of vendored `package.json` files, never their own `name`/`version` — so it never actually cataloged the real installed npm packages Pokkum ships, undermining CVE lookups against the built image. Fixed to record each vendored package's own declared identity; see `TestExtractImagePackages_VendoredPackageJSON`.
- [ ] **Compressed Embedded Supervisor Binaries**: Pre-compress `pokkum-init` binaries with `zstd` before `go:embed` and decompress on-the-fly, saving ~8MB.
- [ ] **Compiler Build Optimization Flags**: Enforce `-trimpath -ldflags="-s -w"` in release pipelines for significant binary size reduction.

### 3. Container Image Size Reduction
- [x] **Vendor Layer Pruning**: Automatically strip `*.map`, `*.d.ts`, and test/doc files from `node_modules` during bundling for a 15-35MB reduction. *(⚠️ Requires long-term maintenance: The exclusion rules must be continually updated as NPM ecosystem packaging norms and SvelteKit runtime dependencies evolve)* The doc/metadata patterns (README, LICENSE, ...) were bare wildcard prefixes that also matched real runtime source files starting with the same word (`readme.js`, `license-checker.js`), silently deleting them from the vendor layer — a would-be module-not-found crash at container startup. Fixed with an exact-basename-plus-known-extension match (`isDocFile`); see `TestIsJunk_DocFilesNotFalsePositive`.
- [x] **ELF Native Addon Stripping**: Run `strip --strip-unneeded` on native `.node` addons to discard debug symbols and unneeded relocations. On any host without an ELF-capable strip tool on `$PATH` (e.g. plain macOS), the failure fell through silently and the result was discarded — native addons shipped completely unstripped with zero warning the feature had done nothing. Fixed to surface a `Warn`-level log naming the skipped files; see `TestStripDirectory_NoWorkingStripTool_SurfacesSkipped`.
- [x] **Build-Time Static Asset Pre-Compression**: Pre-compress `/app/client` assets with Brotli (`.br`) and Zstandard (`.zst`) alongside `.gz` so the server serves them immediately without runtime CPU compression overhead. Sidecar generation only checked whether a `.gz`/`.br`/`.zst` file already existed, never whether the source had since changed — a reused output directory could serve stale, mismatched compressed content indefinitely. Fixed with an mtime-based staleness check; see `TestPrecompressFile_RecompressesStaleSidecar`.

### 4. System Efficiency & Remote Caching
- [x] **Composite Remote OCI Input Caching & Anti-Poisoning Verification**: Skip builds entirely (sub-100ms) if an image already exists in the registry for a given hash of `(source + lockfile + baseDigest + bunVersion + platforms + flags)`. Fully closed all known cache integrity gaps: (1) fixed-length digest framing prevents source-tree/lockfile second-preimage framing collisions; (2) input hash comprehensively covers the entire runtime, telemetry, compile, and SBOM contract; (3) `ReconcileTags` failure propagation prevents false cache hits; and (4) **Cryptographic Signature Verification Before Tag Promotion**: before promoting release tags or accepting a cache hit, `remotecacheutils` cryptographically verifies Cosign static-key or Sigstore keyless signatures on the candidate cache-hit image (`<repo>:<alg>-<hex>.sig`). Unverified or poisoned cache entries are cleanly rejected, seamlessly falling back to a full compilation from source without permitting unverified code promotion, and signed builds can safely participate in build-skip caching when candidate images carry verified signatures matching trusted credentials. The repo-claim comparison in the signature-payload check (`checkSimpleSigningClaims`) originally used a suffix match rather than an exact match — a payload claiming `"evil.com/" + expectedRepo"` was wrongly accepted as matching `expectedRepo`. For the keyless path this check is the *only* thing standing between a validly-signed-by-the-trusted-identity payload for a different repo and a false accept (sigstore-go's `Verify` proves identity + payload integrity, never which repo the payload claims to describe); the static-key path is separately protected by `cosign.Signer.Verify`'s own exact-match check, so this was a real gap specifically in the keyless mode. Fixed to exact match, matching the convention already established in `internal/adapters/baseimage/resolver.go`. See `internal/adapters/remotecacheutils/remotecacheutils_test.go` (`TestCacher_Check_KeylessVerification_SuffixRepoClaimRejected`) and `internal/core/pipeline_test.go`.
- [x] **`sync.Pool` Buffer Recycling**: Eliminate repetitive large slice allocations during tarball generation to reduce GC pressure and memory spikes.
- [ ] **Fast Sub-Millisecond Supervisor Startup**: Parallelize Bun child process fork/exec with the `/healthz` HTTP probe binding in `pokkum-init`.

---

## Recommended Next Steps (Prioritized)

Prioritized backlog for ongoing development:

### 1. Monorepo Affected-Detection (High Priority)
- **Problem**: In multi-app monorepos with multiple `pokkum://` image URIs, running `resolve`/`apply` builds all services regardless of which files changed.
- **Solution**: Implement git-diff input tracking per SvelteKit app directory. If files in an application's source tree have not changed since a base commit, skip compilation and packaging entirely.
- **Flags/Interface**: `--since=<git-ref>` on `pokkum resolve` and `pokkum apply`.

### 2. Static / Prerendered Page Optimization (Medium Priority)
- **Problem**: Fully static or predominantly prerendered SvelteKit sites still incur Bun runtime memory overhead and layer distribution size.
- **Solution**:
  - Extract prerendered static assets (`.svelte-kit/output/prerendered`) to a dedicated slim OCI layer with immutable Cache-Control metadata.
  - Add a `--static` mode compiling purely static sites onto an `nginx:alpine` or minimal static file server image with zero JavaScript runtime overhead.
- **Flags/Interface**: `--static` on `pokkum build`.

### 3. Policy as Code Enforcement (Low Priority / Backlog)
- **Problem**: Security compliance rules (e.g., "no critical CVEs", "must contain SBOM", "must run as non-root") are currently validated imperatively across disparate flags.
- **Solution**: Integrate Open Policy Agent (OPA) / Rego policy evaluation to assert organizational supply-chain and container security policies against image metadata, SBOMs, and scan results prior to publishing.
- **Flags/Interface**: `pokkum policy check --policy=<path>` subcommand and `--policy=<path>` build gate.

---

## Beyond v1.0 / Long-term Backlog

_Features planned for future architectural iterations:_

- [ ] Multi-Environment Management: Config templating and secret-manager integrations. (new flag: `--target-env=<name>` on `build`/`resolve`/`apply`)
- [ ] Hooks System: Pre/post-build hooks (shell, Bun scripts, webhooks). (new subcommands: `pokkum hook pre-build`, `pokkum hook post-build`; new flag: `--skip-hooks` on `build`)
- [ ] Service Mesh Integration: Auto-generate Istio/Linkerd configs and mTLS paths. (new subcommand: `pokkum mesh generate`, new flags: `--mtls`, `--mesh-telemetry`)
- [ ] Progressive Deployment Strategies: Canary/blue-green deployments for advanced GitOps controllers. (new subcommand: `pokkum deploy`, new flags: `--canary=<percent>`, `--blue-green`, `--auto-rollback`)
- [ ] Asset Optimization Pipeline: Automatic WebP/AVIF variants, cache-busting, CDN-origin separation. (new flag: `--optimize-assets` on `build`)
- [ ] Plugin System: Extensible via external packages (high complexity/supply-chain risk). (new subcommands: `pokkum plugin add|list|remove <name>`)
- [ ] `--verify-base-on-cache-hit` (opt-in, strict): On a confirmed remote-cache hit, still run `BaseImageResolver.VerifyBaseImage` before accepting the hit. Today a cache hit deliberately skips base-image signature verification (nothing is built from the base on a hit, and the cache key already binds the base image digest via `pokkum.lock`); the skip is disclosed with an explicit audit log line. This flag exists for strict supply-chain-audit environments that want the uniform guarantee "every emitted/promoted image traces to a verified base, hit or miss" — notably catching the narrow case of a base whose pinned digest still matches but whose signature was later revoked/rekeyed. Must stay **structurally independent** of `--cache-verify` (which authenticates the cache-hit *output* image; this flag authenticates the *base*). Opt-in so the sub-100ms fast path is preserved by default. (new flag: `--verify-base-on-cache-hit` on `build`; see `AdditionalFeatures.md`)

