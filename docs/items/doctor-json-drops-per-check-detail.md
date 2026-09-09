<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: doctor-json-drops-per-check-detail)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# doctor --output json loses which check failed, and exited 0 when red

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.2 |
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

## Decision

Both paths now build one `ports.JSONEnvelope` carrying the full checks array, following
`scan.go`'s existing pattern for partial-failure commands rather than adding a third shape.

A second, worse bug surfaced while fixing it, and the framing of this item was part of why
it had gone unnoticed: the item said "the exit code stays as it is". It should not have.
`pokkum doctor --output json` on a red project printed `status:"error"`, `passed:false`
and every failing check — and exited **0**, while text mode exited 1 on the identical
project. `runDoctor` has one failure signal at the end of the function and the JSON branch
returned before reaching it. A `set -e` CI step reads only the exit status, so a gate on
`pokkum doctor --output json` passed while doctor was failing. It also contradicted
[the exit-code table](exit-code-reference.md) published the same day, which says exit 1
covers a red doctor with no format caveat.

Fixed by returning a `silentExitError` from the JSON branch, so the process exits 1
without a second log line that would add nothing and corrupt the JSON stream.
`TestDoctorExitStatusIsIndependentOfOutputFormat` runs one failing fixture through both
formats and requires their error-ness to match. Logged in `Lessons.md` with
`mem:self_review_checklist` row 73: an output-format flag selects a serialization and must
never change the exit status, the verdict, or which side effects ran.

## Implementation

- [cmd/pokkum/doctor.go](../../cmd/pokkum/doctor.go)

## Related

- [Standardized machine-readable output (--output=json)](json-output-envelope.md)
- [pokkum doctor does not check the effective SvelteKit adapter](doctor-effective-adapter-check.md)
- [pokkum mcp — Model Context Protocol server as a second driving adapter](mcp-server.md)
- [Documented CLI exit-code table](exit-code-reference.md)

