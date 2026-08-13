# testdata/trusted-root-wrong-keys.json

A byte-for-byte copy of `../trusted-root-public-good.json` with exactly one
field changed: `tlogs[0].publicKey.rawBytes` was replaced with a freshly
generated, unrelated P-256 public key (via `openssl ecparam -genkey` /
`openssl ec -pubout -outform DER`, base64-encoded — same 91-byte PKIX DER
shape as the original, so it still parses as a structurally valid trusted
root). `tlogs[0].logId.keyId` was deliberately left unchanged, so this file
is internally inconsistent with real Sigstore public-good trust roots (there
the log ID is `sha256(publicKey.rawBytes)`); that inconsistency is the point,
not a bug — it lets the Rekor log lookup by log ID still succeed while the
signature verification against that log's key fails.

It exists solely so `TestVerify_WrongTrustedRootFailsClosed` in
`verifier_test.go` can prove that `KeylessVerifyRequest.TrustedRootJSON` is
actually read and enforced, rather than the adapter silently falling back to
the embedded default. It is not a real Sigstore instance and must never be
used for anything but that one negative test.
