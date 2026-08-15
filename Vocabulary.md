# Pokkum CLI Vocabulary

A single reference for every flag Pokkum's CLI exposes, the conventions flags follow, and the open backlog items on [Roadmap.md](Roadmap.md). See [fixes-to-v1.md](fixes-to-v1.md) and [for-users.md](for-users.md) for a post-v1.0 audit round that changed some of the behavior documented below.

Pokkum's stated model is "`ko` for SvelteKit" (see [README.md](README.md)), so the vocabulary borrows `ko`'s shape deliberately: a `*_DOCKER_REPO` environment variable as the one required input, `-f`/`--recursive` for manifest commands, `--local`/`--push`-style output-mode flags, `--platform` as the multi-arch surface, and boolean toggles that read as prose (`--bare`, `--insecure-registry` in `ko`; `--hardened`, `--dry-run` here). Where this document says "follows `ko` convention," that is the specific precedent being matched.

---

## 1. Conventions

These are the load-bearing patterns established across `cmd/pokkum/`:

1. **`POKKUM_` prefix for every environment variable**, mirroring `ko`'s `KO_` prefix (`KO_DOCKER_REPO` → `POKKUM_DOCKER_REPO`). Config keys in `.pokkum.yaml` are dotted (`docker.repo`) and map onto `POKKUM_DOCKER_REPO` via Viper's `.`→`_` replacer — a new env var implies a new dotted config key for free, and vice versa. See `internal/adapters/config`.
2. **Precedence: flag > environment variable > config file > default**, enforced per-value by the `config.Loader` (`GetString`/`GetBool`/`GetStringSlice`). Document this precedence for every setting readable from more than one source.
3. **Boolean toggles ship in on/off pairs, not a single bidirectional flag.** `--telemetry` / `--no-telemetry`, `--security-context` / `--no-security-context`. The negative flag always wins when both are set (`securityContextEnabled`, `k8s.go:26`) — this is the tie-break rule, not an error, so keep using it rather than rejecting the combination.
4. **Shorthand letters are reserved for flags used on (almost) every invocation**: `-p` for `--platform` (`build`), `-f` for `--file` (`resolve`/`apply`/`rollback`), matching `ko build -p` / `ko resolve -f`. Do not hand out a shorthand to a flag used occasionally.
5. **A convenience flag that's shorthand for a more general one is named after the concrete case, not the mechanism**: `--hardened` is sugar for `--base chainguard`, not `--base-preset=hardened`. Prefer this over adding more enum values to an existing flag when the shortcut deserves top-billing.
6. **Mutually exclusive flags are rejected explicitly and early**, before any network or filesystem work (`--local` + `--tarball`, `--dry-run` + `--print-manifest`). A new pair of mutually exclusive flags should fail with a `fmt.Errorf("cannot specify both --x and --y")`-shaped message before `req.Normalize()`.
7. **Output-mode flags are exclusive and default to "push"**: no flag set means push to `POKKUM_DOCKER_REPO`, exactly like plain `ko build`. `--local` and `--tarball` are the two escape hatches, following `ko`'s `--local`/`--push=false` distinction.
8. **`--output=json` is the one CI-consumption flag**, reserved for *results* (a stable, versioned schema), never for retargeting where logs go — logs always go to stderr, structured or not, controlled separately by `--log-format`.
9. **Global logging flags (`--log-level`, `--log-format`) are registered twice on purpose**: once as `rootCmd.PersistentFlags()` for subcommands that don't redeclare them, and once again on `build` itself, because `main.go` pre-parses `os.Args` by hand (the `flag()` helper) to configure `slog` *before* cobra has even constructed the command tree.
10. **`pokkum resolve` / `pokkum apply` intentionally expose no per-project build flags** other than cluster-facing options — every `pokkum://` reference is built with `Normalize()`'s defaults (multi-platform, distroless, SPDX-JSON SBOM). New build-time flags belong on `pokkum build`; only cluster-facing toggles belong on `resolve`/`apply`.

11. **Compiler build optimization flags are enforced in every release build path, not exposed as CLI flags** (Roadmap "Compiler Build Optimization Flags"): every `pokkum` binary is compiled with `-trimpath` and `-ldflags="-s -w"` (strip DWARF/symbol tables) for a significant size reduction, in three places — the `Makefile` `build`/`supervisor` targets, `.goreleaser.yaml` (the official `pokkum upgrade` release pipeline), and `.github/workflows/slsa-builder.yml` (the SLSA L3 / trusted-builder path). All three also set `-X main.version/commit/buildDate` so `pokkum version` reports real release metadata (the SLSA path resolves these from git rather than goreleaser's templating). `scripts/check-build-flags.sh`, wired into `make verify` as its Step 0, fails the build if any of the three paths drops `-trimpath`, `-s -w`, or the `-X main.version` ldflag — guarding the size optimization against silent regression.

---

## 2. Global Flags (Root Command, Persistent)

| Flag | Env / Config | Default | Description |
|---|---|---|---|
| `--log-level` | — | `INFO` | Log level: `DEBUG`, `INFO`, `WARN`, `ERROR`. |
| `--log-format` | — | `text` | Log format: `text` or `json`. |
| `--output` | — | `text` | Output serialization format: `text` or `json`. |

---

## 3. `pokkum build [dir]`

| Flag | Shorthand | Env / Config | Default | Description |
|---|---|---|---|---|
| `--platform` | `-p` | — | `linux/amd64,linux/arm64` | Target platform(s); repeatable. `all` selects every supported platform. |
| `--base` | — | — | (unset → distroless) | Base image preset (`distroless`, `chainguard`) or a custom image reference. |
| `--hardened` | — | — | `false` | Shorthand for `--base chainguard`. |
| `--sbom` | — | — | `spdx-json` | SBOM format: `spdx-json`, `cyclonedx-json`, or `none`. |
| `--sbom-attach` | — | — | `referrer` | SBOM attachment mode: `referrer` (OCI 1.1) or `tag` (legacy `.sbom` tag). |
| `--local` | — | — | `false` | Load into the local Docker daemon instead of pushing. Mutually exclusive with `--tarball`. |
| `--image-label` | — | — | (none) | Custom image label (`key=value`), repeatable. `org.opencontainers.image.revision`/`.source`/`.version`/`.created` are auto-populated by default even without this flag; an explicit `--image-label org.opencontainers.image.<key>=...` always overrides the auto-populated value. `revision`/`source`/`version` come from git (commit SHA, remote URL, `git describe`) or CI env vars — no opt-out flag exists, and outside a git repo they're silently absent (no warning). `created` is set to the same resolved `SOURCE_DATE_EPOCH` timestamp used everywhere else in the build (never independently resolved, so it can't disagree with the image's real layer timestamps), and is left unset rather than fabricated when that timestamp is undetermined. |
| `--base-verify-mode` | — | — | `auto` | Base image verification mode: `auto` (keyless for stock presets, static-key for custom), `keyless`, or `static-key`. |
| `--base-keyless-identity` | — | — | (preset default) | Override the expected Fulcio certificate SAN for keyless verification. Setting only this (without `--base-keyless-issuer`) makes the identity non-empty, so the preset's own default is *not* merged in for the missing half — verification then fails with a "must specify Issuer criteria" error rather than falling back. Set both together, or neither. |
| `--base-keyless-issuer` | — | — | (preset default) | Override the expected OIDC issuer for keyless verification. Same partial-override caveat as `--base-keyless-identity` above, in reverse. |
| `--sigstore-trusted-root` | — | — | (embedded) | Path to a custom Sigstore trusted root snapshot (e.g. for a private Sigstore deployment). |
| `--no-verify-base` | — | — | `false` | Suppress base image signature verification entirely (both static-key and keyless). |
| `--allow-secret-pattern` | — | — | (none) | Regex pattern to ignore during build-time secret scanning, repeatable. |
| `--require-env` | — | — | (none) | Declare required runtime environment variables (comma-separated or repeatable). Stamped into image annotations (`pokkum.dev/required-env`) and validated by the supervisor on container boot. |
| `--fail-on-cve` | — | `POKKUM_FAIL_ON_CVE` | (none) | Fail build if base image vulnerabilities exceed threshold (`low`, `medium`, `high`, `critical`; default warn-only). |
| `--allow-incomplete` | — | — | `false` | Allow build to succeed even if base image vulnerability database lookups fail (default: fail closed when `--fail-on-cve` is active). |
| `--hermetic` | — | — | `false` | Enforce strict hermetic build mode (zero network egress, cached base images and node_modules required). |
| `--registry-config` | — | — | (none) | Path to a `docker config.json`-style auth file, keyed by registry hostname (`"auths": {"<host>": {...}}`), with dynamic credential-helper execution (`credHelpers`, `credsStore`) shelling out to `docker-credential-*` binaries (e.g. ECR, GCR, OSXKeychain) and caching credentials in-memory, merged ahead of `authn.DefaultKeychain`. |
| `--push-concurrency` | — | — | `0` (→ `4`) | Number of concurrent layer uploads during registry push. `0` defers to the registry adapter's built-in default (currently 4 parallel jobs via `remoteConfig.Jobs`); set a positive integer to override. Also raises the pull-limiter used by the same push's manifest `Head`/`Tag` reconciliation calls, since they share the same `remote.Option` set — a minor harmless side effect worth knowing about, not a separate tunable. |
| `--tarball` | — | — | (none) | Export as an OCI archive to the given path. Mutually exclusive with `--local`. |
| `--dry-run` | — | — | `false` | Resolve and report; perform no writes. Mutually exclusive with `--print-manifest`. |
| `--print-manifest` | — | — | `false` | Emit the computed OCI manifest/config without pushing. Mutually exclusive with `--dry-run`. |
| `--log-level` | — | — | `INFO` | Same as global flag; redeclared for early pre-parse. |
| `--log-format` | — | — | `text` | Same as global flag; redeclared for early pre-parse. |
| `--telemetry` | — | — | `false` | Enable OpenTelemetry auto-instrumentation and metrics export. |
| `--no-telemetry` | — | — | `false` | Explicitly disable telemetry; wins over `--telemetry` if both are set. |
| `--otel-export` | — | — | (none) | Override the OTLP exporter endpoint URL. |
| `--telemetry-env` | — | — | (none) | Target environment for telemetry (`dev`, `preview`, `production`). |
| `--trace-sample-rate` | — | — | `1.0` | Trace span sampling ratio, `0.0`–`1.0`. |
| `--metrics-only` | — | — | `false` | Disable trace spans while keeping OTEL metrics active. |
| `--with-otel-sidecar` | — | — | `false` | Inject an OTEL Collector sidecar spec into generated Kubernetes manifests. |
| `--sign` | — | — | `true` | Enable SLSA, Cosign, and DSSE signing. |
| `--no-sign` | — | — | `false` | Explicitly disable signing; wins over `--sign` if both are set. |
| `--inject` | — | — | `true` | Enable zero-config auto-injection (`svelte.config.js`, `SOURCE_DATE_EPOCH` pinning). |
| `--no-inject` | — | — | `false` | Explicitly disable auto-injection; wins over `--inject` if both are set. |
| `--update-base` | — | — | `false` | Force re-resolving base image tags against remote registry and update `pokkum.lock`. |
| `--offline` | — | — | `false` | Strictly enforce using `pokkum.lock` and local cache without remote registry calls. |
| `--bun-binary` | — | — | (none) | Local filesystem path escape hatch to a `bun` executable (skips resolution/download). |
| `--bun-variant` | — | — | `standard` | Bun CPU variant (`standard` [AVX2 required on x86-64] or `baseline`). |
| `--strategy` | — | — | `layered` | Packaging strategy (`layered` [8-layer layout], `static` [zero-JS static site], or `exe` [single executable, deprecated]). |
| `--static` | — | — | `false` | Shorthand for `--strategy=static`: compile a purely static site onto a minimal libc-free `chainguard/static` image served by the embedded `pokkum-static` PID-1 file server (no Bun runtime, no compiled executable). Conflicts with `--strategy=exe`; defaults the base to the static image when no `--base`/`--hardened` is given. |
| `--compression` | — | — | `gzip` | Layer compression algorithm (`gzip` or `zstd`). |
| `--sourcemap` | — | `POKKUM_SOURCEMAP` | `false` | Generate and preserve source maps in compiled bundles and vendor layers. |
| `--no-prune` | — | — | `false` | Disable build-time stripping of non-runtime files (`*.d.ts`, `*.map`, `tsconfig.json`, `README*`, tests) from `/app/vendor`. |
| `--keep-vendor` | — | — | (none) | Custom glob pattern(s) of vendor files to preserve during pruning, repeatable (e.g. `--keep-vendor='*.md'`). |
| `--no-precompress` | — | — | `false` | Disable build-time static asset pre-compression (`.gz`, `.br`, `.zst`) for `/app/client`. |
| `--no-strip` | — | — | `false` | Disable build-time stripping of unneeded debug symbols from native `.node` ELF addons. |
| `--no-cache` | — | — | `false` | Disable checking and publishing to the remote composite OCI input cache. |
| `--no-cache-verify` | — | — | `false` | Disable cryptographic signature verification on remote cache-hit images. |
| `--cache-verify-mode` | — | `POKKUM_CACHE_VERIFY_MODE` | `auto` | Cache image signature verification mode: `auto` (default), `static-key`, or `keyless`. |
| `--cache-verify-key` | — | `POKKUM_CACHE_PUBKEY` | (none) | Path or PEM string for static Cosign public key to verify remote cache hits. |
| `--cache-keyless-identity` | — | `POKKUM_CACHE_KEYLESS_IDENTITY` | (none) | Expected Fulcio certificate Subject Alternative Name for keyless cache verification. |
| `--cache-keyless-issuer` | — | `POKKUM_CACHE_KEYLESS_ISSUER` | (none) | Expected OIDC issuer for keyless cache verification. |
| `--cache-verify-strict` | — | — | `false` | Strict cache verification: fail build if candidate cache tag has invalid signature instead of falling back to clean rebuild. |

Positional: `[dir]` — project directory, defaults to `.`.

Reads environment variables: `POKKUM_DOCKER_REPO` (required for push mode), `SOURCE_DATE_EPOCH` (reproducible build timestamp), `POKKUM_CACHE_DIR` (custom base cache directory for layers and runtime binaries; defaults to `~/.cache/pokkum`), `POKKUM_SOURCEMAP` (enable source map preservation), `POKKUM_CACHE_PUBKEY` (static public key for remote cache verification), `POKKUM_CACHE_VERIFY_MODE`, `POKKUM_CACHE_KEYLESS_IDENTITY`, and `POKKUM_CACHE_KEYLESS_ISSUER`.

---

## 4. `pokkum resolve` / `pokkum apply`

| Flag | Shorthand | Default | Description |
|---|---|---|---|
| `--file` | `-f` | (required) | File or directory of Kubernetes manifests; `-` reads stdin. |
| `--recursive` | — | `false` | Process YAML files in the directory recursively. |
| `--security-context` | — | `true` | Inject hardened `securityContext` defaults for pokkum-built containers. |
| `--no-security-context` | — | `false` | Disable securityContext injection; wins over `--security-context` if both are set. |
| `--network-policy` | — | `true` | Generate NetworkPolicy document restricting ingress and egress, `podSelector` scoped to the workload's own Pod-template labels when found. |
| `--no-network-policy` | — | `false` | Disable NetworkPolicy generation. |
| `--resource-defaults` | — | `true` | Inject default CPU/memory requests and limits and append a PodDisruptionBudget, selector-scoped to the workload (skipped entirely if no labels are found — never emitted namespace-wide). |
| `--no-resource-defaults` | — | `false` | Disable resource default injection and PodDisruptionBudget generation. |
| `--cluster-inspect` | — | `true` | (`apply` only) Query live cluster workload annotations before resolution to seed multi-generation deployment history. |
| `--no-cluster-inspect` | — | `false` | (`apply` only) Disable live cluster annotation inspection; wins over `--cluster-inspect` if both are set. |
| `--since` | — | (none) | Git ref (commit, branch, tag) to diff against for monorepo affected-detection: a `pokkum://` project whose source tree has not changed since this ref, and for which a prior digest is known (from the manifest's `pokkum.dev/current-image` annotation or live cluster state when inspecting), skips compilation/packaging and reuses that digest. If no prior digest is known, or the project is affected, it is built normally. Fail-closed: an unknown ref or git failure errors out. |
| `--registry-config` | — | (none) | Path to a `docker config.json`-style auth file, keyed by registry hostname; see the `pokkum build` flag table above for the exact merge behavior. |

---

## 5. `pokkum dev [dir]`

| Flag | Shorthand | Default | Description |
|---|---|---|---|
| `--debug` | — | `false` | Drop into an interactive shell inside the container environment. |
| `--port` | `-p` | `3000:3000` | Port mapping for local container execution. |
| `--watch` | — | `true` | Watch source directory and auto-rebuild container on file changes. |
| `--env-file` | — | (none) | Path to an environment file for container execution. |

---

## 6. `pokkum doctor [dir]`

| Flag | Shorthand | Default | Description |
|---|---|---|---|
| `--dir` | `-d` | `.` | Path to SvelteKit project directory. |
| `--fix` | — | `false` | Automatically repair mechanical issues (e.g. create default `.pokkumignore`). |

---

## 7. `pokkum init [dir]`

| Flag | Shorthand | Default | Description |
|---|---|---|---|
| `--dir` | `-d` | `.` | Path to SvelteKit project directory. |
| `--defaults` | — | `false` | Accept default initialization settings without interactive prompts. |

---

## 8. `pokkum explain` / `pokkum why` / `pokkum diff`

| Command / Flag | Default | Description |
|---|---|---|
| `pokkum explain [image]` | — | Inspects layer hierarchy, sizes, and functions for an image or local build. |
| `pokkum why <file-path>` | — | Traces which layer and input source embedded a specific file. |
| `pokkum diff <img1> <img2>` | — | Compares layer size and structure differences between two images. |

---

## 9. `pokkum metrics`

| Flag | Default | Description |
|---|---|---|
| `--metrics-port` | `8889` | Port exposed for application metrics scraping. |

---

## 10. `pokkum scan [target]`

`pokkum scan` inspects project directories, container images (e.g. `gcr.io/distroless/cc-debian12:nonroot`), or OCI tarballs (`image.tar`). For images and tarballs, it enumerates OS packages (Debian, Ubuntu, Alpine, Wolfi, Chainguard) and toolchain packages using native zero-dependency targeted parsers (`scannerutils`), querying OSV.dev via batch API (`/v1/querybatch`) for CVE lookups and CVSS severity scoring.

| Flag | Default | Description |
|---|---|---|
| `--fail-on` | `critical` | Minimum vulnerability severity threshold causing scan failure (`low`, `medium`, `high`, `critical`). |
| `--toolchain` | `false` | Restrict scan to embedded Bun and SvelteKit toolchain advisories. |
| `--output` | `text` | Output serialization format (`text` or `json`). |
| `--offline` | `false` | Disable remote vulnerability database queries and use embedded advisories. |
| `--allow-incomplete` | `false` | Report success even if a vulnerability database lookup fails (default: fail closed on reduced coverage). |

---

## 11. `pokkum rollback`

Updates image references in Kubernetes deployment manifests across multiple generations using `pokkum.dev/image-history` and `pokkum.dev/previous-image` annotations.

| Flag | Shorthand | Default | Description |
|---|---|---|---|
| `--file` | `-f` | (required) | Manifest file to roll back. |
| `--to` | — | (optional) | Target container image reference or digest to roll back to (overrides generation index). |
| `--generation` | `-g` | `1` | Number of generations back to roll back (default `1` = immediate previous image). |
| `--list` | — | `false` | List available historical image generations recorded in manifest annotations. |
| `--output` | — | `text` | Output format (`text` or `json`). |

---

## 12. `pokkum upgrade`

| Flag | Default | Description |
|---|---|---|
| `--check` | `false` | Check for available CLI updates without installing. |
| `--version` | (latest) | Target version to upgrade to. |
| `--output` | `text` | Output format (`text` or `json`). |
| `--offline` | `false` | Disable network calls to release API (returns `Verified: false`). |
| `--key` | (embedded) | Path to public key PEM file for release artifact signature verification. Overrides the embedded `DefaultReleasePublicKeyPEM`, which must match this repo's `COSIGN_PRIVATE_KEY` CI secret — see [for-users.md](for-users.md). |

Without `--check`, `pokkum upgrade` refuses to install anything if it cannot verify the release signature (fails closed, including when no verifier is configured at all) — it does not fall back to an unverified install.

---


## 13. `pokkum verify` / `pokkum repro doctor`

| Command / Flag | Default | Description |
|---|---|---|
| `pokkum verify <ref>` | — | Performs attestation summary and signature validation. |
| `--no-rebuild` | `false` | Skip full image rebuild during verification. |
| `--against <path>` | (none) | Local image tarball or ref to compare against. |
| `--expect-source <repo>@<ref>` | (none) | Expected git repository and ref source attestation. |
| `pokkum repro doctor [dir]` | — | Stage-level non-determinism bisection diagnostic wizard. |
| `--fast` | `false` | Run fast static reproducibility checks. |
| `--perturb` | `false` | Inject environmental perturbations to detect hidden non-determinism. |

---

## 14. `pokkum base`

| Subcommand / Flag | Default | Description |
|---|---|---|
| `pokkum base update [--preset <name>] [--mirror-registry <repo>]` | — | Re-resolve upstream base image tags against remote registry and update `pokkum.lock`. If `--mirror-registry=<repo>` is supplied, copies the base image/index and its Cosign `.sig` tag into the project-controlled mirror repo, recording `mirror_ref` in `pokkum.lock`. Does **not** run signature verification during update — the resolved digest is pinned on trust-on-first-use; `pokkum build` re-verifies the locked digest against the live signature at build time regardless. |
| `pokkum base check` | — | Inspect current base image lockfile status and digest pinning. Same no-verification caveat as `base update` above. |

---

## 15. `pokkum version`

No flags.

---

## 16. `pokkum adopt [dir]`

Migration codemod that inspects an existing SvelteKit repository (configured for `@sveltejs/adapter-node`, `adapter-vercel`, `adapter-auto`, `adapter-cloudflare`, etc.), updates `package.json` dependencies and `svelte.config.js` to Pokkum compilation defaults, generates `.pokkumignore`, and optionally removes legacy Dockerfile configurations.

| Flag | Default | Description |
|---|---|---|
| `--dir`, `-d` | `.` | Path to SvelteKit project directory. |
| `--dry-run` | `false` | Report planned migration changes without writing to disk. |
| `--remove-dockerfile` | `false` | Remove legacy Dockerfile, `.dockerignore`, and compose files. |
| `--output` | `text` | Output serialization format (`text` or `json`). |

---

## 17. `pokkum history <image>`

Inspects build provenance timeline and CI metadata for an image reference, verifying SLSA v1.0 attestations, Cosign signatures, builder environment, and extracting links back to GitHub Actions CI workflow runs, Pull Requests, and commits.

| Flag | Default | Description |
|---|---|---|
| `--expect-source` | (none) | Expected git repository and ref source attestation (e.g. `github.com/org/repo@main`). |
| `--output` | `text` | Output format (`text` or `json`). |

---

## 18. Environment Variables (Runtime, Read by Supervisor)

These configure the image's *runtime* behavior inside the container (read by `/pokkum/init`), not the CLI:

| Variable | Default | Description |
|---|---|---|
| `PORT` | `3000` | Port the compiled app listens on. |
| `POKKUM_PROBE_PORT` | `8081` | Port the supervisor serves `/healthz` and `/readyz` on. |
| `POKKUM_SHUTDOWN_TIMEOUT` | `30s` | Grace period after `SIGTERM` before `SIGKILL`. |
| `POKKUM_REQUIRED_ENV` | (none) | Comma-separated list of required environment variable names that must be present and non-empty at container boot; supervisor fails fast if any are missing. |
| `POKKUM_PRERENDERED_DIR` | (none) | Path (in the image) of the mounted prerendered pages tree. Set by the packager to `/app/prerendered` for `--strategy=layered`; the patched adapter-node handler serves prerendered pages from here. |
| `POKKUM_STATIC_ROOTS` | `/app/client:/app/prerendered` | Colon-separated list of static roots that `pokkum-static` serves (via Content-Encoding/Range/ETag) for `--strategy=static` images. Set by the packager at build time. |

---

## 19. Beyond v1.0 / Backlog

Post-v1.0 items from [Roadmap.md](Roadmap.md):

| Roadmap Item | Proposed Flag(s) | Notes |
|---|---|---|
| Monorepo Affected-Detection | `--since=<git-ref>` on `resolve` | Skips unchanged `pokkum://` apps entirely based on git diffs. |
| Static/Prerendered Page Optimization (v1.0 — done) | `--static` on `build` | Builds a zero-JS-runtime image served by the embedded Go `pokkum-static` file server (no Bun runtime), plus a dedicated prerendered-page layer for the layered strategy. |
| Multi-Environment Management | `--target-env=<name>` on `build`/`resolve`/`apply` | Named `--target-env` to avoid colliding with `build`'s OTel `--telemetry-env`. |
| Hooks System | `pokkum hook pre-build`, `pokkum hook post-build`, `--skip-hooks` | Subcommands run hooks directly; `--skip-hooks` bypasses them during `build`. |
| Image Provenance Timeline | `pokkum history <image>`, `--output=json` | Reuses standard JSON envelope format. |
| Policy as Code | `pokkum policy check`, `--policy=<path>` | Rego/OPA policy file validation. |
| Service Mesh Integration | `pokkum mesh generate`, `--mtls`, `--mesh-telemetry` | Named `--mesh-telemetry` to remain distinct from OTel `--telemetry`. |
| Progressive Deployment Strategies | `pokkum deploy --canary=<percent>`, `--blue-green`, `--auto-rollback` | Canary and blue-green deployment specs. |
| Asset Optimization Pipeline | `--optimize-assets` on `build` | Opt-in asset minification/compression pipeline. |
| Plugin System | `pokkum plugin add\|list\|remove <name>` | Subcommand family for npm-based Pokkum CLI plugins. |

---

## 20. Open Naming Questions

Two proposed backlog flags above collide with already-shipped flags of the same short name (`--telemetry`, `--env`) because backlog notes in `AdditionalFeatures.md` predate the OTel work landing in v0.2. Resolve these before implementing the corresponding backlog item — do not ship a second, differently-scoped `--telemetry` or `--env`.
