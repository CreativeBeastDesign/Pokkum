# Memory Index — read this first, then read only what you need

| Memory | Holds | Read when |
|---|---|---|
| `mem:state` | Current shipped reality per subsystem (signing, base trust, static strategy + viability analysis + preflight gate, runtime dim, dev modes incl. `--cluster`, `pokkum deploy` and `--check`, the agent-facing surface — `pokkum guide`, CLI exit codes, JSON envelopes and the generated config schema — supervisor, caching, secrets, tests, telemetry, asset overlay, hermetic mode, reproducibility/vite-config injection, SBOM, output modes, multi-platform index, SLSA provenance & source verification) — implementation facts, not roadmap status | Before touching any subsystem's code, so you don't re-derive current state by grepping |
| `mem:open_decisions` | Maintainer-facing open decisions: options, tradeoffs, recommendation, status | Before proposing a design touching lock-slot keying, `TrustedRootPath`, node telemetry, exe secret scanning, asset-overlay verify, cache-key inheritance, or hermetic capability dropping |
| `mem:core` | Durable architectural invariants only (hexagonal boundaries, bit-for-bit reproducibility, zero clock access, fail-closed verification, an output format is a serialization and never a verdict, `utils` naming) — nothing here should ever go stale | Before any structural change; these must never regress |
| `mem:conventions` | Concrete adapter-level architecture rules, named packages/ports, and their own current-state notes | Implementing or reviewing an adapter |
| `mem:tech_stack` | Language/runtime/dependency choices and versions | Checking what library/version backs a feature |
| `mem:staticserver` | Deep dive on `--strategy=static` / `pokkum-static` | Working in `internal/adapters/staticserver` or `supervisor/cmd/pokkum-static` |
| `mem:task_completion` | The `make verify` 5-step suite, its two generated-artifact freshness guards (docs, schema), plus `supervisor/`/`tests/integration/` caveats outside its scope | Before declaring any Go change complete |
| `mem:self_review_checklist` | 78-row cross-harness bug-pattern checklist (verified 2026-09-09 — re-check the count before citing it, it grows often). Run it in the two passes its own header describes: the always-on core, then the rows your diff's triggers match | Before declaring any non-trivial diff complete |
| `mem:lessons_index` | Category routing into `Lessons.md`'s 100+ post-mortems — ask "what KIND of change am I making?" rather than guessing the author's keyword | Before writing code in an area with prior incidents; `CLAUDE.md`'s pre-task checklist routes here first |
| `mem:memory_maintenance` | Discovery model and the rule for which memory a new fact belongs in | Before writing or editing any memory |

Roadmap/feature status (what's planned, prioritized, or shipped in product
terms) lives in `docs/roadmap/*.yaml` (generated into `docs/`; retired
hand-maintained predecessors are read-only under `docs/archive/`)
(generated docs, the intended single source once built) — not in Serena.
