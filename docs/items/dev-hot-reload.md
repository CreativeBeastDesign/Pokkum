<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: dev-hot-reload)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum dev (container-parity hot reload)

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Developer Experience |

## Summary

Builds the image, loads it into the local Docker/Podman daemon, and rebuilds on source changes so local iteration exercises the same runtime the production image ships.

## Problem

The container-mode watch/rebuild loop reused a single buffered result channel across container
generations: when a rebuild killed the previous container, its goroutine's now-irrelevant
"signal: killed" write landed in the same channel the next generation's select read from, so
the loop reported a false "container exited with error" and returned — the flagship dev loop
could not survive a single file save. Fixed by giving each generation its own channel, passed
in as a parameter rather than captured, so a superseded generation's write lands in a channel
nobody reads.

## Flags

- `--debug`
- `--port`
- `--watch`
- `--env-file`
- `--platform`
- `--bun-binary`
- `--bun-variant`
- `--bun-version`

## Implementation

- [cmd/pokkum/dev.go](../../cmd/pokkum/dev.go)

## Evidence

- Commits: `1f8e5bf`

## Related

- [pokkum dev --no-container](no-container-dev-mode.md)
- [pokkum dev --cluster](cluster-dev-loop.md)

