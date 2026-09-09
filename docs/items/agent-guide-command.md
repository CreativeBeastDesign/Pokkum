<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: agent-guide-command)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# pokkum guide — the operating manual, shipped inside the binary

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.2 |
| Kind | dx |
| Tier | foundation |
| Area | Developer Experience |

## Summary

A task-shaped guide the binary prints itself, so an agent driving Pokkum in someone else's SvelteKit project reads instructions that cannot be a different version from the binary it is driving.

## Problem

Everything Pokkum knows how to say is written for one of two audiences, and neither is
"an agent working in a project that is not this repo". `AGENTS.md`, `CLAUDE.md` and
`DSH.md` instruct agents working ON Pokkum. `Vocabulary.md` is an exhaustive flag
reference — organised by flag rather than by decision, and large enough that pasting it
into a context window is itself the cost. `--help` is thorough on flags and silent on the
packaging model.

The gap is not invocation. An agent with a shell can already run `pokkum build`. What it
gets wrong is everything upstream of that:

- `runtime`, `strategy` and `base` compose, and it picks them as three independent values.
- It assumes a Dockerfile's `COPY .` semantics, and is surprised that only the adapter's
  `build/` output enters the image.
- It designs a runtime write path under `/app`, which ships mode `0555` in every Pokkum
  image with no opt-out — a fact stated once, in a code comment
  (`internal/adapters/packager/layer.go`). See item
  [read-only /app](readonly-app-invariant-undocumented.md).
- It reaches for `--no-verify-base`, `--allow-incomplete` or a relaxed `--fail-on` to make
  a failing command exit 0. A human reads those as "I am accepting risk"; an agent reads
  them as "the command passes now".
- It hand-rolls `docker cp` + `docker exec` to answer a question `pokkum explain why`
  already answers, because nothing told it that command exists.

And the docs it can find are frequently the wrong version. A Homebrew-installed 1.0.6
ships a README that does not mention `pokkum deploy` at all, while a newer binary earlier
on the same PATH carries the whole Dokploy/SwiftWave surface. An agent reading this
repository's docs for a binary it did not install is reading fiction. That version-skew
is the specific argument for putting the guide *in the binary* rather than in a file.

## Recommendation

`pokkum guide` writes the manual to stdout. Generated from the same flag/env registry
`scripts/gen-docs` already uses, and covered by the same parity guard that
`cmd/pokkum/flags_docs_test.go` and `envvar_docs_test.go` apply — so a new flag cannot
ship with the guide silently stale.

Task-shaped, not reference-shaped. Intended contents:

- **What Pokkum is, in operational terms only.** SvelteKit project in, OCI image out; what
  does and does not enter the image. Not the reproducibility or supply-chain story — that
  is why someone chooses Pokkum, not what an agent needs to drive it.
- **The invariants that shape application code before Pokkum ever runs.** `/app` is
  read-only; only adapter output ships; writes go to the OS temp directory, never a
  project-relative path. This is the greenfield half, and no tool call can deliver it
  retroactively — by the time a build fails, the app is already written the wrong way.
- **Defaults, and how to change them.** The ten or so fields an agent actually sets, each
  with its default, its alternatives, and the precedence chain
  (flag > env > profile > top-level > default). Deliberately NOT the exhaustive field
  reference — that belongs in [the JSON Schema](pokkum-yaml-json-schema.md), which an
  agent can validate against rather than merely read, and which cannot drift from the
  parser. The guide links to it and stops.
- **The composition rule stated once, loudly**: `runtime: node` requires
  `strategy: layered` and a Node-carrying base. `strategy: static` ships no JavaScript
  runtime, so `runtime` is meaningless beside it.
- **One recipe per deployment target** — Kubernetes (`resolve`/`apply`), Dokploy,
  SwiftWave, and plain registry-pull ("vanilla") — each ending at a verification command
  rather than at the deploy call. The SwiftWave limitation must be stated in the recipe,
  not in a footnote: it cannot be repointed at a new image reference, so the application
  must be pinned to a mutable tag, and `update_image` is rejected for that target. An
  agent that learns this from a rejected API call has already burned a cycle.
- **The shape of "my app needs another binary" (Typst, pandoc, ffmpeg, ImageMagick).**
  Today the honest answer is that Pokkum builds SvelteKit images and nothing else, and the
  working pattern is a separately built image plus a hand-written manifest — `pokkum
  resolve`/`apply` patch image references by container name, so a sidecar in the operator's
  own manifest survives untouched while the app container's ref is updated. Negative
  knowledge like this is the highest-value content in the guide: agents lose the most time
  on things a tool *almost* does. Revisit when [extra project paths](extra-project-paths.md)
  lands.
- **Verification recipes.** `pokkum explain why <image> <path>` answers "did my file make
  it in" without starting a container. And when a container is started: exercise it as the
  real runtime UID (`docker run -u 65532`), never `docker exec`, which defaults to root and
  bypasses the `0555` bits via `DAC_OVERRIDE` — a root-shell success proves nothing about
  the process that will actually run.
- **Escape hatches and when they are lies.** `--no-verify-base`, `--allow-incomplete`,
  `--reveal-secrets`, and loosening `--fail-on`: what each actually turns off, and the
  rule that none of them is a response to a failure whose cause is not yet understood.
- **Failure taxonomy**: error text → what it means → the next command. Pairs with
  [the exit-code table](exit-code-reference.md).

Two pointers make it discoverable rather than merely present: one line in the root
command's long help so it appears in `pokkum --help`, and a README section giving users a
copy-pasteable snippet to anchor in their own `CLAUDE.md`/`AGENTS.md` — the artifact a
user actually needs is the snippet, not prose describing what to write.

Serving the same text as an MCP resource is what makes [pokkum mcp](mcp-server.md) a
thin layer rather than a second body of knowledge to keep in sync.

## Decision

Shipped as `pokkum guide` with 14 addressable topics: all sections by default,
`pokkum guide <topic>` for one, `pokkum guide topics` for the index, and an unknown topic
as a usage error naming the valid set. Registered first in the command tree so it heads
`pokkum --help`, whose long description now opens with `START WITH: pokkum guide`.

Authored and parity-guarded rather than generated. That reverses this item's original
recommendation, and the reason is that the repo's own convention for CLI documentation is
already authored-plus-guarded (`flags_docs_test.go`, `envvar_docs_test.go`); generation is
used for `docs/roadmap` output only. Three guards apply: every `yaml:` field of
`ports.ProjectConfig`/`ports.BuildProfile` must appear in the config section (70 today),
every command in the cobra tree must be named in the guide, and the pre-existing
`flagmentions_test.go` AST scan rejects any flag mention that is not a registered flag.

Both new guards were red on their first run rather than needing to be broken to prove
they work: the field guard caught the `deploy:` block documented as recipes but absent
from the field reference, and the command guard caught the guide omitting itself.

The config reference is reflected out of the real structs, not copied from
`testdata/config/pokkum.yaml.golden` — whose header claims it carries every field of
schema version 1 while omitting `runtime`, `stub_launcher`, the whole `deploy:` block, six
`image` header fields, `build.allow_server_code_in_static`, `security.vex_exemptions` and
three `cache` keys. Its guards prove it parses and validates, not that it is exhaustive.

## Implementation

- [cmd/pokkum/guide.go](../../cmd/pokkum/guide.go)
- [cmd/pokkum/guide_test.go](../../cmd/pokkum/guide_test.go)
- [cmd/pokkum/main.go](../../cmd/pokkum/main.go)
- [README.md](../../README.md)
- [Vocabulary.md](../../Vocabulary.md)

## Related

- [pokkum mcp — Model Context Protocol server as a second driving adapter](mcp-server.md)
- [JSON Schema for .pokkum.yaml](pokkum-yaml-json-schema.md)
- [Documented CLI exit-code table](exit-code-reference.md)
- [The read-only /app invariant is stated only in a code comment](readonly-app-invariant-undocumented.md)
- [pokkum deploy (Dokploy, SwiftWave)](paas-deploy-targets.md)

