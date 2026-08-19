<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: static-strategy-real-fixture)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Real @sveltejs/adapter-static test fixture replaces a fictional mock

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | infra |
| Tier | foundation |
| Area | Testing & Infrastructure |

## Summary

A genuine, scaffolded-and-built @sveltejs/adapter-static project replaced a synthetic mock that had been fabricating a flat prerendered/index.html, immediately exposing four bugs the mock's own wrong assumption had been hiding.

## Implementation

- [testdata/fixtures/sveltekit-static](../../testdata/fixtures/sveltekit-static)
- [tests/integration/static_e2e_test.go](../../tests/integration/static_e2e_test.go)

## Evidence

- Commits: `0ef9dd0`, `1c33509`
- Findings: #4, #6, #7 (see [overnight-findings.md](../archive/overnight-findings.md))

## Known Limitations

- This is the sharpest instance of a recurring lesson in this codebase: a mock encoding the same wrong assumption as the code it tests can never detect the mismatch. TestFixtureDrivenE2E_Static had passed throughout, because both the mock and the production code shared the same incorrect belief that prerendered output is a flat tree — see [--strategy=static](strategy-static.md) in build-packaging.yaml for the bugs this found (findings 2, 3, 4, 6, 7).

