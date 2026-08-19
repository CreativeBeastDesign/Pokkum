<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: console-log-rendering)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Human-readable console output for build logs

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.1 |
| Kind | dx |
| Tier | polish |
| Area | Developer Experience |

## Summary

Build progress rendered with level glyphs and aligned attribute blocks on a terminal, while piped and CI output stays byte-identical logfmt.

## Problem

Every build line was raw logfmt: a full RFC3339 timestamp with a timezone offset, then
`level=`, then `msg=` in quotes, then six `key=value` pairs on one 200-column line. The
timestamp alone consumed about a third of the width, pushing the message past where the eye
lands, and a record like `build starting` — projectDir, repo, platforms, output, dryRun,
printManifest — was a wall rather than something scannable.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Add a colour library (fatih/color or similar) | Use an established library for styling and terminal capability detection. | Ergonomic, but a new module dependency in a project whose stated identity is zero-dependency, to emit escape sequences four bytes long. |
| Hand-rolled slog handler with ANSI and glyphs | A custom slog.Handler emitting level glyphs, a bold message and attributes either inline or as an aligned indented block. | About 200 lines to own, including the slog.Handler contract (WithAttrs/WithGroup, concurrency) which is easy to get subtly wrong — but no new dependencies, and golang.org/x/term is already direct since pokkum init uses it to decide whether to prompt. |

## Recommendation

Hand-rolled. The dependency cost is the wrong trade for escape sequences, and the handler contract is testable.

## Decision

Shipped 2026-08-19, hand-rolled, zero new dependencies.

The split that makes it safe: the renderer is reached only when stderr is an interactive
terminal. Anything piped, redirected or captured in CI keeps the original logfmt byte for
byte, because build logs are parsed by other programs and prettier-but-unparseable would be a
bad trade. `--log-format` gained `auto` (the new default, doing that detection), `console`
(force the human view even when piped, for `2>&1 | less -R`) and kept `text` (force logfmt
even on a terminal — what scripts should pin) and `json`.

Severity is a **glyph** rather than a hue, so `NO_COLOR`, `TERM=dumb` and a colour-blind
reader all still get a legible line; both keep the layout and drop only the escape sequences.
Timestamps are omitted interactively — the widest, least useful column when a human is
watching — and retained in logfmt for everything that needs them.

Attributes go inline when they fit and become an aligned indented block when they do not,
measured against the real terminal width (read once at construction, not per record) rather
than a fixed budget: a fixed number gets one case or the other wrong, exploding a list of
similar findings into five lines each on a wide terminal or wrapping everything on a narrow
one. Widths are counted in runes, since these messages contain em dashes and typographic
quotes.

Tested for what a hand-written handler most easily breaks: attributes bound via `With()`
being dropped, groups not prefixing keys, one derived handler's attributes leaking into a
sibling's through a shared backing array, and interleaved writes — the build fans out over
platforms and logs from several goroutines, so the concurrency test runs under `-race`.

## Flags

- `--log-format`

## Implementation

- [cmd/pokkum/consolelog.go](../../cmd/pokkum/consolelog.go)
- [cmd/pokkum/main.go](../../cmd/pokkum/main.go)

## Related

- [Secret-guard findings reported a count with no locations](secretguard-reports-locations.md)
- [Standardized machine-readable output (--output=json)](json-output-envelope.md)

