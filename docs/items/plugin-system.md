<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: plugin-system)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# npm-distributed plugin system

| Field | Value |
| --- | --- |
| Status | wont-do |
| Kind | dx |
| Tier | non-goal |
| Area | Developer Experience |

## Summary

Will not be built: an npm-based extension model would undercut the exact supply-chain hardening story Pokkum exists to provide.

## Decision

The DX case for a `pokkum-plugin-*` ecosystem (community plugins for custom adapters and
preprocessors, an esbuild/Vite-style marketplace) is real, but an npm-based extension model
directly undercuts Pokkum's own hardening story — this is a supply-chain-security tool that
would be inviting the exact npm supply-chain risk its own `secretguard`, hermetic-build, and
CVE-gate features exist to mitigate. Stated as a decision, not a silent omission.

