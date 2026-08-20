<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: walk-callback-symlink-toctou)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Root-scoped filesystem APIs in filepath.Walk callbacks (gosec G122)

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | infra |
| Tier | polish |
| Area | Testing & Infrastructure |

## Summary

Five filepath.Walk/WalkDir callbacks operate on the walked path directly, which gosec G122 flags as symlink-TOCTOU-prone; os.Root would make the containment structural rather than incidental.

## Problem

Upgrading to golangci-lint v2 enabled gosec's G122 check, which flags a filesystem
operation inside a `filepath.Walk`/`WalkDir` callback that acts on the callback's path:
between the walk observing a directory entry and the callback opening it, that path can
be replaced with a symlink pointing outside the tree. Five sites are affected —
`internal/adapters/bunexec/prerendered_flatten.go`, `internal/adapters/sbom/generator.go`,
and `supervisor/cmd/pokkum-init/attest.go`.

Unlike the taint-analysis checks excluded alongside it (G702/G703), this one is not a
false positive: the containment is incidental rather than structural. In practice the
exposure is low — these walks traverse either the user's own project directory at build
time or the image's own layers at boot, not attacker-chosen trees — which is why it was
excluded in `.golangci.yml` rather than rushed, but "low" is not "none", and a
build-time walk over a directory a dependency can write into is the interesting case.

`os.Root` (Go 1.24+) makes this structural: operations resolve against a root handle and
cannot escape it, so the guarantee no longer depends on every future caller remembering
the check. See mem:self_review_checklist row 22 on containment checks.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Convert the five sites to os.Root | Open an os.Root for the walk base and use root-relative operations inside each callback, then drop the G122 exclusion from .golangci.yml so the check guards new code. | Removes the whole class and re-arms the linter, but touches three packages including the PID-1 attest path, which ships inside the image and has the least tolerance for churn. |
| Convert the build-time walks only, keep the exclusion for supervisor/ | Harden bunexec and sbom, where a dependency-writable tree is plausible, and leave the in-image attest walk as-is. | Cheaper and targets the reachable case, but leaves the exclusion in place repo-wide, so the check still cannot guard new code. |

## Recommendation

Take the first option, but as its own change rather than folded into unrelated work: the
diff is mechanical yet spans a security-sensitive PID-1 binary, and the payoff is
re-arming G122 so it guards future walk callbacks instead of being permanently muted.

## Decision

Option one shipped 2026-08-20. All five findings converted to `os.Root`, and G122 removed
from `.golangci.yml`'s gosec excludes so it now guards new walk callbacks: lint is 0 issues
with the check armed, and reverting any one site makes it fire again (proven for the sbom
site: 3 findings).

The reachable escape was more than theoretical at one site. `prerendered_flatten` moves
files up one level, and while the walked relative path cannot contain `..`, a symlinked
*destination* parent had `os.Rename` deposit a prerendered file outside the staging tree
and return nil. Containment tests assert on canary bytes outside the tree, not on error
values, because the old code returned no error at all.

For the PID-1 attest site the escape is not statically reachable through the walk —
`WalkDir` never follows symlinks and the `IsRegular` filter drops every symlink it
reports — so old and new code produce identical digests for any static tree. The
vulnerable window is one syscall wide and not reproducible on demand, so the observable
was moved to the read primitive, where old and new genuinely differ, and the walk-level
test asserts the invariant the design maintains instead. That choice is written into the
test file so it is not "fixed" later by re-adding a test for a guarantee deliberately not
made.

Verified beyond the standard suite because this binary ships inside the image: a real
container booted with the rebuilt `pokkum-init`, and the `os.Root` walk re-derived a
startup digest matching the packager's build-time manifest over 79 files.

## Known Limitations

- One deliberate behaviour change: `os.Root` refuses an **absolute** symlink target even when it names a path inside the root, because openat-based resolution cannot prove it. Relative in-root symlinks are unaffected. Impact is nil for the attest site (symlinks are filtered before the read) and negligible for the other two, and it is pinned by a test rather than left to be rediscovered as a bug.
- Second, smaller change: `sbom.scanProject` now errors when `ProjectDir` is not a directory (`OpenRoot` returns ENOTDIR) where it previously produced an empty package list. Unreachable from `pokkum build`.
- **G122's analysis is intraprocedural, so "armed and 0 issues" means no walk callback performs a direct unscoped filesystem operation — not that no walk callback can be TOCTOU'd.** All 15 Walk/WalkDir callbacks were enumerated; the 12 unflagged ones hand the walked path to a helper (`striputils.StripELFFile`, `precompressutils.PrecompressFile`, `sveltekitutils.ReadPackageJSON`) which performs the operation internally. Same class, structurally invisible to the linter — tracked as [Helper-delegated walk callbacks are outside G122's reach](walk-callback-helper-delegation-toctou.md).

