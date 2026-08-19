<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: openvex-exemptions)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# OpenVEX exemptions for the CVE gate

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

`.pokkum.yaml`'s vex_exemptions lets a specific CVE bypass the --fail-on-cve threshold, but only with a real OpenVEX justification code, a mandatory expiry, and a mandatory owner.

## Flags

- `--vex-output`

## Implementation

- [internal/core/model.go](../../internal/core/model.go)
- [internal/adapters/vexutils/document.go](../../internal/adapters/vexutils/document.go)

## Known Limitations

- An unjustified or already-expired exemption entry is rejected outright at config-parse time, not silently honored — this was flagged externally as a gap (both reviewers named missing VEX support as a top-tier concern) before this shipped.
- Diff mode ("N new vulnerabilities since last build") from the original CVE-scanning concept remains unbuilt; this item covers exemption consumption only, not vulnerability-diffing.

