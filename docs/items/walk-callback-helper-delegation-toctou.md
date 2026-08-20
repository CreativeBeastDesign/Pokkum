<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: walk-callback-helper-delegation-toctou)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Helper-delegated walk callbacks are outside G122's reach

| Field | Value |
| --- | --- |
| Status | open |
| Kind | infra |
| Tier | polish |
| Area | Testing & Infrastructure |

## Summary

12 of the repo's 15 Walk/WalkDir callbacks pass the walked path to a helper that opens it internally, so the symlink-TOCTOU class survives in a form gosec G122 structurally cannot see.

## Problem

[Root-scoped filesystem APIs in filepath.Walk callbacks (gosec G122)](walk-callback-symlink-toctou.md)
converted the five callbacks that called `os.*` on the walked path directly, and re-armed
G122. That check is intraprocedural: it sees `os.ReadFile(p)` inside the callback, and does
not see `helper(p)` where the helper does the same thing one frame down.

Enumerating all 15 Walk/WalkDir callbacks found 12 in the second shape — the walked path is
handed to `striputils.StripELFFile`, `precompressutils.PrecompressFile` (which stats and
reads it internally), or `sveltekitutils.ReadPackageJSON`/`ResolveVersion` on
`filepath.Dir(p)`. The exposure is the same class as the converted sites, and the linter
reports zero issues on all of them.

This is not an argument that the conversion was pointless: the converted sites are now
structurally contained, and the armed check does prevent the *direct* shape from returning.
It is a statement of what "G122 armed, 0 issues" actually buys, so nobody reads it as
"walk callbacks in this repo cannot be TOCTOU'd".

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Thread an os.Root through the helper APIs | Change the shared helpers to accept an *os.Root plus a root-relative path instead of an absolute path, so containment is a property of the signature. | Eliminates the class and makes it unrepresentable, but changes a shared API used by many callers across ~10 packages, including internal/adapters/secretguard, and is a genuinely large diff. |
| Convert only the walks over trees an attacker could plausibly influence | Triage the 12 by what they traverse — a dependency-writable node_modules tree is interesting, the image's own layers at boot much less so — and convert that subset. | Much smaller and targets real exposure, but leaves the class present and the linter still blind to it, so the next such callback is unguarded. |
| Accept and document | Record that these walks traverse trees the build already trusts, and leave them. | Zero cost; relies on every future walk callback author knowing the distinction, which is what row 22 exists because people do not. |

## Recommendation

Second option first: triage by what is actually traversed, and convert the build-time walks
over dependency-writable trees. The first option is the real fix but should not ride along
with anything else — it is a shared-API change, and the fact that it touches secretguard
means it needs the same care the PID-1 conversion just got.

