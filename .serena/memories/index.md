# Memory Index — read this first, then read only what you need

| Memory | Holds | Read when |
|---|---|---|
| `mem:state` | Current shipped reality per subsystem (signing, base trust, static strategy, runtime dim, dev mode, supervisor, caching, secrets, tests, telemetry, asset overlay, hermetic mode) — implementation facts, not roadmap status | Before touching any subsystem's code, so you don't re-derive current state by grepping |
| `mem:open_decisions` | Maintainer-facing open decisions: options, tradeoffs, recommendation, status | Before proposing a design touching lock-slot keying, `TrustedRootPath`, node telemetry, exe secret scanning, asset-overlay verify, cache-key inheritance, or hermetic capability dropping |
| `mem:core` | Durable architectural invariants only (hexagonal boundaries, bit-for-bit reproducibility, zero clock access, fail-closed verification, `utils` naming) — nothing here should ever go stale | Before any structural change; these must never regress |
| `mem:conventions` | Concrete adapter-level architecture rules, named packages/ports, and their own current-state notes | Implementing or reviewing an adapter |
| `mem:tech_stack` | Language/runtime/dependency choices and versions | Checking what library/version backs a feature |
| `mem:staticserver` | Deep dive on `--strategy=static` / `pokkum-static` | Working in `internal/adapters/staticserver` or `supervisor/cmd/pokkum-static` |
| `mem:task_completion` | The `make verify` 5-step suite, plus `supervisor/`/`tests/integration/` caveats outside its scope | Before declaring any Go change complete |
| `mem:self_review_checklist` | 38-row cross-harness bug-pattern checklist — read whole, not summarized | Before declaring any non-trivial diff complete |
| `mem:memory_maintenance` | Discovery model and the rule for which memory a new fact belongs in | Before writing or editing any memory |

Roadmap/feature status (what's planned, prioritized, or shipped in product
terms) lives in `Roadmap.md` / `Roadmap-v1-Archive.md` / `docs/roadmap/*.yaml`
(generated docs, the intended single source once built) — not in Serena.
