<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: paas-deploy-targets)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum deploy (Dokploy, SwiftWave)

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Developer Experience |

## Summary

Hands a pushed image straight to a self-hosted PaaS control plane, so a `deploy:` block in `.pokkum.yaml` replaces a hand-written CI deploy step.

## Problem

Pokkum's output is a plain OCI image, so every PaaS that pulls from a registry was already a
valid target — but the deploy step itself was left to the operator. A `deploy:` block in
`.pokkum.yaml` closes that: a successful push calls the platform's own API, and `pokkum deploy`
does the same standalone for a redeploy or a retry.

The engineering content is not sending the request, it is classifying the response. Both
platforms answer HTTP 200 for outcomes that are not deployments. SwiftWave's redeploy webhook
returns `200 OK - No rebuild` whenever the posted body does not contain the application's own
configured image reduced to `owner/name`, so a status-code check reports a deploy that changed
nothing as a success; Pokkum posts the pushed references as the body and treats that string as
a failure. Dokploy's `application.saveDockerProvider` is a full overwrite whose input schema
requires all five fields, so it rewrites `username`/`password`/`registryUrl` on every call —
`update_image` therefore defaults off, carries explicit pull credentials when enabled, and
warns when it clears stored ones.

## Flags

- `--no-deploy`
- `--image`

## Implementation

- [cmd/pokkum/deploy.go](../../cmd/pokkum/deploy.go)
- [internal/core/deploy.go](../../internal/core/deploy.go)
- [internal/ports/deploy.go](../../internal/ports/deploy.go)
- [internal/adapters/deploy/deploy.go](../../internal/adapters/deploy/deploy.go)
- [internal/adapters/deploy/dokploy.go](../../internal/adapters/deploy/dokploy.go)
- [internal/adapters/deploy/swiftwave.go](../../internal/adapters/deploy/swiftwave.go)

## Known Limitations

- SwiftWave cannot be repointed at a new image reference: both its webhook and its `rebuildApplication` mutation rebuild the application's current deployment, so the application must be pinned to a mutable tag that Pokkum republishes. `update_image` is rejected for that target rather than silently ignored.
- Only a registry push deploys. `--local`, `--tarball` and `--to-oci-layout` leave nothing a remote PaaS can pull, so auto-deploy is skipped with a warning naming the output mode.
- Vercel and other edge/serverless platforms remain out of scope — they do not run OCI images, which is the existing non-goal stated in README.md.
- The two platform contracts were verified against Dokploy's and SwiftWave's own source rather than their prose docs, but they are third-party APIs and can drift; the adapters fail closed on any response they cannot positively identify as a started rollout.

