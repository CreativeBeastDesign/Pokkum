# Pokkum — DeepSeek Harness (DSH) Agent Instructions

Welcome to **Pokkum**, a Go-based zero-dependency OCI container image compiler for SvelteKit applications.

This is the **single, self-contained** instruction file for DeepSeek Harness (DSH) agents working on this codebase. It is the only project instruction file DSH reads (`instructionFileCandidates`). Everything relevant is contained inline — do not assume knowledge of other agent tooling formats.

> [!IMPORTANT]
> - **Serena MCP is the single source of truth** for project knowledge. The `.md` docs in this repo are for humans; the `mem:*` graph is for agents. When prose and memories conflict, **follow the memories** and update the docs to match.
> - DSH loads this file automatically only if `DSH.md` is in the loader's `instructionFileCandidates`. If you are reading this, it already is.
> - These instructions are guidance. They never override system, developer, or direct user instructions; more specific instructions take precedence over broader ones.

---

## 1. Serena MCP as the source of truth

**Serena MCP** manages the persistent project memory graph. Use it as your primary knowledge base.

### Startup protocol
At the beginning of any non-trivial task you MUST:
1. Read `mem:core` via the `read_memory` tool.
2. Follow references to domain-specific memories — `mem:conventions`, `mem:tech_stack`, `mem:telemetry`, `mem:task_completion` — as needed for the task.

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

Run the canonical 4-step suite with the consolidated make target:

```bash
make verify
```

`make verify` executes, in order:
1. **Formatting & static analysis:** `gofmt -s -w . && go vet ./...`
2. **Adapter unit tests:** `go test ./internal/adapters/...`
3. **CLI compilation check:** `go build -o ./pokkum-test ./cmd/pokkum && rm -f ./pokkum-test`
4. **Full internal test suite** (includes architecture purity `internal/architecture_test.go`): `go test ./internal/...`

Before declaring a non-trivial feature or refactor complete, review your own diff (`git diff`) line by line against the functional intent, paying attention to: off-by-one errors, nil pointer dereferences, unhandled errors, state inconsistencies; unintended side effects outside the intended Hexagonal boundary; goroutine leaks, unclosed `io.Reader`/`io.Closer`/`os.File` handles; and clock access violations.

If a bug or edge-case failure is found during self-review, flag it explicitly, fix it, and re-run `make verify`. **Silent patching is strictly forbidden.**

### Root-cause analysis & `Lessons.md`
Upon discovering a bug during self-review or debugging:
1. Conduct a root-cause analysis into why the bug was introduced (faulty assumption, missed boundary, leaky abstraction).
2. Log the incident and key takeaway in `Lessons.md` (creating the file if it does not exist).

---

## 6. DSH planning & orchestration policy

This governs every plan produced in plan mode for a non-trivial task, and how execution is delegated.

### 6.1 Plan document format
> **Tables over prose.** Every plan MUST include an **Execution Table** as its primary structure. Use prose only for context that genuinely cannot be tabulated (e.g. the one hard constraint, a subtle correctness bug). **Bold the single most important constraint or risk** so it is visually distinct — do not bury it in a paragraph.

Required sections, in order:

| Section            | Purpose                                            | Format                                       |
| ------------------ | -------------------------------------------------- | -------------------------------------------- |
| Context            | What the task addresses, current behavior          | Short prose + file:line refs                 |
| Hard constraint(s) | Anything that must never regress                   | **Bolded**, isolated, one line each          |
| Execution Table    | The step-by-step delegation plan                   | Table (see 6.2) — **required**               |
| Behavior changes   | User-visible change that isn't a defect            | Table: `#` \| Change \| Why intentional      |
| Tests              | New + must-update tests                            | Table: Test name \| File \| What it guards   |
| Verification       | Commands to run, manual checks                     | Table or short checklist                     |

### 6.2 Execution Table (required in every plan)
The single most important artifact — lets you scan the whole plan in seconds instead of reading prose.

| #   | Step                                         | File(s)                                 | Agent / Model | Depends on | Risk     |
| --- | -------------------------------------------- | --------------------------------------- | ------------- | ---------- | -------- |
| 1   | Add stub method to mocks                     | `pipeline_test.go`, `e2e_test.go`       | Sub-agent     | —          | Low      |
| 2   | Add interface method + field                 | `ports/baseimage.go`                    | Sub-agent     | —          | Low      |
| 3   | Security-sensitive resolver change           | `resolver.go`                           | Sub-agent     | #2         | **High** |
| 4   | Pipeline restructuring                       | `pipeline.go`                           | Sub-agent     | #2, #3     | Medium   |
| 5   | New regression tests                         | `resolver_test.go`, `pipeline_test.go`  | Sub-agent     | #3, #4     | Medium   |
| 6   | Adversarial full-diff review                 | (all changed files)                     | Sub-agent     | #1–#5      | High     |

- Use DSH `subagent` / `subagent_fork` for delegated work (background by default); keep delegations self-contained.
- Use the `workflow` tool when work fans out across many independent pieces (audits, migrations, multi-angle review).
- If a delegated result is corrected more than once for the same file, escalate or re-scope instead of re-prompting the same delegate a third time.
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
- **`Roadmap.md`** — task completion status (`[x]`), scopes, priorities.
- **`ARCHITECTURE.md`** — diagrams, adapter contracts, layer layouts, boundary descriptions.
- **`Vocabulary.md`** — complete reference of CLI commands, subcommands, flags, defaults, runtime env vars.
- **Serena MCP memories (`mem:*`)** — machine-readable knowledge graph for agents (`mem:core`, `mem:conventions`, `mem:tech_stack`, `mem:telemetry`, `mem:task_completion`, etc.).

---

## 8. "Next Best Steps" policy

After finishing any implementation task, milestone, or phase, MUST include a **"Next Best Steps"** section in the final answer.
- Base recommendations on remaining items in `Roadmap.md` and `AdditionalFeatures.md`.
- Highlight logical next features by priority, developer-experience impact, and architectural dependencies.
