<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: doctor-effective-adapter-check)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum doctor does not check the effective SvelteKit adapter

| Field | Value |
| --- | --- |
| Status | open |
| Stage | v1.2 |
| Kind | fix |
| Tier | polish |
| Area | Developer Experience |

## Summary

`checkSvelteKitWorkspace` confirms `@sveltejs/kit` is a dependency and stops there — an `adapter-auto` project passes doctor cleanly and fails at build.

## Problem

`bunexec` already refuses a build whose `svelte.config.js` does not reference the adapter
the strategy requires, and the message is a good one: it names `adapter-auto` explicitly,
says a fresh `sv create` project ships it, and gives the exact `bun add -D` and import to
write (`internal/adapters/bunexec/compiler.go`, added 2026-08-16).

`pokkum doctor` knows none of this. `checkSvelteKitWorkspace` (`cmd/pokkum/doctor.go`)
reads `package.json`, finds `@sveltejs/kit`, and reports "valid SvelteKit project
detected". So the command whose entire purpose is "tell me what is wrong before I build"
passes the single most common starting state that cannot build.

That inverts doctor's contract. The preflight is the safety net; doctor is supposed to be
the early warning, and here the net catches what the warning missed.

## Recommendation

Extend `checkSvelteKitWorkspace` — or add a sibling check — to resolve the effective
adapter the same way the build preflight does, and report the same remediation text.
Reuse the preflight's logic rather than restating it, so the two cannot drift into
disagreeing about what counts as configured.

A post-build assertion that the adapter actually produced its entrypoint is a *different*
guard covering a different failure (adapter correctly configured, build silently emitted
nothing) and is worth considering alongside, but it does not substitute for this one.

## Implementation

- [cmd/pokkum/doctor.go](../../cmd/pokkum/doctor.go)
- [internal/adapters/bunexec/compiler.go](../../internal/adapters/bunexec/compiler.go)

## Related

- [pokkum doctor](doctor-preflight.md)
- [pokkum adopt](adopt-codemod.md)
- [pokkum guide — the operating manual, shipped inside the binary](agent-guide-command.md)

