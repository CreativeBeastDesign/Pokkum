<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: config-schema-subcommand)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum config schema

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | dx |
| Tier | polish |
| Area | Developer Experience |

## Summary

The generated JSON Schema lives only in this repository, so the audience it was built for — someone working in their own project — cannot reach it.

## Problem

[The schema](pokkum-yaml-json-schema.md) is checked in at `schema/pokkum.schema.json`.
That serves an editor pointed at a raw GitHub URL, and nothing else. An operator or agent
working in their own SvelteKit project — the audience
[pokkum guide](agent-guide-command.md) exists for — has the binary and not the
repository, and a hardcoded URL to a branch is the version-skew problem the guide was
built to avoid, one artifact over.

A `pokkum config schema` printing the embedded schema to stdout closes that: the schema a
binary emits is by construction the schema for that binary's config parser.

## Recommendation

Print the schema to stdout, flag-free, matching `pokkum guide`'s shape.

The blocker is mechanical and worth recording, because it is the reason this was not done
alongside the generator: `go:embed` cannot reach a file above the importing package's own
directory, so `schema/pokkum.schema.json` at the repository root is not embeddable from
`cmd/pokkum`. Closing this means either making the canonical location a package directory
that embeds it and re-pointing the generator and freshness guard there, or accepting a
second generated copy — the first is right, the second recreates exactly the drift the
generator exists to prevent.

## Related

- [JSON Schema for .pokkum.yaml](pokkum-yaml-json-schema.md)
- [pokkum guide — the operating manual, shipped inside the binary](agent-guide-command.md)
- [pokkum config view / validate, build profiles](config-management.md)

