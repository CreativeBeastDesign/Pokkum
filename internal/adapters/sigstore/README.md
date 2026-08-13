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

See [fixes-to-v1.md](../../../fixes-to-v1.md) at the repo root for details on
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

Captured on **2026-08-12**, from the example trust root shipped inside the
`sigstore-go` Go module itself:

```
go get github.com/sigstore/sigstore-go@v1.3.0
cp "$(go env GOMODCACHE)/github.com/sigstore/sigstore-go@v1.3.0/examples/trusted-root-public-good.json" \
   internal/adapters/sigstore/trusted-root-public-good.json
```

The file was copied verbatim (byte-for-byte, no edits) — `md5` of the
module-cache source and the copy in this package match.

It was verified to:

- Parse successfully via `root.NewTrustedRootFromJSON` from
  `github.com/sigstore/sigstore-go/pkg/root` (see
  `trustedroot_test.go`, which runs this check as part of `go test`).
- Contain the expected public-good log identities: the Rekor tlog key ID
  decodes (base64 -> hex) to `c0d23d6a...`, and one of the two CT log
  entries decodes to `dd3d306a...` — both are the well-known public-good
  Rekor/CT log key IDs.

`sigstore-go` ships this file specifically so that consumers who want to
pin a known-good snapshot (rather than fetch the live trust root at
runtime via `root.FetchTrustedRoot()`, which hits Sigstore's TUF
repository over the network) have a ready-made, versioned copy to embed.
Using the module's own example keeps this package's trust root in lock
step with whatever `sigstore-go` version Pokkum depends on.

## How to refresh it

Trust root rotations are rare (they happen when Sigstore rotates the
Fulcio root CA, the CT log, or the Rekor log key), but when one occurs:

1. Bump the `github.com/sigstore/sigstore-go` dependency to the version
   that ships the updated example (`go get
   github.com/sigstore/sigstore-go@<new-version>` then `go mod tidy`).
2. Re-run the copy command above against the new module cache path.
3. `git diff internal/adapters/sigstore/trusted-root-public-good.json` to
   see exactly what changed (new CA cert, new log key, expiry window
   changes, etc.) and sanity-check the diff before committing.
4. Run `go test ./internal/adapters/sigstore/...` to confirm the new file
   still parses via `root.NewTrustedRootFromJSON`.
5. Update the "Captured on" date and command output above in this README.

If a future `sigstore-go` release ever stops shipping the `examples/`
directory (some Go modules exclude non-`.go` files from what `go get`
downloads), fall back to fetching the live trust root directly: write a
throwaway Go program that imports `github.com/sigstore/sigstore-go/pkg/root`,
calls `root.FetchTrustedRoot()` to pull the current snapshot from
Sigstore's TUF repository, and serializes the result back to JSON. Inspect
`pkg/root/trusted_root.go` in that module version for the exact
marshal/unmarshal round trip it expects.
