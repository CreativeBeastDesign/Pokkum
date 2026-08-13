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

**Now:** verification does real cryptography against both static public keys
and keyless Sigstore identities. Two modes are available:

1. **For stock `distroless` and `chainguard` base image presets, keyless
   Sigstore verification (Fulcio + Rekor) now runs by default.** Verification
   checks the certificate chain against Fulcio's root CA, validates the
   certificate's Subject Alternative Name (SAN) identity and OIDC issuer match
   expected values, and confirms the signature was logged to the Rekor
   transparency log. The verified identities are:
   - `distroless`: OIDC issuer `https://accounts.google.com`, certificate SAN
     `keyless@distroless.iam.gserviceaccount.com`.
   - `chainguard`: OIDC issuer `https://token.actions.githubusercontent.com`,
     certificate SAN `https://github.com/chainguard-images/images/.github/workflows/release.yaml@refs/heads/main`.
   These defaults are automatic and verified to work with the live upstream
   images. You can override them with `--base-keyless-identity` and
   `--base-keyless-issuer` if needed (e.g. verifying a custom base that uses
   keyless signing, or a private Sigstore deployment). `--sigstore-trusted-root`
   allows you to provide a custom trust root snapshot.
2. **If you sign your own custom base image** (or re-sign a pinned digest with
   your own key as part of your own base-image pipeline), set
   `POKKUM_BASE_IMAGE_PUBKEY` to your public key PEM and static-key verification
   will check it — pass `--base-verify-mode=static-key` to use that path
   explicitly. This mode is now real and tested.
3. **Verification mode selection** is controlled by `--base-verify-mode {auto|keyless|static-key}`
   (default `auto`). In `auto` mode, `distroless`/`chainguard` presets default to
   keyless verification, while a `custom` base image preset defaults to static-key.
   Pass `--base-verify-mode=keyless` or `--base-verify-mode=static-key` to force a
   specific mode — the resolver will fail explicitly if you request a mode that
   doesn't match the signature material found, rather than silently falling back.
   `--no-verify-base` disables verification entirely (the workaround if you need
   to skip these checks for a base image that isn't signed).

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
