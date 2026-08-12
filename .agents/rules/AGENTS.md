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

---

## 4. OpenTelemetry & Observability Rules

- Respect SvelteKit version boundaries (>= 2.31.0 for native tracing).
- Inspect user project for `src/instrumentation.server.ts|js|mjs`. If present, preserve user file intact. If missing, fall back to virtual injection (`.pokkum/src/instrumentation.server.ts`).
- When `--with-otel-sidecar` is set, ensure the OpenTelemetry Collector sidecar spec (`4317` gRPC, `4318` HTTP, `8889` metrics) is correctly injected into Kubernetes Pod manifests.

---

## 5. Task Completion Verification Protocol

The verification protocol applies **ONLY when Go source code (`.go` files, build manifests, dependencies) has been created, modified, or refactored**.

> [!IMPORTANT]
> - Do **NOT** run the verification test suite for simple user questions, code exploration, planning, or documentation updates (e.g. `Roadmap.md`, `AGENTS.md`, `.md` files, docstrings, or memory graph edits).
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

## 6. Roadmap Updates

Agents **MUST** update `Roadmap.md` whenever applicable:
- A planned feature is completed or partially implemented.
- A task's scope, timeline, or feasibility changes significantly.
- Priorities shift based on new user instructions.
- Ensure the roadmap reflects the current state of the project accurately.
