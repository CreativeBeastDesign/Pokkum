# Pokkum — AI Agent Instructions & Guidelines

Welcome to **Pokkum**, a Go-based zero-dependency OCI container image compiler for SvelteKit applications.

This document defines the core guidelines, architectural invariants, tool usage policies, and verification protocols for AI agents working on this codebase.

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
> - Do **NOT** run the verification test suite for simple user questions, code exploration, planning, or documentation updates (e.g. `docs/roadmap/*.yaml`, `AGENTS.md`, `Lessons.md`, `.md` files, docstrings, or memory graph edits).
> - Tests should be executed **only before declaring completion of actual code modifications**.

When code changes have occurred, agents **MUST** execute the following 4-step verification suite before declaring completion:

1. **Formatting & Static Analysis**:
   ```bash
   gofmt -s -w . && go vet ./...
   ```
2. **Adapter Unit Tests**:
   ```bash
   go test ./internal/adapters/...
   ```
3. **CLI Compilation Check**:
   ```bash
   go build -o ./pokkum-test ./cmd/pokkum && rm -f ./pokkum-test
   ```
4. **Full Internal Test Suite (includes Architecture Purity Verification `internal/architecture_test.go`)**:
   ```bash
   go test ./internal/...
   ```

---

## 6. Clean-Context Sub-Agent Verification & Bug Quarantine Protocol

To eliminate confirmation bias and prevent regressions, after implementing any non-trivial feature or refactor (and passing initial verification), the main agent **MUST** invoke a sub-agent with a clean, isolated context (`invoke_subagent` with `TypeName: "self"` or a specialized verifier).

### Sub-Agent Context Isolation Boundary

The prompt to the sub-agent must contain **ONLY**:

1. The list of modified, added, or deleted files (`git status` / file paths).
2. A concise functional specification of what the changes/features are intended to accomplish.
   _(No previous conversational history, assumptions, or debug traces)._

### Sub-Agent Verification Checklist

The sub-agent executes independently in its clean context and must:

1. **Run Full Test Suite**: Execute the 4-step verification protocol to confirm baseline health.
2. **Author 2–3 Adversarial / Edge-Case Tests**: Write 2–3 additional, non-trivial unit or integration tests specifically targeting edge cases, boundary conditions, invalid inputs, or nuanced changed behavior (strictly beyond the happy path).
3. **Three-Pronged Static & Semantic Code Scan**:
   - **(a) Logic Errors**: Inspect for off-by-one errors, nil pointer dereferences, unhandled errors, missed edge cases, and state inconsistencies.
   - **(b) Unintended Side Effects**: Verify that changes have not leaked into or regressed unrelated code paths (which should never happen if Hexagonal boundaries are kept intact, unless changes touch shared `-utils` adapters like `sveltekitutils` or `ignoreutils`).
   - **(c) Race Conditions & Resource Leaks**: Scan for goroutine leaks, unclosed `io.Reader`/`io.Closer`/`os.File` handles, mutex contention, and clock access violations (`time.Now()` instead of `SourceDateEpoch`).
4. **Diff Verification**: Verify every suspected issue directly against the actual `git diff` before reporting.
5. **Transparent Remediation (Zero Silent Patches)**:
   - If a bug, flaw, or edge case failure is found:
     - The sub-agent **MUST** explicitly flag the defect, implement the fix, and re-run all tests.
     - The sub-agent **MUST** log full details of the bug, root cause, affected files, and the applied diff back to the main agent.
     - **Silent patching is strictly forbidden.**

### Main Agent Root Cause Analysis & `Lessons.md`

Upon receiving a bug/defect report from the sub-agent:

1. The main agent **MUST** conduct a root-cause analysis into _why_ the bug was introduced in the first place (e.g., faulty assumption, missed boundary, leaky abstraction).
2. The main agent **MUST** log the incident and key takeaway in [`Lessons.md`](Lessons.md) (creating the file if it does not exist).

---

## 7. Keeping Documentation, Lessons & Project State Up-to-Date

Agents **MUST** keep the project documentation and persistent knowledge graph synchronized whenever code, CLI flags, interfaces, or architectural patterns change:

- **`Lessons.md`**: Record bug post-mortems, root causes, and preventative rules flagged during sub-agent verification or debugging sessions.
- **Roadmap / shipped log / feature list — `docs/roadmap/*.yaml` ONLY.** These four documents are **generated**: `docs/Roadmap.md`, `docs/Shipped.md`, `docs/Features.md`, and `docs/items/*.md`. **Never hand-edit them** — `make docs` overwrites them and deletes orphaned item pages, so an edit made there is silently discarded. Edit the item in `docs/roadmap/<area>.yaml`, then run `make docs`. `make check-docs-freshness` fails the build if generated output does not match its source, and the generator rejects an unknown field, a bad enum value, an `impl` path that does not exist on disk, or an `[title](item:<id>)` reference to an unknown id.
- **`docs/archive/`**: retired historical documents (the old hand-maintained `Roadmap.md`, `Feature-list.md`, `AdditionalFeatures.md`, `overnight-findings.md` and the v1-era logs). **Read-only** — cite them for provenance, never update them.
- **`ARCHITECTURE.md`**: Update architectural diagrams, adapter contracts, layer layouts, and boundary descriptions.
- **`Vocabulary.md`**: Maintain the complete, human-readable reference of all CLI commands, subcommands, flags, defaults, and runtime environment variables.
- **Serena MCP Memories (`mem:*`)**: Keep the machine-readable project memory graph for agents (`mem:core`, `mem:conventions`, `mem:tech_stack`, `mem:telemetry`, `mem:task_completion`, etc.) updated with latest invariants and decisions.

---

## 8. "Next Best Steps" Policy

After finishing any implementation task, milestone, or phase, agents **MUST** include a **"Next Best Steps"** section in their final answer.

- Base recommendations directly on the open items in [`docs/Roadmap.md`](docs/Roadmap.md) (generated from [`docs/roadmap/*.yaml`](docs/roadmap)).
- Highlight logical next features based on priority, developer experience impact, and architectural dependencies.
