<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: doctor-json-drops-per-check-detail)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# doctor --output json loses which check failed

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | fix |
| Tier | polish |
| Area | Developer Experience |

## Summary

A red `pokkum doctor --output json` returns a summary and an error code, without the per-check array the green path includes — so a machine consumer cannot tell what actually failed.

## Problem

`runDoctor` emits its results two different ways. The success path writes a full envelope
including the `checks` array, so each check's name, verdict, message and remediation
survive. The failure path goes through the shared error writer instead, which carries a
summary string and a code and nothing else.

The result is backwards: the JSON consumer gets structured detail exactly when everything
passed and nothing needed reading, and gets an opaque summary in the one case where it
needed to know which check was red and what the remediation was. A caller has to re-run in
text mode and parse prose to recover what the command already had in hand.

Found while adding the effective-adapter check
([doctor-effective-adapter-check](doctor-effective-adapter-check.md)) — that check's
whole value is the remediation string it carries, which is the field the JSON failure path
drops.

## Recommendation

Emit the same envelope shape on both paths, with the checks array populated either way and
a top-level status distinguishing them. The exit code stays as it is; this is about what
accompanies it.

Related to [json-output-envelope](json-output-envelope.md): the point of a machine
format is that a failure is as parseable as a success, and both halves of that are needed
before [pokkum mcp](mcp-server.md)'s diagnose tool would have anything to return.

## Implementation

- [cmd/pokkum/doctor.go](../../cmd/pokkum/doctor.go)

## Related

- [Standardized machine-readable output (--output=json)](json-output-envelope.md)
- [pokkum doctor does not check the effective SvelteKit adapter](doctor-effective-adapter-check.md)
- [pokkum mcp — Model Context Protocol server as a second driving adapter](mcp-server.md)
- [Documented CLI exit-code table](exit-code-reference.md)

