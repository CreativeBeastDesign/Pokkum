<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: generic-secret-rule-matched-minified-code)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# The generic secret rule fired on minified code

| Field | Value |
| --- | --- |
| Status | shipped |
| Stage | v1.1 |
| Kind | fix |
| Tier | foundation |
| Area | Supply Chain & Attestation |

## Summary

Any 8+ run of non-quote, non-space bytes after a password/token/secret key counted as a credential, so minified bundles produced constant false positives.

## Problem

The generic rule's value class was `[^"'\s]{8,}` — anything at least eight bytes long that is
not a quote or whitespace. Minified JavaScript is full of such runs, and a reported finding
captured `,!!_),!_){this.error=` after a `token:` key: unambiguously code, reported as a
hardcoded credential.

The cost is not just noise. A secret scanner that cries wolf gets switched off, and this one
is on by default and fails the build, so a project with a stale minified bundle in its tree
could not build until it either exempted the finding or disabled the scan.

## Decision

Shipped 2026-08-19. The value class now also excludes JS structural punctuation —
`( ) { } [ ] ;` — and nothing more.

That specific choice matters. Restricting values to an alphanumeric-plus-base64 class would
have killed this false positive and simultaneously stopped matching `p@ss,w0rd!` — a real
password, of exactly the kind the rule exists to catch. Missing a genuine secret is the worse
failure for a secret scanner, so the tightening targets the shape of *code* (brackets, braces,
statement separators) rather than the shape of *secrets*. Tested in both directions: five code
shapes rejected, five real credential shapes still caught, including a punctuation-heavy
password and a padded base64 value.

Alongside it, findings whose first path segment is a conventional output directory (`build`,
`dist`, `out`, `.output`, `.vercel`, `.netlify`, `.next`) now trigger a warning naming those
directories and suggesting `.pokkumignore`. `pokkum init`'s default excludes `build/`, but init
never rewrites an existing `.pokkumignore`, so every project initialised before that default
kept scanning its own build output with nothing to explain why. Only the first segment counts:
a nested `src/build/` is far more likely to be real source, and suggesting it be ignored would
be actively bad advice.

**Side effect worth stating**, found by the marker tests going red: `[REDACTED]` contains
brackets, so the reported redaction-placeholder false positives are no longer flagged at all —
the rule fix subsumes them and the inline marker is not needed for that case. The marker tests
had to be repointed at a fixture the rule still matches, and doing so exposed that
`fallbackPassword`/`thirdPassword` never matched either: the rule requires a word boundary
before `password`, so a camelCase key like `apiKey` or a suffixed one like `dbPassword` is
outside it. That is a real coverage gap, left alone here rather than widened in the same change
as a tightening — recorded as its own follow-up.

## Implementation

- [internal/adapters/secretguard/guard.go](../../internal/adapters/secretguard/guard.go)
- [internal/core/pipeline.go](../../internal/core/pipeline.go)

## Known Limitations

- The generic rule's key list was word-boundary anchored, so camelCase (`apiKey`) and suffixed (`dbPassword`) identifiers were not matched. Widening it belonged in its own change, alongside the false-positive budget that widening would spend — since closed by [The generic secret rule misses camelCase and suffixed key names](generic-secret-rule-key-coverage.md), which measured that cost at zero on real build output.

## Related

- [Secret findings gave no usable location in minified output, and build artifacts were scanned as source](secret-findings-navigable-in-minified-output.md)
- [Secret-inlining guard (secretguard)](secret-inlining-guard.md)

