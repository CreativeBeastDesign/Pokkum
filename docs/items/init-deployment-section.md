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

Whether init should configure `deploy:` at all. Step 1 of the recommendation (`deploy --check`) has shipped; the remaining question is `pokkum deploy init`.

## Problem

`pokkum init` writes every other section of `.pokkum.yaml` and never touches `deploy:`, so the
one step that gets an image in front of users is the one left to hand-editing against
Vocabulary.md.

`deploy:` is not shaped like the fields init already writes. It needs secrets (`token_env`
names a credential; a webhook `endpoint` contains its secret in the URL), it needs facts only
the platform has (`application` is a platform-side id), and its answers cannot be validated
offline.

**`application` is what decides this.** It cannot be typed from memory and cannot be checked
without calling the platform, so any prompt in `pokkum init` — which runs before the user has
a reason to have set `POKKUM_DEPLOY_TOKEN` — structurally cannot obtain it. Every option that
writes a `deploy:` block without it writes a block that does not work.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| A. Prompt in init for the non-secret half | Ask target/method/endpoint; never prompt for the token; leave `application` empty with a comment. | Smallest diff, but writes a block that is incomplete by construction — the user's next `pokkum deploy` fails on the missing `application`, which is the 2026-08-19 'init recommended a command it had guaranteed could not work' shape in a new place. |
| B. A dedicated `pokkum deploy init` | Runs after the credential exists: reads the token, lists the platform's applications, lets the user pick one, writes a complete block verified by --check. | The only option that can get `application` right. Costs a new subcommand and a list-applications call per target; SwiftWave's webhook method has no equivalent and would stay manual. |
| C. Diagnostics only | Leave configuration to the file; spend the effort on `pokkum deploy --check`. | Zero risk, useful in CI regardless of how the config was authored — but leaves first-time setup unaddressed. |

## Recommendation

B, on top of C. **C has now shipped** as [pokkum deploy --check](deploy-check.md), which
also built the resolution-reuse machinery B needs.

What remains is `pokkum deploy init`: the same resolution, plus a list-applications call, plus
an interactive picker, writing the block only once `--check` passes against it. `pokkum init`
gains one closing line pointing at it and writes no `deploy:` key itself.

Worth reassessing before building. `--check` may have closed enough of the gap on its own,
and the honest test is whether anyone still gets `application` wrong now that a single
command tells them their config resolves.

## Known Limitations

- A `pokkum deploy init` picker would be Dokploy-only; SwiftWave's webhook method has no application-listing equivalent.

## Related

- [pokkum deploy --check](deploy-check.md)
- [pokkum init](workspace-init-wizard.md)
- [pokkum deploy (Dokploy, SwiftWave)](paas-deploy-targets.md)

