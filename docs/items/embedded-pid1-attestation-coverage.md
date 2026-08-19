<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: embedded-pid1-attestation-coverage)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Embedded PID-1 binaries brought under CI attestation

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | hardening |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

pokkum-init and pokkum-static are now built by CI/releases and freshness-checked, closing the gap where every image's PID 1 was a developer-laptop binary outside the attested pipeline.

## Implementation

- [internal/adapters/staticserver/blob_freshness_test.go](../../internal/adapters/staticserver/blob_freshness_test.go)
- [Makefile](../../Makefile)

## Evidence

- Commits: `5693980`, `a86baa3`, `81a6fb6`
- Findings: #6 (see [overnight-findings.md](../archive/overnight-findings.md))

## Known Limitations

- The two embedded blobs are gitignored build artifacts (only `.gitkeep` is tracked), so `make check-embedded-blobs` guards local working-tree staleness specifically — CI itself is structurally safe since it always rebuilds both blobs from the checked-out commit before any test runs.
- This closes the gap described in the finding, not a hypothetical: for a supply-chain tool, the one component that had been running as PID 1 in every produced image, outside the CLI's own SLSA-attested build, was the sharpest edge found during that run.

