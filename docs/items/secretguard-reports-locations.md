<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: secretguard-reports-locations)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Secret-guard findings reported a count with no locations

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.1 |
| Kind | fix |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

A failing build said how many secrets it found and nothing about where, so the finding could not be acted on.

## Problem

`runSecretScan`'s failure was `detected %d hardcoded secret(s) in %s` and nothing more. An
operator was told their build contained four secrets and given no file, no line, no rule —
the one thing needed to do anything about it.

The data was already there and discarded: `ports.SecretMatch` carries `FilePath`,
`LineNumber`, `RuleName` and `SecretSnippet`. The adjacent skipped-files branch already
listed paths, so this was an internal inconsistency as much as a gap: an incomplete scan told
you which files it could not read, while a successful scan that found secrets would not tell
you which files they were in.

Reported by a maintainer running a real build and asking where the secrets were.

## Decision

Shipped 2026-08-19. Each finding is logged as its own structured line — file, line, rule,
stage — before the error is returned, and the error points at them.

**Values are deliberately redacted.** `SecretSnippet` is the matched substring, i.e. the
secret; echoing it to make the report useful would copy the value into terminal scrollback,
CI logs and anything scraping build output, which is a poor trade for a tool whose purpose is
keeping secrets out of places they do not belong. `file:line` plus the rule name is enough to
act on, and the rule name describes the shape matched without revealing the value. The field
is emitted as `value=redacted` rather than omitted, so someone wondering why can see it was a
decision.

Structured per-finding lines rather than a multi-line error string: slog quotes an error
value, so embedded newlines reach the terminal as a literal `\n` and the list would read
worse than the single line it replaced. This was caught by looking at real output rather than
at the code.

Listing is capped at 10 with the withheld count reported — a minified bundle is one logical
line and every rule reports every match on it, so hundreds of findings are possible, and
silently truncating would understate the problem. The error names
`--allow-secret-pattern=<regex>` as the escape hatch for a false positive.

## Flags

- `--allow-secret-pattern`

## Implementation

- [internal/core/pipeline.go](../../internal/core/pipeline.go)

## Related

- [Secret-inlining guard (secretguard)](secret-inlining-guard.md)
- [`--strategy=exe` secret-scanning gap](exe-secret-scan-gap.md)

