# Concept: Pokkum Base Image Lockfile (`pokkum.lock`)

## 1. Problem Statement & Motivation

Currently, when running `pokkum build` without specifying an explicit digest in `--base` (e.g. using `--base distroless` or default options), Pokkum resolves the tag (`gcr.io/distroless/cc-debian12:nonroot`) against the remote registry at build time and records the resolved digest (`sha256:...`) into the image metadata.

While this provides **observability** (you can inspect which base digest was used for a specific build), it does **not enforce reproducibility across builds**. A CI run executed today and another run executed next month on the exact same commit will pull different base image digests if `gcr.io/distroless/cc-debian12:nonroot` has been updated upstream.

To guarantee true **reproducibility by default**, Pokkum needs a persistent lock mechanism similar to `bun.lockb`, `package-lock.json`, or `Cargo.lock`.

---

## 2. Lockfile Specification (`pokkum.lock`)

### Format & Location
* **File Name**: `pokkum.lock` (human-readable JSON or YAML, saved at the root of the SvelteKit project directory).
* **Git Version Control**: Tracked in `git` alongside `package.json` and `bun.lockb`.

### Lockfile Schema (`pokkum.lock`)
```json
{
  "version": 1,
  "updated_at": "2026-08-10T20:45:00Z",
  "bases": {
    "distroless": {
      "ref": "gcr.io/distroless/cc-debian12:nonroot",
      "digest": "sha256:e96397368a514d35e19...123",
      "pinned_ref": "gcr.io/distroless/cc-debian12@sha256:e96397368a514d35e19...123",
      "updated_at": "2026-08-10T20:45:00Z"
    },
    "chainguard": {
      "ref": "cgr.dev/chainguard/glibc-dynamic:latest",
      "digest": "sha256:a1b2c3d4e5f6...789",
      "pinned_ref": "cgr.dev/chainguard/glibc-dynamic@sha256:a1b2c3d4e5f6...789",
      "updated_at": "2026-08-10T20:45:00Z"
    }
  }
}
```

---

## 3. CLI Workflow & Resolution Logic

### `pokkum build` (Default Behavior)
1. **Lockfile Exists & Contains Preset/Ref**:
   - Pokkum reads `pokkum.lock`.
   - If the requested base image preset (e.g., `distroless`) or tag reference exists in `pokkum.lock`, Pokkum **uses the locked digest directly** without hitting the registry to re-resolve the tag.
   - Eliminates network round-trips for base image tag resolution.
   - Guarantees 100% deterministic, bit-for-bit reproducible builds across environments and time.

2. **Lockfile Missing or Preset Unlocked**:
   - Pokkum queries the remote registry for the tag.
   - Resolves the current digest (`sha256:...`).
   - Automatically creates or updates `pokkum.lock` with the newly resolved digest.
   - Prompts/logs: `[INFO] Locked base preset "distroless" to sha256:... in pokkum.lock`.

3. **Explicit CLI Overrides**:
   - `--base gcr.io/distroless/cc-debian12@sha256:112233...`: Passing an explicit digest on CLI overrides the lockfile for that build run.
   - `--offline`: Strictly enforces using `pokkum.lock` without making any network calls. If the locked image is not in local cache or lockfile is missing, fails fast.

---

## 4. Bumping Base Images (`pokkum base update`)

To update base images to their latest upstream releases, users explicitly invoke lock maintenance commands:

```bash
# Update all locked base images to latest upstream digests
pokkum base update

# Update only distroless preset
pokkum base update --preset distroless

# Check for available base image updates without modifying pokkum.lock
pokkum base check
```

### CLI Command Options

| Command | Action |
|---|---|
| `pokkum base update` | Queries registry for latest tag digests, updates `pokkum.lock`, and prints diff. |
| `pokkum base check` | Checks if upstream digests have changed and reports available updates. |
| `pokkum build --update-base` | One-shot flag to update the lockfile during build. |

---

## 5. Benefits

1. **True Reproducibility by Default**: Two developers or CI runners building git commit `abc123` 6 months apart produce the exact same OCI image digest.
2. **Offline & Air-gapped Builds**: Once `pokkum.lock` exists and base layers are cached locally, `pokkum build` can operate entirely offline.
3. **Explicit Supply-Chain Audit Trail**: Base image updates become visible Git diffs in PRs when `pokkum.lock` is updated, making security dependency updates explicit and testable.
