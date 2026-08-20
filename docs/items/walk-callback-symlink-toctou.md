<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: walk-callback-symlink-toctou)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Root-scoped filesystem APIs in filepath.Walk callbacks (gosec G122)

| Field | Value |
| --- | --- |
| Status | open |
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

