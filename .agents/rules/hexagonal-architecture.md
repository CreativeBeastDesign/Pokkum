# Rule: Hexagonal Architecture Boundaries

- **`internal/ports`**: Leaf node of the internal dependency graph. It imports standard library + `go-containerregistry/pkg/v1` only. It must **NEVER** import `internal/core` or any concrete adapter (`internal/adapters/*`).
- **`internal/core`**: Domain logic layer. Imports `internal/ports` and standard library. Re-exports port vocabulary as type aliases (`core.Platform = ports.Platform`).
- **`internal/adapters/*`**: Infrastructure layer. Imports `internal/ports` and `internal/core` (for sentinel errors).
- **`cmd/pokkum`**: Composition root where concrete adapters are instantiated and wired together.
- **Shared Utility Package Naming (`utils`)**: Non-port utility packages residing under `internal/adapters/` (e.g. `internal/adapters/sveltekitutils`, `internal/adapters/ignoreutils`) MUST append `utils` to their package name and declare `const IsUtilityPackage = true` to explicitly distinguish them from concrete port adapters.
- **Bit-for-Bit Determinism**: Adapters must never call `time.Now()` or read system clocks. Derive all timestamps from `req.SourceDateEpoch`. Explicitly sort directory iterations and map keys before tarball creation.
- **Zero Mutation**: Auto-injections happen in `.pokkum/` virtual scratch space during builds and never overwrite user-authored repository files.
