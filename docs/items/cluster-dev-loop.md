<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: cluster-dev-loop)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum dev --cluster

| Field | Value |
| --- | --- |
| Status | open |
| Stage | v1.1 |
| Kind | dx |
| Tier | moat |
| Area | Developer Experience |

## Summary

Watch, rebuild, and sync app server and client output directly into a running pod via the Kubernetes API, without an image build or registry round-trip.

## Problem

`--no-container` (see [pokkum dev --no-container](no-container-dev-mode.md)) shipped the
cheap local-process half of the dev-loop problem. This is the more ambitious, actual
differentiator: the SvelteKit analog of hot-swapping a Go binary in a running pod. It would put
Pokkum on Skaffold/Tilt's own turf, but with a narrower, SvelteKit-specific implementation —
watch, rebuild, sync `/app/server` + `/app/client` into a running pod, restart the Bun process,
no registry round-trip. Per the decision matrix, this scores highest on DX (10/10) of anything
on the roadmap, with the cost concentrated in needing a Kubernetes client and pod exec/copy
plumbing that a narrower reimplementation of what Skaffold/Tilt already do generically requires.
Both external reviews independently flagged the dev loop as the weakest part of the day-to-day
experience, and this is the option that actually answers that, as opposed to `--no-container`
which only removes the container-build tax.

## Flags

- `--cluster`
- `--namespace`
- `--selector`

## Related

- [pokkum dev --no-container](no-container-dev-mode.md)
- [--to-oci-layout for daemonless cluster loading](oci-layout-dev-output.md)

