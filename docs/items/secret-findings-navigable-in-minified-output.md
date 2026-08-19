<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: secret-findings-navigable-in-minified-output)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Secret findings gave no usable location in minified output, and build artifacts were scanned as source

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.1 |
| Kind | fix |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

A finding in a 44 KB single-line chunk reported only line 3, and that chunk was a generated build artifact being scanned during the pre-build source stage.

## Problem

Two defects surfaced by one real finding in
`build/client/_app/immutable/chunks/lZKtnC6z.js`, reported at "line 3" of a 44 KB file with
three lines.

**The location was unusable.** Minified output is one logical line tens of kilobytes long, so a
line number points at the whole file. Redacting the value — correct as a default, since for a
genuine finding it is the credential — left the operator with nothing to act on at all in
exactly the case where reading the file by hand is hardest.

**A build artifact was scanned as source.** `build/` is `@sveltejs/adapter-node`'s output
directory. The pre-build *source* scan was reading a previous run's generated artifacts, which
are not inputs to this build, and whose shipped equivalent is scanned separately afterwards.
`.svelte-kit` was already skipped for precisely this reason; `build/` was missed, and neither
the default ignore patterns nor `pokkum init`'s `.pokkumignore` excluded it.

## Decision

Shipped 2026-08-19, three changes.

`ports.SecretMatch` gained `Column`, the 1-based byte offset within the line, reported as
`col=` on every finding. That makes a minified finding navigable — editors take a line:column
jump, `cut -c` reaches it from a shell — while revealing none of the matched text. This is the
fix for the actual problem, and it leaks nothing.

`--show-secret-values` reveals the matched text, off by default. It exists because a false
positive in minified output cannot be judged from a location alone, and the operator asking is
on their own machine looking at their own code. The redaction notice names the flag, so the
capability is discoverable rather than hidden; the flag's help and Vocabulary both say never to
set it in CI, where it would copy real credentials into build logs.

`build` is added to `pokkum init`'s default `.pokkumignore`, with a comment saying why and
that it should be deleted if the project keeps real source there. Deliberately not hardcoded
into the scanner's skip list alongside `.svelte-kit`: `build/` is a conventional name, not a
guaranteed one, and a project that keeps source under it would be silently unscanned with no
way to tell. An ignore file entry is visible and removable.

## Flags

- `--show-secret-values`
- `--allow-secret-pattern`

## Implementation

- [internal/ports/secretguard.go](../../internal/ports/secretguard.go)
- [internal/adapters/secretguard/guard.go](../../internal/adapters/secretguard/guard.go)
- [internal/core/pipeline.go](../../internal/core/pipeline.go)
- [cmd/pokkum/init.go](../../cmd/pokkum/init.go)

## Related

- [Secret-guard findings reported a count with no locations](secretguard-reports-locations.md)
- [Scoped secret-allow annotations](scoped-secret-allow-annotations.md)

