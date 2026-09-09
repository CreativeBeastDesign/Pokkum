<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: config-schema-subcommand)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum config schema

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.2 |
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

## Decision

Shipped as `pokkum config schema`, flag-free, writing the document to stdout so
redirection covers every use a `--out` flag would.

The `go:embed` constraint this item recorded as the blocker turned out to have a third
resolution neither of the two it named: `go:embed` cannot reach a file *above* the
importing package, but a package sitting *beside* the file works. `schema/embed.go`
makes `schema/` a package, so `schema/pokkum.schema.json` stays exactly where it is.
That mattered more than it first appears — the path is the URL an editor's YAML plugin is
pointed at, so it is a public contract, and moving it to suit an implementation detail
would have broken every editor config the moment it shipped. The originally-recommended
option (re-point the generator at a package directory) would have done exactly that.

The embedded bytes are the checked-in file read at build time, so the binary cannot ship
a schema disagreeing with the repository's; the only drift possible is between that file
and `internal/ports/config.go`, which `make check-schema-freshness` already guards.

`TestConfigSchemaPrintsTheCheckedInSchema` asserts the output is the file verbatim and
parses as a schema. What it really guards is the command around bytes that are already
correct by construction: that it stays wired, and that nobody later re-indents it, wraps
it in an envelope or prepends a banner — each of which breaks redirect-to-a-file silently
while still looking like a schema. Shown red by prepending one comment line.

## Related

- [JSON Schema for .pokkum.yaml](pokkum-yaml-json-schema.md)
- [pokkum guide — the operating manual, shipped inside the binary](agent-guide-command.md)
- [pokkum config view / validate, build profiles](config-management.md)

