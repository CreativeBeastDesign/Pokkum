# Pokkum Layer-Caching Concept: Replacing `Hugo-Dz/exe` with a Hand-Rolled Adapter

Status: PROPOSAL — targets post-v0.1

## 1. Problem Statement

Pokkum v0.1 builds images via `@jesterkit/exe-sveltekit` (a `Hugo-Dz/exe` derivative): the adapter emits a server entrypoint with embedded static assets, and Pokkum runs `bun build --compile` per platform. The result is a single ~90 MB executable that fuses four things with very different change frequencies:

| Component | Typical change frequency | Size |
|---|---|---|
| Bun runtime | Weeks/months (Bun upgrade) | ~90 MB |
| npm dependencies | Days/weeks (lockfile change) | 1–30 MB |
| App server code | Every commit | 0.1–5 MB |
| Static client assets | Every commit | 0.5–20 MB |

Because all four live in one binary, **every commit invalidates one monolithic layer per architecture**. Consequences:

- Every push/pull moves ~90 MB × 2 arches, even for a one-line change.
- Registry storage grows by ~180 MB per build.
- Kubernetes nodes re-pull the full app layer on every deploy.
- The compile step itself (`bun build --compile`, twice) dominates build time.

## 2. Goal

Replace the exe adapter with a hand-rolled build strategy that:

1. Splits the image into layers ordered by change frequency (classic `ko`/Jib layering).
2. Keeps every hardening property Pokkum already guarantees (distroless base, nonroot, PID-1 supervisor, reproducible digests, SBOM/SLSA/DSSE/Cosign).
3. Ideally makes the app layers **architecture-independent** (JS is portable; only the runtime is not).

## 3. Proposed Target Layout

```
Layer 5  /app/client/**, /app/prerendered/**   ← every commit   (arch-independent)
Layer 4  /app/server/index.js (+ chunks)       ← every commit   (arch-independent)
Layer 3  /app/server/vendor-*.js               ← lockfile change (arch-independent)
Layer 2  /pokkum/init                          ← Pokkum release  (per-arch)
Layer 1  /usr/local/bin/bun                    ← Bun upgrade     (per-arch)
Layer 0  distroless/cc-debian12 | chainguard   ← base bump       (per-arch)
```

Entrypoint becomes:

```
/pokkum/init -- /usr/local/bin/bun /app/server/index.js
```

Key win beyond caching: **Layers 3–5 are identical across `linux/amd64` and `linux/arm64`.** The JS build runs once; only the Bun runtime and supervisor layers differ per arch. Today Pokkum compiles the full binary twice.

### Why bundle to JS instead of shipping `node_modules`

Two candidate strategies for the dependency layer:

- **(A) Bundle with `bun build` (no `--compile`)** into `index.js` + a vendor chunk. Deps become plain JS.
- **(B) Ship pruned production `node_modules`** and run unbundled output.

**Recommendation: (A).** (B) has severe determinism and portability problems: `.bin` symlinks, postinstall scripts, absolute paths, and — critically — platform-specific `optionalDependencies` (e.g. `sharp`, `esbuild`) that would force per-arch dependency layers and re-introduce the tar-of-a-directory-tree reproducibility problem at scale. (A) keeps the deterministic-artifact surface small (a handful of bundler output files). Native `.node` addons are the exception — see §7.

### Phasing the vendor split

The vendor chunk (Layer 3) is the riskiest determinism item (see hurdles). Phase it:

- **Phase 1:** Layers 4+3 collapsed into one "app JS" layer. Already captures ~95 % of the win — the 90 MB runtime layer is cached, and per-commit churn drops to a few MB.
- **Phase 2:** Split vendor via `bun build --splitting` with controlled chunk naming, once determinism is proven in CI.

## 4. Component Design

### 4.1 Hand-rolled SvelteKit adapter (new, TypeScript)

A minimal in-repo adapter (`adapter/` or `packages/pokkum-adapter/`, published as e.g. `@pokkum/adapter`) implementing the SvelteKit `Adapter` API:

- `writeClient()` / `writePrerendered()` → emit files to `build/client`, `build/prerendered` (no embedding).
- `writeServer()` → emit an entry that instantiates SvelteKit's `Server` (from `@sveltejs/kit`) inside `Bun.serve()`:
  - Static serving for `/_app/immutable/**` with `cache-control: public, immutable, max-age=31536000`; other assets with sane defaults.
  - Prerendered-page lookup before hitting the SSR handler.
  - `HOST`/`PORT` env handling matching the current runtime contract (`PORT` default 3000).
  - Optional precompressed `.br`/`.gz` serving (deterministic compression required — pin flags).
- Honors `SOURCE_DATE_EPOCH` exactly as today (the `kit.version.name` pinning in `injector.go` stays unchanged).

Reference points: `svelte-adapter-bun` and `@sveltejs/adapter-node` show the full required surface (this is where most correctness bugs will hide — see hurdles).

**Viability & trade-offs.** A custom adapter is viable: the SvelteKit `Adapter` API is small (`adapt(builder)` + `builder.writeClient/writeServer/writePrerendered/generateManifest`) and has been stable across SvelteKit 2.x; `adapter-node` is ~500 LOC total. What you buy: full control over output layout (layer-friendly `client/prerendered/server` split), `Bun.serve` performance, and freedom from upstream bugs like the `assets.generated.ts` ordering issue. What it costs: you chase two moving targets (SvelteKit internals like `Server.respond()` semantics, and Bun), and own the correctness long-tail (streaming bodies, `read()` from `$app/server`, prerender edge cases).

**Lower-maintenance alternative: official `@sveltejs/adapter-node`, executed by Bun.** Bun runs Node `http`/polka servers fine, and the adapter is maintained by the SvelteKit team with exactly the output shape we need (`build/client`, `build/server`, `build/prerendered`... handled via `handler.js`). Trade-offs: no `Bun.serve` fast path, an extra polka dependency in the bundle, and Bun/Node-compat edge cases move from "our adapter code" to "Bun's node:http implementation". Recommended sequencing: **start with adapter-node under Bun for M2** (eliminates hurdle #1 almost entirely), hand-roll only if `Bun.serve` perf or WebSockets later justify it. The layering design is identical either way.

### 4.2 Bun runtime resolver (new Go adapter: `internal/adapters/bunruntime`)

- Downloads official Bun release archives per platform (`bun-linux-x64`, `bun-linux-aarch64`), **pinned by version and SHA256** recorded in Pokkum source (or a future `pokkum.lock`, cf. `pokkum-lock-concept.md`).
- Caches verified binaries under `~/.cache/pokkum/bun/<version>/<platform>/`.
- Exposes the binary as a layer input with the same pinned tar headers used today (`uid/gid 65532`, `mode 0555`, `SOURCE_DATE_EPOCH` mtime).
- Decision needed: `bun-linux-x64` requires AVX2; expose `--bun-variant=baseline` for older x86-64 nodes.
- The runtime binary's digest becomes an SLSA `resolvedDependencies` entry (`internal/adapters/slsa/generator.go`).

### 4.3 Build pipeline changes (`internal/adapters/bunexec`, `internal/core/pipeline.go`)

- `bun run build` (SvelteKit + new adapter) stays, minus the exe adapter's `assets.generated.ts` sort workaround — that file no longer exists.
- Replace per-platform `bun build --compile` with **one** `bun build --target=bun --minify [--bytecode?]` bundling pass (arch-independent). Note: `--bytecode` output is Bun-version-tied and likely arch/endianness-sensitive — if used, it moves the server layer back to per-arch. Default off; offer as an opt-in startup optimization.
- `internal/adapters/sveltekit/injector.go`: inject `@pokkum/adapter` instead of `@jesterkit/exe-sveltekit`; `project.go` validation likewise.

### 4.4 Packager generalization (`internal/adapters/packager/layer.go`)

- Generalize from the fixed supervisor+app two-layer model to an ordered list of layer specs, each either a single file or a directory tree.
- Directory-tree layers require the full determinism treatment: lexicographically sorted walk, explicit parent dir entries, pinned headers, symlink policy (reject or normalize), same zeroed-gzip settings.
- Add a local **blob cache** keyed by uncompressed diffID so unchanged layers skip re-gzip/re-hash entirely (registry-side dedup via `go-containerregistry` blob-existence checks already works once digests are stable — that's the actual cache mechanism; no daemon needed).

### 4.5 Inspection, SBOM, supply chain

- `nativeinspect`: point the `DT_NEEDED`/glibc check at the **Bun runtime binary** (same glibc surface as compiled output today, so `distroless/cc`/Chainguard defaults hold).
- `sbom`: improves — syft now sees real bundler inputs/lockfile instead of an opaque binary; add the Bun runtime as an explicit SBOM package with version + digest.
- Cosign/DSSE/SLSA flow is unchanged (they sign the image digest); add bun-runtime + adapter version to SLSA materials.

### 4.6 Static assets: files on disk vs. embedding

Three options for `build/client/**` and prerendered pages:

- **(a) Plain files in their own layer (recommended).** Served from disk by the adapter's static handler. Integrity comes from the image digest at admission + `readOnlyRootFilesystem` at runtime — the same guarantees the app JS layer has. Best caching behavior: asset churn never invalidates the server layer and vice versa.
- **(b) Embed as byte strings in the JS bundle** (base64 / `Uint8Array` literals, i.e. what the exe adapter effectively does today minus `--compile`). Rejected: ~33 % base64 size overhead, assets become heap-resident, startup parse time grows with asset size, and — fatally for this proposal — every asset change invalidates the server JS layer, undoing the layering. Note Bun's native `import ... with {type: "file"}` embedding only works under `--compile`; in a plain bundle it copies files to the outdir anyway. The only benefit of embedding is single-artifact sealing, which the image digest already provides at a stronger level (it covers the base image too).
- **(c) Deterministic asset-pack archive (middle ground).** Pack assets into one uncompressed, sorted, offset-indexed archive shipped as its own layer; the server mmaps it and serves by offset; its SHA-256 is baked into the server bundle (or the supervisor manifest, §5) and verified at startup. Gives sealed-style tamper-evidence without readonly-fs, no heap bloat, still a separate cacheable layer. Costs custom pack/serve code. Worth considering only if (a)'s reliance on K8s-level hardening is deemed insufficient — it composes with Option C below, which achieves the same property more generally.

## 5. Hardening Analysis (what changes, what doesn't)

Unchanged:

- Distroless/Chainguard base: no shell, no package manager, no coreutils.
- `runAsNonRoot`, `65532:65532`, `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault` injection via `pokkum resolve`.
- PID-1 supervisor contract (`/healthz`, `/readyz`, signal forwarding, shutdown timeout).
- Image signing, provenance, reproducible digests.

Changed — honest assessment:

- **A general-purpose JS runtime now ships in the image.** A compiled exe is a sealed artifact; `bun` can execute arbitrary JS/TS handed to it. Post-compromise (attacker already has code exec in the container), this modestly widens options — though a SvelteKit app already contains a full JS interpreter either way, so the delta is smaller than it looks. Mitigations:
  - Fixed argv via supervisor: `/pokkum/init -- /usr/local/bin/bun /app/server/index.js` — the supervisor should refuse to exec anything else (compile the target path in, don't read it from env).
  - Document/inject `readOnlyRootFilesystem: true` in the resolver's default securityContext (worth adding regardless — the exe path benefits too).
  - No shell in the image means `bun` is not trivially reachable without an existing exec primitive.
- **App code is now files on disk, not sealed in a binary.** Integrity is still guaranteed by the image digest + Cosign at admission time; runtime tamper resistance comes from the read-only rootfs. Net: equivalent under standard K8s hardening, weaker without it — document this.
- **Bun download supply chain** is a new trust edge. Mitigated by version+SHA256 pinning in-source (not TOFU), recorded in SLSA provenance. Support an offline `--bun-binary=<path>` escape hatch for air-gapped builds.

### 5.1 Reality check on the v0.1 baseline

The "sealed binary" premise is weaker than it looks: Bun single-file executables embed the full Bun runtime and **expose the complete Bun CLI via `BUN_BE_BUN=1`** (documented: bun.com/docs/bundler/executables, "Act as the Bun CLI"; added in oven-sh/bun#20251). `BUN_BE_BUN=1 /app/server evil.js` runs arbitrary scripts, `... /app/server x <pkg>` is `bunx`. There are even open bugs where the CLI activates *without* the env var (oven-sh/bun#23205, #19536). So v0.1's compiled exe is, security-wise, approximately "bun on disk with a default entrypoint" already. Parity is therefore an achievable bar, not a fundamental obstacle.

### 5.2 Options for closing the runtime-surface gap

Ordered by recommendation; A, B, and C compose.

- **Option A — Compiled stub launcher (strongest parity, keeps caching).** Instead of shipping stock `bun`, ship a tiny `bun build --compile` executable whose only code is `await import("/app/server/index.js")` (path compiled in — not argv, not env). Rebuild it once per (Bun version × arch); the layer digest changes only on Bun upgrades, so caching is fully preserved. CLI surface is then *identical* to v0.1 (including the `BUN_BE_BUN` caveat — no worse, no better). Cost: must verify compiled exes can runtime-import external files including transpilation (spike this in M1); Pokkum builds the stub itself, so the `bunruntime` adapter compiles instead of downloads.
- **Option B — Stock bun + defense-in-depth (simplest).** Fixed argv baked into the supervisor binary; `readOnlyRootFilesystem: true` injected by the resolver; distroless (no shell to reach `bun` with); `seccompProfile: RuntimeDefault`; optionally an AppArmor/SELinux profile denying exec of `/usr/local/bin/bun` by anything but `/pokkum/init`. Argument for parity: an attacker needs an exec primitive to abuse on-disk bun, and an attacker with in-process JS execution already owns a full JS runtime in both models. Given §5.1, this is defensible as ≥ v0.1 in practice.
- **Option C — Supervisor startup attestation (add-on).** At build time, bake a SHA-256 manifest of every `/app` and runtime file into the `/pokkum/init` layer; the supervisor verifies it before exec'ing the app and refuses to start on mismatch. Restores the sealed artifact's tamper-evidence property *without* depending on cluster-level readonly-fs policy, and covers option (a) static files too. Cheap (~100 LOC in the supervisor), no runtime overhead after startup.
- **Option D — Custom-built/stripped bun (rejected).** Compiling bun from source with `install`/`x`/`repl`/`exec` disabled would genuinely shrink the surface below v0.1, but means owning a Zig toolchain build of a fast-moving runtime, losing upstream binary provenance, and a large maintenance tax. Not worth it while Option A achieves parity for free.

**Recommended composition: A + C, with B's `readOnlyRootFilesystem` and fixed-argv items adopted unconditionally** (they benefit the exe path too). This lands at parity-or-better vs. v0.1 while keeping every caching benefit.

## 6. Trade-Offs Summary

Gains:

- **Layer caching:** per-commit delta drops from ~180 MB (2 arches) to single-digit MB; registry dedup and node pull cache both work.
- **Arch-independent app layers:** one JS build instead of two compiles; adding a future arch costs only a runtime layer.
- **Faster builds:** `bun build --compile` × 2 disappears; bundling is seconds.
- **Better SBOMs** (real dependency graph visible).
- **Unlocks native `.node` addons** (§7) — currently a hard error in `StrictNativeAdapter`.
- **Removes the upstream-adapter dependency** and its bug workarounds (`assets.generated.ts` sorting).

Costs:

- **You own a SvelteKit adapter now.** Adapter API + `Bun.serve` correctness (streaming, websockets?, `read()` from `$app/server`, prerendered edge cases) is real ongoing maintenance against two moving targets (SvelteKit, Bun).
- **Slightly larger total image** (~equal actually: bun runtime ≈ compiled exe size; plus assets no longer deduped by compile-time embedding — marginal).
- **Cold start** gains JS parse/transpile time (hundreds of ms typically). Mitigations: `--minify`, optional `--bytecode` (at the cost of per-arch server layers).
- **Larger determinism surface:** multi-file tree layers and bundler output hashing vs. one binary today.
- **Security model shifts** from "sealed binary" to "runtime + readonly files" (see §5) — defensible, but must be documented.
- Two build strategies coexist during migration (suggest `--strategy=exe|layered`, exe default until parity, then flip and deprecate).

## 7. Native Modules Side-Benefit

The `ClosuredNativeAdapter` groundwork (ELF `.node` parsing, `DT_NEEDED` closure, `LD_LIBRARY_PATH=/app/lib`) fits this design far better than the exe path: native addons and their `.so` closure become **one more per-arch layer** between Layers 2 and 3, resolved per-platform from `optionalDependencies`. This turns today's `ErrNativeModulesUnsupported` hard-stop into a supported path.

## 8. Most Likely Hurdles (ranked)

1. **Adapter correctness long-tail.** Prerender fallbacks, trailing-slash handling, `Bun.serve` vs Node `http` semantics in SvelteKit's `Server.respond()`, request body streaming, client IP/proto headers behind ingress. Mitigation: crib the test matrix from `adapter-node`/`svelte-adapter-bun`; run SvelteKit's adapter test app in `tests/integration`.
2. **Deterministic directory-tree layers.** Sorted walks and pinned headers are solved in `packager`, but trees add: dir entries, empty dirs, symlinks, and — sneaky — Vite asset hashing being deterministic only if inputs are (it is, given pinned `kit.version.name`; verify with the existing `reproducibility_e2e_test.go` doubled-build check).
3. **Vendor-chunk stability (Phase 2).** Bun's `--splitting` chunk naming/hashing isn't contractually stable across Bun versions, and app↔vendor boundary churn silently invalidates the "stable" layer. Mitigation: treat vendor split as an optimization, assert in CI that a deps-only change doesn't touch Layer 4+5 digests, and fail loudly like the current `assets.generated.ts` guard does.
4. **Bun runtime pinning UX.** Whose Bun version wins — host PATH (build) vs pinned download (runtime)? They must match or be explicitly decoupled; recommend: pinned download is authoritative for both bundling and the runtime layer, host Bun only bootstraps `bun install`/`bun run build`.
5. **`--bytecode`/startup vs arch-independence tension.** Pick a default (no bytecode) and document the trade.
6. **AVX2 baseline variant matrix** doubles the x64 runtime-layer cache space and needs a flag.
7. **Migration/back-compat:** keeping the exe path alive behind a flag doubles the e2e matrix for a while; time-box it.
8. **gzip stability across Go versions** (existing known limit) now applies to more layers — unchanged in nature, larger in blast radius.

## 9. Implementation Milestones

- **M1 — Packager + runtime plumbing (Go only):** `bunruntime` resolver with pinned digests; N-layer + directory-tree support in `packager`; blob cache; entrypoint change; `nativeinspect` retarget. Spike the Option-A stub launcher (runtime `import()` of external files from a compiled exe). Exe path untouched.
- **M2 — Hand-rolled adapter (TS) + Phase-1 layering:** adapter emitting `build/{client,prerendered,server}`; single app-JS layer; injector swap behind `--strategy=layered`; port the reproducibility e2e (double-build digest equality) and runtime-contract tests.
- **M3 — Vendor split + native closure:** `--splitting` vendor layer with CI digest-stability guard; `ClosuredNativeAdapter` layer injection for `.node` addons.
- **M4 — Hardening + cutover:** `readOnlyRootFilesystem` default in resolver, SLSA materials extension (bun digest, adapter version), docs (README §Base Images, Runtime Contract, ARCHITECTURE §2/§3), flip default strategy, deprecate exe path.

## 10. Optimization Catalog (applies to exe path, layered path, or both)

### Image size

- **`--minify` + `--sourcemap=none` on the JS bundle** (both; exe path embeds the JS, so it shrinks the binary too). Cheap, do always.
- **Base image `base` vs `cc` variant** (both): ~2 MB saving, already deliberately rejected for correctness — keep `cc`.
- **musl targets** (`bun-linux-x64-musl`/`arm64-musl`, both paths): enables smaller musl bases (Wolfi/Alpine-family). Saves maybe 10–20 MB of base but swaps the entire glibc compat story `nativeinspect` is built around, and musl performance is historically worse for JS runtimes. Not recommended for the default; possible `--base` expert option.
- **Precompressed static assets** (`.br`/`.gz` emitted at build time, layered path only): shrinks the asset layer *and* saves runtime CPU. Compression must be deterministic (pin encoder + level). Recommended.
- **`.pokkumignore` discipline** (both): biggest wins are usually unminified duplicate assets and source maps accidentally shipped.
- **UPX-packing the binary/runtime** (both): rejected — breaks memory page sharing (each replica pays full unpacked RSS), trips security scanners, and adds a non-reproducibility risk surface for marginal transfer savings that layer caching already addresses.
- **Bytecode note**: `--bytecode` *increases* size (roughly 2× for the JS portion) — it's a startup lever, not a size lever.

### Transfer & cold-start on Kubernetes

- **zstd layer compression** (both): OCI supports zstd media types; containerd ≥ 1.5 decompresses zstd substantially faster than gzip and ratios are better. Worth a `--compression=gzip|zstd` flag (gzip default for registry/runtime compat; go-containerregistry has zstd support). Bonus: sidesteps the Go `compress/flate` digest-stability caveat for zstd layers.
- **eStargz / lazy pulling** (both): seekable layers let containerd's stargz-snapshotter start containers before the full pull completes. Real cold-start win for 100 MB-class images, but requires node-level snapshotter config — out of scope for Pokkum core; document as compatible.
- **Layer-count balance** (layered): each layer adds manifest/mount overhead; 5–6 layers is fine, don't go finer-grained (per-route layers etc.) without evidence.
- **Arch-independent layers** (layered only): the amd64 and arm64 manifests reference the *same* JS/asset blobs — halves registry storage and warm-cache pulls on mixed-arch clusters. The single biggest transfer win after caching itself.

### Build time

- **Parallelize per-platform work** (exe path especially): the two `bun build --compile` runs are independent; so are per-arch layer assembly and blob uploads. Likely the cheapest big win for v0.1 as-is.
- **Skip-if-unchanged fast path** (both): with reproducible digests, Pokkum can compute the expected manifest and HEAD the registry before doing any work — a no-op build becomes seconds. Natural companion to `pokkum resolve` in GitOps loops.
- **Base-image layer cache + cross-repo blob mounts** (both): cache resolved base layers locally keyed by digest; when pushing to the same registry host, mount base blobs instead of re-uploading.
- **Local blob cache keyed by diffID** (layered, §4.4): skips re-gzip/re-hash of unchanged layers.
- **Vite/SvelteKit incremental cache** (both): keeping `.svelte-kit`/Vite caches across CI runs speeds `bun run build`, but caches must never leak into layer inputs — safe because layers are built from adapter output only.

### Runtime startup

- **`--bytecode`** (both paths; requires `format: "cjs"`, `target: "bun"`): Bun's biggest cold-start lever (skips parse). Exe path: free to enable. Layered path: makes the server layer Bun-version-coupled and per-arch-agnostic-but-version-fragile — acceptable if the stub launcher (Option A) already pins the Bun version, since they invalidate together. Offer `--bytecode` as opt-in.
- **Compiled exe / stub launcher inherently skips transpile** of the entry (both) — already have this on the exe path; Option A retains it for the stub itself.
- **Lazy route loading** (layered): `--splitting` means route chunks parse on first hit instead of at boot — mild startup win that comes free with the Phase-2 vendor split.

### Runtime memory & throughput

- **`bun --smol`** (both): trades throughput for significantly lower heap — worth exposing as a runtime env/flag for memory-constrained pods, default off.
- **`Bun.serve` vs `node:http`** (layered, adapter choice §4.1): the hand-rolled adapter's main quantifiable perf argument over adapter-node-under-Bun; benchmark before hand-rolling.
- **Precompressed assets** (layered): zero-CPU static serving; pairs with the size item above.
- **`NODE_ENV=production` baked into image env** (both): trivial, ensure it's set — framework dev-mode branches are a classic silent throughput killer.

## 11. Open Questions

- Publish `@pokkum/adapter` to npm, or vendor it and inject via the virtual-config mechanism (`.pokkum/svelte.config.js`) from an embedded copy? Embedded copy avoids a registry dependency and version skew — preferred, but bloats the Go binary slightly.
- WebSocket support in the adapter (Bun.serve makes this easy; SvelteKit has no first-class story) — in or out of scope?
- Keep `--tarball`/`--local` semantics identical? (Yes — nothing in this design touches them.)
- Does the supervisor grow a "verify argv target exists and is regular file owned by root" preflight? Cheap, worth it.
