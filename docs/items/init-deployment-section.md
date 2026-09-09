<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: init-deployment-section)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# A deployment section in pokkum init, and what pokkum deploy would need to earn it

| Field | Value |
| --- | --- |
| Status | awaiting-decision |
| Stage | v1.2 |
| Kind | feature |
| Tier | polish |
| Area | Developer Experience |

## Summary

Whether init should configure `deploy:` at all, and which of three shapes the expansion of `pokkum deploy` takes.

## Problem

`pokkum init` writes `docker`, `strategy`, `runtime`, `base`, `platforms`, `security`, `sbom`
and `profiles`, and never touches `deploy:` — so the one step that gets an image in front of
users is the one step init leaves entirely to hand-editing `.pokkum.yaml` against
Vocabulary.md. The gap is real.

But `deploy:` is not shaped like the fields init already writes, and that is the whole
difficulty rather than an implementation detail:

- **It needs secrets.** `token_env` names an environment variable holding an API credential,
  and a webhook `endpoint` contains its secret inside the URL. Init writes a file intended to
  be committed. Anything it asks for here it must be careful NOT to write.
- **It needs facts only the platform has.** `application` is a platform-side id. The user
  cannot type it from memory; it has to be looked up in a panel, or fetched over the API with
  a credential init has not been given yet.
- **Answers cannot be validated offline.** Every other init answer is checked against a fixed
  set. An endpoint, an application id and a token can only be validated by calling the
  platform — and `pokkum config validate` reporting a `deploy:` block valid that
  `pokkum deploy` then refuses is a defect this repo has already shipped once (2026-09-01,
  validator-consumer disagreement).
- **Only two targets exist.** `dokploy` and `swiftwave`. A prompt is a poor fit for a
  two-value choice most users answer with "neither".

So the honest question is not "should init ask about deployment" but "what could init ask
that is worth more than the risk of writing a half-configured deploy block", and that depends
on what `pokkum deploy` grows.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| A. Offer deployment in init, ask only for the non-secret half | Add a prompt: target (`none` / `dokploy` / `swiftwave`, default `none`), and for a non-none answer, `method` and `endpoint`. Write `token_env: POKKUM_DEPLOY_TOKEN` as a comment-documented default and NEVER prompt for the token itself. Leave `application` empty with a generated comment saying where to find it. `auto` stays false. | Smallest diff and no new commands. But it writes a `deploy:` block that is structurally incomplete by construction — the user's next `pokkum deploy` fails on the missing `application`, which is the "init recommended a command it had guaranteed could not work" shape (2026-08-19) in a new place. Mitigable by having init's closing `next_command` advice account for it, which is machinery that already exists. |
| B. Keep init out of it; add `pokkum deploy init` | A dedicated interactive subcommand that runs AFTER the platform credential is available: reads `$POKKUM_DEPLOY_TOKEN`, calls the platform's list-applications API, and lets the user pick their application from a real list rather than typing an id. Writes the complete, validated `deploy:` block. `pokkum init` prints one line pointing at it. | Produces a block that is correct and verified against the live platform, which is the only way `application` can be right, and puts the secret-handling in a command whose entire premise is that a credential is present. Costs a new subcommand, a list-applications call per target (Dokploy has one; SwiftWave's webhook method has no equivalent and would stay manual), and network access in a setup path. |
| C. Neither — document it and expand `pokkum deploy` diagnostics instead | Leave configuration to the file and Vocabulary.md. Spend the effort on `pokkum deploy --check`: validate the block, resolve `token_env`, call the platform's authenticated no-op endpoint and report exactly which of endpoint/token/application is wrong. | Zero risk of writing a bad block, and `--check` is useful for CI regardless of how the config was authored. But it leaves the actual gap — first-time setup — unaddressed, and a diagnostic is a worse experience than not needing one. |

## Recommendation

**B, with C's `--check` as its foundation and built first.**

The reasoning is that `application` decides this. It is the one field that cannot be
typed correctly from memory and cannot be validated offline, and every option that writes a
`deploy:` block without it writes a block that does not work. A prompt in `pokkum init`
structurally cannot obtain it, because init runs before the user has a reason to have set
`POKKUM_DEPLOY_TOKEN`. Option A therefore buys a partially-filled block and a deferred
failure, and this codebase's own history says deferred failures from generated config are
expensive.

Sequencing, because the second half is worth shipping alone if the first is enough:

1. `pokkum deploy --check` (option C). Resolves the config exactly as `pokkum deploy` does,
   then reports per-field status. This must reuse the deploy path's own resolution rather
   than reimplementing it — the 2026-09-01 entry is precisely a validator that disagreed
   with its consumer. Ship, and see whether the setup gap persists.
2. `pokkum deploy init` (option B) on top: the same resolution, plus a list-applications
   call, plus an interactive picker, writing the block only once `--check` passes against it.
   `pokkum init` gains one closing line pointing at it and writes no `deploy:` key itself.

What init should do in the meantime is nothing beyond that one line. It is the correct
amount for a command that cannot verify what it would be writing.

## Known Limitations

- SwiftWave's webhook method has no application-listing equivalent, so option B's picker would be Dokploy-only; SwiftWave would fall back to pasting the webhook URL into an env var.
- `pokkum deploy --check` needs an authenticated no-op endpoint per target. If a target has none, the check degrades to config-shape validation only, and must SAY so rather than reporting a pass it did not earn.

## Related

- [pokkum init](workspace-init-wizard.md)
- [pokkum deploy (Dokploy, SwiftWave)](paas-deploy-targets.md)
- [Static-viability analysis (does this project need a server?)](static-viability-analyzer.md)

