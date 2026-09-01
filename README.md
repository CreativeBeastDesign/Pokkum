# Pokkum: SvelteKit to OCI in One Command

[![GitHub Actions Build Status](https://github.com/CreativeBeastDesign/Pokkum/workflows/ci/badge.svg)](https://github.com/CreativeBeastDesign/Pokkum/actions)
[![pkg.go.dev](https://pkg.go.dev/badge/github.com/CreativeBeastDesign/pokkum.svg)](https://pkg.go.dev/github.com/CreativeBeastDesign/pokkum)
[![Go Report Card](https://goreportcard.com/badge/github.com/CreativeBeastDesign/pokkum)](https://goreportcard.com/report/github.com/CreativeBeastDesign/pokkum)
[![SLSA 3](https://slsa.dev/images/gh-badge-level3.svg)](https://slsa.dev)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)

Containerising a SvelteKit app usually means writing a Dockerfile you then own forever: a multi-stage build, a `node:22-alpine` base with a package manager and a shell in it, `npm ci` at image-build time, and a fresh crop of CVEs every time you rebuild. Then you write a second one, slightly differently, for the next project.

Pokkum replaces that file with a command:

```bash
pokkum adopt .                              # converts an existing SvelteKit project
POKKUM_DOCKER_REPO=ghcr.io/you/app \
  pokkum build .
```

No Dockerfile. No Docker daemon. The output is a distroless multi-arch OCI image with no shell and no package manager in it, an SBOM, SLSA provenance, and — because every timestamp derives from `SOURCE_DATE_EPOCH` rather than the clock — the same bytes every time you build the same commit.

**See it for yourself:** [`benchmarks/three-way`](benchmarks/three-way) builds one identical SvelteKit app three ways — a typical hand-written Dockerfile, a tuned multi-stage one, and Pokkum — then prints image size, package count, CVE count, and whether two consecutive builds produce identical digests. It runs on your machine, against your registry, and you can read every line of it.

### Why you might want this

- **Nothing to exploit in the runtime layer.** Distroless by default; `--strategy=static` drops even the JS runtime for a small Go file server, which for a static site means nothing left to CVE-scan.
- **Reproducible, and checkable.** `pokkum verify` rebuilds from source and compares against what is actually in your registry, at three levels: manifest digest, layer diffIDs, and file-level diffs. It tells you *where* two images differ, not just *that* they do.
- **Supply chain, if you need it.** SLSA v1.0 provenance, Cosign/DSSE signing, SBOMs via the OCI Referrers API, keyless Sigstore verification of the base image, and hermetic builds with no network egress.
- **Deploys where you already deploy.** Kubernetes (`pokkum resolve`/`apply`/`rollback`, with hardened `securityContext`, `NetworkPolicy` and probe defaults injected), or straight to a self-hosted PaaS — see [Deploying](#deploying).
- **Your source tree is never touched.** Adapter and telemetry configuration is staged in `.pokkum/`, never written over your files.

### Honest caveats, up front

Three things are worth knowing before you decide, rather than after:

1. **`--sign` on its own does not produce a signed image.** It generates a real SLSA statement either way, but a Cosign signature and DSSE attestation need a key (`--signing-key` / `POKKUM_SIGNING_KEY`). Without one, a signing-enabled build pushes **unsigned** with a loud warning; `--require-signed` turns that into a hard failure. The SLSA-3 badge describes what the pipeline can produce, not a guarantee for every build.
2. **Reproducibility has one edge Pokkum cannot always close for you.** SvelteKit's `kit.version.name` defaults to `Date.now()`, which lands in `_app/version.json` and renames every hashed client chunk. Pokkum pins it wherever it authors your Vite config, which covers most projects. Where your build script does more than `vite build` — so taking it over would skip the rest — the build **warns that the image is not bit-for-bit reproducible** and names the one line that fixes it, instead of shipping a non-reproducible image quietly.
3. **It optimises for verifiability, not for the shortest path to a running container.** The defaults are stricter than `docker build` on purpose, and there is no "build now, verify later" fast path. If time-to-first-deploy is what you are optimising, that is a deliberate trade-off here, not an oversight.

Think of it as [`ko`](https://github.com/ko-build/ko) for SvelteKit.

---

## Example Usages

### 1. Quickest & Lightest (Standard One-Liner)

Zero-config build and push to your container registry:

```bash
POKKUM_DOCKER_REPO=ghcr.io/example/my-app pokkum build ./my-app
```

- **What this does**:
  - Automatically detects your SvelteKit project directory.
  - Compiles your app into an architecture-independent layout using the Bun runtime.
  - Assembles a multi-layer OCI container image on a Distroless glibc base (run `pokkum explain` for the real per-build layer breakdown).
  - Generates SPDX-JSON Software Bill of Materials (SBOMs).
  - Pushes the multi-architecture image (`linux/amd64`, `linux/arm64`) to `ghcr.io/example/my-app`.

---

### 2. Maximum Security (Enterprise Hermetic & Hardened)

Strict SLSA L3 hermetic build on a hardened Chainguard base, with Cosign signature checks, custom OCI annotations, and hardened Kubernetes manifest resolution:

**Build the Image:**

```bash
POKKUM_DOCKER_REPO=ghcr.io/example/my-app pokkum build ./my-app \
  --hardened \
  --hermetic \
  --image-label org.opencontainers.image.vendor="Acme Corp" \
  --allow-secret-pattern="(?i)PUBLIC_.*"
```

- **What this does**:
  - `--hardened`: Uses `cgr.dev/chainguard/glibc-dynamic:latest` base image.
  - `--hermetic`: Enforces strict zero-network egress during compilation, requiring pre-cached base images and pre-populated `node_modules/`.
  - **Secret Guard**: Scans both pre-build source and, since 2026-08-18, the actual packaged build output against five fixed regex patterns (private key headers, AWS/GitHub/Google API key shapes, generic password/secret/token assignments) — not entropy analysis; entropy-based detection of arbitrary high-randomness tokens is tracked as future work, not shipped.
  - **Base Image Verification**: Verifies both static-key Cosign signatures (for custom/self-signed bases) and keyless Sigstore signatures (Fulcio + Rekor, for stock `distroless`/`chainguard` presets by default).

**Resolve Hardened Kubernetes Manifests:**

```bash
POKKUM_DOCKER_REPO=ghcr.io/example/my-app pokkum resolve -f deployment.yaml \
  --security-context \
  --network-policy \
  --resource-defaults \
  --registry-config=~/.docker/config.json
```

- **What this does**:
  - Replaces `pokkum://` URIs in `deployment.yaml` with pinned immutable image digests (`repo@sha256:...`).
  - Injects hardened container `securityContext` (`runAsNonRoot: true`, `seccompProfile: RuntimeDefault`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`).
  - Generates restricted `NetworkPolicy` ingress/egress rules and injects CPU/memory `requests`/`limits` with a `PodDisruptionBudget`.

---

## Key Features

- **Zero-Mutation Build Sandbox**: No manual SvelteKit adapter configuration needed. Auto-injects virtual build sandbox configuration in `.pokkum/` workspace without mutating user source files.
- **Architecture-Independent Layer Caching**: the default `--strategy=layered` splits a build into independently-cached layers — a pinned Bun runtime layer (`/usr/local/bin/bun`), the PID-1 supervisor, the SvelteKit server/asset output, and, when a given build produces them, separate layers for the vendor `node_modules` tree, native `.node` addons, and prerendered pages. The exact layer set and count vary per build (a purely static site or a native-addon-free app produces fewer layers than one with both) — run `pokkum explain <image>` against a built image for its real, per-build breakdown rather than assuming a fixed number.
- **Bit-for-Bit OCI Reproducibility**: All timestamps and tar headers derive deterministically from `SOURCE_DATE_EPOCH` / git commit metadata.
- **Security Scanning & Guardrails**: Integrated `pokkum scan` vulnerability auditing with native, zero-dependency OS package enumeration (Debian, Ubuntu, Alpine, Wolfi, Chainguard) and OSV.dev batch queries, build-time secret leak prevention, and base image CVE reactivity.
- **Base Image Signature Verification & Escrow Mirroring**: Real keyless Sigstore verification (Fulcio certificate chain + Rekor transparency log) runs automatically against live `distroless`/`chainguard` base image signatures. Base image escrow mirroring (`pokkum base update --mirror-registry`) copies base images and Cosign `.sig` tags to project-controlled registries with automated lockfile fallback.
- **Optional OpenTelemetry Bootstrap**: `--telemetry` compiles a real OTel NodeSDK + OTLP trace exporter directly into the image, started at container boot. Automatic HTTP/framework instrumentation isn't possible under Bun's runtime today (a Bun limitation, not Pokkum's) — real, route-templated spans need one documented line added to your own `hooks.server.ts`; see [Vocabulary.md](Vocabulary.md) §3a. OTLP metrics export is not currently functional (a Bun bundler bug); `--metrics-only` warns rather than silently doing nothing.
- **Route Exclusion** (`--exclude-route`, `build.exclude_routes`): keep dev-only routes — a component gallery, a style guide, an internal dashboard — out of the shipped image. A `+page.svelte` is a bundle entry point, and reachability is the definition of an entry point, so no amount of tree-shaking removes one; Pokkum instead points `kit.files.routes` at a filtered mirror of your routes directory, so the route's code, imports and SBOM entries are never in the image at all. Where it cannot author your Vite config it falls back to removing the route's prerendered output and says so, rather than implying the code is gone.
- **Missing-Dependency Build Guard**: a production dependency that will not resolve inside the image fails the build instead of surfacing as a 500 on the first request that reaches it — the container would otherwise start cleanly and pass both probes.
- **Day-2 Lifecycle Management**: Annotation-based manifest rollbacks (`pokkum rollback`, no `--to` needed for the last change) and signed CLI self-upgrades (`pokkum upgrade`).

---

## Scope, Philosophy & Telemetry

**What Pokkum optimizes for.** Pokkum is designed for maximum security and bit-for-bit reproducibility, not the fastest possible path to a running deployment. Hermetic builds, SLSA provenance generation, keyless base-image verification, and reproducibility checks (`pokkum verify`, whose rebuild-and-compare path runs by default — `--no-rebuild` opts out) are all either on by default or one flag away — and there is no "just build it, verify later" fast path that skips them. Turning that provenance into a cryptographically signed, self-verified artifact needs one more thing from you: a signing key (`--signing-key`/`POKKUM_SIGNING_KEY`; `--require-signed` to enforce it in CI) — see above. If your priority is the shortest possible time-to-first-deploy over a verifiable supply chain, that trade-off is a deliberate design choice here, not an oversight; expect the defaults to feel stricter than a typical `docker build`.

**What Pokkum does not do.** Pokkum compiles SvelteKit applications into OCI container images — it does not target edge/isolate runtimes (Cloudflare Workers, Deno Deploy, Vercel Edge Functions). Those aren't OCI images; they're a fundamentally different deployment model, and SvelteKit's own `adapter-cloudflare`/`adapter-vercel`/`adapter-deno` already serve that use case directly. This is an explicit non-goal, not an unaddressed gap — if edge deployment is what you need, Pokkum isn't the tool for that half of your stack.

**Telemetry.** The `pokkum` CLI itself sends no telemetry, analytics, or usage data anywhere, ever — no phone-home, no update-check pings beyond an explicit `pokkum upgrade --check`. (This is unrelated to the `--telemetry` _build_ flag, which configures OpenTelemetry instrumentation inside the SvelteKit application you're compiling — see [Vocabulary.md](Vocabulary.md) §3a. That feature is entirely opt-in, off by default, and exports only to the OTLP endpoint you configure.)

---

## Installation & Setup

Choose your preferred installation method:

### 1. Homebrew (macOS & Linux)

```bash
brew tap CreativeBeastDesign/pokkum
brew trust CreativeBeastDesign/pokkum
brew install pokkum
```

All three lines are required on current Homebrew, which added two gates for third-party taps:

- it no longer auto-taps, so `brew install CreativeBeastDesign/pokkum/pokkum` alone fails with _"No available formula or cask"_;
- it refuses to load formulae from an untrusted tap until you run `brew trust`.

`brew trust` is a deliberate security decision — it tells Homebrew you accept running code from this tap. If you would rather not, the [installer script](#3-standalone-installer-script-linux--macos) verifies a SHA-256 against the release checksums and needs no trust grant.

---

### 2. NPM / NPX

```bash
# One-off, no install
npx @pokkum/cli build .

# Or install it
npm install -g @pokkum/cli
```

`@pokkum/cli` is a thin launcher: the real binary ships in one of four platform
packages (`@pokkum/pokkum_{linux,darwin}_{amd64,arm64}`), listed as
`optionalDependencies` so npm downloads only the one matching your machine.
Pin a version in CI (`npx @pokkum/cli@<version>`, using a published
[release](https://github.com/CreativeBeastDesign/pokkum/releases)) rather than
tracking `latest`.

> [!NOTE]
> The npm packages carry no signature of their own. If you need the supply-chain
> guarantees, use the installer script or a release archive below — both verify
> the binary's SHA-256 against the release's `checksums.txt`, which is itself
> Cosign-signed and covered by the release's SLSA provenance.

---

### 3. Standalone Installer Script (Linux / macOS)

```bash
curl -fsSL https://raw.githubusercontent.com/CreativeBeastDesign/pokkum/main/install.sh | sh
```

Detects your OS and architecture, downloads the matching release archive, and **verifies its SHA-256 against the release's `checksums.txt` before installing** — a mismatch aborts without installing anything. Override the destination with `POKKUM_INSTALL_DIR` (default `/usr/local/bin`) or pick a [released version](https://github.com/CreativeBeastDesign/pokkum/releases) with `POKKUM_VERSION` (default: the latest release):

```bash
POKKUM_VERSION=v<version> POKKUM_INSTALL_DIR="$HOME/.local/bin" \
  sh -c "$(curl -fsSL https://raw.githubusercontent.com/CreativeBeastDesign/pokkum/main/install.sh)"
```

---

### 4. GitHub Action for CI/CD Pipelines

Build and push straight from a workflow. The action installs the CLI (verifying
its SHA-256 against the release checksums), builds the image, and hands back the
immutable digest-pinned reference:

```yaml
- name: Log in to GitHub Container Registry
  uses: docker/login-action@v3
  with:
    registry: ghcr.io
    username: ${{ github.actor }}
    password: ${{ secrets.GITHUB_TOKEN }}

- name: Build & Push SvelteKit Container
  id: pokkum
  uses: CreativeBeastDesign/pokkum@v1
  with:
    project-dir: ./my-app
    repo: ghcr.io/${{ github.repository }}
    platforms: linux/amd64,linux/arm64

- name: Deploy the Exact Image That Was Just Built
  run: echo "${{ steps.pokkum.outputs.ref }}"
```

`outputs.ref` is a `repo@sha256:…` reference — prefer it over a tag for
deployment, since it cannot drift. The job needs `packages: write` to push, and
`id-token: write` for keyless signing. Linux and macOS runners only.

**[docs/GITHUB_ACTION.md](docs/GITHUB_ACTION.md)** documents every input and
output, plus ECR/Docker Hub login, multi-arch matrices, and Kubernetes
deployment.

<details>
<summary>Just want the CLI on PATH, without the build step?</summary>

Use the installer action directly and drive `pokkum` yourself:

```yaml
- name: Setup Pokkum
  uses: CreativeBeastDesign/pokkum/.github/actions/setup-pokkum@v1
  with:
    version: "v<version>" # a released tag; omit for the default, 'latest'

- name: Build & Push SvelteKit Container
  env:
    POKKUM_DOCKER_REPO: ghcr.io/${{ github.repository }}
  run: pokkum build ./my-app
```

</details>

---

## Deploying

Pokkum's output is an ordinary OCI image in an ordinary registry, so **anything that can run a container from a registry can run it** — Fly.io, Cloud Run, App Runner, Azure Container Apps, DigitalOcean App Platform, Coolify, CapRover, Dokku, plain `docker run`. For those, Pokkum's job ends at the push and the platform pulls as usual.

Two self-hosted PaaS platforms get a first-class integration, so a successful build can deploy itself instead of you wiring up a separate CI step: **Dokploy** and **SwiftWave**.

```yaml
# .pokkum.yaml
deploy:
  target: dokploy
  endpoint: https://panel.example.com
  application: <your app id>
  token_env: DOKPLOY_API_KEY # the NAME of an env var, never the token itself
```

`pokkum build` now deploys after a successful push. `--no-deploy` skips it for one build, `deploy.auto: false` turns the automatic behaviour off permanently, and `pokkum deploy` runs it on its own — handy for a redeploy or for retrying after a failure. Put a `deploy:` block inside a profile and `-P staging` / `-P production` hit different panels.

**No credential is ever stored in `.pokkum.yaml`.** Every secret is named indirectly, as the name of an environment variable to read at deploy time (`token_env`, `endpoint_env`, `registry_username_env`, `registry_password_env`). A config file is a committed file, and Pokkum ships a scanner whose whole job is stopping secrets from getting committed; it would be a bit rich to then ask you to paste an API key into one.

### Two things that will bite you if nobody says them out loud

Both of these come from how the platforms themselves work, not from Pokkum. They are described here because you cannot discover either one from those platforms' documentation — we found them by reading their source.

**SwiftWave cannot be pointed at a new image, so give it a moving tag.** Both of SwiftWave's routes (its redeploy webhook and its `rebuildApplication` API call) tell it to *redeploy what it already has*. Neither can change *which* image it pulls. So set your SwiftWave application to a tag Pokkum keeps republishing — `:latest`, or `:main` — and the deploy makes it pull that tag again. If you point it at a fixed digest, the deploy will faithfully redeploy that same digest forever. Pokkum refuses `update_image` for SwiftWave rather than accepting the setting and quietly ignoring it.

Related, and the reason Pokkum reads the reply rather than just the status code: **SwiftWave's webhook answers "200 OK" when it has decided to do nothing.** It only rebuilds if the request mentions the image that application is configured with; otherwise it replies `OK - No rebuild` and carries on. Anything checking only the HTTP status would cheerfully report a successful deployment forever. Pokkum treats that reply as a failure and tells you what to check.

**Dokploy's "set the image" call also rewrites your registry login.** Turning on `update_image` lets Pokkum point the application at the exact digest it just pushed, which is the nicer setup — but the Dokploy endpoint that does it overwrites the image *and* the registry username, password and URL in one go. There is no way to change only the image. So if your registry is private, tell Pokkum where the credentials live:

```yaml
deploy:
  target: dokploy
  endpoint: https://panel.example.com
  application: <your app id>
  token_env: DOKPLOY_API_KEY
  update_image: true                   # off by default, precisely because of this
  registry_url: ghcr.io
  registry_username_env: GHCR_USER
  registry_password_env: GHCR_PAT
```

Leave those out and Dokploy's stored credentials get cleared, which is fine for a public image and quietly fatal for a private one. Pokkum warns loudly when it does it rather than letting you find out at the next pull. This is also why `update_image` is **off** unless you ask for it.

### What is deliberately not supported

- **Vercel, Netlify, and edge runtimes.** They do not run OCI images at all — there is nowhere to hand a digest. That is the same non-goal described under [Scope](#scope-philosophy--telemetry), and `@sveltejs/adapter-vercel` is the right tool if that is your target.
- **Deploying anything that was not pushed.** `--local`, `--tarball` and `--to-oci-layout` leave the image on your machine, where no remote platform can pull it, so auto-deploy is skipped with a warning naming the mode rather than failing mysteriously.

Full key-by-key reference: [Vocabulary.md §4b](Vocabulary.md).

---

## Runtime Contract

Every container image produced by Pokkum is supervised by an ultra-lightweight PID-1 init process:

```
/pokkum/init -- /usr/local/bin/bun /app/server/index.js
```

### Health Probes & Signals

- `/pokkum/init` reaps orphaned zombie processes and handles OS signals (`SIGTERM`, `SIGINT`).
- Exposes probe endpoints on `POKKUM_PROBE_PORT` (default `8081`):
  - `GET /healthz` → `200 OK` (supervisor liveness)
  - `GET /readyz` → `200 OK` (app readiness; returns `503 Service Unavailable` during graceful shutdown drain)
- Application listens on `PORT` (default `3000`).

---

## CLI Command Reference

| Command                    | Usage                            | Description                                                                                                                   |
| -------------------------- | -------------------------------- | ----------------------------------------------------------------------------------------------------------------------------- |
| `pokkum build [dir]`       | `pokkum build ./my-app`          | Compiles SvelteKit app into a multi-layer OCI container image.                                                                |
| `pokkum resolve -f <file>` | `pokkum resolve -f deploy.yaml`  | Resolves `pokkum://` URIs in K8s manifests to immutable image digests.                                                        |
| `pokkum apply -f <file>`   | `pokkum apply -f deploy.yaml`    | Resolves manifests and pipes directly to `kubectl apply`.                                                                     |
| `pokkum dev [dir]`         | `pokkum dev ./my-app`            | Local development mode with hot-reloading file watcher and Docker daemon loading.                                             |
| `pokkum scan [target]`     | `pokkum scan ./my-app`           | Security vulnerability scanner for directories, images, or tarballs.                                                          |
| `pokkum doctor [dir]`      | `pokkum doctor ./my-app`         | Diagnostic wizard for preflight checks and mechanical repairs.                                                                |
| `pokkum init [dir]`        | `pokkum init ./my-app`           | Bootstraps project config and `.pokkumignore`.                                                                                |
| `pokkum explain [image]`   | `pokkum explain <ref>`           | Inspects layer hierarchy, file origin tracing (`why`), and image diffing (`diff`).                                            |
| `pokkum rollback`          | `pokkum rollback -f deploy.yaml` | Rolls back to the previous image ref (`pokkum.dev/previous-image` annotation), or pass `--to=<ref>` explicitly. One hop deep. |
| `pokkum upgrade`           | `pokkum upgrade --check`         | Checks for signed CLI release updates.                                                                                        |

---

## Documentation & Deep Dive

- **[ARCHITECTURE.md](ARCHITECTURE.md)**: Architectural invariants, Hexagonal layer boundaries, and image layer layout.
- **[Vocabulary.md](Vocabulary.md)**: Complete CLI flag reference and configuration options.
- **[docs/Roadmap.md](docs/Roadmap.md)**: What's next — SvelteKit-specific DX, supply-chain completions, and ergonomics.
- **[docs/Features.md](docs/Features.md)**: Every shipped capability, with its known limitations stated alongside it.
- **[docs/Shipped.md](docs/Shipped.md)**: What landed, newest first. These three are generated from [docs/roadmap/*.yaml](docs/roadmap) — edit the YAML, not the markdown.
- **[docs/archive/](docs/archive)**: Retired historical documents, including [Roadmap-v1-Archive.md](docs/archive/Roadmap-v1-Archive.md) for the full v1.0 build history.
- **[docs/archive/fixes-to-v1.md](docs/archive/fixes-to-v1.md)**: Post-v1.0 audit findings and the fixes applied for each.
- **[docs/archive/for-users.md](docs/archive/for-users.md)**: User-visible behavior changes from that fix round and what they require of you.
- **[paranoid-testing-guide.md](paranoid-testing-guide.md)**: Step-by-step "believe nothing" verification guide for testing Pokkum against a real project — cross-checks every claim (build, signature, provenance, reproducibility) with an independent tool.

---

## License

Apache 2.0
