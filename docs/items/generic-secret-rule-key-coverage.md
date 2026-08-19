<!--
GENERATED — DO NOT EDIT BY HAND.
Source: docs/roadmap/*.yaml (item id: generic-secret-rule-key-coverage)
Regenerate with: make docs   (or: go run ./scripts/gen-docs)
-->

# The generic secret rule misses camelCase and suffixed key names

| Field | Value |
| --- | --- |
| Status | open |
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

## Implementation

- [internal/adapters/secretguard/guard.go](../../internal/adapters/secretguard/guard.go)

## Related

- [The generic secret rule fired on minified code](generic-secret-rule-matched-minified-code.md)

