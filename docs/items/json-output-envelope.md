<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: json-output-envelope)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Standardized machine-readable output (--output=json)

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.2 |
| Kind | feature |
| Tier | foundation |
| Area | Developer Experience |

## Summary

`--output=json` emits a machine-readable envelope on most commands — but not on `build` or `dev`, where it is accepted and silently ignored.

## Problem

`--output` is registered as a *persistent* flag on the root command
(`cmd/pokkum/main.go`), so cobra accepts it on every subcommand and `ParseOutputFormat`
validates the value centrally. Ten commands then read it (`init`, `doctor`, `config`,
`scan`, `verify`, `explain`, `adopt`, `history`, `rollback`, `repro doctor`).

`build` and `dev` do not. Neither `cmd/pokkum/build.go` nor `cmd/pokkum/dev.go` ever
consults the resolved format, so `pokkum build --output json` exits 0 having printed
human-readable text — the flag is accepted, validated, and dropped. That is worse than
rejecting it: a caller has no signal that it asked for something it did not get.

This item was previously recorded as `status: shipped` with `impl: cmd/pokkum/build.go`,
which is the single file in the CLI that does not implement it. Corrected here rather
than left as a green row.

## Recommendation

Wire the resolved `ports.OutputFormat` through `build` and `dev` the way the other ten
commands already do. `build`'s envelope is the load-bearing one: digest, tag set,
per-platform manifest digests, output mode, layer sizes, and any warnings — the values a
caller would otherwise scrape out of logfmt.

Prerequisite for [pokkum mcp](mcp-server.md), whose `build` tool would otherwise be
text-scraping its own CLI, and for anything that branches on a build result in CI.

## Decision

`build` now emits a versioned envelope carrying the ref, digest, tags, output mode,
platforms and deploy result, and routes every error return through a shared helper so a
failure is an envelope too. In JSON mode `core.Build` is handed a nil stdout — the
mechanism `k8s.go` already used — so the pipeline's own raw ref write does not precede the
envelope. Text output is unchanged.

`dev` rejects `--output json` rather than emitting one. It streams container logs, an
interactive `--debug` shell, or an unbounded `--cluster` watch loop, so there is no point
at which a single envelope could be emitted, and a startup envelope would be interleaved
with and indistinguishable from what follows. Rejecting is the honest option; silently
ignoring was the bug.

## Flags

- `--output`

## Implementation

- [cmd/pokkum/main.go](../../cmd/pokkum/main.go)
- [cmd/pokkum/build.go](../../cmd/pokkum/build.go)
- [cmd/pokkum/dev.go](../../cmd/pokkum/dev.go)

## Known Limitations

- `dev` rejects `--output json` rather than supporting it — deliberate, since it has no single completion point to emit an envelope from.
- `build`'s envelope carries the published ref's own digest, not per-platform manifest digests, and no warnings array: neither exists on `core.BuildResult`, and inventing them would have meant a pipeline change. Documented as absent rather than faked.
- `doctor`'s failure path still drops its per-check array — see item doctor-json-drops-per-check-detail.

## Related

- [pokkum mcp — Model Context Protocol server as a second driving adapter](mcp-server.md)
- [Documented CLI exit-code table](exit-code-reference.md)
- [pokkum guide — the operating manual, shipped inside the binary](agent-guide-command.md)

