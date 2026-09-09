<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: cluster-dev-loop)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum dev --cluster

| Field | Value |
| --- | --- |
| Status | shipped |
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

## Decision

Built on `kubectl` rather than on a linked Kubernetes client, matching the existing
cluster-facing code in `cmd/pokkum` (`newKubectlClusterInspector`) and keeping the
zero-dependency posture: shelling out inherits the developer's kubeconfig, current context,
exec credential plugins and RBAC for free, which is exactly what a dev loop pointed at
someone's own cluster should do.

The hard constraint that shaped everything else is that a Pokkum image is distroless. It has
no shell and no `tar`, so `kubectl cp` — which shells out to `tar` *inside* the container* —
cannot work against it, and there is nothing in the image able to kill and re-exec the server
process. Both halves are therefore provided by `pokkum-init`, the one executable already in
every layered image: a hidden `pokkum-init __dev-sync` subcommand extracts a tar from stdin
into an allowlist of `--root` directories, and SIGHUP is reinterpreted as "stop the
application and start a fresh one" instead of being relayed to the child. This is the same
shape Tilt's `live_update` needs a restart wrapper for; Pokkum already ships one.

Both behaviours are gated on `POKKUM_DEV_MODE`, off by default and never set by the packager,
because between them they let anyone who can already `kubectl exec` into the pod overwrite
`/app` and restart the server. `pokkum dev --cluster` reads the target container's env and
refuses up front when it is unset, rather than building and streaming a whole project to earn
a rejection at the far end.

Two integrity properties are deliberate rather than incidental. The extractor prints a JSON
summary and the sending side refuses any result where the counts do not match what it sent,
or where a requested restart did not happen — a tar that streamed perfectly into a container
that wrote none of it is otherwise indistinguishable from a working sync. And containment is
enforced twice: lexically, on whole path segments against the `--root` allowlist, and again
through `os.Root`, which refuses to traverse a symlink out of the root — the case a lexical
check cannot see, and the one that matters because the tree being written into is one a
previous sync may already have modified.

## Flags

- `--cluster`
- `--namespace`
- `--selector`
- `--container`

## Implementation

- [cmd/pokkum/dev_cluster.go](../../cmd/pokkum/dev_cluster.go)
- [internal/ports/clusterdev.go](../../internal/ports/clusterdev.go)
- [internal/adapters/clusterdev/syncer.go](../../internal/adapters/clusterdev/syncer.go)
- [internal/adapters/clusterdev/tar.go](../../internal/adapters/clusterdev/tar.go)
- [supervisor/cmd/pokkum-init/devsync.go](../../supervisor/cmd/pokkum-init/devsync.go)
- [supervisor/cmd/pokkum-init/supervisor.go](../../supervisor/cmd/pokkum-init/supervisor.go)

## Known Limitations

- Requires `POKKUM_DEV_MODE=1` on the target container. It is off by default, never set by the packager, and must never be set on a production workload: it makes the in-pod `__dev-sync` subcommand callable and turns SIGHUP into a process restart, both of which hand real power to anyone who can already exec into the pod.
- Layered images only. An `exe` or `static` image has no `/app/server`, and the extractor refuses to create a `--root` that does not exist rather than materialising a tree that would look synced and serve nothing.
- Deletions are not propagated: a file removed from the local build stays in the pod until the pod is replaced. Only `/app/server` and `/app/client` are synced — a change to production dependencies, prerendered pages, vendor or native trees still needs a real image build.
- Extraction restores the owner write bit on the `/app` directories it writes into (the packager ships them `0555`) and leaves synced entries at `0755`/`0644`, so a dev-synced pod is no longer byte-identical to its image. The container user must own the `/app` tree; if it does not, the sync fails rather than half-completing.
- If the target container sets `POKKUM_ATTESTATION_DIGEST`, the pod will fail its *next* start with exit 125, because startup attestation re-derives the digest of the very `/app` tree this loop rewrote. The running pod is unaffected — attestation runs once, at supervisor startup — but an eviction or reschedule turns into a crash loop. A `Warn` says so when the target has it set.
- A multi-container pod (e.g. with `--with-otel-sidecar`) requires `--container`. There is deliberately no first-container heuristic: it would be a coin flip between the application and the collector.

## Related

- [pokkum dev --no-container](no-container-dev-mode.md)
- [--to-oci-layout for daemonless cluster loading](oci-layout-dev-output.md)

