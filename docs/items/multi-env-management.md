<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: multi-env-management)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Multi-environment management

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | dx |
| Tier | foundation |
| Area | Kubernetes & Operations |

## Summary

Deferred: staging/production config templating and secret-manager integration are real needs, but a large surface most teams already solve at the CI/CD layer.

## Problem

`pokkum config env` plus `--env=staging`-style overrides and secure secret injection from
1Password/Vault/AWS Secrets Manager would require config templating plus one or more
secret-manager SDK integrations. Real, but most teams already solve environment-specific
config and secret injection at the CI/CD layer rather than wanting their image builder to own
it. Revisit if repeatedly requested rather than build speculatively.

