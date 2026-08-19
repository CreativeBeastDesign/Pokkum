<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: no-container-dev-mode)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum dev --no-container

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | moat |
| Area | Developer Experience |

## Summary

Runs the project's own dev server directly on the host, skipping image construction entirely, for the fastest possible local iteration loop.

## Problem

The default container-parity dev loop goes through full image construction and a Docker/Podman
daemon on every change — table stakes, not a differentiator, and both external reviews
independently flagged the dev loop as the weakest part of the day-to-day experience.
`--no-container` closes the cheap half of that gap: it does not layer a second, Pokkum-owned
watch/rebuild loop on top of what Vite/SvelteKit's own HMR already does better, it simply gets
out of the way and runs `bun run dev` directly.

## Flags

- `--no-container`

## Implementation

- [cmd/pokkum/dev.go](../../cmd/pokkum/dev.go)

## Evidence

- Commits: `18f056c`

## Known Limitations

- No supervisor, no startup attestation, no health/readiness probes, no base image, and no non-root user — a single startup warning states this explicitly and the default remains full container-parity mode so nobody debugs a production discrepancy against a mode never meant to model it.
- `--debug`, `--platform`, `--bun-version`, and `--bun-variant` are rejected outright rather than silently ignored, since each describes a property of an image that is never built.
- `--port` and `--watch` warn (rather than reject) when explicitly set, since the dev server picks its own port and hot reload is inherent rather than opt-in.

