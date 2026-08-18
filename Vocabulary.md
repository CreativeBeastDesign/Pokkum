# Pokkum CLI Vocabulary

A single reference for every flag Pokkum's CLI exposes, the conventions flags follow, and the open backlog items on [Roadmap.md](Roadmap.md). See [fixes-to-v1.md](fixes-to-v1.md) and [for-users.md](for-users.md) for a post-v1.0 audit round that changed some of the behavior documented below.

Pokkum's stated model is "`ko` for SvelteKit" (see [README.md](README.md)), so the vocabulary borrows `ko`'s shape deliberately: a `*_DOCKER_REPO` environment variable as the one required input, `-f`/`--recursive` for manifest commands, `--local`/`--push`-style output-mode flags, `--platform` as the multi-arch surface, and boolean toggles that read as prose (`--bare`, `--insecure-registry` in `ko`; `--hardened`, `--dry-run` here). Where this document says "follows `ko` convention," that is the specific precedent being matched.

---

## 1. Conventions

These are the load-bearing patterns established across `cmd/pokkum/`:

1. **`POKKUM_` prefix for every environment variable**, mirroring `ko`'s `KO_` prefix (`KO_DOCKER_REPO` → `POKKUM_DOCKER_REPO`). Unlike `ko`, there is **no** generic dotted-key → `POKKUM_*` binding: `internal/adapters/config` used to construct a `viper.Viper` for exactly that, but it was dead code (`Load()` always parsed `.pokkum.yaml` directly via `gopkg.in/yaml.v3`, never through Viper) and was removed. Each environment override is instead a plain, explicit `os.Getenv("POKKUM_...")` call at its own use site in `cmd/pokkum/build.go`, covering only this fixed set of `.pokkum.yaml` fields: `docker.repo` (`POKKUM_DOCKER_REPO`), `security.fail_on_cve` (`POKKUM_FAIL_ON_CVE`), a profile's `sourcemap` (`POKKUM_SOURCEMAP`), `stub_launcher` (`POKKUM_STUB_LAUNCHER`), `cache.verify_mode` (`POKKUM_CACHE_VERIFY_MODE`), `cache.pubkey` (`POKKUM_CACHE_PUBKEY`), `cache.keyless_identity` (`POKKUM_CACHE_KEYLESS_IDENTITY`), and `cache.keyless_issuer` (`POKKUM_CACHE_KEYLESS_ISSUER`). Every other field — `strategy`, `base`, `platforms`, all of `image.*`, `security.verify_base`/`allow_incomplete_scans`/`allow_secret_patterns`, `sbom.*`, `cache.enabled`, `otel.*`, and `docker.repo`/`security.fail_on_cve`/etc. *within a named profile* — is config-file/flag only, with no environment override. A new field does **not** imply a new env var for free; adding one means adding an explicit `os.Getenv` call at the field's read site in `build.go` and documenting it here. See `internal/adapters/config` and the canonical example at `testdata/config/pokkum.yaml.golden`.
2. **Precedence: flag > environment variable > profile > config file > default**, threaded through `cmd/pokkum/build.go` per-field (see item 1 above for exactly which fields have an environment step) and, for profiles, resolved by `config.Manager.ApplyProfile` before flags/env are layered on top. Document this precedence for every setting readable from more than one source.
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

| Flag | Shorthand | Environment Variable | Default | Description |
|---|---|---|---|---|
| `--profile` | `-P` | — | (none) | Activate a named build profile defined in `.pokkum.yaml` (e.g. `--profile local`, `--profile production`). Applies profile overrides with precedence: CLI flag > Env var > Profile > Top-level config > Defaults. |
| `--platform` | `-p` | — | `linux/amd64,linux/arm64` | Target platform(s); repeatable. `all` selects every supported platform. |
| `--base` | — | — | (unset → distroless) | Base image preset (`distroless`, `chainguard`) or a custom image reference. |
| `--hardened` | — | — | `false` | Shorthand for `--base chainguard`. |
| `--sbom` | — | — | `spdx-json` | SBOM format: `spdx-json`, `cyclonedx-json`, or `none`. |
| `--sbom-attach` | — | — | `auto` | SBOM attachment mode: `auto` (tries OCI 1.1 Referrers first, falls back to the legacy `.sbom` tag on registries that don't support referrers — ECR, older Harbor, older Artifactory), `referrer` (OCI 1.1, fails loudly if unsupported rather than falling back), or `tag` (always the legacy `.sbom` tag). |
| `--local` | — | — | `false` | Load into the local Docker daemon instead of pushing. Mutually exclusive with `--tarball`. |
| `--image-label` | — | — | (none) | Custom image label (`key=value`), repeatable. `org.opencontainers.image.revision`/`.source`/`.version`/`.created` are auto-populated by default even without this flag; an explicit `--image-label org.opencontainers.image.<key>=...` always overrides the auto-populated value. `revision`/`source`/`version` come from git (commit SHA, remote URL, `git describe`) or CI env vars — no opt-out flag exists, and outside a git repo they're silently absent (no warning). `created` is set to the same resolved `SOURCE_DATE_EPOCH` timestamp used everywhere else in the build (never independently resolved, so it can't disagree with the image's real layer timestamps), and is left unset rather than fabricated when that timestamp is undetermined. |
| `--base-verify-mode` | — | — | `auto` | Base image verification mode: `auto` (keyless for stock presets, static-key for custom), `keyless`, or `static-key`. |
| `--base-keyless-identity` | — | — | (preset default) | Override the expected Fulcio certificate SAN for keyless verification. Setting only this (without `--base-keyless-issuer`) makes the identity non-empty, so the preset's own default is *not* merged in for the missing half — verification then fails with a "must specify Issuer criteria" error rather than falling back. Set both together, or neither. |
| `--base-keyless-issuer` | — | — | (preset default) | Override the expected OIDC issuer for keyless verification. Same partial-override caveat as `--base-keyless-identity` above, in reverse. |
| `--sigstore-trusted-root` | — | — | (embedded) | Path to a custom Sigstore trusted root snapshot (e.g. for a private Sigstore deployment). |
| `--no-verify-base` | — | — | `false` | Suppress base image signature verification entirely (both static-key and keyless). |
| `--allow-secret-pattern` | — | — | (none) | Regex pattern to ignore during build-time secret scanning, repeatable. |
| `--require-env` | — | — | (none) | Declare required runtime environment variables (comma-separated or repeatable). Stamped into image annotations (`pokkum.dev/required-env`) and validated by the supervisor on container boot. |
| `--origin` | — | — | (none) | Canonical origin (e.g. `https://example.com`) written to `ORIGIN` — see §18b. Recommended for any deployment behind a reverse proxy/ingress. |
| `--protocol-header` | — | — | (none) | Proxy header adapter-node trusts for the request protocol, written to `PROTOCOL_HEADER` — see §18b. |
| `--host-header` | — | — | (none) | Proxy header adapter-node trusts for the request host, written to `HOST_HEADER` — see §18b. |
| `--address-header` | — | — | (none) | Proxy header adapter-node trusts for the client IP, written to `ADDRESS_HEADER` — see §18b. |
| `--xff-depth` | — | — | `0` (adapter-node's own default of `1` applies) | Trusted proxy hop count for `--address-header`, written to `XFF_DEPTH` — see §18b. |
| `--body-size-limit` | — | — | (none, adapter-node's own default of `512K` applies) | Request body size cap in adapter-node's size-string format, written to `BODY_SIZE_LIMIT` — see §18b. |
| `--fail-on-cve` | — | `POKKUM_FAIL_ON_CVE` | (none) | Fail build if base image vulnerabilities exceed threshold (`low`, `medium`, `high`, `critical`; default warn-only). |
| `--allow-incomplete` | — | — | `false` | Allow build to succeed even if base image vulnerability database lookups fail (default: fail closed when `--fail-on-cve` is active). |
| `--vex-output` | — | — | (none) | Write a real OpenVEX v0.2.0 JSON document (one `not_affected` statement per active `.pokkum.yaml` `security.vex_exemptions` entry) to the given path after a successful build. No file is written if there are no exemptions. |
| `--hermetic` | — | — | `false` | Enforce strict hermetic build mode: on Linux, real kernel-enforced network isolation (an unprivileged network namespace) around both build subprocess stages, no IP network egress possible regardless of what a build script does. Also strips `SSH_AUTH_SOCK`/`SSH_AGENT_PID`/`GPG_AGENT_INFO`/`DBUS_SESSION_BUS_ADDRESS`/`DOCKER_HOST` from the subprocess's environment — closes SSH-agent-forwarding fully, but a *pathname* Unix domain socket reachable by hardcoded/conventional path (e.g. a bind-mounted `/var/run/docker.sock`) remains a known, documented residual gap, see `Roadmap.md`. On non-Linux, advisory-only (`BUN_OFFLINE=1` and friends, clearly logged as such). Also requires cached base images and pre-populated `node_modules`, and fails closed (rather than downloading) on a cold Bun-runtime cache. |
| `--registry-config` | — | — | (none) | Path to a `docker config.json`-style auth file, keyed by registry hostname (`"auths": {"<host>": {...}}`), with dynamic credential-helper execution (`credHelpers`, `credsStore`) shelling out to `docker-credential-*` binaries (e.g. ECR, GCR, OSXKeychain) and caching credentials in-memory, merged ahead of `authn.DefaultKeychain`. |
| `--push-concurrency` | — | — | `0` (→ `4`) | Number of concurrent layer uploads during registry push. `0` defers to the registry adapter's built-in default (currently 4 parallel jobs via `remoteConfig.Jobs`); set a positive integer to override. Also raises the pull-limiter used by the same push's manifest `Head`/`Tag` reconciliation calls, since they share the same `remote.Option` set — a minor harmless side effect worth knowing about, not a separate tunable. |
| `--tarball` | — | — | (none) | Export as an OCI archive to the given path. Mutually exclusive with `--local`. |
| `--dry-run` | — | — | `false` | Resolve and report; perform no writes. Mutually exclusive with `--print-manifest`. |
| `--print-manifest` | — | — | `false` | Emit the computed OCI manifest/config without pushing. Mutually exclusive with `--dry-run`. |
| `--log-level` | — | — | `INFO` | Same as global flag; redeclared for early pre-parse. |
| `--log-format` | — | — | `text` | Same as global flag; redeclared for early pre-parse. |
| `--telemetry` | — | — | `false` | Enable a real OpenTelemetry NodeSDK + OTLP trace exporter bootstrap, compiled into the image's entrypoint. **`--strategy=exe` only — has no effect for the default `--strategy=layered`** (layered has no compile step for the bootstrap to wrap; see §3a). Does **not** auto-instrument HTTP/framework code, and does not export metrics — see §3a below for exactly what this does and doesn't do, and the required `hooks.server.ts` snippet to get any real spans at all. |
| `--no-telemetry` | — | — | `false` | Explicitly disable telemetry; wins over `--telemetry` if both are set. |
| `--otel-export` | — | — | (none) | Override the OTLP exporter endpoint URL. |
| `--telemetry-env` | — | — | (none) | Target environment for telemetry (`dev`, `preview`, `production`). |
| `--trace-sample-rate` | — | — | `1.0` | Trace span sampling ratio, `0.0`–`1.0`. |
| `--metrics-only` | — | — | `false` | ⚠️ **Currently non-functional** (see §3a) — combining an OTLP metrics exporter with the SDK crashes once compiled under Bun (a real Bun bundler bug, not a Pokkum bug). The compiled bootstrap detects this flag and logs a runtime warning rather than silently doing nothing or crashing. |
| `--with-otel-sidecar` | — | — | `false` | Inject an OTEL Collector sidecar spec into generated Kubernetes manifests. |
| `--sign` | — | — | `true` | Enable SLSA, Cosign, and DSSE signing. |
| `--no-sign` | — | — | `false` | Explicitly disable signing; wins over `--sign` if both are set. |
| `--inject` | — | — | `true` | Enable zero-config auto-injection: surgically injects/overrides the required SvelteKit adapter via `.pokkum/vite.config.ts` wrapper and runs `bunx vite build --config .pokkum/vite.config.ts` without mutating user-authored files. |
| `--no-inject` | — | — | `false` | Disable auto-injection; fails fast with `core.ErrAdapterMisconfigured` if the project's own config lacks the required adapter. |
| `--update-base` | — | — | `false` | Force re-resolving base image tags against remote registry and update `pokkum.lock`. |
| `--offline` | — | — | `false` | Strictly enforce using `pokkum.lock` and local cache without remote registry calls. |
| `--bun-binary` | — | — | (none) | Local filesystem path escape hatch to a `bun` executable (skips resolution/download/stub-compilation). |
| `--bun-variant` | — | — | `standard` | Bun CPU variant (`standard` [AVX2 required on x86-64] or `baseline`). |
| `--bun-version` | — | — | (none, resolves to the pinned default) | Bun release version to embed. A small set of common versions is checksum-verified against statically pinned SHA256 digests; any other version is verified against Bun's own GPG-signed `SHASUMS256.txt.asc` release manifest before use — never installed unverified. Also available on `pokkum dev`. |
| `--stub-launcher` | — | `POKKUM_STUB_LAUNCHER` | `false` | Compile a minimal entrypoint launcher stub instead of embedding stock Bun runtime (layered strategy runtime hardening). |
| `--strategy` | — | — | `layered` | Packaging strategy (`layered` [multi-layer layout — see `pokkum explain` for a given build's real breakdown], `static` [zero-JS static site], or `exe` [single executable, deprecated]). |
| `--static` | — | — | `false` | Shorthand for `--strategy=static`: compile a purely static site onto a minimal libc-free `chainguard/static` image served by the embedded `pokkum-static` PID-1 file server (no Bun runtime, no compiled executable). Conflicts with `--strategy=exe`; defaults the base to the static image when no `--base`/`--hardened` is given. |
| `--compression` | — | — | `gzip` | Layer compression algorithm (`gzip` or `zstd`). |
| `--sourcemap` | — | `POKKUM_SOURCEMAP` | `false` | Generate and preserve source maps in compiled bundles and vendor layers. |
| `--no-prune` | — | — | `false` | Disable build-time stripping of non-runtime files (`*.d.ts`, `*.map`, `tsconfig.json`, `README*`, tests) from `/app/vendor`. |
| `--keep-vendor` | — | — | (none) | Custom glob pattern(s) of vendor files to preserve during pruning, repeatable (e.g. `--keep-vendor='*.md'`). |
| `--no-precompress` | — | — | `false` | Disable build-time static asset pre-compression (`.gz`/`.br`; also `.zst` under `--strategy=static`) for `/app/client`. |
| `--no-strip` | — | — | `false` | Disable build-time stripping of unneeded debug symbols from native `.node` ELF addons. |
| `--no-cache` | — | — | `false` | Disable checking and publishing to the remote composite OCI input cache. |
| `--no-cache-verify` | — | — | `false` | Disable cryptographic signature verification on remote cache-hit images. |
| `--cache-verify-mode` | — | `POKKUM_CACHE_VERIFY_MODE` | `auto` | Cache image signature verification mode: `auto` (default), `static-key`, or `keyless`. |
| `--cache-verify-key` | — | `POKKUM_CACHE_PUBKEY` | (none) | Path or PEM string for static Cosign public key to verify remote cache hits. |
| `--cache-keyless-identity` | — | `POKKUM_CACHE_KEYLESS_IDENTITY` | (none) | Expected Fulcio certificate Subject Alternative Name for keyless cache verification. |
| `--cache-keyless-issuer` | — | `POKKUM_CACHE_KEYLESS_ISSUER` | (none) | Expected OIDC issuer for keyless cache verification. |
| `--cache-verify-strict` | — | — | `false` | Strict cache verification: fail build if candidate cache tag has invalid signature instead of falling back to clean rebuild. |

Positional: `[dir]` — project directory, defaults to `.`.

Reads environment variables: `POKKUM_DOCKER_REPO` (required for push mode), `SOURCE_DATE_EPOCH` (reproducible build timestamp), `POKKUM_CACHE_DIR` (custom base cache directory for layers and runtime binaries; defaults to `~/.cache/pokkum`), `POKKUM_SOURCEMAP` (enable source map preservation), `POKKUM_STUB_LAUNCHER` (compile minimal entrypoint launcher stub), `POKKUM_CACHE_PUBKEY` (static public key for remote cache verification), `POKKUM_CACHE_VERIFY_MODE`, `POKKUM_CACHE_KEYLESS_IDENTITY`, and `POKKUM_CACHE_KEYLESS_ISSUER`.

**Automatic `$env/static/*` detection (no flag):** every build scans the project's `src/` tree for `$env/static/public`/`$env/static/private` imports — SvelteKit inlines these as literal values at build time, so an image importing from them is pinned to whatever environment built it. A `Warn` names the exact bindings found, and they're stamped as a `pokkum.dev/env-baked` manifest annotation (comma-separated binding names, mirroring the existing `pokkum.dev/required-env` convention). This is a source scan, not a data-flow analysis — it does not follow a re-export or a dynamically-computed import specifier. `$env/dynamic/*` is correctly excluded (read at container startup, never baked).

---

## 3a. OpenTelemetry: What `--telemetry` Actually Does (and Doesn't)

> [!WARNING]
> **`--strategy=exe` only.** `--telemetry` currently has **no effect** for `--strategy=layered`, Pokkum's default. The mechanism wraps the compiled entrypoint `Compile` produces (`bun build --compile`'s input) — but `--strategy=layered` has no compile step at all: it packages the SvelteKit adapter's raw build output directly and the image execs a fixed `/app/server/index.js` via the embedded Bun runtime. Requesting `--telemetry` with `--strategy=layered` logs a build-time warning and does nothing further; it is not a silent no-op. Making this work for layered builds needs a different mechanism (packaging the bootstrap into the image and conditionally adding `bun --preload <path>` to the layered entrypoint's argv) — tracked as a real, scoped follow-up in `Roadmap.md`, not yet implemented.

`--telemetry` generates a small TypeScript bootstrap (`.pokkum/otel-bootstrap.ts`, never written to your real project tree) that imports and starts a real `@opentelemetry/sdk-node` `NodeSDK` with an OTLP trace exporter, then wraps your app's compiled entrypoint so the bootstrap runs first. For `--strategy=exe`, this part is real and automatic: enable `--telemetry`, and a real OTel SDK genuinely initializes at container startup and genuinely attempts to export spans to `--otel-export`'s endpoint.

**What it does not do, both confirmed by actually compiling and running real code, not assumed:**

- **No automatic HTTP/framework instrumentation.** `@opentelemetry/auto-instrumentations-node` (and every package like it) works by patching Node's module loader to intercept calls — and that patching does not take effect under Bun's runtime. A real HTTP request through a Bun-compiled binary, with the patch registered as early as technically possible, produces zero spans. This is a Bun limitation, not a Pokkum bug, and nothing in Pokkum's bootstrap can work around it.
- **No metrics export (`--metrics-only` is currently non-functional).** Combining an OTLP metrics exporter with `NodeSDK` crashes at runtime once compiled via `bun build --compile` (`TypeError: Cannot call a class constructor OTLPExporterNodeBase without |new|`) — a real Bun bundler bug where the compiler produces two non-interoperable copies of a shared dependency in one binary. The bootstrap detects `--metrics-only` and logs a runtime warning rather than silently doing nothing or crashing; no metrics are exported either way. Tracked in `Roadmap.md`.

**The SDK starting is not the same as spans existing.** Since nothing calls the SDK automatically, `--telemetry` alone produces a running exporter with nothing to export. To get real spans — including route-templated names (`/blog/[slug]`, not `/blog/hello-world`) — add this to your own `src/hooks.server.ts` (a real SvelteKit file you own; Pokkum does not generate or touch it, since safely auto-injecting into it would require either mutating your source or a much larger virtual-build-root mechanism):

```ts
// src/hooks.server.ts
import { trace, SpanStatusCode } from "@opentelemetry/api";
import type { Handle, HandleServerError } from "@sveltejs/kit";

const tracer = trace.getTracer("my-app");

export const handle: Handle = async ({ event, resolve }) => {
  const routeId = event.route.id ?? "unknown";
  const span = tracer.startSpan(`${event.request.method} ${routeId}`);
  span.setAttribute("http.route", routeId);
  span.setAttribute("http.method", event.request.method);
  span.setAttribute("url.path", event.url.pathname);

  try {
    const response = await resolve(event);
    span.setAttribute("http.status_code", response.status);
    if (response.status >= 500) {
      span.setStatus({ code: SpanStatusCode.ERROR });
    }
    return response;
  } catch (err) {
    span.recordException(err as Error);
    span.setStatus({ code: SpanStatusCode.ERROR });
    throw err;
  } finally {
    span.end();
  }
};

export const handleError: HandleServerError = ({ error, event }) => {
  const span = trace.getActiveSpan();
  if (span) {
    span.recordException(error as Error);
    span.setStatus({ code: SpanStatusCode.ERROR });
  }
  return { message: "Internal Error" };
};
```

`trace.getTracer(...)`/`trace.getActiveSpan()` are OpenTelemetry's own global API — this snippet needs nothing from Pokkum beyond `@opentelemetry/api` being installed (see below) and `--telemetry` having started the SDK it exports through.

**Your project must have these npm packages installed** (`bun add -D` or `-d`, matching whichever the rest of your dependencies use) for a `--telemetry` build to compile at all — Pokkum's compile step does not install them for you, and a hermetic build (`--hermetic`) cannot reach the network to do so even if it tried:

- `@opentelemetry/api`
- `@opentelemetry/sdk-node`
- `@opentelemetry/exporter-trace-otlp-proto`
- `@opentelemetry/sdk-trace-base`

If you use the `hooks.server.ts` snippet above, `@opentelemetry/api` is also a direct dependency of your own source, not just the generated bootstrap.

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
| `--probe-defaults` | — | `true` | Inject `readinessProbe`/`livenessProbe`/`startupProbe` defaults (`httpGet` against the supervisor's `/readyz`/`/healthz` on port `8081`) for pokkum-built containers, checked independently per probe type — a container with its own `livenessProbe` still gets `readinessProbe`/`startupProbe` filled in. See §19b. |
| `--no-probe-defaults` | — | `false` | Disable probe default injection. |
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

Initializes a SvelteKit workspace for Pokkum by creating `.pokkum.yaml` (with default configuration and build profiles) and `.pokkumignore`. In interactive terminal sessions (TTY), runs a guided questionnaire for target container registry, base image preset, build strategy, local profile, and CVE gating policy.

| Flag | Shorthand | Default | Description |
|---|---|---|---|
| `--dir` | `-d` | `.` | Path to SvelteKit project directory. |
| `--defaults` | — | `false` | Accept default initialization settings without interactive prompts. |
| `--output` | — | `text` | Output serialization format (`text` or `json`). |

---

## 8. `pokkum config`

Subcommand group to inspect and validate project configuration files (`.pokkum.yaml`). See `testdata/config/pokkum.yaml.golden` for a fully-commented, schema-complete example (base config plus `local`/`production`-style profiles) — it is parsed and validated end-to-end by tests in both `internal/adapters/config` and `cmd/pokkum`, so it cannot drift out of sync with the schema below.

| Subcommand / Flag | Default | Description |
|---|---|---|
| `pokkum config view [dir]` | — | Resolves and prints the active configuration after evaluating defaults and environment bindings. |
| `pokkum config view --profile <name>` | — | Resolves and prints configuration with the specified named profile overrides applied. |
| `pokkum config validate [dir]` | — | Validates syntax, schema version, presets, and platform strings, both at the top level AND for every named profile in `profiles:` (a profile-only mistake, e.g. an invalid `strategy` set only inside `profiles.production`, is caught and reported as `profile "production": ...`, not silently ignored). Also rejects unknown/misspelled top-level or nested keys (`gopkg.in/yaml.v3`'s `KnownFields(true)`) and validates `docker.repo` (base and per-profile) as a syntactically valid registry reference via `go-containerregistry/pkg/name`. |
| `--profile`, `-P` | (none) | (`view` only) Profile to resolve and display. |
| `--dir`, `-d` | `.` | Path to SvelteKit project directory. |
| `--output` | `text` | Output serialization format (`text` or `json`). |

---

## 9. `pokkum explain` / `pokkum explain why` / `pokkum explain diff`

`why` and `diff` are subcommands of `explain`, not top-level commands — the invocation is `pokkum explain why ...` / `pokkum explain diff ...`.

| Command / Flag | Default | Description |
|---|---|---|
| `pokkum explain <image>` | — | Reads a real image (registry ref, or a local `.tar` from `pokkum build --output=tarball`) and reports its actual per-layer digest, compressed size, real file count, and purpose (derived from the image's own history metadata for layers Pokkum built). The layer count printed is whatever the image actually has. |
| `pokkum explain why <image> <file-path>` | — | Traces which real layer a file came from, was deleted in (via a whiteout), or reports it was never present in any layer. |
| `pokkum explain diff <image1> <image2>` | — | Compares two real images' manifests and, for any layer whose digest differs, reports real added/removed/modified files. |
| `--platform` (`-p`) | host platform | On all three: which child image to inspect when the target is a multi-arch index, e.g. `linux/amd64`. |
| `--registry-config` | — | On all three: path to a `docker config.json`-style auth file for private registries. |

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
| `POKKUM_ATTESTATION_DIGEST` | (none) | Expected SHA-256 root digest of the layered `/app` runtime tree (startup attestation, hardening Option C). Set by the packager at build time for `--strategy=layered`. When present, `pokkum-init` re-derives the digest from the live `/app` tree before exec and refuses to start (exit **125** — see "Supervisor Exit Codes" below) on mismatch — tamper-evidence without cluster-level readonly-rootfs. Absent ⇒ verification silently disabled (deliberate escape hatch, no log). Malformed (non-empty but not 64 lowercase hex chars) ⇒ verification also disabled, but `pokkum-init` logs a `Warn`-level message naming the variable, since this means the build pipeline failed to stamp the env correctly and the security control is silently not running. Only applies to the layered strategy. |
| `POKKUM_STATIC_ROOTS` | `/app/client:/app/prerendered` | Colon-separated list of static roots that `pokkum-static` serves (via Content-Encoding/Range/ETag) for `--strategy=static` images. Set by the packager at build time. |
| `POKKUM_STATIC_FALLBACK` | (none) | **Opt-in SPA fallback** for `--strategy=static`: an in-image file path (e.g. `/app/client/200.html`) that `pokkum-static` serves with `200` for any unmatched `GET`/`HEAD` route (same ETag/Range/Content-Encoding negotiation as any served file). Set by the packager only when the source project configures an `@sveltejs/adapter-static` `fallback` page that was actually emitted into the client staging. Absent/empty (the default) means unmatched routes keep returning a plain `404`; the server logs a one-per-process `Warn` on the first such `404` pointing at this doc. Mirrors the `-fallback` flag on the `pokkum-static` binary. |

## 18b. Environment Variables (Runtime, Read by the Application)

Unlike the table above, these are read directly by the bundled `@sveltejs/adapter-node` server itself (`handler.js`), not by `pokkum-init` — they only apply to `--strategy=layered`/`--strategy=exe`, which embed adapter-node. All are optional; adapter-node falls back to its own default for any that are unset. Set via `--origin`/`--protocol-header`/`--host-header`/`--address-header`/`--xff-depth`/`--body-size-limit` on `pokkum build`, or the matching `image.*` keys in `.pokkum.yaml`.

| Variable | Default (adapter-node's own) | Description |
|---|---|---|
| `ORIGIN` | (none — derived from the raw socket) | The app's own canonical origin (e.g. `https://example.com`), used to validate the `Origin` header on form-action POSTs and to build absolute URLs. **Strongly recommended for any deployment behind a reverse proxy or ingress** — without it, adapter-node derives origin/protocol from the raw socket, which reports the wrong scheme/host behind TLS termination and produces `403 Cross-site POST form submissions are forbidden` on the app's first form action. `pokkum build` logs a `Warn` at build time when this is unset for a layered/exe build. |
| `PROTOCOL_HEADER` | (none) | Proxy header adapter-node trusts for the original request protocol (e.g. `x-forwarded-proto`). |
| `HOST_HEADER` | (none) | Proxy header adapter-node trusts for the original request host (e.g. `x-forwarded-host`). |
| `ADDRESS_HEADER` | (none) | Proxy header adapter-node trusts for the real client IP (e.g. `x-forwarded-for`). Without it, `event.getClientAddress()` reports the proxy's own address for every request. |
| `XFF_DEPTH` | `1` | Number of trusted proxy hops adapter-node counts back from when parsing `ADDRESS_HEADER`. |
| `BODY_SIZE_LIMIT` | `512K` | Request body size cap, in adapter-node's own size-string format (e.g. `512K`, `10M`, `Infinity` to disable). |

---

## 19. Supervisor Exit Codes (`pokkum-init`)

`pokkum-init` (PID 1 in every Pokkum-built image) terminates with one of the codes below, whether it refuses to start the child at all or is reporting the child's own termination. This is the only signal available to an operator triaging a crash-looping pod from a dashboard that shows exit status but not full logs, so each code below is deliberately distinct — none of the rows may ever be collapsed onto another.

| Exit code | Meaning | Source |
|---|---|---|
| `0` | Child exited cleanly (or caught its termination signal and exited 0). | `exitCode` in `supervisor.go` |
| `1` | Child could not be started for a reason other than the specific cases below (uncommon). | `startExitCode` in `supervisor.go` |
| `2` | `pokkum-init` itself was invoked with a bad flag or missing command (usage error). | `exitUsage` in `main.go` |
| `<N>` (0–255) | Child ran and exited on its own with status `N`, propagated verbatim. | `exitCode` in `supervisor.go` |
| `125` | **Startup attestation mismatch.** `POKKUM_ATTESTATION_DIGEST` was set (non-empty, well-formed) and the live `/app` tree's re-derived digest does not match it — the filesystem was tampered with after the image was built, or the image is corrupted. The child is never exec'd. | `exitAttestationMismatch` in `main.go` |
| `126` | Child binary exists but could not be exec'd (`EACCES`/`EPERM`/`ENOEXEC` — e.g. missing execute bit, wrong architecture). **Not** used for attestation failures (see `125` above); the two were split apart specifically so this code keeps its single, pre-existing meaning. | `startExitCode` in `supervisor.go` |
| `127` | Child binary not found (`ENOENT` / not on `PATH`). | `startExitCode` in `supervisor.go` |
| `128+N` | Child was killed by signal `N` (e.g. `137` = `128+SIGKILL`, `143` = `128+SIGTERM`), including the shutdown-timeout escalation to `SIGKILL` when the child ignores the forwarded termination signal past `POKKUM_SHUTDOWN_TIMEOUT`. | `exitCode` in `supervisor.go` |

---

## 19b. Kubernetes Probe Defaults (`pokkum resolve`/`apply --probe-defaults`)

`--probe-defaults` (default `true`) injects three probes against pokkum-init's probe server (`POKKUM_PROBE_PORT`, default `8081`) into any pokkum-built container that doesn't already define its own — checked independently per probe type, so an existing custom `livenessProbe` doesn't block `readinessProbe`/`startupProbe` from being filled in:

| Probe | Path | `periodSeconds` | `failureThreshold` | Purpose |
|---|---|---|---|---|
| `readinessProbe` | `/readyz` (`ports.ProbePathReady`) | `10` | `3` | Traffic gating — a TCP-connect check to the app's own port; adapter-node's server (`handler.js`) resolves its module-level `await server.init(...)` — which is what runs `hooks.server.js`'s `init` hook — before `index.js` ever calls `server.listen()`, so a successful connect here already implies `init()` has completed. |
| `livenessProbe` | `/healthz` (`ports.ProbePathLive`) | `10` | `3` | Process-alive check — restarts the container if the supervisor reports the child process as not running. |
| `startupProbe` | `/readyz` | `2` | `30` (60s total grace) | Protects a legitimately slow `init()` (a slow DB connection, a large cache warm) from being killed by `livenessProbe`'s normal cadence before the app has even finished starting — while a `startupProbe` hasn't yet succeeded once, `readinessProbe`/`livenessProbe` don't count against the container at all. |

---

## 20. Beyond v1.0 / Backlog

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

## 21. Open Naming Questions

Two proposed backlog flags above collide with already-shipped flags of the same short name (`--telemetry`, `--env`) because backlog notes in `AdditionalFeatures.md` predate the OTel work landing in v0.2. Resolve these before implementing the corresponding backlog item — do not ship a second, differently-scoped `--telemetry` or `--env`.
