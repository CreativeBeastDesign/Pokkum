<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: multi-registry-auth)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Multi-registry authentication (--registry-config)

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Kubernetes & Operations |

## Summary

Shells out to `docker-credential-*` binaries (ECR, GCR, OSXKeychain) with in-memory caching, falling back to static `auths` blocks.

## Flags

- `--registry-config`

## Implementation

- [internal/adapters/registryutils/keychain.go](../../internal/adapters/registryutils/keychain.go)

