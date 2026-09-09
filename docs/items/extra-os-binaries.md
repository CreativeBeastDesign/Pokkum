<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: extra-os-binaries)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Vendoring a third-party OS binary into the image (Typst, pandoc, ffmpeg)

| Field | Value |
| --- | --- |
| Status | awaiting-decision |
| Stage | backlog |
| Kind | feature |
| Tier | polish |
| Area | Build & Packaging |

## Summary

A SvelteKit app shelling out to a real CLI tool currently needs a hand-built custom base and a separate buildx pipeline, which undercuts the zero-Dockerfile pitch for that whole class of app.

## Problem

"SvelteKit app that invokes a native tool" is a common shape — Typst, pandoc, ImageMagick,
ffmpeg. Today it requires building a custom base image by hand (`FROM <tool image>` /
`FROM <resolved preset digest>` / `COPY --from`), which means running `pokkum base update`
to discover the preset's real reference, keeping a Dockerfile and a buildx builder
configured for multi-platform, and re-verifying the tool's tag still resolves on both
architectures. Done end to end in a real session (2026-09); it worked, and it was most of
the work.

The machinery is largely present. `bunruntime` already downloads a GitHub-release binary,
verifies its checksum, caches it and embeds it as its own deterministic layer — a Typst
resolver is close to that adapter with different URLs. The supervisor is runtime-agnostic.
The packager, multi-arch index and the whole attestation chain sit above the application
type, not inside it.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Do not build it | Keep Pokkum a SvelteKit compiler. Document the custom-base recipe properly in the guide and leave the tool image to a Dockerfile, apko or ko. | Preserves the product boundary and the supply-chain story exactly. Leaves a common application shape needing a second toolchain, which is the thing Pokkum exists to remove. |
| build.extra_binaries sourced from an upstream image | A config list of {from, path, to}, compiled as an extra COPY --from layer, as suggested from the field. | Narrower than a general image builder — no second Compiler, no reshaped PackageRequest. But it lets arbitrary bytes from an arbitrary image into the image while bypassing base-image verification, and the scanner and SBOM would need to cover a layer whose provenance nothing established. |
| A first-class tool resolver, modelled on bunruntime | Named, version-pinned, checksum-verified resolvers for a small set of tools, resolved from upstream releases rather than from another container image. | Keeps verification and reproducibility intact and fits the existing adapter shape. Costs a maintained resolver per tool, and does not generalise — every new tool is a code change. |

## Recommendation

Hold at awaiting-decision, and ship [extra_paths](extra-project-paths.md) first: it
covers the adjacent half of the same field report at a fraction of the risk, and it will
show whether the binary case is really the common one or was incidental to a single
project.

If it is built, the constraint that decides the design is supply chain, not plumbing. An
arbitrary `COPY --from` is a hole straight through the base-image verification, CVE gate
and SBOM coverage that are the reason to use this tool at all — so any source image must
pass the same verification path a `--base` custom reference already does
([custom-base-lock-slot](custom-base-lock-slot.md),
[base-image-signature-verification](base-image-signature-verification.md)), get its own
lock slot, and be scanned. Under that constraint the third option is closer to Pokkum's
grain than the second, and the second is only acceptable if it fails closed by default the
way a custom base does.

Explicitly out of scope either way: building images for non-SvelteKit inputs. That changes
what Pokkum is, and the field report's own conclusion — build the tool image separately,
keep Pokkum for the app — remains the right answer until a second real use case exists.
`pokkum resolve`/`apply` already patch image references by container name, so a sidecar in
a hand-written manifest survives a deploy untouched; what Pokkum cannot do is generate that
sidecar, since only the OTel one is injectable.

## Related

- [build.extra_paths — ship project directories beyond the adapter's output](extra-project-paths.md)
- [Per-ref pokkum.lock slot for custom --base images](custom-base-lock-slot.md)
- [Base image signature verification](base-image-signature-verification.md)
- [Dedicated chainguard-static base image preset](chainguard-static-preset.md)
- [pokkum guide — the operating manual, shipped inside the binary](agent-guide-command.md)

