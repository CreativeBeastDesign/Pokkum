<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: generic-secret-rule-key-coverage)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# The generic secret rule misses camelCase and suffixed key names

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.1 |
| Kind | hardening |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

password/secret/api_key/token are word-boundary anchored, so apiKey, dbPassword and accessToken are not matched at all.

## Problem

The generic rule keys on `\b(?:password|secret|api_key|token)`. The leading `\b` means a
camelCase or suffixed identifier never matches: `apiKey`, `dbPassword`, `accessToken`,
`clientSecret` and `refreshToken` all slip past, and those are the dominant naming conventions
in JavaScript and TypeScript — the ecosystem Pokkum exists to build.

Found while repairing test fixtures after the minified-code tightening: two fixture lines named
`fallbackPassword` and `thirdPassword` produced no findings, which made a test vacuous and
revealed the gap rather than any deliberate scoping.

This is a false-negative gap in a security control, so it is more serious than the false
positive fixed alongside it — but it must not be fixed in the same change, because widening the
key set spends false-positive budget that the tightening had just recovered, and the two effects
would be impossible to attribute.

## Options

| Option | Description | Tradeoffs |
| --- | --- | --- |
| Case-insensitive suffix match on the key | Match a key ending in password/secret/token/key/api_key, e.g. `[A-Za-z_]*(?:password\|secret\|token\|api_?key)`, still requiring a credential-shaped value. | Covers the real conventions; risks matching `tokenizer`, `secretary`, `keyboard` and similar, so it needs a stop-word check or a trailing word-boundary. |
| Explicit alternation of common conventions | Enumerate apiKey, api_key, apikey, accessToken, clientSecret, dbPassword and the rest. | Precise and readable, but a list to maintain that will always trail real-world naming. |

## Recommendation

Suffix match with a trailing boundary and a small stop-word list, validated against a corpus of real minified bundles so the false-positive cost is measured rather than assumed.

## Decision

Shipped. The key side became suffix-anchored:

    (?i)\b[a-z0-9_]*(?:password|secret|token|api_?key)\s*[:=]\s*["']([^"'\s(){}\[\];]{8,})["']

`[a-z0-9_]*` (case-folded by the leading `(?i)`) absorbs any prefix, so the keyword only has
to be the identifier's *last* component. `api_?key` folds `api_key` and `apikey` into one
alternative — `(?i)` folds letters, not the literal underscore, so the optional `_` has to be
written out. The value class is byte-for-byte unchanged, which is the point: the protection
that stopped this rule matching minified code lives entirely on the value side, so widening
the key cannot reopen it by construction.

**No stop-word list was added, and that is deliberate.** The recommendation above allowed for
one, but the `\s*[:=]` at the end of the key IS the trailing boundary, and it excludes every
candidate stop word structurally: `tokenizer`, `tokenize`, `secretary`, `passwordless`,
`keyboard`, `keys`, `keyof` and `monkey` do not end in the keyword immediately before an
assignment operator, and neither do keyword-as-prefix identifiers like `passwordHash` or
`tokenStore`. A maintained list of exempted words would be an allowlist that decays — each
entry eventually either dead weight or a pre-authorised blind spot for a key genuinely named
that way (`mem:self_review_checklist` row 46). The words live in the test table instead, as
coverage of the structural rule rather than as configuration the rule reads.

**The false-positive cost was measured on real bundles, not assumed.** Three sweeps of the
old and new key patterns, with the scanner's own matching semantics, over real artefacts
already on disk:

| Corpus | Files | Bytes | Before | After |
| --- | --- | --- | --- | --- |
| Real Vite/Rollup output from this repo's own SvelteKit fixtures (`build/`, `.svelte-kit/output`, longest single line ~29 000 chars) | 219 | 2.65 MiB | 0 | 0 |
| Every genuinely minified file on disk (longest line > 500 chars): published npm bundles of the Svelte compiler, Vite, TypeScript, Rollup, Rolldown, esbuild, svelte-check, plus the fixture build output | 109 | 70.0 MiB | 0 | 24 |
| Every real `.js`/`.mjs`/`.cjs`/`.ts` under `testdata/` | 4 598 | 105.6 MiB | 0 | 24 |

All 24 are one class, in three copies of one file — Vite's bundled `js-tokens` lexer, which
reassigns a local named `lastSignificantToken`/`nextLastSignificantToken` to sentinel strings
(`"?InterpolationInTemplate"`, `"?NonExpressionParenEnd"`, four more). They live under
`node_modules/`, which `ScanDirectory` unconditionally skips along with `.git`,
`.svelte-kit` and `.pokkum`, so none is reachable by the deployed control: over the file set
the scanner actually walks in those three fixture projects (134 text files, 727 KiB), the
count is 0 before and 0 after.

The measurement is now a permanent test rather than a one-off. It scans the five real
build-output directories through the adapter and asserts zero findings, with floors on file
count, byte count and longest line so a shrunken or de-minified corpus fails loudly instead
of passing vacuously; a companion test splices a `refreshToken="…"` credential into a real
29 215-character minified line and requires it to be found, so the zero is a scan that can
fail rather than a check that agrees with everything (`mem:self_review_checklist` rows 47
and 50).

Fail-first proof: with only the `Pattern` line reverted to `\b(?:password|secret|api_key|token)`
— the package still compiling, so the failures are behavioural — 16 of the 17 key forms fail,
both suffixed-key column subtests fail, and the spliced-credential corpus test fails. The one
form that keeps passing is bare `api_key`, which the old rule already matched; that is what
shows the table discriminates the fix rather than the surrounding code.

## Implementation

- [internal/adapters/secretguard/guard.go](../../internal/adapters/secretguard/guard.go)
- [internal/adapters/secretguard/generic_key_coverage_test.go](../../internal/adapters/secretguard/generic_key_coverage_test.go)
- [internal/adapters/secretguard/minified_corpus_test.go](../../internal/adapters/secretguard/minified_corpus_test.go)

## Evidence

- Commits: `5af6a29`

## Known Limitations

- An identifier that legitimately ends in `Token`/`Secret`/`Password`/`ApiKey` without being a credential still matches — measured at 24 occurrences of one such class (Vite's bundled `js-tokens` lexer) across 105.6 MiB of real JS. Nothing structural separates `lastSignificantToken` from `accessToken`, and naming the specific identifiers in a stop-word list was rejected as a decaying allowlist. Unreachable today because `node_modules` is never walked, but a vendored or re-bundled copy inside build output would be flagged.
- A quoted key is still not matched: `"apiKey": "…"` fails because the closing quote sits between the keyword and the `[:=]` anchor. This is pre-existing and unchanged by the widening — the old word-anchored rule missed it too — but it means JSON and JSONC config files get no generic-rule coverage at all.
- Kebab-case `api-key` is not matched; `api_?key` folds only the underscore spelling.
- Keyword-as-prefix identifiers (`passwordHash`, `tokenStore`) are deliberately excluded. The rule claims to catch identifiers that ARE a credential, not every identifier that mentions one.

## Related

- [The generic secret rule fired on minified code](generic-secret-rule-matched-minified-code.md)

