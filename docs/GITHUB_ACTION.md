# Pokkum GitHub Action Guide

The official **Pokkum GitHub Action** (`CreativeBeastDesign/pokkum`) enables fast, reproducible, and hardened container builds for SvelteKit and Bun applications directly inside GitHub Actions CI/CD workflows.

> [!IMPORTANT]
> The action wraps the `pokkum build` CLI subcommand. Its inputs are translated into the *real* flags/environment variables `pokkum build` actually accepts (verified against `pokkum build --help`) — not the invocation this action used before this doc was corrected. In particular: `repo` becomes the `POKKUM_DOCKER_REPO` environment variable (there is no `--repo` flag), and `output` becomes `--local`/`--tarball` (the CLI's own `--output` flag is an unrelated text/json serialization setting, not a push/local/tarball switch). See "Known limitation: `tags`" below for the one input that currently has no CLI-side effect at all.

---

## Quickstart

Add `CreativeBeastDesign/pokkum@v1` to your repository's `.github/workflows/deploy.yml`:

```yaml
name: Build & Publish Container

on:
  push:
    branches: [main]

jobs:
  build:
    runs-on: ubuntu-latest
    permissions:
      contents: read
      packages: write
      id-token: write # Required for ambient OIDC keyless signing

    steps:
      - name: Checkout Source Code
        uses: actions/checkout@v4

      - name: Log in to GitHub Container Registry
        uses: docker/login-action@v3
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Build & Push Container with Pokkum
        id: pokkum
        uses: CreativeBeastDesign/pokkum@v1
        with:
          repo: ghcr.io/${{ github.repository }}
          platforms: linux/amd64,linux/arm64

      - name: Output Pushed Image Reference
        run: |
          echo "Pushed Image Ref: ${{ steps.pokkum.outputs.ref }}"
          echo "Image Digest:     ${{ steps.pokkum.outputs.digest }}"
```

This pushes `ghcr.io/<owner>/<repo>:latest` and exposes the immutable, digest-pinned `ref` output — use that output (not a tag) for deployments, since custom tags aren't wired up yet (see below).

---

## Inputs Reference

| Input | Description | Default | Required |
| :--- | :--- | :--- | :--- |
| `project-dir` | Path to the SvelteKit project directory. | `.` | No |
| `repo` | Destination container repository (e.g. `ghcr.io/acme/app`). Passed to the CLI via the `POKKUM_DOCKER_REPO` environment variable (`pokkum build` has no `--repo` flag). Can also be left unset if `.pokkum.yaml`'s `docker.repo` already configures it. | `""` | No |
| `tags` | **Not yet functional** — see "Known limitation" below. | `latest` | No |
| `platforms` | Comma-separated target platforms (e.g. `linux/amd64,linux/arm64`), passed to `--platform`. | `linux/amd64` | No |
| `output` | Build mode: `push` (default, publish to `repo`), `local` (load into the runner's local Docker daemon via `--local`), or `tarball` (export an OCI archive via `--tarball`; see `tarball-path`). | `push` | No |
| `tarball-path` | Archive path written when `output: tarball`. Ignored otherwise. | `image.tar` | No |
| `dry-run` | Resolve and validate the build without publishing anything (`--dry-run`). | `false` | No |
| `print-manifest` | Print the generated Kubernetes manifest after build (`--print-manifest`). | `false` | No |
| `log-level` | Logging level: `debug`, `info`, `warn`, `error`. | `info` | No |
| `version` | Pokkum CLI version to install (e.g. `v1.0.1` or `latest`). | `v1.0.1` | No |

### Known limitation: `tags`

`pokkum build` has **no `--tag`/`--tags` flag** and `.pokkum.yaml` has no tags field either — every publish is tagged `latest` (the CLI's own default tag), regardless of what `tags` is set to here. Setting `tags` to anything other than `latest` makes the action emit a `::warning::` annotation in the build log rather than silently doing nothing.

The `tags` input is kept for forward/backward compatibility and will start working once `pokkum build` gains a real tagging mechanism. Until then:

- Rely on this action's `digest` and `ref` outputs (`repo@sha256:...`) for deployments — they're immutable and don't depend on tags at all.
- If you need a human-readable tag today, apply it out-of-band after the push (e.g. `crane tag`/`docker buildx imagetools create`) using the `ref` output as the source.

---

## Outputs Reference

| Output | Description | Example |
| :--- | :--- | :--- |
| `ref` | Primary, immutable image reference with digest. | `ghcr.io/acme/app@sha256:1111111...` |
| `digest` | SHA256 digest of the published image manifest or index. | `sha256:1111111...` |

Both outputs are populated by parsing the CLI's own structured `published` log line (captured with `--log-format json`) and are empty for `dry-run` or `print-manifest` runs, since those modes stop before anything is published.

---

## Authentication & Registry Credentials

Pokkum relies on standard Docker ambient credentials stored in `~/.docker/config.json`. Use standard credential helper actions prior to calling Pokkum:

### GitHub Container Registry (GHCR)
```yaml
- name: Log in to GHCR
  uses: docker/login-action@v3
  with:
    registry: ghcr.io
    username: ${{ github.actor }}
    password: ${{ secrets.GITHUB_TOKEN }}
```

### Amazon ECR
```yaml
- name: Configure AWS Credentials
  uses: aws-actions/configure-aws-credentials@v4
  with:
    aws-region: us-east-1
    role-to-assume: arn:aws:iam::123456789012:role/my-github-role

- name: Log in to Amazon ECR
  uses: aws-actions/amazon-ecr-login@v2
```

---

## Advanced Workflow Examples

### Example 1: Pull Request Validation & Kubernetes Manifest Inspection

In pull requests, run Pokkum in `dry-run` mode with `print-manifest: true` to validate the build and preview generated Kubernetes `securityContext` defaults without publishing images:

```yaml
name: PR Verification

on:
  pull_request:
    branches: [main]

jobs:
  verify:
    runs-on: ubuntu-latest
    permissions:
      contents: read

    steps:
      - uses: actions/checkout@v4

      - name: Verify Build & Preview Manifest
        uses: CreativeBeastDesign/pokkum@v1
        with:
          repo: ghcr.io/${{ github.repository }}
          dry-run: "true"
          print-manifest: "true"
```

### Example 2: Deploying to Kubernetes with Immutable Digest Reference

Use `steps.pokkum.outputs.ref` directly in your deployment pipeline to update Kubernetes manifests with immutable digests — this is unaffected by the `tags` limitation above, since it's the digest-pinned reference, not a tag:

```yaml
      - name: Build & Push
        id: pokkum
        uses: CreativeBeastDesign/pokkum@v1
        with:
          repo: ghcr.io/${{ github.repository }}

      - name: Deploy to Kubernetes Cluster
        run: |
          kubectl set image deployment/my-svelte-app \
            app=${{ steps.pokkum.outputs.ref }}
```

### Example 3: Exporting a Local Tarball Instead of Pushing

Use `output: tarball` for workflows that need the image artifact without pushing it anywhere (e.g. to hand off to a separate scanning or signing job):

```yaml
      - name: Build to OCI Tarball
        uses: CreativeBeastDesign/pokkum@v1
        with:
          project-dir: .
          output: tarball
          tarball-path: app-image.tar

      - name: Upload Image Artifact
        uses: actions/upload-artifact@v4
        with:
          name: app-image
          path: app-image.tar
```

### Example 4: Reproducible Builds & `SOURCE_DATE_EPOCH` in CI

Pokkum automatically derives `SOURCE_DATE_EPOCH` from the last git commit timestamp (`git log -1 --pretty=%ct`), ensuring that rebuilding from the exact same git commit SHA produces byte-identical image layer digests:

```yaml
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0 # Ensure git history is available for SOURCE_DATE_EPOCH derivation

      - uses: CreativeBeastDesign/pokkum@v1
        with:
          repo: ghcr.io/${{ github.repository }}
```
