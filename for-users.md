# For Users: What Changed in This Fix Round

Most of [fixes-to-v1.md](fixes-to-v1.md) is invisible unless you were
relying on the specific broken behavior. This page covers what you might
actually notice, and what — if anything — you need to do.

## 2026-08-18: static-key signature verification now requires a real key — no more silent placeholder

**Before:** if you never set a `POKKUM_*_PUBKEY` env var, base-image
static-key verification, remote-cache verification, and `pokkum verify`
all quietly fell back to one shared, hardcoded placeholder public key.
That key was real enough to parse, but nobody held its private half —
its own doc comment admitted as much — so it could never actually verify
anything. In practice this mostly failed safely (a genuine signature
never matches a key nobody signed with), but it meant "verification
happened" and "verification meant anything" were two different claims
wearing the same clothes, and in one place — see below — the gap was
worse than that.

**Now:** the placeholder is gone. Deleted, not replaced — a trust anchor
nobody owns is worse than having no default at all. All three sites fail
closed with an explicit, actionable error instead:

1. **`pokkum build --base-verify-mode=static-key`** (the default mode for
   a `custom` base image preset) now **requires**
   `POKKUM_BASE_IMAGE_PUBKEY` to be set. Without it, the build fails
   immediately with an error telling you to set it — where before it
   would attempt verification against the placeholder and fail with a
   generic "signature invalid"-shaped error instead. If you sign your own
   custom base image, set `POKKUM_BASE_IMAGE_PUBKEY` to your public key
   PEM (path or literal text).
2. **Remote-cache static-key verification** (`--cache-verify-mode=static-key`,
   or `auto` mode encountering a static-key-signed cache candidate) now
   requires one of `--cache-verify-key`, `POKKUM_CACHE_PUBKEY`,
   `POKKUM_SIGNING_PUBKEY`, or `POKKUM_BASE_IMAGE_PUBKEY` to be set —
   whichever you already use for your other signing/verification flows
   works here too. Without any of them, that cache candidate is treated
   as unverified: cleanly rejected, falling through to a full rebuild
   (or a hard build failure under `--cache-verify-strict`), same as
   before — just with a clearer reason in the log.
3. **`pokkum verify` now hard-fails on a static-key-signed image when no
   key is configured — in every mode, including the default
   rebuild-and-compare path.** This is the one place the old placeholder
   was a genuine, not just theoretical, gap: the previous implementation
   returned `SignatureValid: false` with a **nil error**, so nothing in
   `verify`'s rebuild/comparison logic ever actually checked
   `SignatureValid` before proceeding — a signed image with no key
   configured was silently treated the same as one that passed. Pass
   `--public-key`, or set `POKKUM_SIGNING_PUBKEY`/`POKKUM_BASE_IMAGE_PUBKEY`,
   to verify a static-key-signed image; `pokkum verify --no-rebuild`
   attestation-only checks are affected the same way.

**If you already had a real key configured** (the common case if you
sign your own base images or use `--signing-key`), none of this changes
anything for you — you were never relying on the placeholder in the
first place. This only affects builds/verifications that had no key
configured at all and were quietly getting a no-op check.

## 2026-08-18: escrow-mirrored base images are now checked against the digest `pokkum.lock` actually locked

**Before:** `pokkum base update --mirror-registry=<repo>` mirrors a base
image and its Cosign `.sig` tag into a registry you control, and records
a `mirror_ref` (a mutable `"<mirror>:sha256-<hex>"` tag, not a pinned
digest) plus a `digest` field in `pokkum.lock`. `pokkum build` pulled the
base image via that mirror tag — but never compared what the mirror
actually served against the `digest` `pokkum.lock` had locked. Anyone
with push access to your own mirror repository (a much lower bar than
compromising the actual upstream image signer) could retarget that tag
to point at a **different image** — say, an older, real, genuinely
upstream-signed release of the same base with known CVEs — and the build
would resolve against it with every signature check still passing: real
certificate, correct identity, valid transparency-log entry, matching
repository name. The digest lock in `pokkum.lock` existed and was
written, but nothing ever read it back to compare.

**Now:** every mirror pull compares the digest the mirror actually served
against the locked `digest` in `pokkum.lock`, and fails the build closed
— naming both digests — on any mismatch. The very first time a preset is
mirrored (nothing locked yet) still works normally; every subsequent
build enforces the match.

**What this means for you:** if your mirror only ever serves the exact
content you mirrored it from, this is invisible — it always matched, and
still does. If you ever manually retag or repopulate a mirror repository
outside of `pokkum base update --mirror-registry`, and end up with a tag
serving different content than `pokkum.lock` expects, `pokkum build` will
now refuse to build instead of silently proceeding against the swapped
image. Re-run `pokkum base update --mirror-registry=<repo>` to
re-mirror and re-lock the digest you actually want.

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

**Update — real `cosign` dry-run completed, and it found three more bugs,
now fixed:** the previous `release-private.pem` (a plain OpenSSL-generated
key) does **not** work — a real `cosign` CLI (tested against v3.1.3)
rejects plain PEM private keys entirely, in both SEC1 and PKCS8 form,
regardless of format. It only accepts a key pair generated by
`cosign generate-key-pair`, which is always password-protected. Two other
real bugs surfaced in the same dry-run: `.github/workflows/release.yml`
never installed the `cosign` CLI at all (the release would have failed
immediately with "command not found"), and cosign v3.x removed the
`--output-signature` flag the `signs:` block relied on — `sign-blob` now
only supports `--bundle`, a JSON envelope (including a real public
Rekor transparency-log entry) rather than a bare signature file.

All three are fixed:
- **`scripts/cosign-sign-blob.sh`** (new) wraps the real `cosign` CLI,
  signs with `--bundle`, then extracts just `messageSignature.signature`
  into a plain file — so `pokkum upgrade`'s Go verifier code needed zero
  changes; it never has to know the bundle format exists.
- **`.github/workflows/release.yml`** now installs `cosign`
  (`sigstore/cosign-installer`) before the GoReleaser step.
- **`cmd/pokkum/upgrade.go`**'s embedded `DefaultReleasePublicKeyPEM` is
  now the public half of a properly-generated `cosign generate-key-pair`
  key pair.

**You need to update two GitHub Actions secrets** (the old
`COSIGN_PRIVATE_KEY` you set from `release-private.pem` no longer works —
replace it, don't just add to it):
- `COSIGN_PRIVATE_KEY` — the *new* cosign-native private key.
- `COSIGN_PASSWORD` (new secret) — the password that key was generated
  with. Cosign's own key format is always encrypted; there's no
  unencrypted option.

I generated a fresh key pair as part of verifying this fix and can hand you
the private key + password through the same channel as before (a local
file path, not pasted in chat) — ask if you'd like me to point you at it,
or generate your own with `cosign generate-key-pair` and swap the public
half into `DefaultReleasePublicKeyPEM` yourself.

**Proven, not assumed:** I ran the actual `.goreleaser.yaml` pipeline
locally in snapshot mode (`goreleaser release --snapshot --clean`) against
a throwaway test key first (found all three bugs), then again against the
real fixed pipeline end-to-end — real `cosign` CLI, real wrapper script,
real GoReleaser signing step (`release succeeded after 4m7s`) — and
independently verified the resulting `checksums.txt.sig` against the
*exact* public key now embedded in `upgrade.go`, using Pokkum's own
`cosign.Signer.VerifyArtifactSignature` (the same function `pokkum upgrade`
calls in production). It passed. This is no longer an assumption — a real
tagged release should still be a final sanity check, but the mechanism
itself is now confirmed working, not just plausible.

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

**Two things worth knowing:**
- If you override the keyless identity, set **both** `--base-keyless-identity`
  and `--base-keyless-issuer` together — setting only one produces a generic
  "must specify Issuer criteria" error instead of falling back to the preset's
  default for the half you didn't set. Safe (it fails rather than silently
  under-checking), just not the friendliest error message.
- `pokkum base update`/`pokkum base check` (which manage `pokkum.lock`) don't
  run signature verification themselves — a digest gets pinned into the
  lockfile on trust-on-first-use, by design. `pokkum build` re-verifies the
  locked digest's real signature at build time regardless, so this isn't a
  way to slip an unverified image past you permanently — just don't treat
  `base update` succeeding as itself a verification result. **If you use
  `--mirror-registry`**: as of 2026-08-18, that build-time re-verification
  also checks the mirror-served digest against the digest `pokkum.lock`
  actually locked (see the dedicated section above), closing a real gap
  where a compromised mirror could previously serve different, older, but
  still genuinely-signed content and pass every check.

Separately, `--registry-config` staying a generic `docker config.json`
reader (no ECR/GCR/ACR-specific credential-helper code) is also by design,
not an oversight — it's the deliberate boundary that keeps Pokkum
zero-dependency instead of vendoring cloud-provider SDKs. If your registry
supports static credentials in `docker config.json` (most do, including via
`docker login`), you're covered; credential-helper-based cloud auth is out
of scope.

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

## `pokkum rollback` no longer needs `--to`

**Before:** `--to=<ref>` was required — you had to already know what to roll
back to.

**Now:** `pokkum rollback -f manifest.yaml` with no `--to` rolls back to the
image ref that was most recently replaced, read from a
`pokkum.dev/previous-image` annotation that `resolve`/`apply` (and
`rollback` itself) write into the manifest automatically. `--to` still
works if you want to target something specific.

**One real limit to know about:** this only remembers one step. Running
`rollback` twice in a row just toggles back and forth between the two most
recent refs — it can't reach an image from three deploys ago. That needs a
real build-history store, which doesn't exist yet (tracked in
[Roadmap.md](Roadmap.md)'s Backlog as "Multi-Generation Rollback History").
If your manifest doesn't have the annotation yet (e.g. it's never been
through `resolve`/`apply`), `rollback` without `--to` will error — pass
`--to` explicitly the first time.

## OCI annotations now stamp themselves from git automatically

**Before:** `org.opencontainers.image.*` annotations only appeared if you
passed `--image-label` yourself.

**Now:** every `pokkum build` automatically sets `.revision` (commit SHA),
`.source` (remote URL), and `.version` (`git describe` output) — no flag
needed, and there's no opt-out flag if you'd rather it didn't. An explicit
`--image-label org.opencontainers.image.revision=...` still overrides the
auto-populated value if you need something different.

`.created` is included too, and it's guaranteed to match the actual image:
it's set to the exact same `SOURCE_DATE_EPOCH`-resolved timestamp used for
the image's real layer mtimes and config, not a separately-computed value
that could disagree with it — and it's left unset (not filled in with the
current wall-clock time) if that timestamp can't be determined, consistent
with Pokkum never using the build machine's clock for anything that ends up
in the image.

**One thing worth knowing:** if you build outside a git repository, or
`git` isn't installed, `.revision`/`.source`/`.version` are just silently
absent — the build succeeds, nothing warns you. Worth a sanity check
(`pokkum explain <image>` or inspecting the pushed manifest) if you're
relying on these for traceability and your build environment is unusual
(e.g. a packaged source tarball instead of a git checkout).
