# For Users: What Changed in This Fix Round

Most of [fixes-to-v1.md](fixes-to-v1.md) is invisible unless you were
relying on the specific broken behavior. This page covers what you might
actually notice, and what — if anything — you need to do.

## `pokkum upgrade` now refuses to install an unverified binary

**Before:** if release verification was misconfigured or unavailable for
any reason, `pokkum upgrade` would silently skip the check and install the
release anyway, reporting `"verified": false` in JSON output but completing
the install regardless.

**Now:** `pokkum upgrade` (without `--check`) errors out and installs
nothing if it cannot verify the release signature. `pokkum upgrade --check`
still works and reports an honest `verified: false` if verification
couldn't run — nothing is installed by `--check` either way, so that path
stays informational rather than blocking.

**What this requires from the maintainers of this repo:** a
`COSIGN_PRIVATE_KEY` secret in GitHub Actions, matching the public key
embedded in `cmd/pokkum/upgrade.go` (`DefaultReleasePublicKeyPEM`). If
you're reading this after already running
`gh secret set COSIGN_PRIVATE_KEY < release-private.pem`, this is done —
just confirm a real tagged release actually signs and that
`pokkum upgrade` against it reports `verified: true`. The `cosign` CLI
itself wasn't available to test this fix against in the environment it was
written in, so a real release dry-run is worth doing once rather than
assuming it works.

**If you want to verify against a different key** (e.g. you rotate the
signing key, or you're building a fork with your own release pipeline):
pass `pokkum upgrade --key /path/to/public-key.pem` to override the
embedded default without rebuilding the CLI.

## Base image signature verification (`--verify-base`, default on)

**Before:** verification against one specific magic test-fixture image name
was hardcoded to always fail; every other reference — including real,
legitimately signed images — would either silently pass (if the embedded
key or annotation lookup was ever repaired) or always fail (with the
broken default key, which is what actually shipped). Neither behavior was
useful.

**Now:** verification does real ECDSA/Ed25519 cryptography against a
static public key. Two things follow from that:

1. **If you build against `distroless` or `chainguard` base image presets
   with the default configuration, verification does not protect you.**
   Both projects sign their images with keyless Sigstore signing (Fulcio +
   Rekor), which has no fixed public key to check against. See
   [unfixed-limitation.md](unfixed-limitation.md) for the full explanation.
   If you were relying on `VerifySignature`/`--verify-base` defaulting to
   `true` as a real security control against the stock base image presets,
   it currently is not one.
2. **If you sign your own custom base image** (or re-sign a pinned
   digest with your own key as part of your own base-image pipeline), set
   `POKKUM_BASE_IMAGE_PUBKEY` to your public key PEM and verification will
   genuinely check it — this path is now real and tested.

## PodDisruptionBudget: no longer generated for unlabeled workloads

**Before:** `pokkum resolve`/`apply --resource-defaults` always generated a
PodDisruptionBudget document, but its selector matched every Pod in the
namespace — including ones it had nothing to do with. This could restrict
voluntary evictions cluster-wide in ways you didn't intend.

**Now:** the PDB (and NetworkPolicy's `podSelector`, same underlying fix)
is scoped to your workload's own labels, read from
`spec.template.metadata.labels` (Deployment/StatefulSet/DaemonSet/Job) or
`metadata.labels` (a bare Pod). **If your manifest doesn't declare labels on
the Pod template, no PDB is generated at all** — you'll see one fewer
document in the output than before. This is deliberate: a namespace-wide
PDB was actively wrong, not just imprecise.

Practically, this should rarely matter: `spec.template.metadata.labels` is
already required to match `spec.selector.matchLabels` on any real
Deployment/StatefulSet/DaemonSet (the Kubernetes API rejects the resource
otherwise), so any manifest that would have passed validation before
already has the labels this needs.

## Roadmap wording

[Roadmap.md](Roadmap.md)'s description of `pokkum rollback` was corrected —
it does not read from any build history; it rewrites a manifest's `image:`
reference to whatever ref you pass via the required `--to` flag. Behavior
is unchanged, only the description was wrong.
