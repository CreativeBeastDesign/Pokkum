<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: exit-code-reference)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# Documented CLI exit-code table

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.2 |
| Kind | dx |
| Tier | polish |
| Area | Developer Experience |

## Summary

The CLI's exit codes are now a published table in both `Vocabulary.md` and `pokkum guide exit-codes`, guarded so a new code cannot ship undocumented.

## Problem

This item's own summary was wrong on both counts, which is worth recording rather than
quietly editing away: `125` and `126` are **not** CLI codes and were **not** undocumented.
They belong to `pokkum-init` — the supervisor running as PID 1 inside the container — and
`Vocabulary.md` §19 has documented them, and five others, in full for some time.

The genuinely undocumented set was the CLI's own, and it contained a real distinction
nobody could use. `pokkum verify` exits `2` when verification **could not be performed**
(unreadable trust root, unresolvable Sigstore root, unresolvable provenance, signature
material it could not characterize) and `1` when verification **ran and produced a
negative verdict**. That is precisely the "found nothing" versus "could not check"
separation `mem:self_review_checklist` row 52 exists to enforce — implemented correctly,
and documented nowhere, so every caller gating on `!= 0` collapsed a broken verifier and
a tampered image into the same outcome with nothing to warn them.

## Decision

`Vocabulary.md` §18d publishes the CLI table, deliberately adjacent to §19's supervisor
table and opening by saying the two sets are unrelated — that confusion is the reason
this item's own summary was wrong. `pokkum guide exit-codes` carries the same content for
readers who do not have this repository, and states the `1`/`2` split as the reason to
gate CI on the specific code rather than on non-zero.

Two facts documented that were previously discoverable only from source: cobra usage
errors exit `1`, not `2`, so exit status alone cannot distinguish a mistyped command from
a failed run; and `pokkum apply` propagates `kubectl`'s own exit code verbatim rather
than collapsing it.

`cmd/pokkum/exitcodes_test.go` AST-scans `cmd/pokkum` for literal `os.Exit`/`exitFunc`
arguments and requires each to appear in both places, with a separate guard for the
kubectl passthrough that no literal scan can reach. Shown red by introducing a throwaway
`os.Exit(3)`, which the guard reported by file and line.

## Implementation

- [Vocabulary.md](../../Vocabulary.md)
- [cmd/pokkum/guide.go](../../cmd/pokkum/guide.go)
- [cmd/pokkum/exitcodes_test.go](../../cmd/pokkum/exitcodes_test.go)
- [cmd/pokkum/verify.go](../../cmd/pokkum/verify.go)

## Known Limitations

- The CLI still has only three codes. Giving categories of failure distinct codes (config vs network vs policy) would be a behaviour change, not a documentation one, and is not attempted here.

## Related

- [pokkum guide — the operating manual, shipped inside the binary](agent-guide-command.md)
- [Standardized machine-readable output (--output=json)](json-output-envelope.md)
- [pokkum mcp — Model Context Protocol server as a second driving adapter](mcp-server.md)

