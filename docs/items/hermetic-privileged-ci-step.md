<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: hermetic-privileged-ci-step)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Hermetic Mount Isolation tests in privileged CI container

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | infra |
| Tier | foundation |
| Area | Testing & Infrastructure |

## Summary

The `--hermetic-mount-isolation` security control is exercised by end-to-end tests that bind-mount isolation barriers over hermetic-sensitive paths; these tests cannot run on unprivileged GitHub-hosted ubuntu runners, so they run in a privileged Docker container in the e2e-real-build CI job, alongside real-build integration tests.

## Decision

Shipped 2026-08-19. The control `--hermetic-mount-isolation` is a build-sandbox security feature
that uses bind-mount masking to prevent layer assembly from accessing sensitive paths. Its two
end-to-end tests (`TestPrepare_HermeticMountIsolation_EndToEnd` and
`TestHermeticMountIsolation_BlocksPathSocketAccess` in `internal/adapters/bunexec`) must execute
as root or in a privileged container to bind-mount the isolation barriers.

GitHub-hosted ubuntu runners do not grant mount privileges. Setting
`kernel.apparmor_restrict_unprivileged_userns=0` was attempted but did not help — the sysctl
applied cleanly, yet the bind-mount still returned EPERM, indicating the blocker is the runner's
mount privileges generally, not AppArmor's unprivileged-userns restriction specifically.

Resolution: The `e2e-real-build` CI job now includes a step named "Hermetic Sandbox Tests
(privileged container)" that runs `docker run --rm --privileged ... golang:1.26 go test
-count=1 -run 'Hermetic' ./internal/adapters/bunexec/`. This brings the control from "skipped
everywhere" to "tested 14/14 with 0 skips". The tests pass on the ephemeral GitHub-hosted VM
inside a privileged container, and they also pass locally on developer machines (verified in
rootless and privileged Linux Docker environments). The skip path remains as a fallback for
developer systems that cannot run privileged containers.

The accepted risk: CI is triggered on `pull_request` (see `.github/workflows/ci.yml` on: block),
so a fork PR's code executes inside a privileged container. The user explicitly chose to accept
this. Rationale: fork PRs already run arbitrary code on an ephemeral GitHub-hosted VM with a
read-only token, and that job holds no credentials or secrets; --privileged adds container-escape
reach, but the VM is disposable and has no durable state. No net increase in blast radius.

## Implementation

- [.github/workflows/ci.yml](../../.github/workflows/ci.yml)
- [internal/adapters/bunexec/hermetic_reexec_linux_test.go](../../internal/adapters/bunexec/hermetic_reexec_linux_test.go)

## Evidence

- Commits: `2b9da12`, `0339ec6`

## Known Limitations

- Fork PRs execute code in a privileged container. Revisit if: (a) any secret or credential is ever added to the e2e-real-build job, (b) a self-hosted runner becomes available that can grant the mount capability without --privileged, or (c) GitHub runners ever permit unprivileged-userns bind-mounts.
- The sysctl kernel.apparmor_restrict_unprivileged_userns=0 was diagnosed as the blocker and attempted; it did not help, which is documented in the commit message to save future investigation of the same dead end.

