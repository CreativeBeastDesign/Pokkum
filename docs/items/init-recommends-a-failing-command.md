<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: init-recommends-a-failing-command)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum init recommended a command it had guaranteed could not work

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.1 |
| Kind | fix |
| Tier | foundation |
| Area | Developer Experience |

## Summary

init always ended with "you can now run pokkum build", which fails immediately for the local-only setup its own first prompt invites.

## Problem

`pokkum init`'s first prompt offers the registry as "empty for local only". Accepting that
leaves no destination repository, and `pokkum build` defaults to push mode, so it refused to
start:

  validation failed: destination repository is required in push mode

init nevertheless closed with the constant `You can now run \`pokkum build\``. It was
recommending a command it had just guaranteed could not work, for the option it had just
offered as reasonable.

Reported from a real first run immediately after the generated-config fix, and the same
class one level up: advice is output too, and output the next command rejects is a defect.
`pokkum build --local` was the command that worked all along — it clears configuration
entirely and reaches genuine preflight (a missing `@sveltejs/adapter-node`).

## Decision

Shipped 2026-08-19. The closing line is derived from the config init actually wrote, or
loaded when one already exists, so re-running init on a configured project gives advice
matching that project rather than the defaults. With no registry it recommends
`pokkum build --local` and explains that a registry is what plain `pokkum build` needs; with
one configured it recommends `pokkum build` as before. The recommendation is also exposed as
`next_command` in the JSON envelope so scripted callers get the same answer as humans.

Guarded by a workflow test that reads the recommendation out of init's own JSON output and
runs it, rather than hardcoding a command — so it follows the advice wherever it goes. It
asserts not that the command succeeds (impossible without a JS toolchain) but that it never
fails for a configuration or usage reason.

## Implementation

- [cmd/pokkum/init.go](../../cmd/pokkum/init.go)
- [tests/integration/cli_workflow_test.go](../../tests/integration/cli_workflow_test.go)

## Related

- [pokkum init wrote a config pokkum build refused](init-generates-invalid-config.md)
- [Multi-command CLI workflow tests](cli-workflow-tests.md)

