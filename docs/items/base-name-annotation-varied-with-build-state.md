<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: base-name-annotation-varied-with-build-state)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# base.name annotation varied with local build state

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.2 |
| Kind | fix |
| Tier | foundation |
| Area | Build & Packaging |

## Summary

The first build of a project produced a different image digest from every later build of identical source, because `org.opencontainers.image.base.name` recorded the base reference as resolved rather than an invariant one.

## Problem

`org.opencontainers.image.base.name` was set from the base image reference *as resolved*, which
the resolver rebinds with local state: to the lockfile's pinned digest form once `pokkum.lock`
exists, and to the escrow mirror's tag when a mirror is in use. That string is baked into the
image config, so it changes the config digest, then the manifest digest, then the index digest.

Observable effect: the FIRST build of a project — before `pokkum.lock` existed — produced a
different image digest from every build after it, for byte-identical source, and a build
pulling through an escrow mirror produced a different digest from one that did not. Measured on
the three-way benchmark: five consecutive builds, only the first differed, and the config diff
was this one annotation with every layer and diffID identical.

It now records `UpstreamRef`, documented as never rebound to a mirror or a locked digest form,
which is also what the annotation is for — `org.opencontainers.image.base.digest` carries the
digest separately and authoritatively. `BaseImageInfo.Ref` is deliberately not a fallback on
any path, since reintroducing it anywhere reintroduces the bug.

## Known Limitations

- Breaking: base.name moves from the pinned digest form back to the upstream tag for builds that had a lockfile, so every image digest changes once. `pokkum verify` against an image built before this change correctly reports a mismatch.

## Related

- [--strategy=static](strategy-static.md)

