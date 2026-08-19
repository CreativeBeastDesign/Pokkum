<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: verify-base-on-cache-hit)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Verify base image on cache hit

| Field | Value |
| --- | --- |
| Status | open |
| Stage | backlog |
| Kind | hardening |
| Tier | polish |
| Area | Supply Chain & Attestation |

## Summary

Opt-in flag to re-verify the base image's signature even on a confirmed remote-cache hit, for audit environments that need a uniform verified-base guarantee.

## Problem

A confirmed remote-cache hit deliberately skips base-image signature verification —
nothing is built from the base image on a hit, and the composite cache key already binds
the base image's pinned digest, so a hit can only match the exact base the verifier would
have checked. This is disclosed via an explicit audit log line, but it leaves one narrow
case unclosed: a base whose pinned digest still matches (so cache hits keep firing) but
whose signature was later revoked, rekeyed, or withdrawn upstream — only re-running
verification would notice that.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Add --verify-base-on-cache-hit | When set and a cache hit is confirmed, run VerifyBaseImage (the base check, not the cache-output check) before accepting the hit. | Structurally independent of --cache-verify (which authenticates the cache-hit output image, not the base) — the two must not be coupled. One extra Cosign/keyless verify per cache hit; opt-in so the fast path stays fast by default. |

## Flags

- `--verify-base-on-cache-hit`

