<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: static-strategy-preflight)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum build preflight for strategy: static

| Field | Value |
| --- | --- |
| Status | open |
| Stage | v1.2 |
| Kind | hardening |
| Tier | foundation |
| Area | Developer Experience |

## Summary

Reject `strategy: static` before the build starts when the project has server-side code, instead of failing deep inside SvelteKit's build.

## Problem

`static-viability-analyzer` shipped as advice only, reached from `pokkum init`. A project
that adds a `+server.ts` a month after init still discovers the incompatibility from
SvelteKit's own error, minutes into a build, with no mention of Pokkum's strategy setting —
which is the failure this analysis exists to prevent, merely moved later in the project's
life rather than removed.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Hard failure in core validation | core.Build refuses the request when the analysis returns blocked. | Strongest signal and earliest possible. But the scan is a sound negative with no proof of completeness, so a false positive would block a build that works today — and it needs a new ports interface, since internal/core cannot import sveltekitutils directly. |
| Warning at preflight, build continues | Log the blockers and proceed. | No false-positive risk, but a warning in a long build log is close to no warning at all. |
| Hard failure with an explicit opt-out flag | Refuse by default; `--allow-server-code-in-static` (or a config key) overrides. | Keeps the strong signal while leaving an escape hatch for a false positive. Costs one more flag on an already-large surface. |

## Recommendation

Option 3. The analysis is a sound negative — every blocker it reports is genuinely
incompatible with adapter-static — so the false-positive risk is low enough to justify
failing closed, and an opt-out costs one flag against a class of failure that currently
surfaces as somebody else's error message.

Needs a `ports.StaticViabilityAnalyzer` interface wrapping `sveltekitutils`, injected at the
composition root, because `internal/core` must not import an adapter — the same shape
`ports.EnvBakeDetector` and `ports.RouteFilter` already use.

## Related

- [Static-viability analysis (does this project need a server?)](static-viability-analyzer.md)

