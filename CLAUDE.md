# Pokkum — AI Agent Instructions & Guidelines (Claude Code)

Welcome to **Pokkum**, a Go-based zero-dependency OCI container image compiler for SvelteKit applications.

This document defines the core guidelines, architectural invariants, verification protocols, and **planning/orchestration policy** for Claude Code working on this codebase.

---

## 1. Serena MCP as the Source of Truth

**Serena MCP** manages the persistent project memory graph for Pokkum.

### Startup Protocol

At the beginning of any non-trivial task, agents **MUST**:

1. Access Serena MCP and read `mem:core` using the `read_memory` tool.
2. Follow references to domain-specific memories (`mem:conventions`, `mem:tech_stack`, `mem:telemetry`, `mem:task_completion`) as needed for the task.

### Keeping Serena Memories Up-to-Date

Serena's memory graph is a living repository. Agents **MUST** update Serena memories whenever:

- Architectural patterns or interface boundaries change.
- New dependencies, CLI flags, or base image defaults are introduced.
- Build/verification protocols or conventions are updated.

Use Serena's `write_memory` or `edit_memory` tools to keep memories concise, invariant-focused, and accurate.

### Symbolic Tool Primacy

- **Codebase Exploration**: Use `get_symbols_overview`, `find_symbol`, or `search_for_pattern` to locate relevant symbols instead of reading whole files.
- **Refactoring & Edits**: Use reference-aware refactoring tools (`rename_symbol`, `safe_delete_symbol`, `replace_symbol_body`) when modifying symbol definitions.
- **Minimizing Context**: Avoid reading entire source files when viewing specific symbol bodies is sufficient.

---

## 2. Core Architectural Invariants

### Hexagonal Architecture Boundaries

- **`internal/ports`**: Leaf node of the internal dependency graph. It imports stdlib + `go-containerregistry/pkg/v1` only. It must **NEVER** import `internal/core` or any concrete adapter (`internal/adapters/*`).
- **`internal/core`**: Domain logic layer. Imports `internal/ports` and standard library. Re-exports port vocabulary as type aliases (e.g. `core.Platform = ports.Platform`).
- **Automated Purity Verification**: `internal/architecture_test.go` enforces boundary rules via AST analysis and executes automatically during `go test ./internal/...` (or via `make check-arch`).

### Shared Utility Package Convention (`utils`)

- **Non-Adapter Utilities**: Any shared or internal helper module that does not implement a concrete Hexagonal port interface MUST be appended with a `utils` suffix (e.g., `internal/adapters/sveltekitutils`, `internal/adapters/ignoreutils`).
- **Utility Package Sentinel**: Utility packages SHOULD declare `const IsUtilityPackage = true` to explicitly distinguish them from port adapter implementations.

### Bit-for-Bit OCI Reproducibility

- **Zero Clock Access**: Adapters must **never** call `time.Now()` or read system clocks. All file timestamps and tar headers must derive from `req.SourceDateEpoch`.
- **Deterministic Ordering**: Directory traversals, tar headers, and map keys must be explicitly sorted before archiving or building layers.

### Zero-Mutation Build Sandbox

- Code injections (such as SvelteKit adapter configuration or OpenTelemetry instrumentation) must occur in `.pokkum/` virtual memory / sandbox space.
- User-authored source files in the working directory must **never** be overwritten or mutated during build or image compilation.

---

## 3. Go Engineering & Quality Standards

- **Error Handling**: Always wrap errors with clear context (`fmt.Errorf("sveltekitutils adapter: %w", err)`). Use sentinel errors defined in `internal/core` for domain error matching.
- **Interface Segregation**: Define narrow interfaces in `internal/ports`. Keep adapter-internal helper structs unexported.
- **Concurrency Safety**: Ensure layer tarball compression and multi-arch OCI image manifest assembly are safe for concurrent execution.
- **Zero Fake Implementations (Verification of Real Code)**:
  - Fake, mock, or placeholder implementations must **ALWAYS** be flagged explicitly and must **NEVER** be assumed or reported as fully implemented.
  - Before claiming any step, service, adapter, CLI flag, or feature is completed, agents **MUST** inspect the underlying code to ensure it contains genuine, functional logic.
  - If any mock, stub, simulated return value, or placeholder exists, it must be clearly and transparently flagged in the agent's final answer.

---

## 4. OpenTelemetry & Observability Rules

- Respect SvelteKit version boundaries (>= 2.31.0 for native tracing).
- Inspect user project for `src/instrumentation.server.ts|js|mjs`. If present, preserve user file intact. If missing, fall back to virtual injection (`.pokkum/src/instrumentation.server.ts`).
- When `--with-otel-sidecar` is set, ensure the OpenTelemetry Collector sidecar spec (`4317` gRPC, `4318` HTTP, `8889` metrics) is correctly injected into Kubernetes Pod manifests.

---

## 5. Task Completion Verification Protocol

The verification protocol applies **ONLY when Go source code (`.go` files, build manifests, dependencies) has been created, modified, or refactored**.

> [!IMPORTANT]
>
> - Do **NOT** run the verification test suite for simple user questions, code exploration, planning, or documentation updates (e.g. `Roadmap.md`, `AGENTS.md`, `CLAUDE.md`, `Lessons.md`, `.md` files, docstrings, or memory graph edits).
> - Tests should be executed **only before declaring completion of actual code modifications**.

When code changes have occurred, agents **MUST** execute the following 4-step verification suite before declaring completion:

1. **Formatting & Static Analysis**: `gofmt -s -w . && go vet ./...`
2. **Adapter Unit Tests**: `go test ./internal/adapters/...`
3. **CLI Compilation Check**: `go build -o ./pokkum-test ./cmd/pokkum && rm -f ./pokkum-test`
4. **Full Internal Test Suite** (includes Architecture Purity Verification `internal/architecture_test.go`): `go test ./internal/...`

Before declaring any non-trivial feature or refactor complete, review your own diff (`git diff`) line by line against the functional spec, with particular attention to: off-by-one errors, nil pointer dereferences, unhandled errors, state inconsistencies; unintended side effects outside the intended Hexagonal boundary; goroutine leaks, unclosed `io.Reader`/`io.Closer`/`os.File` handles, and clock access violations.

If a bug or edge case failure is found during self-review, flag it explicitly, fix it, and re-run the full verification suite. **Silent patching is strictly forbidden.**

### Root Cause Analysis & `Lessons.md`

Upon discovering a bug during self-review or debugging:

1. Conduct a root-cause analysis into _why_ the bug was introduced (e.g., faulty assumption, missed boundary, leaky abstraction).
2. Log the incident and key takeaway in [`Lessons.md`](file:///Users/andrebarlocher/Documents/Go/Pokkum/Lessons.md) (creating the file if it does not exist).

---

## 6. Planning & Sub-Agent Orchestration Policy

This section governs **every** plan produced in Plan Mode (`Shift+Tab` / `/plan`) for a non-trivial task, and how execution is delegated once a plan is approved.

### 6.1 Plan document format

> [!IMPORTANT]
> **Tables over prose.** Every plan MUST include an **Execution Table** (6.2) as its primary structure. Use prose only for context that genuinely cannot be tabulated (e.g. the one hard constraint, a subtle correctness bug found during drafting). Bold the single most important constraint or risk in the plan so it is visually distinct from surrounding text — do not bury it in a paragraph.

Required sections, in order:

| Section            | Purpose                                             | Format                                     |
| ------------------ | --------------------------------------------------- | ------------------------------------------ |
| Context            | What roadmap/issue this addresses, current behavior | Short prose + file:line refs               |
| Hard constraint(s) | Anything that must never regress                    | **Bolded**, isolated, one line each        |
| Execution Table    | The actual step-by-step delegation plan             | Table (see 6.2) — **required**             |
| Behavior changes   | Any user-visible change that isn't a defect         | Table: `#` \| Change \| Why intentional    |
| Tests              | New + must-update tests                             | Table: Test name \| File \| What it guards |
| Verification       | Commands to run, manual checks                      | Table or short checklist                   |

### 6.2 Execution Table (required in every plan)

This table is the single most important artifact in the plan — it is what lets you scan the whole plan in a few seconds instead of reading prose.

| #   | Step                                        | File(s)                                | Agent / Model | Depends on | Risk     |
| --- | ------------------------------------------- | -------------------------------------- | ------------- | ---------- | -------- |
| 1   | Example: add stub method to mocks           | `pipeline_test.go`, `e2e_test.go`      | Haiku         | —          | Low      |
| 2   | Example: add interface method + field       | `ports/baseimage.go`                   | Sonnet        | —          | Low      |
| 3   | Example: security-sensitive resolver change | `resolver.go`                          | Opus          | #2         | **High** |
| 4   | Example: pipeline restructuring             | `pipeline.go`                          | Sonnet        | #2, #3     | Medium   |
| 5   | Example: new regression tests               | `resolver_test.go`, `pipeline_test.go` | Sonnet        | #3, #4     | Medium   |
| 6   | Example: adversarial full-diff review       | (all changed files)                    | Opus          | #1–#5      | High     |

### 6.3 Model selection rubric

Apply this table when assigning the "Agent / Model" column above — don't default to Sonnet for everything, and don't reach for Opus out of caution alone.

| Model      | Use for                                                                                                                                                                                                                                          | Do NOT use for                                                                                 |
| ---------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------------------- |
| **Haiku**  | Mechanical, low-risk, high-confidence work: adding a stub/mock method, renaming, boilerplate test scaffolding, doc/comment updates                                                                                                               | Anything touching security-sensitive code, concurrency, or files with recent hardening commits |
| **Sonnet** | Default for everything else: new features, most refactors, most test-writing, pipeline/control-flow changes                                                                                                                                      | Nothing specific — this is the default; escalate only when the rubric below says to            |
| **Opus**   | Only when it **measurably saves iterations**: security-sensitive files (recent CVE/hardening commits), subtle concurrency/cancellation correctness, final adversarial review of a multi-agent diff, or a bug already caught once at a lower tier | Routine work "just to be safe" — if Sonnet would get it right in one pass, Opus is waste       |

> [!NOTE]
> If a Haiku or Sonnet sub-agent's output gets corrected more than once for the same file in one plan, escalate that file's remaining work to the next tier up rather than re-prompting the same tier a third time.

### 6.4 `TodoWrite` discipline

Claude Code's built-in task list (`TodoWrite`) is the mechanism behind "Aufgaben aktualisiert" — it is not optional bookkeeping, it is the user-facing progress signal. Agents **MUST**:

- Create a `TodoWrite` entry for **every row of the Execution Table**, not a coarser summary — one entry per sub-agent dispatch, not one entry for "implementation."
- Keep each todo's content to a single short, concrete phrase (e.g. `resolver.go — VerifyBaseImage (Opus)`, not `work on the base image stuff`).
- Update status (`pending` → `in_progress` → `completed`) the moment it actually changes — not batched at the end of a phase.
- Never mark a todo `completed` until its verification step (Section 5) has actually run and passed for that piece.

### 6.5 ADHD-friendly status updates (user-facing)

After **every** `TodoWrite` status change or sub-agent completion, emit one short status line to the user — not a paragraph — using this shape:

```
[✅ done | 🔄 running | ⏳ waiting | ❌ failed] <step> (<agent>) → next: <step>
```

Examples:

- `✅ pipeline.go restructuring (Sonnet) → 🔄 resolver.go (Opus) running`
- `❌ resolver_test.go — digest mismatch test failing (Sonnet) → fixing before continuing`
- `⏳ waiting on test-writing agent (Sonnet) — next update on completion or ~25min`

Do not restate the full plan or Execution Table on every update — one line, referencing the step number from the Execution Table if useful, is sufficient. Save prose explanations for when something actually went wrong or a decision point needs input.

---

## 7. Keeping Documentation, Lessons & Project State Up-to-Date

Agents **MUST** keep the project documentation and persistent knowledge graph synchronized whenever code, CLI flags, interfaces, or architectural patterns change:

- **`Lessons.md`**: Record bug post-mortems, root causes, and preventative rules flagged during self-review or debugging sessions.
- **`Roadmap.md`**: Update task completion status (`[x]`), adjust scopes, or re-prioritize items.
- **`ARCHITECTURE.md`**: Update architectural diagrams, adapter contracts, layer layouts, and boundary descriptions.
- **`Vocabulary.md`**: Maintain the complete, human-readable reference of all CLI commands, subcommands, flags, defaults, and runtime environment variables.
- **Serena MCP Memories (`mem:*`)**: Keep the machine-readable project memory graph for agents (`mem:core`, `mem:conventions`, `mem:tech_stack`, `mem:telemetry`, `mem:task_completion`, etc.) updated with latest invariants and decisions.

> [!IMPORTANT]
> **Serena MCP is for Agents, docs are for humans.** Make sure to chose the most suitable wording, style, and tone for each document.

---

## 8. "Next Best Steps" Policy

After finishing any implementation task, milestone, or phase, agents **MUST** include a **"Next Best Steps"** section in their final answer.

- Base recommendations directly on remaining items in [`Roadmap.md`](Roadmap.md) and [`AdditionalFeatures.md`](AdditionalFeatures.md).
- Highlight logical next features based on priority, developer experience impact, and architectural dependencies.
