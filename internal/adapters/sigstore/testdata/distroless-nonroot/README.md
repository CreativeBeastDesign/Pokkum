# testdata/distroless-nonroot

A real, unmodified keyless Sigstore signature captured from the stock
distroless base image. It lets `verifier_test.go` prove the keyless verifier
works against genuine upstream material with no network access, so
`go test -short ./...` still exercises the security-critical path.

## Provenance

Captured on **2026-08-12** from:

| | |
|---|---|
| Image | `gcr.io/distroless/cc-debian12:nonroot` |
| Digest | `sha256:adcd20c7b4c988b73cbfbddb26d2eee574571e6d7c9ffea29b3821e0690efb77` |
| Signature tag | `gcr.io/distroless/cc-debian12:sha256-adcd20c7b4c988b73cbfbddb26d2eee574571e6d7c9ffea29b3821e0690efb77.sig` |
| Rekor log | `c0d23d6ad406973f9559f3ba2d1ca01f84147d8ffc5b8445c224f98b9591801d` (public-good) |
| Rekor log index | `2413841358` |
| Integrated time | `2026-08-10T22:38:10Z` |
| Certificate validity | `2026-08-10T22:38:09Z` .. `2026-08-10T22:48:09Z` |

## Files

| File | Source |
|---|---|
| `payload.json` | the `.sig` manifest's first layer blob — the Simple Signing payload that the signature covers |
| `signature.bin` | `dev.cosignproject.cosign/signature` annotation, base64-decoded |
| `certificate.pem` | `dev.sigstore.cosign/certificate` annotation, verbatim (plain PEM text) |
| `chain.pem` | `dev.sigstore.cosign/chain` annotation, verbatim. Captured for completeness only — the verifier deliberately ignores it, and `TestVerify_ChainAnnotationIsNotTrusted` asserts that it does |
| `bundle.json` | `dev.sigstore.cosign/bundle` annotation, verbatim (plain JSON text) |
| `digest.txt` | the resolved image digest, for cross-referencing the capture |

All four annotation values are stored exactly as the registry serves them; no
re-encoding, reformatting or trimming was applied.

## Why this does not expire

The Fulcio certificate in `certificate.pem` was valid for ten minutes and
expired long ago. That is fine and is the point: Sigstore performs certificate
path validation at the *Rekor entry's integrated time*, not at the current
time. If `TestVerify_RealDistrolessSignature` ever starts failing on a
certificate-expiry basis, that is a genuine regression in how the verifier
establishes the signing time — not a stale fixture.

The fixture does depend on the embedded trust root in
`../../trusted-root-public-good.json` still containing the Fulcio CA and the
Rekor/CT log keys that were in use on the capture date. A trust root refresh
that drops them would require recapturing this fixture.

## How to recapture

Resolve `gcr.io/distroless/cc-debian12:nonroot` to its current digest, fetch
`<repo>:<alg>-<hex>.sig`, and write out the first layer's blob plus the four
layer-descriptor annotations listed above (base64-decoding only the signature).
`verifier_network_test.go`'s `liveKeylessMaterial` helper does exactly this
fetch and is the reference for the procedure. Then update the table above.
