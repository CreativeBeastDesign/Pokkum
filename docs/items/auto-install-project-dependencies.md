<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: auto-install-project-dependencies)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Install the project's dependencies when a checkout has none

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.2 |
| Kind | fix |
| Tier | foundation |
| Area | Build & Packaging |

## Summary

`pokkum build` relied on `node_modules` already existing — true on every developer machine, false on every fresh CI checkout — so the GitHub Action Quickstart failed on its first run for a real project.

## Problem

The SvelteKit build (`bun x vite build` / `bun run build`) resolves `vite`, `@sveltejs/kit`
and the adapter from the project's own `node_modules`, and nothing installed it. A real
`sv create`-shaped project (build tooling entirely in `devDependencies`) followed
`docs/GITHUB_ACTION.md`'s Quickstart exactly — checkout, registry login, the action — and
failed with `[UNRESOLVED_IMPORT] Could not resolve '@sveltejs/kit/vite'` from
`.pokkum/vite.config.ts`. `rm -rf node_modules && pokkum build . --local` reproduced it;
`bun install --frozen-lockfile` first fixed it completely.

Three things let it ship green. `--dry-run` returns before Prepare runs, so a dry run of an
uninstalled project looks clean. Every live test symlinks a real `node_modules` back into
its scratch copy, and every fake-bun test never needed one, so no test ran Prepare against a
project without dependencies. And the action's own CI self-test ran the very
`bun install --frozen-lockfile` step the Quickstart never told anyone to add.

## Decision

Shipped 2026-09-12. `Prepare` installs the project's own dependencies when
`<project>/node_modules` is absent: `bun install --no-save`, plus `--frozen-lockfile` when
a lockfile is present (a missing lockfile warns and installs unpinned, the same stance the
vendor install takes). `--no-save` keeps `package.json` and the lockfile untouched — the
only thing written into the user's tree is `node_modules` itself.

The trigger is presence, not a content-hash stamp of the kind the vendor install keeps.
An existing tree is the user's or their CI cache's: rewriting it on every build would make
Pokkum the thing that clobbers a `bun link`, a patched package or a restored cache, and
would write through the symlinked `node_modules` this repo's own harnesses use. A tree that
is present but stale fails exactly as `bun run build` would have.

This is a separate install from the production vendor step, and stays that way: that one
stages a production-only tree under `.pokkum/vendor` for the image; this one populates the
project's tree, devDependencies included, and never ships. It runs synchronously ahead of
the vendor dispatch — the build needs the tree before it starts, and a warm Bun cache is
what makes the concurrent vendor install fast.

`--hermetic` is unchanged and never installs: Preflight already refuses a hermetic build
without `node_modules`, now through the same `projectNodeModulesPresent` decision function
the install gate uses. `--no-install` turns the install into a fail-fast
`core.ErrDependenciesMissing`. A failed install is `core.ErrDependencyInstallFailed`,
framed as Pokkum's own attempt with bun's stderr attached — never reported as a build
failure, because the build never ran.

Proven three ways: fake-bun tests that go red with the gate removed, a real
`bun install` + real Vite build against an uninstalled copy of the adapter-node fixture,
and the action self-test job, which now stages the fixture without any install step so it
exercises the exact incident shape on every run.

## Implementation

- [internal/adapters/bunexec/project_install.go](../../internal/adapters/bunexec/project_install.go)
- [internal/adapters/bunexec/compiler.go](../../internal/adapters/bunexec/compiler.go)
- [cmd/pokkum/build.go](../../cmd/pokkum/build.go)

## Known Limitations

- A stale but present `node_modules` (a dependency added to `package.json` but not installed) is deliberately not detected; the build fails with Vite's own unresolved-import error, as it would outside Pokkum.
- A monorepo member whose dependencies are hoisted to the workspace root has no local `node_modules`, so Pokkum runs `bun install` there on every build; bun resolves the workspace and the install is a fast no-op, but it is an install nonetheless.

## Related

- [Ship production dependencies so images are self-contained](vendor-production-dependencies.md)
- [Hermetic build mode (--hermetic)](hermetic-build-mode.md)

