<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: cluster-dev-loop-latency)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Sub-second cluster dev loop

| Field | Value |
| --- | --- |
| Status | awaiting-decision |
| Stage | v1.1 |
| Kind | dx |
| Tier | moat |
| Area | Developer Experience |

## Summary

Decide which of the remaining cluster dev-loop options to build next now that --no-container ships the cheap local-process half.

## Problem

`pokkum dev` used to go through full image construction and a Docker/Podman daemon on every
change — table stakes, not a differentiator, and both external reviews independently flagged
the dev loop as the weakest part of the day-to-day experience. `--no-container` (see
docs/steps/SubSecondClusterDevLoop.md) already shipped the cheap first option: a
hot-reloading local Bun process with no image build at all. What's still undecided is which
of the two remaining, more ambitious options to pursue next.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| pokkum dev --cluster | Watch, rebuild, and sync /app/server + /app/client directly into a running pod via the Kubernetes API, then restart the Bun process — no registry round-trip. | The actual differentiator (SvelteKit analog of hot-swapping a Go binary), but a materially larger implementation: needs a Kubernetes client, pod exec/copy, and a narrower reimplementation of what Skaffold/Tilt already do generically. |
| --to-oci-layout=<path> + direct kind/k3d/minikube load | Emit an OCI layout on disk and load it directly into a local cluster, for contributors and CI environments with no daemon at all. | Cheap and adjacent to existing build code, but doesn't address the actual latency complaint — it's a hermetic-CI nicety, not a faster inner loop. |

## Flags

- `--cluster`
- `--to-oci-layout`

## Evidence

- Commits: `18f056c`

