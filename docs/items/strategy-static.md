<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: strategy-static)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# --strategy=static

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Build & Packaging |

## Summary

Compiles a pure static SvelteKit site onto chainguard/static with an embedded pokkum-static Go file server as PID 1 — genuinely functional only since 2026-08-19, after six independent bugs were found by its first real boot test.

## Flags

- `--strategy=static`
- `--static`

## Implementation

- [internal/adapters/staticserver](../../internal/adapters/staticserver)
- [supervisor/cmd/pokkum-static/main.go](../../supervisor/cmd/pokkum-static/main.go)
- [supervisor/cmd/pokkum-static/server.go](../../supervisor/cmd/pokkum-static/server.go)
- [internal/adapters/bunexec/compiler.go](../../internal/adapters/bunexec/compiler.go)
- [testdata/fixtures/sveltekit-static](../../testdata/fixtures/sveltekit-static)

## Evidence

- Commits: `8306d37`, `1c33509`, `5693980`, `61fd873`
- Findings: #2, #3, #4, #6, #7, #12 (see overnight-findings.md)

## Known Limitations

- Had never worked in any prior release before 2026-08-19: both HTTP servers bound no Addr and silently fell back to port 80 (finding 2, fixed 8306d37); Preflight rejected every real adapter-static project because it hard-coded a bun/node adapter check (finding 3); real prerendered output nests under pages/dependencies/data while the code assumed a flat tree (finding 4); there was no <path>.html fallback so every non-root prerendered route 404'd (finding 7); and the embedded pokkum-static blob was gitignored and absent from every CI job and released binary until 5693980 (finding 6). All fixed in 1c33509/5693980, proven against a real @sveltejs/adapter-static fixture rather than the synthetic mock that had been encoding the same wrong flat-tree assumption.
- Conditional GET (If-None-Match -> 304) was missing until 61fd873 (finding 12) — every request re-downloaded the full body even when the client already held the current copy.
- Its own Cache-Control contract (immutable /_app/immutable, no-cache version.json/prerendered HTML) is genuinely tested here (server_test.go, integration_test.go) — see [Cache-Control contract, tested](cache-control-contract.md) for why the layered/exe strategies don't have the equivalent.

