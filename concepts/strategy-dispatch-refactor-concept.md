# Concept: Consolidating `BuildStrategy` Dispatch

## 1. Problem Statement & Motivation

`ports.BuildStrategy` (`layered` / `exe` / `static`) is dispatched as scattered `if`/`switch` conditionals rather than through any shared abstraction. As of the `--strategy=static` addition, a grep for direct strategy comparisons turns up roughly 20 sites across 4 files:

| File                                     | Sites | What varies                                                                                                 |
| ---------------------------------------- | ----- | ----------------------------------------------------------------------------------------------------------- |
| `internal/adapters/bunexec/compiler.go`  | 6     | Target adapter package, entrypoint path, output directory, post-build validation                            |
| `internal/adapters/packager/packager.go` | 8     | Which layers get built (Bun+supervisor+server vs. static-server+client+prerendered), runtime env/entrypoint |
| `internal/core/pipeline.go`              | 6     | Compile-vs-skip, `AllowStatic` base-image gate, toolchain version bookkeeping, `Deps` validation            |
| `cmd/pokkum/build.go`                    | 2–3   | Flag reconciliation (`--static` shorthand vs. explicit `--strategy`), default base image                    |

`layered` and `exe` already established this as a two-way split; adding `static` as a third parallel conditional in each site, rather than generalizing, was flagged in review (2026-08-16) as a real but non-urgent risk: **no compiler-enforced completeness**. A future strategy requires touching the same files again with nothing to catch a missed branch (e.g. updating the `switch` in `packager.go` but forgetting the corresponding case in `pipeline.go`'s fan-out).

This was deliberately **not** fixed as part of that review — introducing an abstraction for a currently-hypothetical fourth strategy carries real regression risk against working code, and conflicts with the "don't design for hypothetical future requirements" guidance in `CLAUDE.md` §"Don't add features, refactor, or introduce abstractions beyond what the task requires." This document exists so the design is ready and reviewed _before_ it's needed, not as a mandate to build it now — see §6.

---

## 2. Constraint: this is not a single cross-cutting interface

The instinctive fix — one `Strategy` interface with `Prepare`/`Package`/`Validate` methods, one implementation per strategy — does not fit this codebase's Hexagonal boundaries cleanly. The three dispatch points that matter live in **two different adapter packages plus core orchestration**:

- `internal/adapters/bunexec` (a concrete adapter implementing `ports.Compiler`)
- `internal/adapters/packager` (a concrete adapter implementing `ports.Packager`)
- `internal/core/pipeline.go` (orchestration, per `CLAUDE.md`'s `internal/core` boundary: imports `internal/ports` + stdlib only, never a concrete adapter)

A single interface spanning all three would require either core importing adapter-specific strategy types (violating the boundary `internal/core` must respect) or `internal/ports` growing an interface whose only purpose is to let two unrelated adapters share dispatch logic with each other — not what `ports` is for. **The correct shape is therefore three separate, package-local dispatch tables, not one shared interface.**

---

## 3. Proposed Design (per package)

### 3.1 `internal/adapters/bunexec`

```go
type strategyProfile struct {
    targetAdapter func(req ports.PrepareRequest) string
    outputDir     func(req ports.PrepareRequest) string
    entrypoint    func(req ports.PrepareRequest) string
    validate      func(req ports.PrepareRequest, outputDir string) error // post-build check (entrypoint exists / prerendered exists)
}

var strategyProfiles = map[ports.BuildStrategy]strategyProfile{
    ports.StrategyLayered: { /* ... */ },
    ports.StrategyExe:     { /* ... */ },
    ports.StrategyStatic:  { /* ... */ },
}
```

`Prepare` looks up `strategyProfiles[req.Strategy]`, returns a clear "unknown strategy" error if absent (replacing 3 scattered `if`/`else if` chains with one lookup + one explicit failure mode for the "forgot to register it" case — the compiler-enforced-completeness gap this doc exists to close).

### 3.2 `internal/adapters/packager`

Same shape: a `map[ports.BuildStrategy]func(ctx, req, ts, addenda) ([]mutate.Addendum, error)` replacing the `if req.Strategy == ports.StrategyLayered { ... } else if req.Strategy.ApplyStatic() { ... } else { ... }` chain in `Build`. `appendPrerenderedLayer` (already extracted, shared by two branches) becomes a natural building block inside both the `layered` and `static` table entries.

### 3.3 `internal/core/pipeline.go`

Smaller surface — mostly boolean gates (`AllowStatic`, whether to skip `Compile`, which toolchain-version field to populate) rather than full behavioral branching. These can likely collapse into `ports.BuildStrategy` methods (the existing precedent: `ApplyStatic()` on `ports.BuildStrategy`, added 2026-08-16) rather than needing their own dispatch table — e.g. `req.Compile.Strategy.RequiresCompile() bool`, `.SkipsBunRuntime() bool`. This keeps `pipeline.go`'s dispatch as simple boolean predicates on the existing type instead of a fourth table.

### 3.4 `cmd/pokkum/build.go`

Out of scope for this refactor — flag reconciliation (`--static` shorthand, `--strategy` conflict detection, default base image per strategy) is CLI-specific and doesn't benefit from the same abstraction; leave as explicit `if` statements, which is the right altitude for one-time flag parsing.

---

## 4. Migration Plan

1. **`bunexec`**: introduce `strategyProfiles`, migrate `Prepare`'s dispatch, keep `patchPrerenderedHandler`/`prerendered_patch.go` untouched (strategy-specific but already isolated).
2. **`packager`**: introduce the layer-building dispatch table, reusing `appendPrerenderedLayer` and the existing `build*Layer` helpers (`layer.go`) as-is — this refactor changes _how_ they're selected, not their bodies.
3. **`ports.BuildStrategy`**: add the boolean predicate methods `pipeline.go` needs, following the `ApplyStatic()` precedent.
4. **Regression coverage**: for each migrated dispatch point, a table-driven test asserting all three strategies produce identical output to pre-refactor behavior (golden-file or struct-equality comparison against the current `if`-chain's output) — this is the step that makes the refactor low-risk; without it, don't proceed.
5. Do **not** attempt a single unified interface across all three packages (§2) — that is the mistake this design deliberately avoids.

---

## 5. Risks

- **Regression risk against working code** for zero present-day bug — the scattered dispatch is correct today; this is a maintainability improvement, not a bug fix. Any migration must ship with the golden-output regression tests in §4.4 before merging, not after.
- **False sense of completeness**: a lookup table with a clear "missing case" error is only as good as never being bypassed — if a future author adds a fourth strategy by pattern-matching an existing `if`-chain elsewhere (e.g. in `cmd/pokkum/build.go`, which stays untouched per §3.4) instead of registering it in all three tables, the same gap resurfaces with an extra layer of indirection hiding it.

---

## 6. Recommendation on timing

Per `CLAUDE.md`'s guidance against designing for hypothetical requirements: **do not build this until a concrete fourth strategy (or a second bug caused by the scattered-dispatch pattern) makes the maintenance cost real, not projected.** Keep this document current if the dispatch sites drift further in the meantime, so the design is ready to execute quickly when the trigger arrives.

---

## 7. Open Questions

- Should `bunexec` and `packager`'s dispatch tables be exported (`ports`-visible) for testability, or package-private? Leaning private — nothing outside either adapter needs to construct a strategy profile directly.
  - Decision: Private
- Is `pipeline.go`'s dispatch actually simple enough for boolean predicates (§3.3), or will a genuine third table be needed once examined line-by-line? Worth a short spike before committing to the design.
  - Decision:
- Does this pattern generalize to `ports.BaseImagePreset` too (see `new-chainguard-static-preset-concept.md`) — both are enum-keyed behavioral dispatch problems in this codebase; worth a shared review once both concepts are acted on, to avoid two subtly different table-dispatch conventions.
  - Decision: Yes, if it makes the code cleaner and easier to maintain.
