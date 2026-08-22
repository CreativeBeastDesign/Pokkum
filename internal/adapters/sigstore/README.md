# internal/adapters/sigstore

## What this package contains

This package embeds a snapshot of the Sigstore **public-good** trust root
(`trusted-root-public-good.json`): the Fulcio root CA and intermediate
certificates, the Certificate Transparency (CT) log public key, and the
Rekor transparency log public key that back `https://fulcio.sigstore.dev`
and `https://rekor.sigstore.dev`. It is the root of trust needed to verify
**keyless** Sigstore signatures — signatures produced via Fulcio-issued
short-lived certificates and logged to Rekor, rather than signed with a
static key pair.

`trustedroot.go` exposes the embedded JSON via `DefaultTrustedRootJSON()`,
which returns a defensive copy of the bytes so callers cannot mutate the
package-level embedded data.

See [fixes-to-v1.md](../../../docs/archive/fixes-to-v1.md) for details on
the keyless verification implementation and how it closes the gap that the
static-key Cosign verifier (`internal/adapters/cosign`) alone could not cover
for real upstream signatures on the `distroless`/`chainguard` base image presets.

## The verifier

`verifier.go` implements `ports.KeylessVerifier`. `Verifier.Verify` is
fully **offline**: given the material a caller has already fetched off a
Cosign signature tag, it makes no network calls of its own. Callers
classify failures with `errors.Is` against the package sentinels —
`ErrNoBundle` (no keyless material present at all, the only error on which
falling back to another verification mode is legitimate),
`ErrMalformedMaterial`, `ErrChainInvalid`, `ErrIdentityMismatch` and
`ErrTlogInvalid`.

`legacybundle.go` translates Cosign's legacy signature-tag annotations
into a Sigstore **v0.1** protobuf bundle, which is the only bundle version
this material can legally be expressed as: it carries a Rekor
`SignedEntryTimestamp` (an "inclusion promise") and no inclusion proof,
and `sigstore-go` requires a promise for v0.1 and a proof for v0.2+.

Two properties are worth calling out because they are load-bearing for
security rather than incidental:

- **The `dev.sigstore.cosign/chain` annotation is never trusted.** Only
  the leaf certificate goes into the bundle; the trust chain is built by
  `verify.Verifier` against the trusted root's own Fulcio CAs. The chain
  annotation is attacker-suppliable data served from the same registry as
  the signature, so honouring it would let a forged intermediate stand in
  for Fulcio's. `TestVerify_ChainAnnotationIsNotTrusted` pins this.
- **An empty `KeylessIdentity` is refused before any Sigstore code runs.**
  "Any certificate Fulcio ever issued" is satisfiable by anyone with a
  GitHub account, so the refusal is unreachable-by-construction here
  rather than something delegated to the library.

Fulcio certificates are valid for about ten minutes, so path validation
cannot use the current time. The Rekor entry's integrated time is the sole
time source, which is why the verifier is configured with
`WithTransparencyLog(1)` + `WithIntegratedTimestamps(1)` (plus
`WithSignedCertificateTimestamps(1)` to require the certificate's embedded
SCT to verify against the CT log key). The reasoning for each option is
documented on `verifierOptions` in `verifier.go`.

`testdata/distroless-nonroot/` holds a real captured distroless signature
so the positive path is tested hermetically under `-short`; see its
README. `verifier_network_test.go` repeats the same check against the live
registry and is skipped under `-short`.

## Provenance: how this copy was obtained

> [!IMPORTANT]
> The previous version of this section instructed maintainers to refresh
> this snapshot by copying `sigstore-go`'s bundled
> `examples/trusted-root-public-good.json`. **Do not do that.** That file
> is itself the stale, pre-2023 snapshot this package used to embed — it
> was already missing the Rekor log Sigstore added in 2025-09, and
> following that procedure would silently reinstall the exact bug fixed
> on 2026-08-19 (see `Lessons.md`'s "embedded Sigstore trust root was
> already rejecting valid signatures" entry). `sigstore-go`'s own example
> is not a live source; it is a fixture bundled with a specific module
> release and can lag the real Sigstore trust root by years.

The current snapshot (`trusted-root-public-good.json`) and its provenance
sidecar (`trusted-root-metadata.json`) are regenerated from the **raw,
TUF-signature-verified `trusted_root.json` target**, fetched directly from
Sigstore's live public-good TUF repository (`tuf-repo-cdn.sigstore.dev`,
the same value as `github.com/sigstore/sigstore-go/pkg/tuf.DefaultMirror`
— see `tufrefresh.go`'s `TrustedRootTUFRepository`). The raw target bytes
are embedded verbatim, not sigstore-go's parsed-and-remarshalled struct,
so what's on disk here is exactly what the TUF repository signed and can
be digested/diffed byte-for-byte. This was verified reproducible: two
independent fetches of the target produced identical bytes.

`trusted-root-metadata.json` (`TrustedRootMetadata` in `trustedroot.go`)
records `capturedAt` (when the snapshot was fetched) and `sha256` (a
digest of the embedded snapshot bytes) as a provenance sidecar — this is
what lets the freshness guards below detect both "this snapshot is old"
and "this snapshot and its own provenance record disagree," independent
of each other.

## How the guards work — and why refresh instructions live in one place, not this file

Trust root rotations are rare (Sigstore rotating the Fulcio root CA, a CT
log key, or — the failure mode that actually bit this package — adding a
**new Rekor transparency log shard**, which a snapshot captured before it
existed rejects with an error that reads exactly like a forged signature,
not a coverage gap). Because a stale snapshot fails *silently* until
someone hits the specific log/cert it doesn't cover, this package does
not rely on a maintainer remembering to check an expiry date. Three
guards, spread across `trustedroot_freshness_test.go` and
`trustedroot_network_test.go`:

1. **Always-on age/expiry tests** (`TestEmbeddedTrustedRoot_IsFreshNow`,
   `TestEmbeddedTrustedRoot_ActiveAnchorExpiryIsWhereExpected`) — fail,
   not warn, once the snapshot is older than `TrustedRootMaxAge` (180
   days) or an anchor is within `TrustedRootExpiryWindow` (90 days) of
   expiring. Run in every `go test ./internal/adapters/sigstore/...`,
   including `-short`.
2. **Network divergence test** (`TestTrustedRootSnapshot_TracksLiveTUFRepository`,
   `trustedroot_network_test.go`) — compares the embedded anchor set
   against the live TUF repository in both directions. **Skips (never
   fails) when the repository is unreachable**, deliberately, so network
   flakiness can never get this guard disabled out of frustration; it
   only runs without `-short`.
3. **Digest tripwire** (`CheckTrustedRootFreshness`'s digest check) —
   fails if the embedded snapshot's bytes don't hash to
   `trusted-root-metadata.json`'s recorded `sha256`, so a snapshot and
   its own provenance record can never silently drift apart and make the
   age check meaningless.

**The refresh command is `RefreshTrustedRootCommand`** (`trustedroot_freshness.go`):

```
POKKUM_UPDATE_SIGSTORE_TRUSTED_ROOT=1 go test ./internal/adapters/sigstore/ \
  -run TestTrustedRootSnapshot_TracksLiveTUFRepository -count=1
```

This is deliberately the *same* test the network divergence guard runs,
following Go's golden-file `-update` convention: the code path that
*detects* the embedded snapshot has drifted from the live TUF repository
is the same code path that *fixes* it, so the two cannot disagree with
each other the way a hand-copied file and a hand-written check can. Every
staleness message in this package (runtime warning, unknown-log error,
test failure) quotes this exact string rather than its own copy, so a
future rename can't leave one of them stale.

After running the refresh command:

1. `git diff internal/adapters/sigstore/trusted-root-public-good.json
   internal/adapters/sigstore/trusted-root-metadata.json` to see exactly
   what changed (new CA cert, new log key, expiry window, new Rekor
   shard) and sanity-check the diff before committing.
2. Run `go test ./internal/adapters/sigstore/...` to confirm the refreshed
   files parse and pass all three guards.
3. **Regenerate `testdata/trusted-root-wrong-keys.json`** — it's derived
   from the snapshot with exactly one field changed (see
   `testdata/README.md`), so its own "one field changed" claim goes stale
   the moment the snapshot it was derived from is regenerated. There is
   no automated check for this; it must be done by hand each time.

## Offline contract

`Verify` (this package's actual verification entry point) never performs
network I/O, unconditionally — the trust root it verifies against is
either the embedded snapshot or a caller-supplied `TrustedRootJSON`, never
a live fetch. The **opt-in** TUF client (`tufrefresh.go`'s
`FetchTrustedRootJSON`/`ResolveTrustedRootJSON`, `TUFOptions`) exists only
for the refresh workflow above and for a caller that explicitly wants a
live-refreshed root at runtime; it is not on any path `Verify` reaches.
Its `Offline` option fails closed with `core.ErrHermeticViolation`
*before constructing a TUF client at all* when set — sigstore-go's own TUF
client has no guaranteed-offline mode (`ForceCache`/`CacheValidity` can
both still trigger a network refresh), so "hermetic" has to be a decision
made before the client exists, not a flag passed into it. This mirrors
`bunruntime`'s hermetic-cache-miss contract. Not yet wired to a CLI flag —
see `docs/archive/Roadmap.md`'s Tier 2 tracking for `--sigstore-tuf-refresh`.

## Rekor-coverage diagnostic

A signature recorded on a Rekor log this package's trust root doesn't
know about still fails closed — the verdict is deliberately unchanged,
never relaxed to "trust unknown logs." What changed (2026-08-19) is the
*diagnosis*: the error now names the logs the embedded snapshot actually
covers and says the failure is most likely a trust-root coverage gap,
rather than reading like a forged-signature error — because until this
fix, that is exactly the failure this package produced for a genuinely
valid signature on a log added after the snapshot was captured. Proven by
reverting the message change alone and confirming only the diagnosis
regressed, not the verdict.
