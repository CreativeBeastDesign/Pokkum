## v0.1

[x] Reproducible layer timestamps: set every layer/config timestamp to `SOURCE_DATE_EPOCH` derived from the last git commit (`git log -1 --pretty=%ct`), not build time
[x] Minimal `securityContext` in generated manifests: Default to `runAsNonRoot: true`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `seccompProfile: RuntimeDefault`.
[x] `.pokkumignore`: Explicit exclude list so `.env.local`, test fixtures, and source maps don't end up embedded
[x] Dry-run mode (`--dry-run` / `--print-manifest`): show what would be built/pushed/applied without doing it - added to `pipeline.go`
[x] Structured, leveled logging so CI logs are parseable
[x] Idempotent registry pushes: skip push if the computed digest already exists remotely (`remote.Head` before `remote.Write`)
[x] Basic config validation surfaced as clear CLI errors before any network call — fail fast, not mid-push

## v0.2

[x] Cosign signing
[x] SLSA provenance attestation
[ ] SBOM as OCI 1.1 referrer instead of `.sbom` tag convention
[ ] `pokkum dev` — build + `--local` + run in one command

## v1.0

### Supply Chain

- Cosign signing + SBOM/provenance attestation, upgraded to a trusted-builder (SLSA L3) mode via an isolated CI job
- Vulnerability gating: run Grype/Trivy against the final image in CI and fail the pipeline above a severity threshold, not just generate an SBOM and hope
- Base image digest pinning + automated update PRs (Renovate/Dependabot-style) so distroless/Chainguard bumps don't silently drift out of control

### Cluster-side hardening

- `NetworkPolicy` generation restricting egress (compiled binary shouldn't need arbitrary outbound access) and ingress limited to expected ports
- Resource `requests`/`limits` and a `PodDisruptionBudget` by default in generated manifests, not left to the user to remember
- `readOnlyRootFilesystem: true` where feasible — supervisor + compiled binary likely don't need to write to disk at runtime, so this is a strong default rather than aspirational
- Secrets via `envFrom` referencing Kubernetes `Secret`/external-secrets, never baked into image layers or ConfigMaps

### Build integrity

- Hermetic builds: no network egress during the actual compile step (vendor/cache dependencies beforehand), so a compromised registry or npm package can't inject anything mid-build — this is a hard SLSA L3 requirement, not just nice-to-have
- Multi-registry support with per-registry auth chains (you already get this mostly free from `authn`'s keychain composition) so the same pipeline works across ECR/GCR/ACR/self-hosted without code changes
- A local, ephemeral test registry (`pkg/registry.New()`) wired into CI for integration tests of the full compile→push→manifest-rewrite path — catches regressions no unit test with fakes will

### Operational maturity

- Version pinning of `pokkum` itself in generated manifest annotations, so you can trace which tool version produced a given deployment when debugging
- A GitHub Action wrapping the CLI (mirroring `setup-ko`), since that's the dominant invocation context and hand-wiring it in raw YAML each time is friction that can be removed once
- Rollback support: since you're rewriting k8s manifests with new digests, keep the previous digest recoverable (e.g., a `pokkum rollback` reading from your own build history) rather than relying purely on `kubectl rollout undo`.
