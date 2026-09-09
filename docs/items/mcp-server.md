<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: mcp-server)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum mcp — Model Context Protocol server as a second driving adapter

| Field | Value |
| --- | --- |
| Status | awaiting-decision |
| Stage | backlog |
| Kind | feature |
| Tier | polish |
| Area | Developer Experience |

## Summary

Expose analysis, planning and build as MCP tools plus the guide as an MCP resource, for agents without a shell — deliberately not a one-tool-per-command wrapper.

## Problem

An agent in Claude Code, Cursor or a similar harness already has a shell, and for those
[pokkum guide](agent-guide-command.md) closes most of the gap on its own: the failures
are knowledge failures, not invocation failures. An MCP server that shells out to
`pokkum build` for such an agent is a slower `Bash` with a schema in front of it.

Two things it would genuinely add:

- **Structure the CLI does not emit.** Chiefly a side-effect-free analysis result — the
  detection [init already runs](static-viability-analyzer.md) and
  [the runtime detection](init-runtime-detection.md), returned as data with per-finding
  `file:line` evidence, without writing `.pokkum.yaml`. There is no `--dry-run` path to
  that today; the analysis only exists as a side effect of a command that writes files.
- **Reach for agents with no shell** (hosted sandboxes, CI-side assistants). For those MCP
  is not a convenience, it is the only transport.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Do nothing beyond `pokkum guide` | Ship the guide, point agents at the CLI, and treat MCP as unnecessary indirection over a tool that is already a good CLI. | Cheapest and possibly correct. Leaves shell-less agents unserved, and leaves the analysis reachable only as a side effect of a writing command. |
| Analysis-only server | `pokkum mcp` exposing one read-only `analyze` tool plus the guide and config schema as MCP resources. No build, no deploy, no writes. | Small, safely pre-approvable (no side effects), and delivers the part Bash genuinely cannot. Does not help a shell-less agent actually build. |
| Full intent-level server | `analyze`, `plan` (resolved config with provenance, and what a build would produce), `build` (structured result, typed errors carrying a remediation field), `diagnose`. | Serves shell-less agents end to end. Costs a second surface to keep in step with the CLI, and is blocked on json-output-envelope, exit-code-reference and config-view-provenance. |
| One MCP tool per CLI command | Mechanical 1:1 mapping of the cobra command tree. | Rejected in drafting. It is a worse shell: same text output, same untyped errors, plus a schema layer and a round trip. |

## Recommendation

Sequence it behind the prerequisites rather than deciding it now. Ship
[pokkum guide](agent-guide-command.md) first; it is independently justified and is most
of the value. Then close the four items that are already open on their own merits and
happen to be exactly this server's foundation —
[json-output-envelope](json-output-envelope.md) for `build`/`dev`,
[exit-code-reference](exit-code-reference.md),
[pokkum-yaml-json-schema](pokkum-yaml-json-schema.md) and
[config-view-provenance](config-view-provenance.md). Revisit the server only once a
shell-less consumer actually exists; building it speculatively repeats the reasoning
[hooks-system](hooks-system.md) was deferred on.

If built, two constraints are already settled by this codebase's own shape. It belongs
**in this binary** as `pokkum mcp` over stdio — not a separate npm package — because the
zero-dependency Go posture is the product, and an out-of-band package reintroduces exactly
the version skew that motivates putting the guide in the binary. And architecturally MCP is
a *driving* adapter over the same core, precisely as the cobra CLI is: it must not become a
second path into the pipeline with its own semantics, and
`internal/architecture_test.go`'s boundary rules apply to it unchanged.

## Related

- [pokkum guide — the operating manual, shipped inside the binary](agent-guide-command.md)
- [Standardized machine-readable output (--output=json)](json-output-envelope.md)
- [Documented CLI exit-code table](exit-code-reference.md)
- [JSON Schema for .pokkum.yaml](pokkum-yaml-json-schema.md)
- [pokkum config view value provenance](config-view-provenance.md)
- [Stable Go library API](stable-go-library-api.md)
- [Pre/post-build shell hooks](hooks-system.md)

