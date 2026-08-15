# Self-Review Checklist

A concise, invariant-style checklist run against every non-trivial code diff before declaring it complete, as part of the verification protocol in `mem:task_completion`. Unlike `Lessons.md` (an append-only, chronological incident log kept in the repo root), this memory holds only the *currently-active, distilled* rules and is edited in place, not appended to — that's why it lives in Serena rather than the log itself.

Shared across every agent/harness working this codebase (each has Serena access) — this is the one source of truth for the checklist; do not fork a duplicate copy into a harness-specific instruction file.

## How to use

1. After a diff is otherwise ready (tests green, `make verify` passing per `mem:task_completion`), walk every row below against the actual diff lines — not from memory of having read it once.
2. If a row doesn't apply, say so explicitly ("N/A: no concurrent dispatch in this diff") rather than skipping it silently.
3. If a bug is found during this pass, log the root cause and preventative rule in `Lessons.md` (repo root), then come back and close the loop here (see Maintenance rule below).

## Checklist

| # | Check | How to verify |
|---|-------|----------------|
| 1 | Fan-out error paths | For every `errgroup.Group`, `sync.WaitGroup`, or bare `go func()` touched by the diff: grep for every `return`/`continue`/`break` sitting between the dispatch call and its corresponding `Wait()`/`Done()`. Each one is a leak candidate until you can point to why it's safe. |
| 2 | Resource cleanup on every branch | For every `os.File`, `io.Reader`/`io.Closer`, network conn, or lock acquired in the diff: trace every return path, including new early-return branches just added, and confirm cleanup runs on each — not just the happy path. |
| 3 | Multi-item collection logic | If the diff processes a collection (map, slice, multi-document stream), does at least one test use ≥2 items with *differing* content/outcome? Single-item or all-identical fixtures hide item-interaction bugs. |
| 4 | Non-first-item failure injection | For every new error path inside a loop or fan-out, does a test trigger the error on a *non-first* item, not just the first/only one? A single-item error test can pass while a real multi-item failure leaks resources. |
| 5 | Ordering / determinism | Are map iterations, directory traversals, and concurrently-produced results consumed in an explicitly sorted or otherwise deterministic order (bit-for-bit reproducibility invariant, `mem:core`)? |
| 6 | Clock / timestamp access | Does the diff introduce any `time.Now()` or system clock read outside `req.SourceDateEpoch`? |

## Origin

Rows 1 and 4 were added after a real goroutine-leak bug in `internal/adapters/k8s/resolver.go`'s monorepo affected-detection fan-out loop (`mem:core`'s Monorepo Affected-Detection entry): an `AffectedDetector` error on a non-first project path returned before `g.Wait()`, orphaning already-dispatched build goroutines. The only existing test for that error path used a single-project manifest, so it passed without ever exercising the leak — the bug survived a prior instruction that told agents, in prose, to "scan for goroutine leaks," because nothing forced a concrete check against the specific lines where it mattered. Row 3 generalizes the same lesson from an independently-found multi-document YAML annotation cross-contamination bug in the same code area.

## Maintenance rule

This table is edited in place, not appended to. Every time a `Lessons.md` entry is logged: check whether its category matches an existing row here.
- If it matches but the row didn't catch it, revise the row's wording so this exact failure mode is unambiguously covered next time.
- If it's a genuinely new category, add a row.

A `Lessons.md` entry with no corresponding update here is a bug likely to recur — this checklist, not the log, is what actually gets run during the next self-review.
