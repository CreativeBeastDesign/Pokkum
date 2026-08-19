<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: cli-docs-invariant-tests)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# CLI/docs drift as a mechanical test failure

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | infra |
| Tier | foundation |
| Area | Testing & Infrastructure |

## Summary

Five new tests check Vocabulary.md and action.yml against the CLI's real flag/env-var surface in both directions, in the spirit of internal/architecture_test.go, and found three genuine drifts on their first run.

## Implementation

- [cmd/pokkum/flags_docs_test.go](../../cmd/pokkum/flags_docs_test.go)
- [cmd/pokkum/envvar_docs_test.go](../../cmd/pokkum/envvar_docs_test.go)
- [cmd/pokkum/actionyml_test.go](../../cmd/pokkum/actionyml_test.go)

## Evidence

- Commits: `548c0e1`

## Known Limitations

- Found and fixed on first run: POKKUM_LOG_LEVEL (read by both PID-1 binaries) was undocumented, --write-config on adopt was undocumented, and Vocabulary.md claimed a verify --rebuild flag that does not exist (the real behavior is rebuild-by-default with --no-rebuild to opt out).
- The same commit closed six merged-but-unvalidated .pokkum.yaml config fields, including profiles.<name>.output, which nothing had validated before.

