<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: json-output-envelope)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Standardized machine-readable output (--output=json)

| Field | Value |
| --- | --- |
| Status | in-progress |
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

## Flags

- `--output`

## Implementation

- [cmd/pokkum/main.go](../../cmd/pokkum/main.go)
- [cmd/pokkum/build.go](../../cmd/pokkum/build.go)
- [cmd/pokkum/dev.go](../../cmd/pokkum/dev.go)

## Known Limitations

- Shipped and working on `init`, `doctor`, `config`, `scan`, `verify`, `explain`, `adopt`, `history`, `rollback` and `repro doctor`; only `build` and `dev` are outstanding.

## Related

- [pokkum mcp — Model Context Protocol server as a second driving adapter](mcp-server.md)
- [Documented CLI exit-code table](exit-code-reference.md)
- [pokkum guide — the operating manual, shipped inside the binary](agent-guide-command.md)

