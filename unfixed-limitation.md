# Unfixed Limitation: Base Image Signature Verification Doesn't Cover Real Upstream Signatures

## What's fixed vs. what isn't

This round of fixes made `internal/adapters/baseimage/resolver.go`'s
signature verification perform genuine cryptography for the first time —
see [fixes-to-v1.md](fixes-to-v1.md). What it did **not** do, and what this
document exists to be explicit about, is make `--verify-base` (or its
default-on `VerifySignature` behavior) actually validate real signatures on
the `distroless` and `chainguard` base image presets Pokkum ships by
default.

[Roadmap.md](Roadmap.md) still describes this feature as: *"Verify upstream
Cosign signatures on distroless/Chainguard base images at pull time."* That
description is not accurate for the stock presets today, and closing that
gap is real, separately-scoped work — not a follow-up bug fix.

## Why: keyless signing has no static key to check

Pokkum's verifier (`ports.CosignSigner.Verify`, implemented in
`internal/adapters/cosign/signer.go`) checks a signature against a single
static public key — either `POKKUM_BASE_IMAGE_PUBKEY` or the embedded
`DefaultBaseImagePublicKeyPEM` fallback. This is the right model for an
organization that signs its own custom base image with its own key pair,
and that path is now genuinely correct and tested.

It is the wrong model for `distroless` and `chainguard`, because neither
project signs with a static key at all. Both use **keyless Sigstore
signing**:

1. The signer authenticates to Fulcio (Sigstore's certificate authority)
   via an OIDC identity (e.g. a GitHub Actions workflow identity) instead
   of holding a long-lived private key.
2. Fulcio issues a short-lived X.509 certificate binding that OIDC identity
   to an ephemeral signing key, valid for minutes.
3. The signature and certificate are logged to Rekor, a public transparency
   log, producing an inclusion proof.
4. Verifying the signature means validating the certificate chains to
   Fulcio's root, checking the certificate's embedded identity matches an
   expected issuer/subject, and checking the Rekor inclusion proof — not
   comparing against any fixed public key, because there isn't one to fix.

A static-key verifier structurally cannot check any of this. Pointing
`POKKUM_BASE_IMAGE_PUBKEY` at some key you found published by Google or
Chainguard would not work even in principle, because that's not how their
signatures are produced.

## Practical impact

`VerifySignature` defaults to `true` for every base image preset
(`--no-verify-base` is the opt-out). For the stock `distroless`/`chainguard`
presets with no `POKKUM_BASE_IMAGE_PUBKEY` override, this means:

- Verification runs and does real cryptographic work.
- It is checking a signature against a key that has no relationship to how
  the base image was actually signed.
- In practice, since real distroless/chainguard images don't publish a
  Cosign signature discoverable at the static-key convention this adapter
  looks for (`<repo>:<alg>-<hex>.sig` with a `dev.cosignproject.cosign/signature`
  annotation, and even if they did, it wouldn't be produced by the matching
  private key), verification will fail closed — the resolver returns
  `core.ErrBaseSignatureInvalid` rather than silently passing. So this is
  not a silent bypass today; it's a feature that doesn't work for the
  default presets and will visibly error rather than pretend to succeed.
  If you hit this, `--no-verify-base` is the honest workaround until
  keyless verification exists, not a sign that something else is broken.

## What closing this gap would actually take

Real keyless verification needs a certificate-chain and transparency-log
validation path that this adapter does not have:

1. Fetch the Sigstore bundle (certificate + Rekor inclusion proof) for the
   image, not just a bare signature.
2. Validate the certificate chains to Fulcio's root CA (or an intermediate,
   depending on which Sigstore instance signed it — public-good vs. a
   private Sigstore deployment).
3. Validate the certificate's SAN/issuer extension matches an expected
   identity (e.g. `https://github.com/GoogleContainerTools/distroless` via
   the `https://token.actions.githubusercontent.com` OIDC issuer) — without
   this check, verification degenerates to "any Fulcio-issued cert is
   trusted," which defeats the purpose.
4. Validate the Rekor inclusion proof against a trusted Rekor public key,
   confirming the signature was actually logged and not just locally
   fabricated.

This is materially more work than the static-key path, and is close to
reimplementing what `cosign verify` (or the `sigstore-go` verification
library it's built on) already does. The pragmatic path forward is almost
certainly to depend on `github.com/sigstore/sigstore-go`'s verifier
directly for this specific case, rather than hand-roll certificate-chain
and Rekor validation inside `internal/adapters/baseimage`. That's a new
port (`ports.KeylessVerifier` or similar) and a meaningfully sized adapter,
not a bug fix — hence tracked here rather than folded into the previous
round's fixes.

## In the meantime

- Signing your own custom base image with a static key pair and setting
  `POKKUM_BASE_IMAGE_PUBKEY` gets you real, working verification today.
- Relying on the `distroless`/`chainguard` presets' default `VerifySignature: true`
  does not currently provide the protection Roadmap.md describes; expect a
  `core.ErrBaseSignatureInvalid` if you don't override the base image, or
  use `--no-verify-base` deliberately if you're accepting that trade-off
  for now.
