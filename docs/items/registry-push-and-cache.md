<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: registry-push-and-cache)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Registry push throughput, tagging, and composite remote-cache

| Field | Value |
| --- | --- |
| Status | shipped |
| Kind | feature |
| Tier | foundation |
| Area | Build & Packaging |

## Summary

Parallel HTTP/2 layer uploads, cross-repo blob mounting, idempotent pushes, repeatable --tag support, and a composite input hash that skips a full rebuild in sub-100ms on a verified registry cache hit.

## Flags

- `--tag`
- `--push-concurrency`
- `--cache-verify`
- `--compression`

## Implementation

- [internal/adapters/registry/push.go](../../internal/adapters/registry/push.go)
- [internal/adapters/registry/mount.go](../../internal/adapters/registry/mount.go)
- [internal/adapters/layercacheutils/layercacheutils.go](../../internal/adapters/layercacheutils/layercacheutils.go)

## Evidence

- Commits: `b350ecb`

## Known Limitations

- Before b350ecb there was no way to tag an image at all — every build published latest unconditionally. Tags apply registry-side after the image is hashed, so the tag set never affects the digest.
- A remote-cache hit skips VerifyBaseImage/native inspection by design (the base digest is already bound into the cache key), but the cache-verify key chain never reads req.Signing.PublicKeyPEM, so a build signed via --signing-key alone doesn't automatically make its own cache entries independently verifiable — falls through to a full rebuild rather than failing fast with a clear story (tracked in Serena mem:open_decisions, not this file's scope).

