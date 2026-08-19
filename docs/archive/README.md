# Archive

Retired documents, kept for provenance. **Read-only — do not update anything in this directory.**

These were hand-maintained status documents that drifted against each other and against the code. They were migrated on 2026-08-19 into [`docs/roadmap/*.yaml`](../roadmap), the single source that now generates [`docs/Roadmap.md`](../Roadmap.md), [`docs/Shipped.md`](../Shipped.md), [`docs/Features.md`](../Features.md) and [`docs/items/`](../items).

## Why they are still here

Two reasons, both practical:

1. **Item numbering.** Code comments and `Lessons.md` entries cite things like "Roadmap.md item 2h", "Tier 1.1", "PR-5" and "PB-1". That numbering exists only in these files — the generated roadmap uses stable slugs (`placeholder-pubkey-fallback-removed`) instead. Deleting these would turn every such citation into a dead reference.
2. **A real code dependency.** [`overnight-findings.md`](overnight-findings.md) is still parsed by the docs generator to validate each item's `evidence.findings` numbers — see `findingsFromPath` in [`scripts/gen-docs/render.go`](../../scripts/gen-docs/render.go). It is not merely a citation target.

## What is here

| File | Was | Superseded by |
|---|---|---|
| [Roadmap.md](Roadmap.md) | The hand-maintained roadmap | [docs/Roadmap.md](../Roadmap.md) |
| [Feature-list.md](Feature-list.md) | Shipped-capability list | [docs/Features.md](../Features.md) |
| [AdditionalFeatures.md](AdditionalFeatures.md) | Backlog and reviewer-response matrix | [docs/Roadmap.md](../Roadmap.md) |
| [overnight-findings.md](overnight-findings.md) | Numbered bug log from the overnight runs | Items' `evidence.findings` (still parsed from here) |
| [Roadmap-v1-Archive.md](Roadmap-v1-Archive.md) | v1.0 build history and the Pre-Publication Gate (PB-1…PB-5, PR-1…PR-9) | — historical only |
| [fixes-to-v1.md](fixes-to-v1.md) | Post-v1.0 audit findings and fixes | — historical only |
| [for-users.md](for-users.md) | User-visible changes from that fix round | — historical only |
| [CodeReview.md](CodeReview.md) | An external code review pass | — historical only |
| [Meantime.md](Meantime.md) | Interim working notes | — historical only |
| [vendor-layer-pruning.md](vendor-layer-pruning.md) | Vendor-pruning design note | — historical only |
| [Supply Chain Hardening v1.md](Supply%20Chain%20Hardening%20v1.md) | v1 supply-chain hardening plan | — historical only |
| [RedditPost.md](RedditPost.md) | Draft announcement copy | — historical only |

`Lessons.md` and `paranoid-testing-guide.md` deliberately stayed at the repository root: both are live documents that are still read and updated.
