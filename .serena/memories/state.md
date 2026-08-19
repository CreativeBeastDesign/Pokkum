# Pokkum State — Current Shipped Reality

Not roadmap/feature status — that lives in `Roadmap.md` / `Roadmap-v1-Archive.md` /
`docs/roadmap/*.yaml` (a parallel effort generating `docs/Roadmap.md`,
`docs/Shipped.md`, `docs/Features.md` — the intended single source for
roadmap/feature status once built). This memory is implementation reality an
agent needs while coding: which mechanism is wired, which path fails closed,
which invariant actually holds today. Update **in place** per subsystem —
don't append prose; each bullet should stay independently editable. Every
claim below names the file/symbol/commit that proves it — verify against code
before trusting a claim that predates the commit it cites.

## Signing (`--sign`, default true)
- Real end-to-end since 2026-08-18: SLSA v1.0 statement, DSSE-signed, Cosign-signs
  the digest, dual-published to the index AND every per-platform manifest
  (`ports.Registry.AttachSignature`/`AttachAttestation`). `core.signAndSelfVerify`
  fetches the material back and re-verifies before reporting success.
- Only actually signs with a key configured (`--signing-key`/`POKKUM_SIGNING_KEY`).
  No key → unsigned push + loud warning, recorded honestly in `BuildResult.Signing`.
  `--require-signed` turns a missing/failed key into a hard build failure.
- No placeholder/fallback trust anchor anywhere (deleted 2026-08-18, `a149b28`).
  All 3 verification sites (base-image, remote-cache, provenance) fail closed,
  naming the exact env var/flag to set.
- `cosign verify-attestation` now accepts Pokkum's attestations (fixed `e918c52`):
  the attestation layer carries `dev.cosignproject.cosign/signature: ""`, matching
  cosign's own convention. Verified against real cosign v3.1.3 for both the
  manifest digest and the index digest.
- Static-key signing only — no keyless (Fulcio/OIDC) path for *signing*. Keyless
  Sigstore exists only on the verification side (base images, `pokkum verify`).

## Base-image trust & verification
- Stock presets (`distroless`/`chainguard`/`distroless-node`) use keyless Sigstore
  (Fulcio+Rekor) by default; custom bases use static-key Cosign (`--base-verify-mode`).
- `--base` now accepts a free-form image reference, not just a preset name (fixed
  `69914ac`). Presets are tried first; only a value containing `/`, `.`, `:`, or
  `@` is parsed as a reference — order is load-bearing (`name.ParseReference`
  accepts a typo'd preset as Docker Hub shorthand, e.g. `distrolss` →
  `docker.io/library/distrolss:latest`).
- **Lock-slot gap, narrowly patched, not resolved** (`internal/adapters/baseimage/resolver.go`):
  every custom `--base` ref shares one `pokkum.lock` key (`lockKey = string(req.Preset)`
  = `"custom"`). `69914ac` added a guard so a `"custom"`-keyed entry is only
  trusted when its recorded `Ref`/`Digest` matches the current request — this
  closes the silent-wrong-image bug, but a second custom ref still evicts the
  first from the shared slot. Proper fix (a per-ref slot) needs a lockfile
  migration — see `mem:open_decisions` row 1.
- Escrow-mirror pulls (`--mirror-registry`) are digest-pinned against
  `pokkum.lock`'s `entry.Digest` (fixed `a149b28`) — a mirror tag retargeted to
  different content now fails closed (`core.ErrBaseSignatureInvalid`).
- Sigstore trust root: the embedded snapshot
  (`internal/adapters/sigstore/trusted-root-public-good.json`) was regenerated
  from a TUF-signature-verified fetch (fixed `9188d56`) — it had been actively
  rejecting valid signatures on the `log2025-1` Rekor shard, not merely stale.
- **`--sigstore-tuf-refresh` is now wired on `pokkum build` and `pokkum verify`**
  (fixed `eeaa83a` — supersedes any earlier note that it had no CLI surface).
  `Offline` is bound to `--hermetic` on build (zero network egress when hermetic,
  verified by mutation testing); `verify` has no hermetic concept, always allows
  the attempt, and falls back to the embedded snapshot with a warning on
  failure. `pokkum base update`/`base check` never set `VerifySignature`, so the
  flag is deliberately absent there — nothing to feed. An explicit
  `--sigstore-trusted-root` always wins, skipping the refresh branch entirely.
- The TUF divergence guard now runs **nightly in CI** (fixed `a86baa3`,
  `-count=1` since Go's test cache can't see a live-repo change) — previously
  network-gated + `-short`-skipped, and CI always runs `-short`, so it had never
  actually executed once.
- `TrustedRootPath` inconsistency, still open: `internal/ports/baseimage.go`'s
  `TrustedRootPath string` (a file path, read via `os.ReadFile` in
  `internal/adapters/baseimage/resolver.go`) is the odd one out — both
  `internal/adapters/sigstore.Verifier` (`trustedRootJSON []byte`) and the TUF
  refresh option consume bytes. Not bridged. See `mem:open_decisions` row 2.

## Static strategy (`--strategy=static`)
- Genuinely functional end-to-end as of the 2026-08-19 fixture-driven batch —
  see `mem:staticserver` for the full deep dive (bind-address bug, `Preflight`
  strategy-awareness, prerendered flattening, `.html` candidate resolution,
  single-port rejection, conditional GET/304 via `If-None-Match`).
- Embedded `pokkum-static`/`pokkum-init` blobs are gitignored build artifacts
  (`.gitignore`); CI and `.goreleaser.yaml` now build both
  (`make supervisor static-server`) before every release/CI run, and
  `make check-embedded-blobs` (wired into `make verify`) rebuilds-and-byte-compares
  to catch local staleness that CI's from-scratch build can't hit.
- VCS stamping (Go's `-buildvcs` default) was churning both binaries' content on
  every commit even with byte-identical source — fixed with `-buildvcs=false` on
  both Makefile targets and the freshness guard's own build (`81a6fb6`). The main
  CLI build deliberately still stamps VCS info — it wants version reporting.

## Runtime dimension (`--runtime=bun|node`, default bun)
- `node` restricted to `--strategy=layered`; targets the `distroless-node`
  preset (its own `pokkum.lock` slot); ships **no Bun layer**, execs
  `/nodejs/bin/node` directly against `adapter-node` output.
- Real Docker boot smoke test now exists
  (`TestRuntimeSmoke_NodeRuntime_BootsAndServes`, fixed `e918c52`).
- Still-open gaps, unchanged: `--telemetry` rejected outright for node
  (`internal/core/model.go:1139`, no Bun `--preload` equivalent); Node-core CVEs
  unqueryable (distroless ships Node outside `dpkg`); `pokkum dev`/`resolve`/`apply`/
  standalone `pokkum scan` have zero runtime awareness (verified: no `RuntimeNode`
  reference anywhere in `cmd/pokkum/dev.go`, `k8s.go`, or `scan.go`). See
  `mem:open_decisions` rows 3/4.

## `pokkum dev` (local iteration)
- `--no-container` (new, `18f056c`, Roadmap Tier 1.2 item 6a): skips image build
  and Docker entirely, runs the project's own `bun run dev`. Deliberately does
  NOT reuse the watch/rebuild loop (Vite/SvelteKit already own HMR). No
  supervisor, no startup attestation, no probes, no base image, no non-root
  user — a single Warn at startup states this is not representative of
  production. `--debug`/`--platform`/`--bun-version`/`--bun-variant` rejected
  outright (they describe an image never built); `--port`/`--watch` warn only if
  explicitly set; `--bun-binary`/`--env-file` still apply, the latter parsed
  locally instead of handed to a daemon.
- Container-mode watch/rebuild loop (the default) uses a fresh result channel
  per container generation (fixed `1f8e5bf`) — previously reused one buffered
  channel across generations, so a stale write from a just-killed container
  could be misread as the new generation's result.

## Supervisor & static-server binaries
- Both `pokkum-init` and `pokkum-static` are zstd-compressed embeds
  (`go:embed all:bin`), decompressed lazily via `sync.OnceValue`. Reproducibility
  guarded by `make check-embedded-blobs` (see Static strategy above — the same
  guard covers both binaries).
- Startup attestation (`POKKUM_ATTESTATION_DIGEST`, `attestutils`) only exists
  for `--strategy=layered`; `--strategy=exe` and `--strategy=static` don't attest.

## Caching
- A composite remote-cache hit (`remotecacheutils`) skips
  `VerifyBaseImage`/native inspection by design — the base digest is already
  bound into the cache key, so a hit can only match the exact base a full build
  would have used.
- **Real gap, still open**: the cache-verify key chain
  (`--cache-verify-key`/`POKKUM_CACHE_PUBKEY`/`POKKUM_SIGNING_PUBKEY`/`POKKUM_BASE_IMAGE_PUBKEY`)
  never reads `req.Signing.PublicKeyPEM` — `internal/core/pipeline.go`'s
  `RemoteCache.Check` call doesn't populate it. A build signed via
  `--signing-key` alone doesn't automatically make its own cache entries
  verifiable; the practical outcome is fail-safe (falls through to a full
  rebuild) rather than fail-fast-with-a-clear-story. See `mem:open_decisions`
  row 7.
- Layer cache (`layercacheutils`) key dropped its `modTime` parameter entirely
  (`1675d4c`) — immutable-binary layers (Bun, supervisor, static-server) use a
  fixed epoch constant, not `SOURCE_DATE_EPOCH`, so their digests don't churn
  per-commit the way genuinely source-derived layers correctly do.

## Secret scanning
- `secretguard` (`deps.SecretGuard`, invoked by `internal/core/pipeline.go`'s
  `runSecretScan`) scans build **output** directories, wired whenever
  `deps.SecretGuard != nil` — not gated by strategy in the pipeline itself.
- Open question, not yet resolved: whether `--strategy=exe`'s single compiled
  binary output gets equivalent coverage to layered/static's directory scan.
  See `mem:open_decisions` row 5.

## Test surface
- `make verify`'s 5 steps cover `./internal/...` + a `cmd/pokkum` build only.
  `./supervisor/...` (`pokkum-init`, `pokkum-static`) needs its own
  `go build`/`go test` — not covered by any of the 5 steps.
- `tests/integration/golden_test.go` (OCI manifest/config/index goldens) and
  `tests/integration/runtime_smoke_test.go` (real Docker boot: layered, static,
  and node variants) are outside `make verify`'s scope — run explicitly for any
  change touching layer compression, tar construction, or OCI assembly.
- Real-build tests copy their fixture into `t.TempDir()` before building and
  symlink `node_modules` (`20ba1ec`), so they no longer mutate checked-in
  `testdata/fixtures/*` in place. Order-independence proven at
  `-count=3 -shuffle=on` incl. the real-bun tests; `git status --porcelain
  testdata/` is clean after a run. Read-only tests were left un-isolated
  deliberately — `sbom`/`packager`/`nativeinspect` contain no `os.WriteFile`
  or `MkdirAll` at all. Copy helper: `tests/integration/harness_test.go`, with a
  justified small duplicate in `internal/adapters/bunexec/integration_test.go`
  (separate package; an adapter→adapter import is banned). Generated
  `testdata/fixtures/*/.pokkum/` and `*/pokkum.lock` are gitignored as
  belt-and-suspenders.
- The project's real-boot/real-compile empirical tests
  (`TestRuntimeSmoke_*` ×3, `TestRealBuild_AssetOverlay_TwoGenerations`,
  `TestLayeredTelemetryBootstrap_RealPreloadRun`) are the test class that has
  caught nearly every severe bug logged in `Lessons.md`. A new non-trivial
  feature should add one of these, not just unit tests — see
  `mem:self_review_checklist` row 17.

## Telemetry (`--telemetry`) — folded in from the deleted standalone `telemetry` memory
- Real for both `--strategy=exe` and `--strategy=layered` since 2026-08-18, via
  two different mechanisms (compile-entrypoint wrapper for exe; a packaged
  `bun --preload`'d file for layered) — neither touches `svelte.config.js` or
  SvelteKit's native `kit.experimental.tracing` (on-disk-config-only, no
  env/CLI override; would violate the Zero-Mutation Build Sandbox invariant).
- `internal/adapters/sveltekitutils/telemetry.go`: `PrepareVirtualTelemetryEntry`
  (exe path) and `PrepareLayeredTelemetryBootstrap` (layered path — writes
  `otel-bootstrap.ts` into `OutputDir`, threaded via
  `PrepareResult.TelemetryPreloadRelPath`).
- `internal/adapters/sveltekitutils/injector.go`'s `EnableTelemetry`/
  `injectExperimentalFlags`/`PrepareVirtualConfig` are DEAD CODE — not part of
  the real mechanism, do not assume they do anything.
- Two real, confirmed-by-actually-running limitations:
  `@opentelemetry/auto-instrumentations-node` produces zero spans under Bun's
  runtime (real spans need a user-added `hooks.server.ts` snippet, documented in
  `Vocabulary.md` §3a, never auto-injected); an OTLP metrics exporter + `NodeSDK`
  crashes once compiled via `bun build --compile` (`--metrics-only` is
  consequently non-functional and warns at runtime instead of silently doing
  nothing).
- Rejected outright for `--runtime=node` — see Runtime dimension above.

## Rolling-deploy asset overlay (`--asset-overlay`)
- Shipped 2026-08-18. Lineage discovery is registry-side via a
  `pokkum.dev/predecessor` manifest annotation, walked backward by
  `internal/adapters/assetoverlay.ResolvePredecessorChain` — independent of
  Kubernetes' `pokkum.dev/image-history` (that annotation is `resolve`/`apply`/
  `rollback`-only and unreachable from `build`).
- Hardened by an adversarial review the same day: attestation-digest dedup
  (`dedupeAttestRecordsByRel`), a path-traversal fix in `extractImmutableAssets`,
  a cross-repo `ResolveDigest` fix, and two availability caps. See `Lessons.md`'s
  2026-08-18 "Adversarial review" entry.
- **Known, still-open gap**: `pokkum verify`'s default rebuild-and-compare path
  is not `--asset-overlay`-aware — verifying an asset-overlay image reports a
  false-positive digest mismatch. See `mem:open_decisions` row 6.

## Hermetic mode (`--hermetic`)
- Network isolation (`CLONE_NEWNET`+`CLONE_NEWUSER`) covers both
  `bunexec.Compiler.Prepare` AND `Compile` (fixed after an adversarial review
  found `Compile` was unsandboxed — a full bypass).
- `docker.sock` masking (`--hermetic-mount-isolation`, opt-in, off by default)
  re-execs into a hidden `pokkum __hermetic-reexec` subcommand and bind-mounts
  an empty file over `/var/run/docker.sock`/`/run/docker.sock`.
- **Honest residual gap**: the sandboxed process retains `CAP_SYS_ADMIN` in its
  own namespace (the capability that created the mount mask), so a
  sufficiently sophisticated dependency aware of this mechanism could in
  principle `umount()` it. Closing this needs `capset(2)` to drop the
  capability before the final exec — tracked as a Roadmap follow-up, not
  attempted. See `mem:open_decisions` row 8.
