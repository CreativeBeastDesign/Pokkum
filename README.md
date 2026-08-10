# Pokkum: SvelteKit to OCI in One Command

**Pokkum** is a compiler that turns a SvelteKit application into a single, reproducible OCI container image with minimal overhead. One command builds a multi-architecture image, generates an SBOM, and pushes to a registry — no Dockerfile, no Docker daemon required.

Think of it as `ko` for SvelteKit: compile to a binary, assemble an image on a hardened distroless base, and ship.

## Quick Start

Assume a SvelteKit project at `./my-app` configured with the `@jesterkit/exe-sveltekit` adapter.

```bash
POKKUM_DOCKER_REPO=ghcr.io/example/my-app pokkum build ./my-app
```

Prints:

```
ghcr.io/example/my-app@sha256:abcd1234...
```

The image is now live in your registry, ready to deploy.

## Prerequisites

- **Bun ≥ 1.2.18** (must be on PATH). Pokkum uses `bun build --compile` to produce a self-contained executable for your app.
- **@jesterkit/exe-sveltekit adapter** installed in your project's `package.json`.
- **svelte.config.js** configured with the exe adapter. Example:

```javascript
import adapter from '@jesterkit/exe-sveltekit';

export default {
  kit: {
    adapter: adapter({
      // Name of the output binary (without extension or arch suffix)
      binaryName: 'server',
      // Output directory for adapter artifacts
      out: 'build',
      // Bun compile target(s); exe adapter omits glibc linux-arm64
      // Pokkum runs bun build --compile itself, so this is advisory only
      target: ['bun-linux-x64', 'bun-linux-arm64'],
    }),
  },
};
```

## Base Images and glibc

Bun-compiled binaries are **dynamically linked**, so a static-only base image cannot execute them. Reading `DT_NEEDED` off a binary produced by Bun 1.3.14 for `linux-x64`:

```
libc.so.6  ld-linux-x86-64.so.2  libpthread.so.0  libdl.so.2  libm.so.6
```

That is plain glibc — Bun statically links its C++ runtime, so `libstdc++`/`libgcc_s` are not required. `scratch` and `distroless/static` still will not work, because they ship no dynamic loader at all.

Pokkum defaults to the `cc` variant rather than the smaller `base` variant deliberately: `cc` is a superset that stays correct if a future Bun release or a different target does pull in `libstdc++`, and it costs ~2 MB against a Bun app binary that is typically 90 MB+.

**Default:** `gcr.io/distroless/cc-debian12:nonroot` (glibc, Debian-based)
- Runs as nonroot user `65532:65532`.
- ~50 MB, includes only essential C libraries and ca-certificates.

**With `--hardened` flag:** `ghcr.io/chainguard-images/glibc-dynamic:latest` (Chainguard)
- Hardened alternative; equivalent glibc surface.

**Custom base:** pass `--base=<image-ref>` to use your own, but verify it includes a glibc environment.

Note: `scratch` and `distroless/static` **will not work** because Bun is dynamically linked.

**Registry note:** distroless images are published on `gcr.io`, not `ghcr.io`.

## Runtime Contract

Every image produced by Pokkum has this entrypoint:

```
/pokkum/init -- /app/server
```

Where:
- `/pokkum/init` is the PID-1 supervisor (reaps zombies, handles signals).
- `/app/server` is your compiled Bun application, mounted read-only.
- `PORT` (default `3000`) is where your app listens.
- `POKKUM_PROBE_PORT` (default `8081`) serves health probes without hitting your app.

### Health Probes

The supervisor exposes:
- `GET /healthz` → `200 OK` (always live, unless supervisor crashed).
- `GET /readyz` → `200 OK` when the app is ready, `503 Service Unavailable` during startup/shutdown.

Use these in Kubernetes probes instead of hitting your app:

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: example
spec:
  containers:
  - name: app
    image: ghcr.io/example/my-app@sha256:...
    ports:
    - containerPort: 3000
      name: app
    - containerPort: 8081
      name: probe
    livenessProbe:
      httpGet:
        path: /healthz
        port: probe
      initialDelaySeconds: 3
      periodSeconds: 10
    readinessProbe:
      httpGet:
        path: /readyz
        port: probe
      initialDelaySeconds: 1
      periodSeconds: 5
```

### Graceful Shutdown

- `POKKUM_SHUTDOWN_TIMEOUT` (default `30s`) is the grace period for your app to exit cleanly after receiving SIGTERM.
- If the app doesn't exit within this window, the supervisor sends SIGKILL.

## Configuration

| Environment Variable | Flag | Default | Description |
|---|---|---|---|
| `POKKUM_DOCKER_REPO` | — | (required) | Registry and repository; e.g. `ghcr.io/org/app`. Omit the tag. |
| — | `--platform` / `-p` | `linux/amd64,linux/arm64` | Target platforms (repeatable; use `all` for all supported). |
| — | `--base` | `distroless` | Base image preset: `distroless` or `chainguard`. |
| — | `--hardened` | `false` | Shorthand for `--base chainguard`. |
| — | `--sbom` | `spdx-json` | SBOM format: `spdx-json`, `cyclonedx-json`, or `none`. |
| — | `--local` | `false` | Load into Docker daemon instead of pushing to registry. |
| — | `--tarball` | (none) | Export as OCI archive to path; e.g. `image.tar`. |
| — | `--dry-run` | `false` | Resolve configuration; do not build or push. |
| — | `--print-manifest` | `false` | Emit OCI manifest/config without pushing. |
| — | `--log-level` | `INFO` | Log level: `DEBUG`, `INFO`, `WARN`, `ERROR`. |
| — | `--log-format` | `text` | Log format: `text` or `json`. |
| `PORT` | — | `3000` | Port your app listens on (read by the supervisor). |
| `POKKUM_PROBE_PORT` | — | `8081` | Port the supervisor serves `/healthz` and `/readyz` on. |
| `POKKUM_SHUTDOWN_TIMEOUT` | — | `30s` | Grace period for graceful shutdown. |

## How It Works

1. **Compile**: Runs `bun run build` to invoke the SvelteKit build and emit a TypeScript entrypoint (via the exe adapter).
2. **Cross-compile per platform**: For each target (e.g., `linux/amd64`, `linux/arm64`), Pokkum runs `bun build --compile --target=<platform>` to produce a self-contained executable. The binary embeds all static assets (client bundle, prerendered pages) via the exe adapter's `embedStatic` setting.
3. **Layer assembly**: For each platform, create an OCI image layer:
   - Base: distroless or Chainguard glibc image.
   - App layer: Copy the compiled binary to `/app/server` and the supervisor to `/pokkum/init`.
   - Metadata: Set entrypoint, user (nonroot), environment defaults.
4. **Multi-arch index**: Combine per-platform images into a single multi-architecture image index.
5. **SBOM generation**: Generate a Software Bill of Materials (SPDX JSON or CycloneDX) from the base image and app dependencies.
6. **Push or export**: Write to registry, Docker daemon (`--local`), or tarball (`--tarball`).

All layer timestamps and hashes are derived from the last git commit (`SOURCE_DATE_EPOCH`), ensuring reproducible builds: the same source tree produces the same digest.

## Architecture Support

Pokkum v0.1 supports:
- `linux/amd64` (x86-64)
- `linux/arm64` (ARM 64-bit)

**Why arm64 works:** Pokkum runs `bun build --compile` itself on the build machine, cross-compiling per target. The exe adapter's own `target` list can be incomplete or platform-specific; Pokkum doesn't rely on it. If Bun publishes a new compile target in the future, Pokkum will support it with a minor update.

## Development

See `Roadmap.md` for v0.2 and v1.0 planned features, including Cosign signing, SLSA provenance, vulnerability scanning, and Kubernetes manifest generation.

## License

TBD
