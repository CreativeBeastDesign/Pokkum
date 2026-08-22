<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: sbom-os-package-coverage)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# SBOM coverage for base-image OS packages

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

The generated SBOM now catalogues the base image's dpkg/apk packages with correct pkg:deb/pkg:apk purls alongside npm dependencies, and the npm side is scoped to what the image actually ships.

## Problem

An independent tester found the generated SBOM contains zero OS packages: `pkg:deb` purl
count 0, and `libssl3`/`libc6` — the two most CVE-bearing components in the default
distroless base — absent by name and by purl, despite the built image demonstrably
shipping eleven dpkg-tracked packages (confirmed separately by exporting a real image's
filesystem and counting `var/lib/dpkg/status.d/*` entries). A CVE scanner fed this SBOM
sees none of the OS surface, which this project's own guide calls worse than no SBOM at
all — it looks like coverage without being coverage.

`internal/adapters/scannerutils/scannerutils.go`'s `ExtractImagePackages` already parses
both dpkg (`var/lib/dpkg/status` and `status.d/*`) and apk (`lib/apk/db/installed`)
package databases out of a `v1.Image` — the gap was never a missing parser, only that
`sbom.Generator.Generate` has no way to receive a base image at all: `ports.SBOMRequest`
carries `ProjectDir` and a couple of Bun-runtime strings, nothing image-shaped.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Thread the resolved base image through ports.SBOMRequest and core/pipeline.go | Add a field to `ports.SBOMRequest` carrying the already-resolved `*ports.BaseImage` (or its `Images map[Platform]v1.Image]`), and have `core.fanOut`'s SBOM goroutine (`internal/core/pipeline.go`, the `deps.SBOM.Generate(gctx, ports.SBOMRequest{...})` call) pass the `base *ports.BaseImage` parameter it already holds — no new fetch, no new network dependency, reusing the exact pull `BaseImageResolver.Resolve` already paid for earlier in the same build. | Touches internal/ports and internal/core, which is why this session (scoped to internal/adapters/sbom and internal/adapters/scannerutils only, per a concurrent multi-agent split) implemented everything up to this point and stopped rather than editing files another agent owned. |
| Have the SBOM generator resolve/pull the base image itself | Give sbom.Generator its own registry client and base-image ref, independent of core.Build's own resolution. | Rejected: duplicates a pull the pipeline already performed, and gives SBOM generation a network dependency it has never needed — directly against this feature's own design constraint. |

## Recommendation

Option 1. `internal/adapters/sbom/generator.go` already has the receiving end built and
tested: a new `(*Generator).GenerateForImage(ctx, req ports.SBOMRequest, images
map[ports.Platform]v1.Image) (*ports.SBOMDocument, error)` method (not part of the
`ports.SBOMGenerator` interface, since adding to it would itself require a ports change)
extracts every platform's OS packages via `scannerutils.ExtractImagePackages`, dedupes
them by name+version+architecture across platforms, and merges them into the same
document as the project's npm dependencies with correct `pkg:deb/<distro>/<name>@<version
>?arch=<arch>` / `pkg:apk/<distro>/...` purls (distro namespace from the image's own
os-release, falling back to "debian"/"alpine" if that's unreadable). Closing this item
needs exactly two small, additive changes outside this session's scope: (1) a
`BaseImage *ports.BaseImage` field on `ports.SBOMRequest`, (2) one line in
`core.fanOut`'s SBOM goroutine setting it from the `base` parameter already in scope, and
swapping that call from `Generate` to `GenerateForImage`.

## Decision

2026-08-22: implemented and tested everything reachable from within
internal/adapters/sbom and internal/adapters/scannerutils, then stopped at the ports/core
boundary per this session's explicit scope split (three agents editing the repo
concurrently; internal/ports and internal/core were owned by others). Also fixed, in the
same change and regardless of the wiring gap: `renderSPDXJSON`/`renderCycloneDXJSON`
previously hardcoded `pkg:npm/...` for every package handed to them, which would have
mislabeled OS packages as npm the moment any caller did feed them in; purl generation is
now an explicit switch over `scannerutils.PackageType`. Every SBOM `Generate()` produces
today — including before this wiring lands — now honestly records
`pokkum:osPackagesScanned=false` (SPDX `creationInfo.comment` / CycloneDX
`metadata.properties`) instead of silently saying nothing about OS coverage, so the gap
this item describes is visible in the SBOM's own bytes rather than only in this roadmap
entry. Once `GenerateForImage` is reachable, a base image confirmed to carry no dpkg/apk
database at all (scratch, distroless/static) reports `pokkum:osPackagesScanned=true
pokkum:osPackageCount=0` — a real, positive zero, structurally distinct from "not
scanned" rather than sharing its representation (this codebase's recurring "found
nothing" vs "could not check" failure mode — see Lessons.md).

The ports/core wiring described above landed the same day, so this is now reachable from
a real build: `ports.SBOMRequest` carries the resolved per-platform base images and the
single port method uses them, rather than a second method a caller can forget. Measured
on a real signed build, ground truth being the package directories present under
/app/node_modules in the pushed image: 11 pkg:deb entries including libssl3 and libc6;
npm packages 378 -> 300; listed-but-not-shipped 127 -> 50; shipped-but-not-listed 0
before and after. The npm scoping is a reachability walk over bun.lock's own graph
(dependencies, optionalDependencies and peerDependencies edges from the root's
dependencies), matching what `bun install --production` stages, not a blanket
devDependencies drop; an undeterminable scope is always kept.

## Implementation

- [internal/adapters/sbom/generator.go](../../internal/adapters/sbom/generator.go)
- [internal/adapters/sbom/os_packages_test.go](../../internal/adapters/sbom/os_packages_test.go)
- [internal/adapters/scannerutils/scannerutils.go](../../internal/adapters/scannerutils/scannerutils.go)

## Known Limitations

- 49 of the 50 remaining listed-but-not-shipped npm packages are cross-platform optional stubs (@esbuild/darwin-x64 and similar). Narrowing them to the host's GOOS/GOARCH would make the SBOM's content depend on which machine ran the build, violating bit-for-bit reproducibility to fix a reporting nit — a deliberate trade-off, not an oversight.
- Dependency scope is undeterminable for pnpm lockfiles and for a bun.lock with no workspaces object; those projects keep the pre-existing behaviour of listing every package.
- OS-package purls assume one distro identity per build (the resolved base image's own os-release, or a debian/alpine fallback); a hypothetical base whose platforms genuinely disagree on distro would have the first-encountered platform's distro win for namespacing every OS purl, not per-platform namespaces.

## Related

- [Toolchain (Bun) CVE awareness](toolchain-cve-awareness.md)
- [Node-core CVE lookup for --runtime=node](node-cve-lookup.md)

