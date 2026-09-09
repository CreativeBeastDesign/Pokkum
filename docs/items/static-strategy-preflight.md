<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: static-strategy-preflight)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum build preflight for strategy: static

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.1 |
| Kind | hardening |
| Tier | foundation |
| Area | Developer Experience |

## Summary

`pokkum build --strategy=static` refuses before it starts when the project has code SvelteKit cannot prerender, listing every offending file.

## Problem

[Static-viability analysis](static-viability-analyzer.md) shipped as advice only, reached
from `pokkum init`. A project that adds a `+server.ts` with a POST handler a month after
init still discovered the incompatibility from SvelteKit's own error, minutes into a build,
with no mention of Pokkum or the strategy setting that caused it.

## Decision

Shipped the recommended option: refuse by default, with `--allow-server-code-in-static`
(and `build.allow_server_code_in_static`) as the escape hatch. `ports.StaticViabilityAnalyzer`
is injected at the composition root, wrapping `sveltekitutils` in a concrete
`internal/adapters/staticviability` adapter, since `internal/core` must not import an
adapter — the shape `ports.EnvBakeDetector` and `ports.RouteFilter` already use.

**Building this found that the classifier it depends on was wrong**, which is the part worth
remembering. Run over the repo's own committed fixtures, it called
`testdata/fixtures/sveltekit-basic` blocked — a fixture `tests/integration/static_e2e_test.go`
builds with `StrategyStatic`. Reading `@sveltejs/kit`'s source rather than trusting the
classification showed two false positives (every `+server.*`, and every server `load`), both
fixed before the gate landed on top. A hard gate over an over-aggressive classifier would
have been Lessons.md's 2026-08-17 entry a second time, as a build failure rather than advice.

Three limits, each deliberate:

- `StrategyStatic` only; every other strategy ships a server.
- Refuses on a `blocked` verdict ONLY. An `unknown` verdict — unreadable project, no routes
  directory, a layout the scan does not understand — is the absence of evidence and never
  fails a build. This is exactly what 2026-08-17 got wrong.
- Overridable, because the scan is a heuristic over source text and nobody should wait for a
  Pokkum release to ship around a bug in it.

The nil-analyzer skip in core is a fail-open that exists for the ~16 test callers
constructing `core.Deps` directly. `cmd/pokkum`'s
`TestCompositionRootWiresTheStaticViabilityGate` asserts the real composition root always
supplies one, because nothing in `internal/core` can see its own composition root — deleting
that one line would otherwise disable the gate for every real build with the whole suite
still green.

## Flags

- `--allow-server-code-in-static`

## Implementation

- [internal/ports/staticviability.go](../../internal/ports/staticviability.go)
- [internal/adapters/staticviability/staticviability.go](../../internal/adapters/staticviability/staticviability.go)
- [internal/core/pipeline.go](../../internal/core/pipeline.go)
- [internal/core/staticgate_test.go](../../internal/core/staticgate_test.go)
- [cmd/pokkum/build.go](../../cmd/pokkum/build.go)
- [cmd/pokkum/staticgate_wiring_test.go](../../cmd/pokkum/staticgate_wiring_test.go)

## Known Limitations

- Source-text heuristic, not a SvelteKit build. It cannot see a handler assembled dynamically, re-exported from another module, or generated at build time.
- Does not implement SvelteKit's root `+server.js` non-HTML-response rule (prerender.js:539), which depends on the response value rather than on the file's shape.
- Dynamic route segments are still not reported; adapter-static cannot crawl an unlinked `[slug]`, and detecting that needs link analysis rather than a per-file scan.

## Related

- [Static-viability analysis (does this project need a server?)](static-viability-analyzer.md)
- [pokkum init](workspace-init-wizard.md)

