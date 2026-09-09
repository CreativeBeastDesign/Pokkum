<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: init-prints-resolved-base-refs)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum init names the presets but not what they resolve to

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | dx |
| Tier | polish |
| Area | Developer Experience |

## Summary

The base-image prompt explains each preset in prose; discovering that `distroless` means `gcr.io/distroless/cc-debian12:nonroot` still requires a separate `pokkum base update`.

## Problem

[init-base-image-recommendations](init-base-image-recommendations.md) gave each preset a
one-line rationale, which fixed the "three bare names" problem. It did not print the
reference. Anyone who needs the concrete image — building a compatible custom base is the
obvious case, since the extra binary must match the preset's libc — has to run `pokkum base
update` to find out.

The values are already constants (`DistrolessBaseRef`, `ChainguardBaseRef` and the
Node-carrying equivalent in `internal/ports/baseimage.go`), so this is a formatting change,
not a lookup.

## Recommendation

Append the resolved reference to each preset line in the prompt and to `init --output json`.
Cheap, and it removes a whole detour for the custom-base case.

## Implementation

- [internal/ports/baseimage.go](../../internal/ports/baseimage.go)
- [cmd/pokkum/init.go](../../cmd/pokkum/init.go)

## Related

- [pokkum init explains the base image presets instead of listing them](init-base-image-recommendations.md)
- [--base accepts a custom image reference](base-flag-custom-reference.md)
- [pokkum guide — the operating manual, shipped inside the binary](agent-guide-command.md)

