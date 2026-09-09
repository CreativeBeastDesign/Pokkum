<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: readonly-app-invariant-undocumented)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# The read-only /app invariant is stated only in a code comment

| Field | Value |
| --- | --- |
| Status | open |
| Stage | v1.2 |
| Kind | dx |
| Tier | polish |
| Area | Developer Experience |

## Summary

Every Pokkum image ships `/app` at mode 0555 with no opt-out, and the only place that says so is a comment in the packager.

## Problem

`internal/adapters/packager/layer.go` sets `fileMode = 0o555` and `dirMode = 0o555`
unconditionally. This is deliberate and correct — nothing in the image writes to the
application tree — but it is a permanent architectural fact that shapes how an application
must be written, and it appears in no `--help` text, no README, and no config reference.

It is discovered as a production 500: an app that shells out to a native tool, or writes a
cache or a temp file to a project-relative path, builds green and fails at runtime. Found
exactly that way in a real session (2026-09, a SvelteKit app invoking Typst).

A second, related trap in the same session: the operator verified the tool worked with
`docker exec`, which defaults to root and bypasses the `0555` bits via `DAC_OVERRIDE`. The
"success" said nothing about the non-root UID the app actually runs as.

## Recommendation

State it where someone hits it before writing the code, not after: a line in
[pokkum guide](agent-guide-command.md)'s invariants section, a note in `pokkum build
--help`, and an entry in `Vocabulary.md`'s runtime section. Pair it with the rule that
follows from it — runtime writes target the OS temp directory, never a project-relative
path — and with the verification note that a container must be exercised as its real UID
(`docker run -u 65532`), not through `docker exec`.

## Implementation

- [internal/adapters/packager/layer.go](../../internal/adapters/packager/layer.go)
- [cmd/pokkum/build.go](../../cmd/pokkum/build.go)
- [Vocabulary.md](../../Vocabulary.md)

## Related

- [pokkum guide — the operating manual, shipped inside the binary](agent-guide-command.md)
- [build.extra_paths — ship project directories beyond the adapter's output](extra-project-paths.md)

