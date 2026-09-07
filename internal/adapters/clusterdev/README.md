# `clusterdev` — in-pod dev sync (`pokkum dev --cluster`)

Implements `ports.ClusterDevSyncer`. It locates a running pod by label
selector, streams freshly built SvelteKit output into it as a tar over
`kubectl exec`, and asks the in-pod `pokkum-init` to restart the application
process. No image is built, nothing is pushed to a registry, and the pod is
never recreated.

This is the only adapter besides `deploy` whose entire job is a side effect
against a live system. It is never called from `core.Build`, and the
determinism and zero-clock invariants that bind the build pipeline do not
apply to it.

## Why the image has to cooperate

A Pokkum image is distroless: no shell, no `tar`. `kubectl cp` shells out to
`tar` *inside* the container, so it cannot work here, and there is nothing in
the image able to kill and re-exec the server. Both halves are therefore
provided by `pokkum-init`, the one executable already in every layered image:

| Half | Mechanism | Source |
| --- | --- | --- |
| Get the bytes in | `pokkum-init __dev-sync --root DIR [--root DIR...]` reads a tar from stdin | `supervisor/cmd/pokkum-init/devsync.go` |
| Restart the process | `SIGHUP` stops the child and starts a replacement, instead of being relayed to it | `supervisor/cmd/pokkum-init/supervisor.go` |

Both are gated on `POKKUM_DEV_MODE`, are off by default, and are never set by
the packager — between them they let anyone who can already `kubectl exec`
into the pod overwrite `/app` and restart the server.

## Layout

| File | Contents |
| --- | --- |
| `syncer.go` | `Syncer` (the `kubectl` shell-out), `SelectTarget` (pure pod/container selection over `kubectl get pods -o json`) |
| `tar.go` | `WriteTar`, the archive builder, including the packager's `/app/server` exclusion list |

`SelectTarget` is exported and pure on purpose: pod selection is the part with
the real edge cases (several replicas, a terminating pod, a sidecar), and it
is testable against literal JSON with no cluster and no `kubectl` anywhere.

## Two properties worth not breaking

**The counts have to agree.** The extractor prints a one-line JSON summary and
`Sync` refuses any result whose file and byte counts differ from what it sent,
or where a requested restart did not happen. A tar that streamed perfectly
into a container that wrote none of it is otherwise indistinguishable from a
working sync — the exit code is 0 either way.

**Containment is enforced twice.** `extractTar` matches entry paths against
the `--root` allowlist on whole path segments (so `/app/serverevil` is not
inside `/app/server`, and `../` cannot climb out), and then writes through
`os.Root`, which additionally refuses to traverse a symlink out of the root.
The lexical check cannot see that second case, and it matters here precisely
because the tree being written into is one a previous sync may already have
modified.

## Scope

Layered images only, `/app/server` and `/app/client` only, additive only
(deletions are not propagated). See `docs/items/cluster-dev-loop.md` for the
full limitation list and `Vocabulary.md` §5a for the operator-facing
description.
