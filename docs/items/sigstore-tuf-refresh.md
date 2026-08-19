<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: sigstore-tuf-refresh)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Sigstore TUF trust-root refresh

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | hardening |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

The embedded Sigstore trust root is regenerated from a TUF-verified fetch and can refresh live; a nightly CI job now catches it silently rotting again.

## Flags

- `--sigstore-tuf-refresh`
- `--sigstore-trusted-root`
- `--hermetic`

## Implementation

- [internal/adapters/sigstore/tufrefresh.go](../../internal/adapters/sigstore/tufrefresh.go)
- [internal/adapters/sigstore/trustedroot.go](../../internal/adapters/sigstore/trustedroot.go)
- [internal/adapters/sigstore/trustedroot_freshness.go](../../internal/adapters/sigstore/trustedroot_freshness.go)

## Evidence

- Commits: `9188d56`, `eeaa83a`, `a86baa3`
- Findings: #10 (see [overnight-findings.md](../archive/overnight-findings.md))

## Known Limitations

- The embedded snapshot was not merely stale when found — it was already actively rejecting valid signatures on the `log2025-1` Rekor shard (live since 2025-09-23) as forgeries, indistinguishable from a real attack from the verifier's own error text.
- `--sigstore-tuf-refresh`'s `Offline` mode is bound to `--hermetic` on `pokkum build`; `pokkum verify` has no hermetic concept, so it always allows the refresh attempt and falls back to the embedded snapshot with a warning on failure.
- An explicit `--sigstore-trusted-root` always wins and skips the refresh branch entirely; `pokkum base update`/`base check` never set `VerifySignature`, so the flag is deliberately absent there.

