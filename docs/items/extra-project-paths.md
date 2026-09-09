<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: extra-project-paths)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# build.extra_paths — ship project directories beyond the adapter's output

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | feature |
| Tier | polish |
| Area | Build & Packaging |

## Summary

Only the adapter's `build/` output enters the image, so a project reading templates, fonts or seed data off disk has to smuggle them in via a postbuild copy.

## Problem

Pokkum packages the adapter's output and nothing else from the project root. That is the
right default — it is most of why the images are small — but it has no escape hatch, and
the class of application that needs one is not exotic: anything reading a template
directory, a font set, or seed data directly off disk.

The workaround that does work today is a postbuild script copying those directories into
`build/` so they ride along in the existing server layer, plus setting `image.working_dir`
to the path that layer mounts at so the app's own `process.cwd()`-relative resolution keeps
working unchanged. It is two moving parts held together by a convention nothing enforces,
and neither part is discoverable — it was reconstructed from first principles in a real
session (2026-09).

## Recommendation

A `build.extra_paths` list, copied to a documented stable path under the app tree and
surfaced to the application through an environment variable so it does not have to infer
the location from `working_dir`. Determinism is unaffected: the same sorted-walk and
`SOURCE_DATE_EPOCH` header rules the existing layers use apply unchanged.

Deliberately narrower than [extra OS binaries](extra-os-binaries.md): the content is the
operator's own source tree, already inside the build context and already covered by
`secretguard` and the SBOM, so it opens no new supply-chain surface. That is what makes it
the one of the two pair worth doing first.

## Related

- [Vendoring a third-party OS binary into the image (Typst, pandoc, ffmpeg)](extra-os-binaries.md)
- [The read-only /app invariant is stated only in a code comment](readonly-app-invariant-undocumented.md)
- [Ship production dependencies so images are self-contained](vendor-production-dependencies.md)

