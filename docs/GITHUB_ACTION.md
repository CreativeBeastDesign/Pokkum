# Pokkum GitHub Action Guide

The official **Pokkum GitHub Action** (`CreativeBeastDesign/pokkum`) enables fast, reproducible, and hardened container builds for SvelteKit and Bun applications directly inside GitHub Actions CI/CD workflows.

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
          tags: ${{ github.sha }},latest
          platforms: linux/amd64,linux/arm64

      - name: Output Pushed Image Reference
        run: |
          echo "Pushed Image Ref: ${{ steps.pokkum.outputs.ref }}"
          echo "Image Digest:     ${{ steps.pokkum.outputs.digest }}"
```

---

## Inputs Reference

| Input | Description | Default | Required |
| :--- | :--- | :--- | :--- |
| `project-dir` | Path to the SvelteKit project directory. | `.` | No |
| `repo` | Destination container repository (e.g. `ghcr.io/acme/app`). Can also be supplied via `POKKUM_DOCKER_REPO` env variable. | `""` | No |
| `tags` | Comma-separated list of image tags (e.g. `latest,v1.0.0`). | `latest` | No |
| `platforms` | Comma-separated target platforms (e.g. `linux/amd64,linux/arm64`). | `linux/amd64` | No |
| `output` | Destination output mode (`push`, `local`, `tarball`). | `push` | No |
| `dry-run` | Perform dry-run without side effects (`true`/`false`). | `false` | No |
| `print-manifest` | Print generated hardened Kubernetes manifest after build (`true`/`false`). | `false` | No |
| `log-level` | Logging level (`debug`, `info`, `warn`, `error`). | `info` | No |
| `version` | Pokkum CLI version to install (e.g. `v0.1.1` or `latest`). | `v0.1.1` | No |

---

## Outputs Reference

| Output | Description | Example |
| :--- | :--- | :--- |
| `ref` | Primary, immutable image reference with digest. | `ghcr.io/acme/app@sha256:1111111...` |
| `digest` | SHA256 digest of the published image manifest or index. | `sha256:1111111...` |

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

Use `steps.pokkum.outputs.ref` directly in your deployment pipeline to update Kubernetes manifests with immutable digests:

```yaml
      - name: Build & Push
        id: pokkum
        uses: CreativeBeastDesign/pokkum@v1
        with:
          repo: ghcr.io/${{ github.repository }}
          tags: ${{ github.sha }}

      - name: Deploy to Kubernetes Cluster
        run: |
          kubectl set image deployment/my-svelte-app \
            app=${{ steps.pokkum.outputs.ref }}
```

### Example 3: Reproducible Builds & `SOURCE_DATE_EPOCH` in CI

Pokkum automatically derives `SOURCE_DATE_EPOCH` from the last git commit timestamp (`git log -1 --pretty=%ct`), ensuring that rebuilding from the exact same git commit SHA produces byte-identical image layer digests:

```yaml
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0 # Ensure git history is available for SOURCE_DATE_EPOCH derivation

      - uses: CreativeBeastDesign/pokkum@v1
        with:
          repo: ghcr.io/${{ github.repository }}
```
