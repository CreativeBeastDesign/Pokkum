# Pokkum State — Current Shipped Reality

Not roadmap/feature status — that lives in `docs/roadmap/*.yaml`, the single
source generating `docs/Roadmap.md`, `docs/Shipped.md`, `docs/Features.md` and
`docs/items/*.md`. This memory is implementation reality an
agent needs while coding: which mechanism is wired, which path fails closed,
which invariant actually holds today. Update **in place** per subsystem —
don't append prose; each bullet should stay independently editable. Every
claim below names the file/symbol/commit that proves it — verify against code
before trusting a claim that predates the commit it cites.

## Reproducibility (vite-config injection, SvelteKit 3) — 2026-08-22
- **Headline fix**: two builds of identical committed source used to produce
  different image digests. Root cause was upstream, not Pokkum's own code:
  SvelteKit builds its `remotes` array in Vite's module-resolution order and
  `generate_manifest` maps over it without sorting, so manifest.js's bytes
  (and therefore its Rollup chunk hash, and every file that imports that
  chunk) differ across otherwise-identical builds. Fixed by
  `remoteManifestSortPrelude` (`internal/adapters/sveltekitutils/injector.go`):
  prepended to the virtual Vite config Pokkum generates, it monkey-patches
  `fs.writeFileSync` to sort the manifest's remote entries the moment
  SvelteKit writes them — before Rollup ever hashes the chunk, so nothing
  downstream needs re-hashing or renaming. Fails loudly (throws) on an
  unrecognised entry shape rather than writing through unchanged, so a future
  SvelteKit manifest-format change surfaces as a build error naming this file
  instead of reproducibility silently regressing.
- **Two entry points, both required**: `PrepareVirtualViteConfig` (adapter
  needs injecting) and `PrepareVirtualViteConfigPassthrough` (adapter already
  correct in the project's own `vite.config.ts` — the documented happy path,
  and the ONLY possible path on SvelteKit 3, which has no `svelte.config.js`
  at all). The prelude alone would have reached only the injection path,
  i.e. projects whose configuration was wrong — close to the opposite of the
  intended audience — so Passthrough exists specifically to also cover the
  already-correct case. Both paths also pin `kit.version.name` to
  `SOURCE_DATE_EPOCH` (`injectViteVersionPin` for injection; a bare call is
  deliberately left unpinned when a `svelte.config.js` exists, since
  `sveltekit({ version: ... })` would make SvelteKit ignore that file
  entirely — see the injection path's own doc comment; `pinViteConfigVersion`
  for passthrough, added `80b4508`) — without it, SvelteKit falls back to
  `Date.now()` for `_app/version.json` and reproducibility breaks a second,
  independent way even with the manifest sorted. `pinViteConfigVersion`
  returns whether it actually could pin; the passthrough caller logs a
  warning (not a hard failure) on the cases it deliberately declines — a bare
  `sveltekit()` call shadowed by an existing `svelte.config.js`, or a call
  shape it can't safely parse — naming the reproducibility gap instead of
  silently leaving it.
- **Verified end to end, both majors, two builds each from an identical
  starting state** (commit `f7e8b7d`): SvelteKit 2.68 (adapter injected) and
  SvelteKit 3.0.0-next.25 (passthrough) both produced byte-identical image
  digests across the two runs.
- **SvelteKit 3 support**: every SK3 build previously failed unconditionally
  at the handler patch step ("no recognizable prerendered or client path
  pattern") — Vite 8 bundles the SSR output with Rolldown, which splits
  `export { h as handler } from './x.js'` into a separate import and export.
  `internal/adapters/bunexec/prerendered_patch.go` now also recognises the
  split form, gated on a local re-export so a file that merely imports a
  handler to use it isn't mistaken for a barrel. Fixture:
  `testdata/fixtures/sveltekit-kit3` (kit@3.0.0-next.25,
  adapter-node@6.0.0-next.10, vite@8.2.2); first fixture to exercise the
  Passthrough path with a real build assertion
  (`tests/integration/sveltekit3_e2e_test.go`), and it also exercises
  `kit.experimental.remoteFunctions: true` end to end (three `.remote.ts`
  files surviving real Vite/Rollup bundling into `app/server/`).
- **Fixed `ca23684`**: the remote-functions reproducibility warning
  (`internal/adapters/bunexec/compiler.go`, now `warnIfRemoteOrderingUnfixed`,
  was `warnIfRemoteFunctionsBreakReproducibility`) used to fire
  unconditionally whenever remote functions were enabled — including on
  builds the prelude above already made byte-identical, falsely telling
  operators their reproducible build was not reproducible. It now fires only
  when the sort prelude could NOT reach the build (no live `sveltekit()` call
  in the project's Vite config, or a build script that isn't exactly
  `vite build`, so Pokkum can't safely take over the invocation) — evaluated
  from `runViteWrapper` after that decision is final.

## SBOM
- Catalogues base-image OS packages (`pkg:deb`/`pkg:apk` purls, Debian/dpkg or
  Alpine/apk via `scannerutils.ExtractImagePackages`) alongside npm packages
  whenever `ports.SBOMRequest.BaseImages` is supplied — routed through the
  single `GenerateForImage` path (`internal/adapters/sbom/generator.go`), so
  omitting the base image's OS surface is no longer the silent default (an
  SBOM missing `libc6`/`libssl3` used to look identical to a complete one).
- npm catalogue is production-scoped by default: a package reachable only via
  devDependencies is excluded via a `bun.lock` reachability walk
  (`scannerutils.ScopeDevelopment`); a package whose scope couldn't be
  determined (`ScopeUnknown`) is kept, never guessed at. Both the excluded
  count and the scope policy are always recorded in document metadata, not
  only when non-zero.
- Signed as a DSSE-enveloped in-toto Statement bound to the image digest
  (`signSBOMStatement`, `internal/core/pipeline.go`), attached as the SECOND
  layer of the `.att` attachment via
  `AttachAttestationRequest.AdditionalEnvelopes`. Layer 0 is a contract, not
  an accident: `FetchAttestation` reads `layers[0]`, and the post-push
  self-verification stage verifies exactly that envelope — so the SLSA
  provenance statement always goes there, and the SBOM statement is always
  appended after it.

## Multi-platform index
- Each per-platform descriptor's `platform` field is now derived from the
  child image's own config file (`descriptorPlatform`,
  `internal/adapters/packager/packager.go`), including `variant` — not just
  copied from the requested `ports.Platform` — so a config/request mismatch
  (including a missing or wrong ARM variant) fails the build instead of
  publishing an index descriptor that misdescribes the child image it points
  at.

## PaaS deploy targets (`deploy:`, `pokkum deploy`) — shipped 2026-09-01

- `internal/ports/deploy.go` (`Deployer`, `DeployRequest`, `DeployResult`,
  `DeployTarget`, `DeployMethod`), `internal/core/deploy.go` (policy: parse,
  validate, resolve, `ShouldAutoDeploy`, `Deploy`), `internal/adapters/deploy`
  (both platforms in ONE package — adapter→adapter imports are forbidden and
  they share the whole HTTP contract), `cmd/pokkum/deploy.go`.
- Runs AFTER the reproducible build. This is the one adapter deliberately
  exempt from the determinism/zero-clock rules: it is a side effect against a
  live system, invoked post-push, never inside `core.Build`.
- **The hard-won fact: both platforms return HTTP 200 for outcomes that are not
  deployments.** A status-code check reports a deploy that changed nothing as a
  success. Every response is classified on its BODY.
  - SwiftWave webhook (`ANY /webhook/redeploy-app/:app-id/:webhook-token`,
    read from `swiftwave_service/rest/webhook.go`): for an image-sourced
    deployment it reduces its configured image to `owner/name` (tag stripped,
    then last two path segments) and rebuilds ONLY if the request body contains
    that substring; otherwise `200 OK - No rebuild`. So the adapter posts the
    pushed refs as the body, as `text/plain` with no percent escapes — the
    handler `url.QueryUnescape`s the body and continues with the EMPTY STRING
    on failure, which silently becomes "No rebuild" — and maps that string to
    `core.ErrDeployNotTriggered`.
  - SwiftWave GraphQL (`POST /graphql`, `Authorization: Bearer <jwt>`):
    `rebuildApplication(id:)`. GraphQL answers 200 for application errors too.
  - Dokploy (`x-api-key`): `POST /api/application.deploy` `{applicationId}`;
    `POST /api/application.saveDockerProvider`
    `{applicationId, dockerImage, username, password, registryUrl}`.
- **Dokploy's `saveDockerProvider` is a FULL OVERWRITE, not a patch** (read
  from `apps/dokploy/server/api/routers/application.ts`): the handler writes
  `dockerImage`/`username`/`password`/`registryUrl` from the request every
  time, and `apiSaveDockerProvider` is `.required()` on all five picked fields.
  Omitting credentials is a validation error; sending nulls CLEARS the app's
  pull credentials. Hence `deploy.update_image` defaults **off**, the payload
  struct uses `*string` WITHOUT `omitempty` (keys present, values nullable),
  and clearing is warned about and reported in `DeployResult.Detail`.
- **SwiftWave cannot be repointed at a new image.** Both its paths rebuild the
  current deployment; changing the image needs a full
  `updateApplication(input: ApplicationInput!)` resupplying every field of a
  running service. `core.SupportsImageUpdate` is dokploy+api only, and
  `update_image` is REJECTED elsewhere rather than silently ignored. SwiftWave
  apps must be pinned to a mutable tag Pokkum republishes.
- No credential is ever stored in `.pokkum.yaml` — only env var NAMES
  (`token_env`, `endpoint_env`, `registry_username_env`,
  `registry_password_env`). Default token env `POKKUM_DEPLOY_TOKEN` (carries a
  `//nolint:gosec` for G101; it is a variable name). A named-but-empty variable
  FAILS rather than degrading to anonymous. A half-configured
  username/password pair is refused.
- Auto-deploy vetoes, all in `core.ShouldAutoDeploy`: `--no-deploy`,
  `--dry-run`, `--print-manifest`, no target, and any output mode other than
  `OutputPush` (local/tarball/oci-layout leave nothing remote to pull; the skip
  logs a warning naming the mode, so "configured but skipped" ≠ "not
  configured").
- `core.Deploy` is the backstop: a `DeployResult` with `Triggered == false` can
  never be returned with a nil error.
- Config plumbing touches FOUR places for any new `DeployConfig` field —
  `ports/config.go`, `config.ApplyProfile`, `deepCopyProjectConfig`, and the
  four `validateConfigFields` call sites in `cmd/pokkum/config.go`.
  `cmd/pokkum/deploy_config_test.go` walks `DeployConfig` by REFLECTION and
  fails naming any field `ApplyProfile` forgot.
- `cmd/pokkum/build.go`'s `resolveProjectConfig` is shared by the build request
  and the deploy step on purpose: two copies of the profile-resolution rules
  would let a deploy target a different environment than the build.
- Vercel/Netlify/edge remain out of scope — they do not run OCI images
  (existing non-goal in `README.md`).

## Output modes
- `--to-oci-layout <dir>` (new, `ports.OCILayoutWriter`) writes a lossless
  OCI image layout directory to disk. Unlike `--tarball` (docker-save
  format), which has no annotations field and flattens a multi-platform index
  into a single manifest, `--to-oci-layout` keeps both. Mutually exclusive
  with `--local` and `--tarball`. Like the other two non-registry output
  modes, `--asset-overlay`'s registry-side lineage-discovery annotation is
  unreachable from it.

## SLSA provenance & source verification
- `slsa.WorkingTreeDirty` (`internal/adapters/slsa/gitdiscovery.go`) returns
  `(bool, error)`, not a bare `bool`, and now reports dirty when git can't be
  consulted at all (missing binary, not a repo, command failure) — previously
  such cases fell through to `false` ("clean"), a fail-open on exactly the
  signal this function exists to catch.
- `pokkum verify --expect-source repo@commit`: the commit half must be at
  least 7 characters (`minAbbreviatedCommitLen`,
  `internal/adapters/provenance/resolver.go`) — shorter prefixes are too
  ambiguous to assert against real commit hashes — and a clean commit match
  is rejected when the build's own provenance recorded a DIRTY working tree
  (a `"<sha>-dirty"` value is prefix-matched by the clean `"<sha>"`, which
  used to silently accept an image built from uncommitted changes).
- Pokkum's Simple Signing verifier now accepts both payload `type` strings
  real upstream cosign has used (`ports.CosignSimpleSigningType` — Red Hat's,
  and Pokkum's own static-key signer's default — and
  `ports.CosignContainerImageSignatureType`), so a signature produced under
  either convention verifies instead of one type failing closed against a
  correctly-signed artifact.

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
- **Lock slots are per-reference for custom bases (closed 2026-08-19).**
  `internal/adapters/baseimage/resolver.go`'s `lockKeyFor(preset, ref)`: fixed
  presets keep their bare-preset-name key unchanged (re-keying them would orphan
  every existing pin); `BaseImageCustom` gets
  `"custom:" + sha256(normalizeRefForLockKey(ref))[:12]`, so two custom bases in
  one project each hold their own stable pin. Three things to know before
  touching this: (1) the `69914ac` `Ref`/`Digest` match guard is deliberately
  KEPT on top of the new keying (truncated hash + hand-editable JSON file), and a
  guard failure degrades to a cache miss, never to the wrong image;
  (2) `lookupLockedBase` reads the legacy bare `"custom"` slot as a fallback,
  only when its recorded `Ref` matches, then copies it verbatim under the new key
  and deletes it — the legacy key is never written, and a legacy entry belonging
  to a *different* ref is neither trusted nor deleted; (3) anything mapping a
  lockfile slot name back to a preset MUST go through
  `lockfileutils.PresetNameForLockKey` (`cmd/pokkum/base.go`'s `runBaseCheck`
  does) — a raw `core.ParseBaseImagePreset` on the slot name skips every
  `custom:<hash>` entry silently. `ports.BaseImageResolver.RecordScanResult` now
  takes `ref` alongside `preset` for the same reason.
- Escrow-mirror pulls (`--mirror-registry`) are digest-pinned against
  `pokkum.lock`'s `entry.Digest` (fixed `a149b28`) — a mirror tag retargeted to
  different content now fails closed (`core.ErrBaseSignatureInvalid`).
- `--dry-run` no longer writes `pokkum.lock` into the user's project
  (`ports.BaseImageRequest.LockfileReadOnly`, threaded from
  `BuildOptions.DryRun` in `internal/core/pipeline.go`): the resolver still
  READS the lockfile to pin against, but every write path is skipped when set.
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
- **All three Sigstore trust-root consumers take bytes (closed 2026-08-19).**
  `ports.BaseImageRequest.TrustedRootJSON []byte` replaced
  `TrustedRootPath string`; `cmd/pokkum/build.go` reads any
  `--sigstore-trusted-root` file ONCE and feeds both `req.BaseImage` and
  `req.CacheVerify` from the same bytes, so no adapter touches the filesystem for
  it and a TUF-refreshed root can be handed to any consumer without a temp file.
  An unreadable file now fails the command closed (wrapping
  `core.ErrBaseSignatureInvalid`) before the build starts, where it previously
  only failed if keyless verification actually ran. Fixed in the same change: the
  cache-verify consumer had been swallowing the read error (`if err == nil`) and
  degrading to the embedded snapshot while the operator believed their explicit
  root was in force — see `Lessons.md` 2026-08-19 and
  `mem:self_review_checklist` row 41. `verifyKey` now keys on a fingerprint of
  the trusted-root bytes, not the path.

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
- `ports.AttestationRoots` (the fixed directory set the layered startup
  attestation covers, mirrored independently in pokkum-init) now includes
  `AppNodeModulesDirPrefix` (`/app/node_modules`, fixed `b439e6b`) — its
  absence had been hashed by the packager at build time but never walked by
  pokkum-init at runtime, so every layered image shipping node_modules
  bricked at startup (digest mismatch, exit 125). Enforced by an AST-parsing
  test (`TestAttestationRoots_MatchSupervisorMirror`) that compares the two
  literals directly, not just a comment promising they match.

## Caching
- A composite remote-cache hit (`remotecacheutils`) skips
  `VerifyBaseImage`/native inspection by design — the base digest is already
  bound into the cache key, so a hit can only match the exact base a full build
  would have used.
- **Closed 2026-08-19: the cache-verify key chain now ends in the signing
  key's public half.** Chain order is `--cache-verify-key`/`.pokkum.yaml
  cache.pubkey` → `POKKUM_CACHE_PUBKEY` → `POKKUM_SIGNING_PUBKEY` →
  `POKKUM_BASE_IMAGE_PUBKEY` → `ports.RemoteCacheVerifyOptions.SigningPublicKeyPEM`.
  Three things to know before touching it: (1) precedence lives in exactly ONE
  place — `internal/adapters/remotecacheutils`' static-key arm — and
  `internal/core/pipeline.go` only *offers* the derived key via the dedicated
  `SigningPublicKeyPEM` field (populated from `req.Signing.PublicKeyPEM` at the
  `RemoteCache.Check` call). It deliberately does NOT write `PublicKeyPEM`,
  because that would pre-empt the `POKKUM_*_PUBKEY` links the adapter resolves
  after it and silently override an explicit operator choice — and it would
  fork the chain into two drifting copies (row 41). (2) The fallback NARROWS
  trust, never widens it: the static-key arm accepts a candidate only if its
  Simple Signing payload verifies against that one key, and keyless-vs-static
  mode selection keys on `KeylessIdentity` alone, so no key field can flip the
  mode. Without the fallback there is no key at all and every candidate is
  refused. (3) It logs at INFO when it engages, asserted by a test — an
  unannounced implicit trust derivation is the rows 38/41 shape. No signing key
  configured leaves the chain behaving exactly as before. Supersedes
  what had been `mem:open_decisions` row 7, deleted as resolved.
- Layer cache (`layercacheutils`) key dropped its `modTime` parameter entirely
  (`1675d4c`) — immutable-binary layers (Bun, supervisor, static-server) use a
  fixed epoch constant, not `SOURCE_DATE_EPOCH`, so their digests don't churn
  per-commit the way genuinely source-derived layers correctly do.

## Secret scanning
- `secretguard` (`deps.SecretGuard`, invoked by `internal/core/pipeline.go`'s
  `runSecretScan`) scans build **output** directories, wired whenever
  `deps.SecretGuard != nil` — not gated by strategy in the pipeline itself.
- **`--strategy=exe` coverage, resolved 2026-08-19 (partially — read the
  residual gap).** The pipeline already scanned exe's `prep.OutputDir`; the
  actually-uncovered surface was the *compile entrypoint's own directory*,
  which is not always inside `OutputDir` — with `--telemetry`,
  `sveltekitutils.PrepareVirtualTelemetryEntry` rewrites `EntrypointPath` to
  `<projectDir>/.pokkum/telemetry-entry.ts` alongside a generated
  `.pokkum/otel-bootstrap.ts`, both bundled into the shipped binary by
  `bun build --compile`. Neither was scanned at either stage: they are written
  by `Prepare` (so absent at the pre-build scan) AND
  `secretguard.ScanDirectory` hard-skips `.pokkum`/`.svelte-kit`/`node_modules`/
  `.git` subdirectories — but only when `rel != "."`, so those trees ARE
  scannable when handed to it as the scan ROOT, which is what makes this fix
  work at all. `postBuildScanDirs(strategy, outputDir, entrypointPath)` now
  returns both trees for exe, deduped via `dirWithin` so the common
  no-telemetry layout (entrypoint inside `OutputDir`) is still scanned once.
  **Residual gap, deliberately not closed**: a secret injected by the
  `bun build --compile` step itself (a `bunfig.toml` preload plugin, a
  `with { type: "macro" }` import) is in neither tree. Scanning the compiled
  binary's string sections was rejected — non-line-oriented, size-unbounded,
  constants may be transformed/split, so it risks both false negatives and the
  noisy false positives that get a scanner switched off. exe is NOT at parity
  with layered/static. Closes what had been `mem:open_decisions` row 5,
  deleted as resolved.

## Documentation system (generated — read this before editing any status doc)
- **`docs/roadmap/*.yaml` is the single source; the markdown is generated output.
  Never hand-edit `docs/Roadmap.md`, `docs/Shipped.md`, `docs/Features.md`, or
  `docs/items/*.md` — `make docs` overwrites them and prunes orphaned item
  pages.** 6 area files, 87 items.
- `scripts/gen-docs` validates strictly: `KnownFields(true)`, every `impl` path
  must exist on disk, enum fields checked against fixed sets, and every
  `[title](item:<id>)` reference must resolve to a real item id. A bad edit
  fails the build rather than shipping a wrong doc.
- Doc-to-doc links use a depth-agnostic `[title](item:<id>)` scheme **in the
  YAML**, resolved per output directory at render time — the same authored
  string is emitted into both `docs/` and `docs/items/`, so no literal relative
  path is correct in both. Two walker tests over the real generated tree assert
  no dead relative links and no unresolved `item:` refs; they exist because the
  original bug was a *call site* passing the wrong directory, which unit tests
  on the helper passed straight through.
- **Retired 2026-08-19.** The hand-maintained `Roadmap.md`, `Feature-list.md`,
  `AdditionalFeatures.md`, `overnight-findings.md` and the v1-era logs now live
  under `docs/archive/` and are read-only — cite them for provenance, never
  update them. `docs/` is the only authoritative status surface. The generator
  still reads `docs/archive/overnight-findings.md` to validate
  `evidence.findings` numbers, so that file's path is a real code dependency
  (`scripts/gen-docs/render.go`'s `findingsFromPath`), not just a citation.

## Test surface
- Full `go test ./...` is green as of 2026-08-22 (49 packages, 0 failures), and CI is green on a real runner for the first time — all three jobs, with the runtime-smoke step booting produced images and hitting its pass floor rather than skipping. Re-verify before citing: this line has been stale by weeks before (`e4175ed`): 47 packages,
  `tests/integration` 36.6s, `cmd/pokkum` 35.0s.
- **Every package that invokes `authn.DefaultKeychain` must isolate
  `DOCKER_CONFIG` in a `TestMain`**, or it hangs for the full 10-minute timeout
  against a real `docker-credential-*` helper instead of failing. All 8 such
  packages now do (`c360ab5` added the last two). `registryutils` is exempt by
  evidence: it only asserts the returned keychain is non-nil, never resolves.
  A previously unexplained `tests/integration` 600s timeout and an apparently
  order-dependent `TestFixtureDrivenE2E_Static_SPAFallback` failure were both
  consequences of two packages holding cores for ~660s — neither reproduces
  now, so neither was a defect of its own.
- `make verify`'s 5 steps cover `./internal/...` + a `cmd/pokkum` build only.
  `./supervisor/...` (`pokkum-init`, `pokkum-static`) needs its own
  `go build`/`go test` — not covered by any of the 5 steps.
- `tests/integration/golden_test.go` (OCI manifest/config/index goldens) and
  `tests/integration/runtime_smoke_test.go` (real Docker boot: layered, static,
  and node variants) are outside `make verify`'s scope — run explicitly for any
  change touching layer compression, tar construction, or OCI assembly.
- CI's Runtime Smoke Tests step (`.github/workflows/ci.yml`) sets
  `POKKUM_REQUIRE_RUNTIME_SMOKE=1` AND greps its own output for any
  `--- SKIP` line and a minimum `--- PASS: TestRuntimeSmoke` count — a
  silently-skipped smoke test (the exact hole that let commit `b439e6b`'s
  startup-attestation brick ship while `go test ./...` reported all green)
  now fails the CI step instead of passing quietly.
- `POKKUM_REQUIRE_MINIFIED_CORPUS turns an absent real-minified-bundle corpus into a hard failure — but it is NOT set in CI and cannot usefully be: the corpus is `testdata/fixtures/*/build` and `.svelte-kit/output`, gitignored build output that the real-build tests never create, because `copyFixtureProject` builds in `t.TempDir()` so the shared fixture directory is never written to (Lessons.md 2026-08-19). A CI step wiring it was added on the assumption the e2e tests populated those directories, failed correctly, and was reverted. The measurement is therefore a developer-machine check, not a CI-enforced guarantee.
  `TestGenericRule_NoFindingsOnRealMinifiedBuildCorpus`
  (`internal/adapters/secretguard/minified_corpus_test.go`) but, verified
  2026-08-22, is **not actually set anywhere in `.github/workflows/*.yml`** —
  the mechanism exists and works when set locally, but nothing in CI sets it
  or builds the gitignored build-output corpus it requires first. Do not
  describe this gate as CI-enforced; it is currently local-only.
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
- **Closed 2026-08-19** (`5455fb3`): `pokkum verify`'s rebuild-and-compare path
  used to report a false-positive digest mismatch on any `--asset-overlay`
  image. `comparator.CompareImages` now reads `pokkum.dev/asset-overlay-sources`
  off the image, rebuilds the merged overlay through the real `assetoverlay`
  resolver using the remote config's own `Created` timestamp (the value the
  original build stamped into every tar entry, so the only one that reproduces
  the DiffID), and splices it in **only** once the reconstructed DiffID equals
  the image's actual overlay-layer DiffID. Compression is fixed at gzip
  regardless of the original, since a DiffID hashes the uncompressed stream.
  Absent annotation, malformed entry, missing reconstruction support,
  unreachable predecessor, and an annotation with no matching overlay layer are
  all hard errors; a *stripped* annotation still fails, because the overlay
  layer remains and the plain rebuild lacks it (tested, not argued).
  Reconstruction reaches layer building via the new `ports.LayerBuilder`, not an
  adapter→adapter import.
- **Residual gap, narrow**: an image whose only output was `--output=tarball`
  carries no annotations at all (the legacy docker-save format has no
  annotations field), so this path cannot engage for it and the old false
  positive survives in that one mode. `--output=tarball`/`--local` now warn and
  name the dropped keys (`cd5c7f0`).

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

## Route exclusion (`--exclude-route`, `build.exclude_routes`) — shipped 2026-08-22

**Two mechanisms. Build-time is primary; output filtering is the fallback.**

- **Build-time** (`internal/adapters/bunexec/route_mirror.go` +
  `routefilterutils/mirror.go`): stage `.pokkum/routes` as a SYMLINK mirror of
  the routes dir minus the excluded routes, and set `kit.files.routes` to it via
  the injector's `RoutesDir` option. The route is then not a bundle entry point,
  so its code is genuinely absent. Verified: a `/dev` marker appears 3x in a real
  layered image without the flag, 0x with it.
- **Symlinks are mandatory, not a convenience.** Vite resolves a symlinked
  module to its real path, so `../../lib/x` from a mirrored route still
  resolves. A copied tree breaks every escaping relative import. Correspondingly
  `resolve.preserveSymlinks: true` is REFUSED — it produces UNRESOLVED_IMPORT.
- **A partially excluded directory must carry ALL its surviving entries,
  layouts included.** Dropping `admin/+layout.svelte` while keeping
  `/admin/panel` builds fine, serves the page in the ROOT layout, and warns
  about nothing. SvelteKit cannot detect it.
- **No `SVELTE_CONFIG_PATH` exists**, and Kit sets vite-plugin-svelte's
  `configFile: false` unconditionally. The only supported override is passing
  config inline to `sveltekit()`, which needs **Kit >= 2.62.0**. Below that,
  fall back — never edit the user's svelte.config.js.
- Known gap: the mirror filters the route GRAPH, not the filesystem. A kept
  route importing `../dev/shared.js` still bundles that module.

- `ports.RouteFilter` / `internal/adapters/routefilter` over
  `internal/adapters/routefilterutils`. Wired in `cmd/pokkum/build.go`'s Deps
  literal; nil disables the feature.
- Runs in `core.Build` AFTER Prepare's errgroup `Wait()` and BEFORE the packager
  reads the tree. Do not move it inside the errgroup — it is fallible, and a
  fallible call between a dispatch and its `Wait()` is this repo's recurring
  goroutine-leak shape (`mem:self_review_checklist` row 1).
- Deletes via `os.Root` scoped to the prerendered dir, and the `WalkDir`
  callback only COLLECTS matches — nothing is removed inside it. gosec G122
  rejects the naive shape.
- **Scope, stated honestly**: this filters prerendered OUTPUT FILES. The route's
  JS chunks, imports and SBOM entries still ship, and a layered server-rendered
  route cannot be removed by deleting a file — such a pattern is reported as
  unmatched. The build-time filter (`kit.files.routes` mirror) that would remove
  the code is roadmap item `route-exclusion-filter`, still open.
- Dead links and unmatched patterns WARN, never fail: removing the route is the
  point of the flag, and failing would make it unusable for its own use case.

## kit.version.name pin — fixed 2026-08-22

- The pin used to live inside the adapter-injection path, so ONLY misconfigured
  projects built reproducibly. It is now a standalone guard in
  `bunexec.Prepare`, keyed on `!runViteWrapper`.
- Where `package.json`'s build script is exactly `vite build`, a Vite config is
  staged solely to pin the version. Where it does more, Pokkum warns instead of
  taking the build over (that would skip the rest of the script).
- See `Lessons.md` 2026-08-22 for both post-mortems, including the `else if`
  that bound to a different `if` than its indentation showed and never ran.

## Unresolved-import guard — shipped 2026-08-22

- `verifyProductionDependenciesResolvable` in
  `internal/adapters/bunexec/unresolved_imports.go`, called from `Prepare`
  right after `stageProductionDependencies`. Layered strategy only.
- **The externals set is `Object.keys(package.json.dependencies)` — verbatim.**
  adapter-node's rollup `external` is exactly that regex list (5.5.7
  `index.js:76-79`; 6.0.0-next.10 adds `@opentelemetry/api`), and it bundles
  every devDependency. Do NOT scan the built bundle for bare specifiers: that
  is the approach that was written, reverted, and would be reverted again.
  Its real failure was JSDoc comments (`/** @import { X } from 'types' */`),
  not "type imports are indistinguishable".
- Vite does not expose its externals list — `shouldExternalize`'s caches are
  unexported, and the SvelteKit-forced Vite manifest holds only internal chunk
  keys. Checked against vite 8.2.1. Don't re-investigate this.
- Resolution test is `<modules>/<name>/package.json` exists as a file — a bare
  directory is not resolvable, because Node resolves through the manifest.
- **Fixture rule this established**: `@sveltejs/kit` belongs in
  `devDependencies` in every test fixture. A fixture declaring it under
  `dependencies` describes a project that cannot work.
