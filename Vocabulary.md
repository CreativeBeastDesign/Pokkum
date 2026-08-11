# Pokkum CLI Vocabulary

A single reference for every flag Pokkum's CLI exposes today, the conventions
new flags are expected to follow, and — for each open item on
[Roadmap.md](Roadmap.md) — the flag(s) that item is expected to introduce.

Pokkum's stated model is "`ko` for SvelteKit" (see [README.md](README.md)), so
the vocabulary borrows `ko`'s shape deliberately: a `*_DOCKER_REPO`
environment variable as the one required input, `-f`/`--recursive` for
manifest commands, `--local`/`--push`-style output-mode flags, `--platform`
as the multi-arch surface, and boolean toggles that read as prose
(`--bare`, `--insecure-registry` in `ko`; `--hardened`, `--dry-run` here).
Where this document says "follows `ko` convention," that is the specific
precedent being matched.

## 1. Conventions

These are the load-bearing patterns already established in `cmd/pokkum/`.
Any new flag added for a Roadmap item should default to one of these shapes
rather than invent a new one.

1. **`POKKUM_` prefix for every environment variable**, mirroring `ko`'s
   `KO_` prefix (`KO_DOCKER_REPO` → `POKKUM_DOCKER_REPO`). Config keys in
   `.pokkum.yaml` are dotted (`docker.repo`) and map onto `POKKUM_DOCKER_REPO`
   via Viper's `.`→`_` replacer — a new env var implies a new dotted config
   key for free, and vice versa. See `internal/adapters/config`.
2. **Precedence: flag > environment variable > config file > default**,
   enforced per-value by the `config.Loader` (`GetString`/`GetBool`/
   `GetStringSlice`). Document this precedence for every new setting that is
   readable from more than one source.
3. **Boolean toggles ship in on/off pairs, not a single bidirectional flag.**
   `--telemetry` / `--no-telemetry`, `--security-context` /
   `--no-security-context`. The negative flag always wins when both are set
   (`securityContextEnabled`, `k8s.go:26`) — this is the tie-break rule, not
   an error, so keep using it rather than rejecting the combination.
4. **Shorthand letters are reserved for the two flags used on (almost)
   every invocation of their command**: `-p` for `--platform` (`build`),
   `-f` for `--file` (`resolve`/`apply`), matching `ko build -p` / `ko
   resolve -f`. Do not hand out a shorthand to a flag used occasionally.
5. **A convenience flag that's shorthand for a more general one is named
   after the concrete case, not the mechanism**: `--hardened` is sugar for
   `--base chainguard`, not `--base-preset=hardened`. Prefer this over
   adding more enum values to an existing flag when the shortcut deserves
   top-billing.
6. **Mutually exclusive flags are rejected explicitly and early**, before
   any network or filesystem work (`--local` + `--tarball`, `--dry-run` +
   `--print-manifest`). A new pair of mutually exclusive flags should fail
   with a `fmt.Errorf("cannot specify both --x and --y")`-shaped message
   before `req.Normalize()`.
7. **Output-mode flags are exclusive and default to "push"**: no flag set
   means push to `POKKUM_DOCKER_REPO`, exactly like plain `ko build`. `
   --local` and `--tarball` are the two escape hatches, following `ko`'s
   `--local`/`--push=false` distinction.
8. **`--output=json` is the one CI-consumption flag**, reserved for
   *results* (a stable, versioned schema), never for retargeting where logs
   go — logs always go to stderr, structured or not, controlled separately
   by `--log-format`.
9. **Global logging flags (`--log-level`, `--log-format`) are registered
   twice on purpose**: once as `rootCmd.PersistentFlags()` for subcommands
   that don't redeclare them, and once again on `build` itself, because
   `main.go` pre-parses `os.Args` by hand (the `flag()` helper) to configure
   `slog` *before* cobra has even constructed the command tree. Any new
   flag that logging setup itself needs to read must go through that same
   pre-parse, not `PersistentFlags()` alone.
10. **`pokkum resolve` / `pokkum apply` intentionally expose no per-project
    build flags** other than `--security-context` — every `pokkum://`
    reference is built with `Normalize()`'s defaults (multi-platform,
    distroless, SPDX-JSON SBOM). New build-time flags belong on `pokkum
    build`; only cluster-facing toggles belong on `resolve`/`apply`.

## 2. Global flags (root command, persistent)

| Flag | Env / Config | Default | Description |
|---|---|---|---|
| `--log-level` | — | `INFO` | Log level: `DEBUG`, `INFO`, `WARN`, `ERROR`. |
| `--log-format` | — | `text` | Log format: `text` or `json`. |

## 3. `pokkum build [dir]`

| Flag | Shorthand | Env / Config | Default | Description |
|---|---|---|---|---|
| `--platform` | `-p` | — | `linux/amd64,linux/arm64` | Target platform(s); repeatable. `all` selects every supported platform. |
| `--base` | — | — | (unset → distroless) | Base image preset (`distroless`, `chainguard`) or a custom image reference. |
| `--hardened` | — | — | `false` | Shorthand for `--base chainguard`. |
| `--sbom` | — | — | `spdx-json` | SBOM format: `spdx-json`, `cyclonedx-json`, or `none`. |
| `--sbom-attach` | — | — | `referrer` | SBOM attachment mode: `referrer` (OCI 1.1) or `tag` (legacy `.sbom` tag). |
| `--local` | — | — | `false` | Load into the local Docker daemon instead of pushing. Mutually exclusive with `--tarball`. |
| `--tarball` | — | — | (none) | Export as an OCI archive to the given path. Mutually exclusive with `--local`. |
| `--dry-run` | — | — | `false` | Resolve and report; perform no writes. Mutually exclusive with `--print-manifest`. |
| `--print-manifest` | — | — | `false` | Emit the computed OCI manifest/config without pushing. Mutually exclusive with `--dry-run`. |
| `--log-level` | — | — | `INFO` | Same as the global flag; redeclared for early pre-parse (see Convention 9). |
| `--log-format` | — | — | `text` | Same as the global flag; redeclared for early pre-parse. |
| `--telemetry` | — | — | `false` | Enable OpenTelemetry auto-instrumentation and metrics export. |
| `--no-telemetry` | — | — | `false` | Explicitly disable telemetry; wins over `--telemetry` if both are set. |
| `--otel-export` | — | — | (none) | Override the OTLP exporter endpoint URL. |
| `--telemetry-env` | — | — | (none) | Target environment for telemetry (`dev`, `preview`, `production`). |
| `--trace-sample-rate` | — | — | `1.0` | Trace span sampling ratio, `0.0`–`1.0`. |
| `--metrics-only` | — | — | `false` | Disable trace spans while keeping OTEL metrics active. |
| `--with-otel-sidecar` | — | — | `false` | Inject an OTEL Collector sidecar spec into generated Kubernetes manifests. |
| `--sign` | — | — | `true` | Enable SLSA, Cosign, and DSSE signing. |
| `--no-sign` | — | — | `false` | Explicitly disable signing; wins over `--sign` if both are set. |
| `--inject` | — | — | `true` | Enable zero-config auto-injection (svelte.config.js, SOURCE_DATE_EPOCH pinning). |
| `--no-inject` | — | — | `false` | Explicitly disable auto-injection; wins over `--inject` if both are set. |
| `--update-base` | — | — | `false` | Force re-resolving base image tags against remote registry and update `pokkum.lock`. |
| `--offline` | — | — | `false` | Strictly enforce using `pokkum.lock` and local cache without remote registry calls. |
| `--bun-binary` | — | — | (none) | Local filesystem path escape hatch to a `bun` executable (skips resolution/download). |
| `--bun-variant` | — | — | `standard` | Bun CPU variant (`standard` [AVX2 required on x86-64] or `baseline`). |
| `--strategy` | — | — | `layered` | Packaging strategy (`layered` [5-layer arch-independent layout, default] or `exe` [single executable]). |


Positional: `[dir]` — project directory, defaults to `.`.

Also reads: `POKKUM_DOCKER_REPO` (env only, no flag — matches `ko`'s
`KO_DOCKER_REPO`, which likewise has no `--repo` equivalent on `ko build`)
and `SOURCE_DATE_EPOCH` (env override for the git-derived build timestamp).

## 4. `pokkum resolve` / `pokkum apply`

Both commands share one flag set (`resolveFlags`/`applyFlags` are
structurally identical).

| Flag | Shorthand | Default | Description |
|---|---|---|---|
| `--file` | `-f` | (required) | File or directory of Kubernetes manifests; `-` reads stdin. |
| `--recursive` | — | `false` | Process YAML files in the directory recursively. |
| `--security-context` | — | `true` | Inject hardened `securityContext` defaults for pokkum-built containers. |
| `--no-security-context` | — | `false` | Disable injection; wins over `--security-context` if both are set. |

Also reads: `POKKUM_DOCKER_REPO` (required — resolving `pokkum://` refs
means building and pushing).

## 5. `pokkum dev`

`pokkum dev [dir]` compiles the SvelteKit application, loads the image into the local Docker daemon, and starts container execution locally with hot-reloading file watching.

| Flag | Shorthand | Default | Description |
|---|---|---|---|
| `--debug` | — | `false` | Drop into an interactive shell inside the container environment. |
| `--port` | `-p` | `3000:3000` | Port mapping for local container execution. |
| `--watch` | — | `true` | Watch source directory and auto-rebuild container on file changes. |
| `--env-file` | — | (none) | Path to an environment file for container execution. |

## 6. `pokkum base update` / `pokkum base check`

| Command / Flag | Default | Description |
|---|---|---|
| `pokkum base update [dir]` | — | Queries remote registry for latest base image digests, updates `pokkum.lock`, and prints diff. |
| `--preset` | (all) | Base image preset to update (`distroless`, `chainguard`). |
| `pokkum base check [dir]` | — | Compares `pokkum.lock` against remote registry without modifying `pokkum.lock`. |

## 6. `pokkum version`

No flags.

## 6. Environment variables (runtime, read by the supervisor)

These configure the image's *runtime* behavior, not the CLI — they are read
inside the container by `/pokkum/init`, not by the `pokkum` binary.

| Variable | Default | Description |
|---|---|---|
| `PORT` | `3000` | Port the compiled app listens on. |
| `POKKUM_PROBE_PORT` | `8081` | Port the supervisor serves `/healthz` and `/readyz` on. |
| `POKKUM_SHUTDOWN_TIMEOUT` | `30s` | Grace period after `SIGTERM` before `SIGKILL`. |

## 7. Planned flags by Roadmap item

Speculative — names are proposed here to keep the eventual implementation
consistent with §1, not committed API. Each row cites the concept doc it was
drawn from where one exists; unmarked rows are new proposals derived from
the conventions above. See [Roadmap.md](Roadmap.md) for the corresponding
checklist entry, which now cross-references this table inline.

### v0.2

| Roadmap item | Proposed flag(s) | Notes |
|---|---|---|
| SBOM as OCI 1.1 referrer | `--sbom-attach=referrer\|tag` on `build` | New axis orthogonal to `--sbom`'s format choice; defaults to `referrer`, `tag` kept for the current `.sbom`-tag consumers. |
| `pokkum dev` | `pokkum dev [dir]`, `--debug` | Per `AdditionalFeatures.md`; `--debug` drops into a shell inside the container. Builds on `--local` + a bare run, no new `build` flags. |
| Base Image Lockfile (`pokkum.lock`) | `--update-base` on `build`; `--offline` on `build`; `pokkum base update --preset <name>` | Per `pokkum-lock-concept.md`. An explicit digest already works today via `--base <ref>@sha256:...` — no new flag needed for that path. |

### v0.3: Layer Caching & Core Architecture Shift

| Roadmap item | Proposed flag(s) | Notes |
|---|---|---|
| M1: Packager + runtime plumbing | `--bun-binary=<path>`; `--bun-variant=standard\|baseline` | **Implemented**. Per `pokkum-layer-caching-concept.md`; offline escape hatch and older-CPU fallback. |
| M2: Hand-rolled adapter + Phase-1 layering | `--strategy=layered\|exe` on `build` | **Implemented**. Emits 5-layer arch-independent layout (`layered` is default). |
| M3: Vendor splitting + native closure | *(none — internal to `--strategy=layered`)* | **Implemented**. Enables `ClosuredNativeAdapter` for ELF `.node` addons, `/app/native` layer, and vendor chunking. |
| M4: Hardening & cutover | *(none — folds into `--security-context`)* | **Implemented**. Injects `readOnlyRootFilesystem: true` in container securityContext, deprecates `exe` strategy with CLI warning. |
| Image Optimization (dedup, zstd) | `--compression=gzip\|zstd` on `build` | **Implemented**. Configurable zstd/gzip layer compression (`--compression=gzip|zstd`, default `gzip`). |

### v0.4: Unified Telemetry & Developer Experience

| Roadmap item | Proposed flag(s) | Notes |
|---|---|---|
| Unified Metrics & Telemetry (`pokkum metrics`) | `pokkum metrics`, `--metrics-port` | Standalone `/metrics` exposure; reuses the existing `--telemetry`/`--otel-export` family for the build-time half — no new `build` flags. |
| `pokkum init` | `--defaults` | Already named inline in `Roadmap.md`. |
| `pokkum doctor` | `pokkum doctor`, `--fix` | Per `AdditionalFeatures.md`; `--fix` performs mechanical repairs (version pin, `.pokkumignore`). |
| Interactive Failure Diagnostics | `--no-diagnostics` on `build --local` | Opt-out for CI logs where the extra dump is noise, following Convention 3. |
| `--output=json` | `--output=json` on `build` | Already named inline in `Roadmap.md`. |
| Diff & Explain | `pokkum diff`, `pokkum explain`, `pokkum why`, `--output=json` | New subcommands; reuse the `--output=json` schema family rather than inventing a second one. |

### v0.5: Reproducibility & Diagnosis

| Roadmap item | Proposed flag(s) | Notes |
|---|---|---|
| M0: Provenance completeness | *(none)* | Recorded automatically in the SLSA statement; no CLI surface. |
| M1/M2: Stage recorder, bisection, attestation-check | `pokkum verify --no-rebuild` | Already named inline in `Roadmap.md`; per `pokkum-verify-concept.md` M1. |
| M3: `layerdiff`, L3 explanation, `repro doctor` | `pokkum repro doctor [dir]`, `--fast` | Per `pokkum-repro-doctor-concept.md` §"Phase 0" / its own M3; static checks only, no build. |
| M4: `pokkum verify --rebuild`, `--perturb`, CI ergonomics | `--perturb`, `--against <path>`, `--expect-source <repo>@<ref>`, `--all-platforms` | `--perturb` already named inline. `--against` and `--all-platforms` are `repro doctor`'s own M4 flags (comparison mode, cross-platform paranoia); `--expect-source` is `verify`'s CI-ergonomics flag — both concept docs' "M4" collapse into this one Roadmap milestone. |

### v1.0: MVP Launch

#### Supply Chain

| Roadmap item | Proposed flag(s) | Notes |
|---|---|---|
| Trusted-Builder Mode | *(none — CI workflow, not a CLI flag)* | Delivered as a reusable GitHub Actions job; the isolation is structural, not a flag pokkum itself parses. |
| CVE Scanning (`pokkum scan`) | `pokkum scan <ref>`, `--fail-on=critical` | Per `AdditionalFeatures.md`. |
| Toolchain CVE Awareness | `pokkum scan --toolchain` | Extends `scan` rather than adding a sibling command, per the doc's "natural extension of `pokkum scan` diff mode." |
| Base Image Signature Verification | `--no-verify-base` on `build` | Opt-out for the new default-on pull-time check, following Convention 3; needed for legitimately unsigned custom `--base` references. |
| Secret-Inlining Guard | `--allow-secret-pattern` on `build` | Per `AdditionalFeatures.md`; escape hatch for false positives. The `--env=disable` bundler setting it forces is internal, not user-facing. |
| Base image digest pinning + update PRs | `pokkum base update --preset <name>` | Same subcommand as the v0.2 lockfile row; the "automated update PRs" half is a bot/Action, not a new flag. |
| Standard OCI Annotations | `--image-label key=value` on `build` | Matches `ko build --image-label` exactly (repeatable). Auto-population from git metadata needs no flag; this one is for user-supplied overrides/extras. |

#### Cluster-side hardening

| Roadmap item | Proposed flag(s) | Notes |
|---|---|---|
| `NetworkPolicy` generation | `--network-policy` / `--no-network-policy` on `resolve`/`apply` | Same on/off pair shape as `--security-context`. |
| Resource `requests`/`limits` + `PodDisruptionBudget` | `--resource-defaults` / `--no-resource-defaults` on `resolve`/`apply` | Same shape; bundles both requests/limits and the PDB under one toggle to match how `--security-context` already bundles several fields. |
| `readOnlyRootFilesystem: true` | *(none — folds into `--security-context`)* | "Where feasible" is a per-workload runtime decision the resolver makes, not something a flag should override per Convention 10 (no new per-project knobs on `resolve`/`apply`). |
| Readiness Drain on SIGTERM | *(none — reuses `POKKUM_SHUTDOWN_TIMEOUT`)* | Supervisor-internal; the existing runtime env var already bounds the drain window. |
| Secrets via `envFrom` | *(none)* | Expressed in the manifest itself (`envFrom`/`Secret`/`ExternalSecret`), not a CLI input. |

#### Build integrity

| Roadmap item | Proposed flag(s) | Notes |
|---|---|---|
| Hermetic builds (no network egress) | `--hermetic` on `build` | Opt-in until proven safe across projects, then candidate for default-on with a `--no-hermetic` escape hatch. |
| Multi-registry auth chains | `--registry-config=<path>` on `build` | Points at per-registry credential config (ECR/GCR/ACR/self-hosted), keeping `POKKUM_DOCKER_REPO` itself unchanged. |
| Ephemeral test registry | *(none — test infra only)* | `pkg/registry.New()` is wired into CI, not exposed as a user flag. |

#### Operational maturity

| Roadmap item | Proposed flag(s) | Notes |
|---|---|---|
| Version pinning in manifest annotations | *(none — folds into `--image-label`/auto-annotations)* | Same mechanism as Standard OCI Annotations above. |
| GitHub Action wrapping the CLI | *(none — Action inputs, not `pokkum` flags)* | Mirrors `setup-ko`'s `version`/`token` inputs at the Action layer. |
| `pokkum rollback` | `pokkum rollback -f <manifest>`, `--to=<ref>` | Reuses `-f`/`--file` from `resolve`/`apply` for the target manifest; `--to` pins the digest/tag to roll back to. |
| Signed Self-Distribution (`pokkum upgrade`) | `pokkum upgrade`, `--check` | `--check` reports available/verified updates without installing. |

### Beyond v1.0 / Backlog

| Roadmap item | Proposed flag(s) | Notes |
|---|---|---|
| `pokkum adopt` | `pokkum adopt [dir]`, `--dry-run`, `--remove-dockerfile` | `--dry-run` reuses the existing `build` semantics (report, don't write) rather than inventing a synonym. |
| Runtime Env Contract | `--require-env=KEY1,KEY2` on `build` | Build-time declaration only; the doc explicitly drops the old build-time *injection* half (`--env-file`) as a secret-baking footgun — validation happens at supervisor startup, not via a new build flag for injection. |
| Monorepo Affected-Detection | `--since=<git-ref>` on `resolve` | Skips unchanged `pokkum://` apps entirely (input-tree git-diff), stronger than digest-HEAD skipping. |
| Static/Prerendered Page Optimization | `--static` on `build` | Per `AdditionalFeatures.md`; builds a zero-JS-runtime Nginx-alpine image instead of the Bun runtime image. |
| Multi-Environment Management | `--env=<name>` on `build`/`resolve`/`apply` | **Naming collision to resolve before implementation**: `build` already uses `--telemetry-env` for a narrower purpose; a bare `--env` here needs to be unambiguous against that and against the *runtime* env-var story (`PORT`, `POKKUM_PROBE_PORT`). Consider `--target-env` instead. |
| Hooks System | `pokkum hook pre-build`, `pokkum hook post-build`, `--skip-hooks` on `build` | Subcommands run hooks directly; `--skip-hooks` is the `build`-time bypass. |
| Image Provenance Timeline | `pokkum history <image>`, `--output=json` | Reuses the results-schema convention. |
| Policy as Code | `pokkum policy check`, `--policy=<path>` | Rego/OPA policy file location. |
| Service Mesh Integration | `pokkum mesh generate`, `--mtls`, `--mesh-telemetry` | **Same collision as above**: `AdditionalFeatures.md` proposes bare `--telemetry` here too; renamed to `--mesh-telemetry` to stay distinct from `build`'s OTel `--telemetry`. |
| Progressive Deployment Strategies | `pokkum deploy --canary=<percent>`, `--blue-green`, `--auto-rollback` | Per `AdditionalFeatures.md`; demoted — Argo/Flux own this space. |
| Asset Optimization Pipeline | `--optimize-assets` on `build` | Opt-in given the "massive build time increase" cost noted in the decision matrix. |
| Plugin System | `pokkum plugin add\|list\|remove <name>` | Subcommand family, not a `build` flag; npm-package-based per the backlog note. |

## 8. Open naming questions

Two proposed flags above collide with an already-shipped flag of the same
short name (`--telemetry`, `--env`) because the backlog notes in
`AdditionalFeatures.md` predate the OTel work landing in v0.2. Resolve these
before implementing the corresponding backlog item — do not ship a second,
differently-scoped `--telemetry` or `--env`.
