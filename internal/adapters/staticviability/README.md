# `internal/adapters/staticviability`

Implements `ports.StaticViabilityAnalyzer`. The package is deliberately thin:
`Adapter.AnalyzeStaticViability` checks `ctx.Err()` and otherwise delegates
straight to `sveltekitutils.AnalyzeStaticViability`, which does the actual
source scan. This package exists only to keep that filesystem work behind the
hexagonal boundary — `internal/core` depends on `ports.StaticViabilityAnalyzer`,
never on `sveltekitutils` directly.

## What it answers

Given a project directory, whether the source contains anything SvelteKit
refuses to prerender: `export const prerender = false`, a route that mixes
`+page` and `+server`, a body-dependent `+server` handler, `+page.server`
form actions, or a `*.remote` module calling a server-only helper. The scan
also reports non-blocking caveats (`hooks.server` running only at prerender
time, a dynamic route with no `entries()`). See
`internal/ports/staticviability.go` for the full `StaticVerdict` /
`StaticBlockerKind` vocabulary and `docs/items/static-viability-analyzer.md`
for the analysis in prose.

A cancelled context yields `StaticUnknown` rather than threading cancellation
through the walk — the scan reads a bounded set of small route sources and
finishes in milliseconds, so there is nothing worth interrupting mid-walk.

## Where it's wired in

`cmd/pokkum/build.go` constructs `staticviability.NewAdapter()` and passes it
into `core.BuildDeps`. `internal/core/pipeline.go`'s `Build` calls it only for
`StrategyStatic`, and only a `StaticBlocked` verdict can fail the build —
`StaticUnknown` (analysis couldn't run) is logged and never blocks, and
`--allow-server-code-in-static` (`ports`/`core` field
`AllowServerCodeInStatic`, config key `build.allow_server_code_in_static`)
downgrades a block to a warning.

`pokkum init`'s static-viability reporting (`cmd/pokkum/init_analysis.go`)
does **not** go through this adapter or the port — it calls
`sveltekitutils.AnalyzeStaticViability` directly, since `init` has no need for
the ctx-cancellation wrapper or the hexagonal boundary this package exists to
enforce for `core.Build`. The two call sites still can't disagree about a
project's verdict, because both bottom out in the same `sveltekitutils`
function; this package is on only one of the two paths.

## Testing

The classifier's behavior — the part with real edge cases — is tested where
it lives, in `internal/adapters/sveltekitutils/staticviability_test.go`.
`internal/core/staticgate_test.go` exercises the gate in `pipeline.go` against
a stub `ports.StaticViabilityAnalyzer`, not this package.

`staticviability_test.go` covers the one piece of logic that exists only
here: the `ctx.Err()` check ahead of the delegation. It was previously
uncovered — the stub in `staticgate_test.go` never constructs this package,
and the classifier's own tests never wrap it — which mattered because of the
direction it fails in. Drop the check and a cancelled build scans anyway and
returns `StaticBlocked`, turning "the build was cancelled" into "this project
cannot build statically": a different and far more alarming claim than the
truth. The test asserts the cancelled path yields `StaticUnknown`, no
blockers, and a populated `UndecidedWhy`, and carries a premise check so a
fixture that stopped producing a decisive verdict cannot make it pass by
comparing two `StaticUnknown`s.
