# Codebase Conventions & Architecture Rules

- **Hexagonal Architecture Rules**:
  - `internal/ports` is the leaf node of the internal dependency graph. It imports standard library + `go-containerregistry/pkg/v1` only. It must NEVER import `internal/core` or any adapter.
  - `internal/core` imports `internal/ports` and standard library. It re-exports boundary vocabulary as type aliases (`core.Platform = ports.Platform`).
  - `internal/adapters/*` import `internal/ports` and `internal/core` (for sentinel errors).
  - `cmd/pokkum` is the composition root where concrete adapters are instantiated.
- **Shared Utility Package Naming (`utils`)**:
  - Non-adapter utility packages residing under `internal/adapters/` (such as `internal/adapters/sveltekitutils` and `internal/adapters/ignoreutils`) must be appended with `utils` to distinguish them from concrete port adapters.
  - Reusable utility packages should declare `const IsUtilityPackage = true` as an explicit package marker.
- **Determinism**:
  - Adapters must never call `time.Now()` or read system clocks. All timestamps derive from `req.SourceDateEpoch`.
  - Directory iteration and map keys must be explicitly sorted before archiving.
- **Precedence & Zero Mutation**:
  - Auto-injections happen in `.pokkum/` virtual scratch space during builds and never overwrite user-authored repository files.
