<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: scoped-secret-allow-annotations)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Scoped secret-allow annotations

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | backlog |
| Kind | feature |
| Tier | polish |
| Area | Build & Packaging |

## Summary

--allow-secret-pattern is a global regex; an inline pokkum:allow-secret comment gives a known-safe line the scoped exemption it actually needs.

## Decision

Shipped 2026-08-19, the inline-comment half. A line carrying a comment that contains
`pokkum:allow-secret` — on the line itself or the one directly above — is skipped. The marker
is matched as a substring, so `//`, `#`, `/* */` and `<!-- -->` all work with no per-language
knowledge in the scanner.

Prompted by a real false positive: a sanitizer whose job is redacting secrets was flagged for
containing the literal `password: '[REDACTED]'` in its replacement strings. Three findings in
one function, all of them the tool matching its own redaction placeholders.

Why a marker rather than a pre-filled regex, which is what was asked for: an allow pattern
matches a whole *line*, so generating one means printing the line — and if the finding is a
genuine secret rather than a placeholder, that suggestion is the secret, in a log and then in
a committed config file. The scanner cannot tell those apart, so suggesting a pattern built
from content would leak by default in exactly the case that matters. A marker describes
nothing.

Why not a file:line suppression, the other option considered: line numbers shift, so the
exemption silently moves to whatever code arrives at that line — the reporter identified this
objection themselves. A marker travels with the line it exempts.

Both mechanisms are named in the failure output, because neither covers everything: a marker
cannot be added to generated output, so a minified bundle carrying redaction strings compiled
from annotated source still needs `security.allow_secret_patterns` (which already existed and
was merely undiscoverable). Verified on the real case: three findings to zero via markers, and
three to zero via a `\[REDACTED\]` pattern.

The `.secretguardignore` variant is deliberately not implemented. Path-scoped exemption is
the blunter tool, and patterns already cover the generated-output case that motivated it.
Scope is deliberate: an exemption is line-scoped, covering its own line and the one below,
proven by a test that marking one redaction line leaves the other two flagged.

Threat model, stated rather than implied: anyone who can add the marker can add the secret
itself, so it grants no new capability. It records intent; it is not a boundary.

## Flags

- `--allow-secret-pattern`

