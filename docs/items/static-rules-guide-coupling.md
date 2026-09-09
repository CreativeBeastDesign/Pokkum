<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: static-rules-guide-coupling)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Nothing couples the guide's prerender-rule table to the classifier

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | dx |
| Tier | polish |
| Area | Developer Experience |

## Summary

`pokkum guide strategy` restates which constructs rule out a static build; the classifier's rules are matchers rather than an enumerable set, so no test can prove the two still agree.

## Problem

The guide's `strategy` section lists what disqualifies `strategy: static`. Its verdicts are
guarded — `TestGuideNamesEveryStaticVerdict` asserts the section names every
`ports.StaticVerdict` — but the rules themselves are not, because they live as matchers
inside the analyser rather than as an enumerable set of constants. `ports.StaticFinding`
carries `Reason` as a free-form sentence fragment.

That is exactly the shape `mem:self_review_checklist` row 70 exists for: rules encoding
another tool's behaviour, restated in a second place, with nothing forcing them to agree.
The classifier itself already got two of four SvelteKit rules wrong once (2026-09-09), so a
prose copy drifting from it is not hypothetical — and a copy inside the binary is more
dangerous than one in a markdown file, since a reader has every reason to trust it.

Mitigated but not closed: the section now tells the reader `pokkum init`'s output is
authoritative for the version they are running and names the specific file and finding, so
a stale table degrades to an out-of-date summary rather than a confident wrong answer.

## Recommendation

Give the analyser an enumerable rule identity — a `StaticBlockerKind` alongside the
free-form `Reason` — and assert the guide names each kind, the way the verdict guard
already works. That is worth doing for its own sake regardless of the guide:
`--output json` consumers currently have to string-match `Reason` to tell one blocker
from another.

## Implementation

- [internal/ports/staticviability.go](../../internal/ports/staticviability.go)
- [cmd/pokkum/guide.go](../../cmd/pokkum/guide.go)
- [cmd/pokkum/guide_test.go](../../cmd/pokkum/guide_test.go)

## Related

- [pokkum guide — the operating manual, shipped inside the binary](agent-guide-command.md)
- [Static-viability analysis (does this project need a server?)](static-viability-analyzer.md)
- [Standardized machine-readable output (--output=json)](json-output-envelope.md)

