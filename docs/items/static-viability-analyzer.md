<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: static-viability-analyzer)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Static-viability analysis (does this project need a server?)

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.1 |
| Kind | feature |
| Tier | foundation |
| Area | Developer Experience |

## Summary

Scans a project's routes for server-side code and reports what rules a static build out, feeding `pokkum init`'s strategy default.

## Problem

`pokkum init` offered `layered` / `static` as a free choice and validated nothing. Nothing
anywhere in the codebase looked at `src/routes` at all: choosing `static` on a project with
a `+server.ts` failed deep inside the SvelteKit build, with SvelteKit's message rather than
Pokkum's, long after the choice was made. Picking correctly required the user to already
know the answer, which is the opposite of what a setup step is for.

## Decision

Shipped as `sveltekitutils.AnalyzeStaticViability`, a DISQUALIFIER scan. It answers "does
anything here rule static out?" soundly and does not attempt the converse, which is not
decidable from source: a load function hitting a runtime-only API, or a dynamic route with
no inbound links for the crawler, passes the scan and fails the build. The verdict wording
and `pokkum init`'s output both say so explicitly.

Blockers: `+server.*`, `+page.server.*` and `+layout.server.*` (each retired by an
`export const prerender = true` in the same file), form actions (`export const actions`,
unconditional — a POST handler cannot be prerendered, so a `prerender = true` alongside it
does not silence the finding), `*.remote.ts|js` using `query()`/`form()`/`command()`, and
an explicit `export const prerender = false` anywhere. A remote module using only
`prerender()` is build-time data and is not a blocker. `hooks.server.*` is reported as a
caveat, not a blocker: server hooks do run during prerendering, so calling them
disqualifying would wrongly reject projects that build today.

Three verdicts, not two. `unknown` (could not scan) is distinct from `viable` (scanned,
found nothing) and carries the reason, so an unreadable or non-SvelteKit directory can
never produce the reassuring answer — the fail-open shape logged three times on
2026-08-21. `FilesScanned` is reported for the same reason at the other end: a walk that
matched nothing must not read as a clean project.

Matching is done on comment-stripped, string-blanked source via the scanner already in
`project.go`, never a whole-file regex. Both halves of that have burned this repo before
(2026-08-16 `fallback:` in a comment, 2026-08-17 `sveltekit(` in a string literal), and a
commented-out `export const prerender = false` silently forcing `layered` would be the same
bug a third time.

## Implementation

- [internal/adapters/sveltekitutils/staticviability.go](../../internal/adapters/sveltekitutils/staticviability.go)
- [internal/adapters/sveltekitutils/staticviability_test.go](../../internal/adapters/sveltekitutils/staticviability_test.go)
- [cmd/pokkum/init_analysis.go](../../cmd/pokkum/init_analysis.go)

## Known Limitations

- A sound negative only. `viable` means nothing found rules static out, never that a static build will succeed.
- Dynamic route segments are not reported at all yet; adapter-static cannot crawl an unlinked `[slug]` route, and detecting that needs link analysis rather than a per-file scan.
- Advisory only. `pokkum build` does not yet refuse `strategy: static` on a project this scan calls blocked — see item static-strategy-preflight.

## Related

- [pokkum init](workspace-init-wizard.md)
- [pokkum init detects bun vs node, and the base image that carries it](init-runtime-detection.md)
- [pokkum build preflight for strategy: static](static-strategy-preflight.md)

