<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: generic-container-tooling-pitfalls)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Generic container-tooling pitfalls stay in the guide, not in the CLI

| Field | Value |
| --- | --- |
| Status | wont-do |
| Kind | dx |
| Tier | non-goal |
| Area | Developer Experience |

## Summary

A field report surfaced several real footguns that belong to Docker and bun rather than to Pokkum; they are documented in `pokkum guide` and deliberately not turned into checks.

## Decision

A from-scratch deploy session (2026-09) produced a list of things that cost real time.
Four of them are not Pokkum's:

- `bun add <pkg>@next` records a resolved version in `package.json` rather than preserving
  the literal `next`, so the dependency stops floating. That is bun's documented behaviour.
- Docker Desktop reporting as "running" is not the same as its daemon being up; `open -a
  Docker` returns immediately either way, so readiness needs a `docker info` poll.
- `docker buildx`'s default driver cannot do multi-platform builds; that needs a
  `docker-container` driver. Only relevant when hand-building a custom base image.
- `docker exec` runs as root and bypasses the image's read-only file modes via
  `DAC_OVERRIDE`, so verifying a container through it proves nothing about the real
  non-root process.

Only the last is stated in [pokkum guide](agent-guide-command.md), and only because it
produces a false *positive* about a Pokkum-specific invariant — someone concludes their
app can write where it cannot. The other three are recorded here rather than acted on:
turning general Docker and bun behaviour into Pokkum preflight checks would mean owning a
growing surface of other tools' semantics, drifting silently as those tools change, for
failures their own error messages already describe.

Stated as a decision rather than left as a silent omission, the same way
[plugin-system](plugin-system.md) is.

## Related

- [pokkum guide — the operating manual, shipped inside the binary](agent-guide-command.md)
- [npm-distributed plugin system](plugin-system.md)
- [pokkum doctor](doctor-preflight.md)

