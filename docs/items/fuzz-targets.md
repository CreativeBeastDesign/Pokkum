<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: fuzz-targets)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Fuzz targets across parsers and codecs

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | infra |
| Tier | foundation |
| Area | Testing & Infrastructure |

## Summary

Fuzz targets now exercise DSSE PAE encoding, ignore-pattern matching, Cosign payloads, asset-overlay merge logic, Bun SHASUMS parsing, scanner version comparison, and the static file server — none of these existed before.

## Implementation

- [internal/adapters/dsse/pae_fuzz_test.go](../../internal/adapters/dsse/pae_fuzz_test.go)
- [internal/adapters/ignoreutils/matcher_fuzz_test.go](../../internal/adapters/ignoreutils/matcher_fuzz_test.go)
- [internal/adapters/cosign/payload_fuzz_test.go](../../internal/adapters/cosign/payload_fuzz_test.go)
- [internal/adapters/assetoverlay/assetoverlay_fuzz_test.go](../../internal/adapters/assetoverlay/assetoverlay_fuzz_test.go)
- [internal/adapters/bunruntime/shasums_fuzz_test.go](../../internal/adapters/bunruntime/shasums_fuzz_test.go)
- [internal/adapters/scanner/version_fuzz_test.go](../../internal/adapters/scanner/version_fuzz_test.go)
- [supervisor/cmd/pokkum-static/server_fuzz_test.go](../../supervisor/cmd/pokkum-static/server_fuzz_test.go)

## Evidence

- Commits: `e6e4746`

