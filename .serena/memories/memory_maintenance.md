# Memory Maintenance

## Discovery Model

- Start at `mem:index` (not `mem:core`) — it's a routing table, not a summary.
  Read it, then read only the one or two memories the task actually needs.
- `mem:core` is a leaf: durable invariants only, no further routing inside it.
- Use topics/folders (`frontend/core`, etc.) to group related memories when the
  project grows enough to need them; not needed at current size.
- Memory references use a `mem:` prefix inside backticks, e.g. `mem:state`.
  The surrounding text should say which part of the target memory is
  relevant, not just its name (e.g. "see `mem:state`'s Caching entry", not
  "see `mem:state`").
- A memory should not describe when to read itself — that's the referencing
  memory's (or `mem:index`'s) job.

## Style

Dense agent notes, not prose docs. Prefer invariants and tight bullets/tables
over paragraphs. Avoid obvious context, rationale, and examples unless they
prevent a likely mistake. Durable and generalizable, not task-local — except
in `mem:state`, whose whole job is task-local current-reality facts; there,
optimize instead for "a single fact can be updated in place without
rewriting a paragraph."

## Where a new fact goes — the routing rule

This is the rule that keeps the graph from drifting back into a grab-bag.
Ask, in order:

1. **Will this be false the moment a related feature ships or a bug gets
   fixed?** → `mem:state`, filed under the relevant subsystem heading. This is
   almost every "X is now wired," "Y was fixed in commit Z," "the gap is still
   open" fact.
2. **Is this a decision only the maintainer can make, with real tradeoffs?**
   → `mem:open_decisions`, as a new row (options + recommendation + status).
   Move it out once it's decided AND implemented — a decided-but-unbuilt item
   stays as a row with status `decided-not-implemented`.
3. **Is this a structural rule that must hold regardless of which feature
   ships next** (hexagonal boundaries, reproducibility, zero clock access,
   fail-closed verification, naming conventions)? → `mem:core`. If you're not
   sure it'll still be true in six months, it's probably `mem:state`, not
   `mem:core`.
4. **Is this a cross-harness process rule** (verification commands, the
   self-review checklist, planning/orchestration format)? → the existing
   dedicated memory (`mem:task_completion`, `mem:self_review_checklist`) —
   don't create a new one for process content.
5. **Is this roadmap/feature status** (what's planned, prioritized, or "done"
   in product terms)? → it does NOT go in Serena at all. That lives in
   `Roadmap.md` / `Roadmap-v1-Archive.md` / `docs/roadmap/*.yaml` (generated).
   Serena's `mem:state` records *implementation* reality an agent needs while
   coding — which mechanism is wired, which path fails closed — and should
   point at the roadmap docs for status rather than restate it.
6. **Is this deep, adapter-specific mechanism detail** (e.g. the static
   server's candidate-resolution order, byte-for-byte)? → a dedicated
   subsystem memory like `mem:staticserver` is fine when the detail is large
   enough to earn its own file; `mem:state` then carries only a short pointer
   + the facts an agent needs without opening that file. Don't let a
   subsystem memory and `mem:state` describe the same fact twice — if they'd
   drift apart, pick one owner and reference it from the other.

A `Lessons.md` entry with no matching `mem:state` update (for the fact) or
`mem:self_review_checklist` update (for the generalizable pattern) is a bug
likely to recur — the Serena memory, not the log, is what's actually consulted
during the next self-review.

## Maintenance Actions

- Renaming memories: use Serena's rename tool — it updates `mem:` references
  automatically.
- Deleting a memory: first `search_for_pattern`/grep every other memory for
  `mem:<name>` and fix or redirect the dangling reference — rename's
  auto-update does not apply to a delete.
- Folding a small memory into a larger one (e.g. a 14-line subsystem memory
  into `mem:state`): move the content first, delete the source second, then
  sweep for dangling `mem:` references the same way as a delete.
