<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: cli-workflow-tests)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Multi-command CLI workflow tests

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.1 |
| Kind | infra |
| Tier | foundation |
| Area | Testing & Infrastructure |

## Summary

Run the real binary through command sequences in one project directory, covering whether commands work together rather than only in isolation.

## Problem

Every test in `tests/integration` drove Pokkum through its Go API. That is the right level
for most things, but it left a whole class uncovered: whether the commands work *with each
other*. `pokkum init` shipped writing an `sbom.attach` value `pokkum build` refused, so the
first two commands a new user runs were incompatible, and nothing caught it — no test ever
ran one command's output into the next. It was found by a maintainer typing the two
commands, after six waves of hardening had passed over the codebase.

Unit tests cannot close that gap by construction: they assert the pieces, and the failure
lived in the seam between two individually-correct pieces.

## Decision

Shipped 2026-08-19. `tests/integration/cli_workflow_test.go` builds the CLI once per run
and executes declared step sequences against one project directory, so each command sees
what the previous one left behind. Five workflows: init→validate→build (the reported bug),
init idempotence over a hand-edited config, a deliberately corrupted config failing with
the offending field named, `--output=json` parsing at every step, and the local-profile
round trip (profiles merge through different code than the base config).

The load-bearing assertion is the *absence* list, not the exit code. This project has no
registry configured, so `build` fails either way — what distinguishes "build rejected
init's own output" from "build wants a repo" is which error appears, so each workflow
names the config-rejection shapes that must not be present.

Fast by default: nothing needs Docker, Bun, a registry or the network, because a build that
stops at request validation still exercises the whole config load-and-validate path, which
is where the bug lived. The five workflows run in about three seconds. Steps needing a real
toolchain are gated on -short.

## Implementation

- [tests/integration/cli_workflow_test.go](../../tests/integration/cli_workflow_test.go)

## Related

- [pokkum init wrote a config pokkum build refused](init-generates-invalid-config.md)

