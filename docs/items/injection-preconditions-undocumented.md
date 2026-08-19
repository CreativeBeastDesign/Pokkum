<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: injection-preconditions-undocumented)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Zero-config adapter injection declined silently, with undocumented preconditions

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.1 |
| Kind | fix |
| Tier | foundation |
| Area | Developer Experience |

## Summary

Injection is advertised as automatic but engages only under two conditions, and when it declined it said nothing, so the failure read as the feature being broken.

## Problem

Zero-config adapter injection (Option B) is real, enabled by default, and works: given a
project whose `package.json` build script is exactly `vite build`, `pokkum build` writes
`.pokkum/vite.config.ts` and uses it, without touching user-authored files. Verified
empirically.

It engages only when two conditions hold, and neither was documented:

1. The adapter **package** must be installed. Injection configures an adapter; it cannot
   install one. Preflight fails first if it is neither configured nor in `package.json`.
2. `package.json`'s `build` script must be exactly `vite build`. Injection replaces the
   build invocation, so it declines when doing so would silently skip anything else the
   script does — env setup, codegen, a task runner. That guard is correct.

What was wrong is that declining was **silent**. The operator got Option C's "fix it in
vite.config.ts" with no hint that Pokkum would have done it for them under a condition they
could meet, and no way to distinguish "Pokkum cannot do this" from "Pokkum would not do this
here". Meanwhile `docs/Features.md`, `Vocabulary.md`'s `--inject` row and `pokkum adopt`'s
own help all advertised injection with no preconditions at all — `adopt` went further and
said "no on-disk edit is actually required", which for a project failing either condition
is false.

Found by running the tool on a real project, immediately after two other defects in the same
three-command sequence.

## Decision

Shipped 2026-08-19. A declined injection now names the precondition that failed and quotes
the offending build script, while still wrapping the adapter sentinel so `errors.Is`
consumers are unaffected. The preflight error additionally explains that injection rewrites
configuration but cannot install a package, which is the confusion that made a correct
message read as a bug.

Documentation corrected in all four places to state both preconditions rather than implying
injection is unconditional. `adopt`'s help now separates the configuration swap (which
injection does handle) from the package requirement (which it cannot).

Two rounds of correction were needed here, worth recording: the first fix stated only the
package precondition, and a second empirical run — adapter installed, `vite.config.ts`
present — showed injection still declining, which is what surfaced the build-script guard.
Reasoning from the code's comments alone produced a confident wrong answer twice; only
running it with each precondition satisfied in turn established what actually holds.

## Flags

- `--inject`
- `--no-inject`
- `--write-config`

## Implementation

- [internal/adapters/bunexec/compiler.go](../../internal/adapters/bunexec/compiler.go)
- [cmd/pokkum/adopt.go](../../cmd/pokkum/adopt.go)

## Related

- [pokkum init recommended a command it had guaranteed could not work](init-recommends-a-failing-command.md)
- [pokkum adopt](adopt-codemod.md)

