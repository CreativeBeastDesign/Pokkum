<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: multi-arch-oci-compilation)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Zero-dependency multi-arch OCI compilation

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Build & Packaging |

## Summary

Compiles a SvelteKit project straight into a multi-arch (linux/amd64, linux/arm64) OCI image with no Docker daemon or buildkit, using the project's configured adapter — or injecting one virtually into .pokkum/vite.config.ts when its two preconditions hold.

## Flags

- `--strategy`
- `--platform`

## Implementation

- [internal/core/pipeline.go](../../internal/core/pipeline.go)
- [internal/adapters/bunexec/compiler.go](../../internal/adapters/bunexec/compiler.go)
- [internal/adapters/packager/packager.go](../../internal/adapters/packager/packager.go)

## Evidence

- Commits: `5693980`

## Known Limitations

- Zero-config injection writes a transformed vite config one directory deeper (.pokkum/<viteConfigName>); __dirname/import.meta.url-derived paths and Vite's root/envDir/publicDir/build.outDir are corrected for this, but any new relative-path construct added to a future SvelteKit/Vite release needs the same audit before it can be assumed safe.

