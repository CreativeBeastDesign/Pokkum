<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: embedded-blob-freshness-guard)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Embedded PID-1 binary freshness guard

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | infra |
| Tier | foundation |
| Area | Testing & Infrastructure |

## Summary

make check-embedded-blobs rebuilds pokkum-init and pokkum-static fresh from source with the exact Makefile flags and byte-compares them against what is actually embedded, catching local staleness a from-scratch CI build structurally cannot hit.

## Implementation

- [internal/adapters/staticserver/blob_freshness_test.go](../../internal/adapters/staticserver/blob_freshness_test.go)
- [Makefile](../../Makefile)

## Evidence

- Commits: `a86baa3`

## Known Limitations

- Must run with -count=1: go test's result cache cannot see through an exec'd go build into another package's source, so a stale cached result would otherwise report clean.
- Deliberately not added to the PR-gate CI job — CI always rebuilds both blobs from the checked-out commit before any test runs, so this specific staleness is structurally impossible there. It exists for the local working-tree hazard, which is exactly where it was first found live (concurrent commits had moved HEAD past the last local rebuild).

