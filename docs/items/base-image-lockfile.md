<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: base-image-lockfile)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Base image lockfile (pokkum.lock) and audit (pokkum base check)

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

pokkum.lock pins base image digests across multi-platform indexes and tracks scan metadata; pokkum base check audits that state without touching the network.

## Flags

- `pokkum base check`
- `pokkum base update`

## Implementation

- [internal/adapters/lockfileutils/lockfile.go](../../internal/adapters/lockfileutils/lockfile.go)
- [cmd/pokkum/base.go](../../cmd/pokkum/base.go)

## Known Limitations

- Every custom `--base` reference currently shares one lockfile slot rather than getting its own — see [Per-ref pokkum.lock slot for custom --base images](custom-base-lock-slot.md).

## Related

- [Per-ref pokkum.lock slot for custom --base images](custom-base-lock-slot.md)
- [Base image CVE build gate](base-image-cve-gate.md)

