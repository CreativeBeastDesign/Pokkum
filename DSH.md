# Pokkum — DeepSeek Harness (DSH) Agent Instructions

Welcome to **Pokkum**, a Go-based zero-dependency OCI container image compiler for SvelteKit applications.

This is the **single, self-contained** instruction file for DeepSeek Harness (DSH) agents working on this codebase. It is the only project instruction file DSH reads. Everything relevant is contained inline — do not assume knowledge of other agent tooling formats.

> [!IMPORTANT]
>
> - **Serena MCP is the single source of truth** for project knowledge. The `.md` docs in this repo are for humans; the `mem:*` graph is for agents. When prose and memories conflict, **follow the memories** and update the docs to match.
> - These instructions are guidance. They never override system, developer, or direct user instructions; more specific instructions take precedence over broader ones.

---

## 1. Serena MCP as the source of truth

**Serena MCP** manages the persistent project memory graph. Use it as your primary knowledge base.

### Startup protocol

At the beginning of any non-trivial task you MUST read exactly these sources, in order:

1. **This file (`DSH.md`)** — the operating instructions. It is already loaded at conversation start via `instructionFileCandidates`; do not re-read it, but treat its rules as binding.
2. **Serena MCP contents** — read `mem:core` via the `read_memory` tool, then follow references to the domain-specific memories (`mem:conventions`, `mem:tech_stack`, `mem:telemetry`, `mem:task_completion`, `mem:self_review_checklist`) as needed for the task. Memory is the machine-readable source of truth; always prefer it over any `.md` prose that conflicts.
3. **`Lessons.md`** — read (or at minimum grep) it for entries tied to the packages/files/behavior about to be touched, **before** writing any code. A prior incident in the same area is a strong signal you are about to repeat it ([`Lessons.md`](file:///Users/andrebarlocher/Documents/Go/Pokkum/Lessons.md), entries newest-first; see §5.2 for the format).

### Pre-task checklist

Before writing any code, in addition to the Serena startup protocol above:

1. **Confirm `Lessons.md` is covered by the startup read above.** If the task involves code changes, re-check the relevant Lessons.md entries right before implementing, not just at session start — the file may have grown.

### Keeping memories up-to-date

Update Serena memories whenever:

- Architectural patterns or interface boundaries change.
- New dependencies, CLI flags, or base image defaults are introduced.
- Build/verification protocols or conventions are updated.

Keep memories concise, invariant-focused, and accurate (`write_memory` / `edit_memory`). **Serena memories are for agents; the `.md` docs are for humans** — pick the appropriate wording, style, and tone for each.

### Symbolic tool primacy

- **Codebase exploration:** use `get_symbols_overview`, `find_symbol`, or `search_for_pattern` to locate symbols instead of reading whole files.
- **Refactoring & edits:** use reference-aware tools (`rename_symbol`, `safe_delete_symbol`, `replace_symbol_body`) when modifying symbol definitions.
- **Minimize context:** avoid reading entire source files when viewing a specific symbol body suffices.

---

## 2. Core architectural invariants

These are hard constraints. Never regress them.

### Hexagonal architecture boundaries (verified automatically)

- **`internal/ports`**: leaf node of the internal dependency graph. Imports stdlib + `go-containerregistry/pkg/v1` only. Must **NEVER** import `internal/core` or any concrete adapter (`internal/adapters/*`).
- **`internal/core`**: domain logic layer. Imports `internal/ports` and standard library. Re-exports port vocabulary as type aliases (e.g. `core.Platform = ports.Platform`).
- **Automated purity verification:** `internal/architecture_test.go` enforces boundary rules via AST analysis and runs during `go test ./internal/...` (or `make check-arch`).

### Shared utility package convention (`utils`)

- Any shared/internal helper that does **not** implement a concrete Hexagonal port interface MUST be appended with a `utils` suffix (e.g. `internal/adapters/sveltekitutils`, `internal/adapters/ignoreutils`).
- Utility packages SHOULD declare `const IsUtilityPackage = true` to distinguish them from port adapter implementations.

### Bit-for-bit OCI reproducibility

- **Zero clock access:** adapters must never call `time.Now()` or read system clocks. All file timestamps and tar headers derive from `req.SourceDateEpoch`.
- **Deterministic ordering:** directory traversals, tar headers, and map keys must be explicitly sorted before archiving or building layers.

### Zero-mutation build sandbox

- Code injections (SvelteKit adapter config, OpenTelemetry instrumentation) occur in `.pokkum/` virtual memory / sandbox space.
- User-authored source files in the working directory must never be overwritten or mutated during build or image compilation.

### Toolchain version stability

- **Never downgrade the Go version.** `go.mod`'s `go` directive and every `go-version:` pin in `.github/workflows/*.yml` must never decrease — only hold steady or raise. If a change appears to need lowering either, that's a signal to fix the actual incompatibility or ask the user, not to quietly regress the pin. Check `git diff -- go.mod '.github/workflows/*.yml'` for a lowered version before declaring any change complete.

---

## 3. Go engineering & quality standards

- **Error handling:** always wrap errors with clear context (`fmt.Errorf("sveltekitutils adapter: %w", err)`). Use sentinel errors defined in `internal/core` for domain error matching.
- **Interface segregation:** define narrow interfaces in `internal/ports`; keep adapter-internal helper structs unexported.
- **Concurrency safety:** layer tarball compression and multi-arch OCI manifest assembly must be safe for concurrent execution.
- **Zero fake implementations (verify real code):**
  - Fake, mock, or placeholder implementations must **ALWAYS** be flagged explicitly and never assumed/reported as fully implemented.
  - Before claiming any step, service, adapter, CLI flag, or feature is completed, inspect the underlying code to confirm it contains genuine, functional logic.
  - If any mock, stub, simulated return value, or placeholder exists, flag it clearly and transparently in the final answer.

---

## 4. OpenTelemetry & observability rules

- Respect SvelteKit version boundaries (>= 2.31.0 for native tracing).
- Inspect user project for `src/instrumentation.server.ts|js|mjs`. If present, preserve the user file intact. If missing, fall back to virtual injection (`.pokkum/src/instrumentation.server.ts`).
- When `--with-otel-sidecar` is set, ensure the OpenTelemetry Collector sidecar spec is correctly injected into Kubernetes Pod manifests (`4317` gRPC, `4318` HTTP, `8889` metrics).

---

## 5. Verification protocol (code changes only)

The verification protocol applies **ONLY when Go source code (`.go` files, build manifests, dependencies) has been created, modified, or refactored**.

> [!IMPORTANT]
> Do **NOT** run the verification suite for simple questions, code exploration, planning, or documentation updates (`.md` files, docstrings, or memory graph edits). Run it only before declaring completion of actual code modifications.

Run the canonical 5-step suite with the consolidated make target:

```bash
make verify
```

`make verify` executes, in order:

1. **Formatting & static analysis:** `gofmt -s -w . && go vet ./...`
2. **golangci-lint:** `golangci-lint run ./...` — `gofmt`/`go vet`/`go test` alone miss findings this catches (`errcheck`, `staticcheck`, ...); see `Lessons.md`'s "4-step verification suite does not run golangci-lint" entry.
3. **Adapter unit tests:** `go test ./internal/adapters/...`
4. **CLI compilation check:** `go build -o ./pokkum-test ./cmd/pokkum && rm -f ./pokkum-test`
5. **Full internal test suite** (includes architecture purity `internal/architecture_test.go`): `go test ./internal/...`

`supervisor/` (the `pokkum-init`/`pokkum-static` PID-1 binaries) shares the root `go.mod` but isn't covered by any of the five steps above. If a diff touches anything under `supervisor/`, also run `go build ./supervisor/... && go test ./supervisor/...`.

Before declaring a non-trivial feature or refactor complete, review your own diff (`git diff`) line by line against the functional intent. Use the §5.1 checklist below to do this — read it as a set of things to _mechanically verify_, not a paragraph to keep in mind while skimming. A checklist you don't actually run against the diff catches nothing more than the paragraph it replaced.

If a bug or edge-case failure is found during self-review, flag it explicitly, fix it, and re-run `make verify`. **Silent patching is strictly forbidden.**

### 5.1 Self-review checklist

> [!IMPORTANT]
> A real bug in this codebase (a goroutine leak in a fan-out resolve loop) survived an instruction that told the agent, in prose, to "scan for goroutine leaks." The instruction wasn't wrong, it just wasn't checkable — nothing forced a concrete look at the specific lines where it mattered.

The checklist itself lives in Serena as `mem:self_review_checklist`, not inline here. It's a cross-harness artifact — every agent working this codebase (DSH included) has Serena access, and an edited-in-place shared checklist beats three drifting local copies. **Read `mem:self_review_checklist` before declaring any non-trivial diff complete, and run every row against the actual diff lines — not from memory of having read it once.** If a row doesn't apply, say so explicitly ("N/A: no concurrent dispatch in this diff") rather than skipping it silently.

§5.3 explains how the checklist grows when a new bug class is found.

### 5.2 Root-cause analysis & `Lessons.md`

Upon discovering a bug during self-review or debugging:

1. Conduct a root-cause analysis into why the bug was introduced (faulty assumption, missed boundary, leaky abstraction).
2. Log the incident in `Lessons.md` (creating the file if it does not exist) using this structure, so future agents can _find and act on it_, not just read it in passing:

   ```
   ## YYYY-MM-DD — <one-line summary>
   **Category:** <e.g. concurrency / resource-leak / multi-item / determinism / boundary>
   **Root cause:** <the faulty assumption or missed case>
   **Where:** <file:line or function>
   **Fix:** <what changed>
   **Preventative rule:** <the general, reusable rule — this is what feeds `mem:self_review_checklist`>
   ```

3. **Close the loop — update `mem:self_review_checklist` in Serena, don't just log the incident.** Use Serena's `edit_memory`/`write_memory` tools to check whether the bug's category matches an existing row:
   - If it matches but the checklist didn't catch it, the row's wording was too vague or too narrow — revise it so this exact failure mode is unambiguously covered next time.
   - If it's a genuinely new category, add a new row.

   A `Lessons.md` entry with no corresponding checklist update is a bug you're likely to reintroduce. The Serena checklist — not the log — is what actually gets consulted during the next self-review, by every harness working this codebase; an entry that never updates it is a write with no matching read.

---

## 6. DSH planning & orchestration policy

This governs every plan produced in plan mode for a non-trivial task, and how execution is delegated.

### 6.1 Plan document format

> **Tables over prose.** Every plan MUST include an **Execution Table** as its primary structure. Use prose only for context that genuinely cannot be tabulated (e.g. the one hard constraint, a subtle correctness bug). **Bold the single most important constraint or risk** so it is visually distinct — do not bury it in a paragraph.

Required sections, in order:

| Section            | Purpose                                   | Format                                     |
| ------------------ | ----------------------------------------- | ------------------------------------------ |
| Context            | What the task addresses, current behavior | Short prose + file:line refs               |
| Hard constraint(s) | Anything that must never regress          | **Bolded**, isolated, one line each        |
| Execution Table    | The step-by-step delegation plan          | Table (see 6.2) — **required**             |
| Behavior changes   | User-visible change that isn't a defect   | Table: `#` \| Change \| Why intentional    |
| Tests              | New + must-update tests                   | Table: Test name \| File \| What it guards |
| Verification       | Commands to run, manual checks            | Table or short checklist                   |

### 6.2 Execution Table (required in every plan)

The single most important artifact — lets you scan the whole plan in seconds instead of reading prose.

| #   | Step                               | File(s)                                | Depends on | Risk     |
| --- | ---------------------------------- | -------------------------------------- | ---------- | -------- |
| 1   | Add stub method to mocks           | `pipeline_test.go`, `e2e_test.go`      | —          | Low      |
| 2   | Add interface method + field       | `ports/baseimage.go`                   | —          | Low      |
| 3   | Security-sensitive resolver change | `resolver.go`                          | #2         | **High** |
| 4   | Pipeline restructuring             | `pipeline.go`                          | #2, #3     | Medium   |
| 5   | New regression tests               | `resolver_test.go`, `pipeline_test.go` | #3, #4     | Medium   |
| 6   | Adversarial full-diff review       | (all changed files)                    | #1–#5      | High     |

- Use DSH `subagent` / `subagent_fork` for each step to delegate work (background by default); keep delegations self-contained. --> The idea is to keep the context minimal and clean.
- You do not have access to other models to use as sub-agents.
- Use the `workflow` tool when work fans out across many independent pieces (audits, migrations, multi-angle review).
- If a delegated result is corrected more than once for the same file, re-scope instead of re-prompting the same delegate a third time.
- Use the `todo_write` tool: one entry per concrete step, keep statuses current (`pending` → `in_progress` → `completed`), and never mark a todo complete until its verification step has actually passed.

### 6.3 Long-running objectives

For a sustained completion objective spanning multiple rounds, use the goal tools:

- `create_goal` once to register the objective.
- `get_goal` before every `update_goal`, copying the exact `goal_id` and `revision`.
- Mark `complete` only when actually achieved; mark `blocked` only after the same concrete blocking condition persists across rounds.

### 6.4 Status updates

After every `todo_write` status change or delegated completion, emit one short status line — not a paragraph:

```
[✅ done | 🔄 running | ⏳ waiting | ❌ failed] <step> → next: <step>
```

Example: `✅ pipeline.go restructuring → 🔄 resolver.go running`
Do not restate the full plan on every update; save prose for actual problems or decision points.

---

## 7. Keeping documentation & project state up-to-date

Keep docs and the persistent knowledge graph synchronized whenever code, CLI flags, interfaces, or architectural patterns change:

- **`Lessons.md`** — bug post-mortems, root causes, preventative rules.
- **Roadmap / shipped log / feature list — edit `docs/roadmap/*.yaml`, then run `make docs`.** `docs/Roadmap.md`, `docs/Shipped.md`, `docs/Features.md` and `docs/items/*.md` are generated output; `make docs` overwrites them and prunes orphaned pages, so hand-edits there are silently lost. `make check-docs-freshness` fails the build on drift.
- **`docs/archive/`** — retired historical docs (old `Roadmap.md`, `Feature-list.md`, `AdditionalFeatures.md`, `overnight-findings.md`). Read-only; cite, never update.
- **`ARCHITECTURE.md`** — diagrams, adapter contracts, layer layouts, boundary descriptions.
- **`Vocabulary.md`** — complete reference of CLI commands, subcommands, flags, defaults, runtime env vars.
- **Serena MCP memories (`mem:*`)** — machine-readable knowledge graph for agents (`mem:core`, `mem:conventions`, `mem:tech_stack`, `mem:telemetry`, `mem:task_completion`, etc.).
- **Package-level `README.md` files** (e.g. `internal/adapters/<name>/README.md`) — update alongside the top-level docs above whenever that package's behavior changes. These are human-facing, same as `ARCHITECTURE.md`/`Vocabulary.md` (Serena `mem:*` remains the source of truth for agents, per §1) — a new flag or field documented at the top level but not in its own package's README leaves a human contributor working in that directory with a stale picture.

---

## 8. "Next Best Steps" policy

After finishing any implementation task, milestone, or phase, MUST include a **"Next Best Steps"** section in the final answer.

- Base recommendations on the open items in `docs/Roadmap.md` (generated from `docs/roadmap/*.yaml`).
- Highlight logical next features by priority, developer-experience impact, and architectural dependencies.
