#!/usr/bin/env bash
# Wraps `cosign sign-blob` for goreleaser's `signs:` block.
#
# Why this wrapper exists: cosign's CLI (v3.x) removed the old
# `--output-signature=<path>` flag that used to write a bare base64
# signature straight to a file — `sign-blob` now requires `--bundle=<path>`,
# which writes a JSON envelope (Sigstore bundle format, including a real
# Rekor transparency-log entry) rather than a plain signature. Pokkum's own
# verifier (internal/adapters/cosign, used by `pokkum upgrade`) expects a
# bare base64 ECDSA signature, matching the pre-bundle convention and
# keeping the Go verification code simple and dependency-free. This script
# is the seam: real `cosign` CLI on one side, a plain `.sig` file with just
# `messageSignature.signature`'s value on the other. No Pokkum Go code
# needs to know the bundle format exists.
#
# Usage: cosign-sign-blob.sh <artifact-path> <signature-output-path>
# Reads COSIGN_PRIVATE_KEY (the key content, cosign-native encrypted format
# — NOT a plain OpenSSL PEM key; cosign's key loader rejects those) and
# COSIGN_PASSWORD (required — cosign's own key format is always encrypted)
# from the environment, matching goreleaser's existing env passthrough.
set -euo pipefail

artifact="$1"
signature_out="$2"

if [ -z "${COSIGN_PRIVATE_KEY:-}" ]; then
  echo "cosign-sign-blob.sh: COSIGN_PRIVATE_KEY is not set" >&2
  exit 1
fi

key_file="$(mktemp)"
bundle_file="$(mktemp)"
trap 'rm -f "$key_file" "$bundle_file"' EXIT

printf '%s' "$COSIGN_PRIVATE_KEY" > "$key_file"

cosign sign-blob \
  --key="$key_file" \
  --use-signing-config=false \
  --bundle="$bundle_file" \
  --yes \
  "$artifact"

jq -r '.messageSignature.signature' "$bundle_file" > "$signature_out"

if [ ! -s "$signature_out" ] || [ "$(cat "$signature_out")" = "null" ]; then
  echo "cosign-sign-blob.sh: failed to extract messageSignature.signature from bundle" >&2
  exit 1
fi
