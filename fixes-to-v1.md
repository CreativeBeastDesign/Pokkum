# Fixes to v1.0

This documents the fixes applied after a multi-agent audit compared
[Roadmap.md](Roadmap.md)'s `[x]` claims against the actual implementation and
found several gaps — some cosmetic, some genuine security bugs. A first
round of fixes closed most of them; this round closes the rest that were
still partial or unaddressed after re-verification.

## PodDisruptionBudget selector was namespace-wide

**File:** [internal/adapters/k8s/resolver.go](internal/adapters/k8s/resolver.go)

`generatePodDisruptionBudgetDocument` emitted `selector: matchLabels: {}` —
an empty selector matches every Pod in the namespace, not just the workload
`pokkum resolve`/`apply` was processing.

Fixed by walking the parsed manifest up to the nearest ancestor mapping that
owns both `metadata` and `spec` (the Pod template for a
Deployment/StatefulSet/DaemonSet/(Cron)Job, or the Pod object itself for a
bare Pod) and reading its `metadata.labels`. Both the PodDisruptionBudget
selector and NetworkPolicy's `podSelector` (previously also `{}`, a smaller
and previously-accepted gap) are now scoped to those labels. When no labels
can be found at all, PDB generation is skipped entirely rather than emit a
document that silently applies namespace-wide — see
[for-users.md](for-users.md) for what this means in practice.

New tests: `TestResolver_Errors/with_network_policy_and_resource_defaults`
now asserts the selector is scoped, and a new
`resource_defaults_without_extractable_labels_skips_PDB` subtest covers the
skip path.

## Base image Cosign signature verification was non-functional

**File:** [internal/adapters/baseimage/resolver.go](internal/adapters/baseimage/resolver.go)

Four separate bugs, each independently making `--verify-base` unable to
actually verify anything:

1. A leftover magic-string stub (`strings.Contains(ref, "unsigned-test-image")`)
   short-circuited verification for one specific test fixture name instead
   of running real logic for it too. Removed.
2. The code looked for the signature under the annotation key
   `dev.cosign.teleor/signature`, which is not a real Cosign annotation.
   Fixed to the actual key Cosign writes: `dev.cosignproject.cosign/signature`.
3. A failed base64 decode of the signature was silently swallowed, falling
   back to using the raw payload bytes as the signature — which would
   deterministically fail crypto verification but for the wrong, confusing
   reason. Now returns a clear error instead.
4. If `r.signer` was ever nil (a zero-value `Resolver{}` bypassing
   `NewResolver`), verification was silently skipped and the ref was logged
   as "verified" anyway. Now returns an error instead of proceeding.
5. `DefaultBaseImagePublicKeyPEM`, the fallback public key when
   `POKKUM_BASE_IMAGE_PUBKEY` isn't set, was not valid PEM at all
   (`pem.Decode` returned `nil`) — every verification failed unconditionally
   on the default path. Replaced with a real, valid P-256 public key.

New tests push a **genuinely Cosign-signed** test image (signed for real
with a generated key pair, using the same `cosign.Signer` production code
signs with) and prove: a correctly signed image passes, a tampered
signature is rejected, and a signature checked against the wrong public key
is rejected. The previous test suite only ever exercised "no signature
present at all," which a magic-string stub or a mis-wired annotation key
would pass just as easily as a real check.

**Scope note:** this now correctly verifies base images signed with a
static key pair. Keyless Sigstore signature verification for stock
`distroless`/`chainguard` presets was added in a later round and is
documented in the "Base image signature verification now covers real
upstream distroless/Chainguard signatures (keyless Sigstore)" section
below.

## `pokkum upgrade` signature verification could fail open

**File:** [cmd/pokkum/upgrade.go](cmd/pokkum/upgrade.go)

Both signature and checksum verification were previously gated behind
`if verifier != nil`, with no `else` — a nil verifier silently skipped both
checks and the command proceeded to download and install the release
binary anyway, reporting `verified: false` in the JSON output but still
completing the install. This is now a hard error in apply mode: a nil
verifier with `--check` unset returns an error before any network call,
instead of installing an unverified binary. `--check` alone (which installs
nothing) still degrades gracefully to an honest `verified: false`.

`DefaultReleasePublicKeyPEM` had the same "not valid PEM" bug as the
base-image key, fixed the same way with a real generated key.

`.goreleaser.yaml`'s `signs:` block used keyless Cosign signing
(`cosign sign-blob --yes` with no `--key`, i.e. Fulcio/Rekor), while the
verifier in `cmd/pokkum/upgrade.go` only supports checking a signature
against a static public key. These two were structurally incompatible —
fixing the key alone would not have made verification work, because a
keyless signature has no static key to check it against. Switched to
`--key=env://COSIGN_PRIVATE_KEY` (key-based signing) so the two halves
agree. See [for-users.md](for-users.md) for the secret this requires.

New tests: `TestUpgradeCommand_NilVerifier_ApplyFailsClosed` and
`TestUpgradeCommand_NilVerifier_CheckReportsUnverified` cover both branches
of the fixed nil-verifier behavior.

## Lint and naming cleanup

- `golangci-lint` flagged 7 new `errcheck` findings in `upgrade_test.go`
  (unchecked `Write`/`ReadFrom`/`WriteFile` return values) introduced by the
  previous round of test additions — all now checked.
- `secretguard.SecretGuardAdapter` stuttered with its own package name
  (`revive`) — renamed to `secretguard.Adapter`. It was only referenced
  within its own file, so the rename had zero blast radius elsewhere.
- Two leftover `"secretguardutils: ..."` error-message prefixes (from the
  earlier `secretguardutils` → `secretguard` package rename) corrected to
  `"secretguard: ..."`.
- `tar.TypeRegA`, deprecated since Go 1.11, removed from
  `extractBinaryFromTarGz`'s type check in `upgrade.go` — `tar.TypeReg`
  alone is sufficient; Go's own `tar.Reader` normalizes the legacy
  zero-value type flag to `TypeReg` on read, confirmed by the existing test
  suite (which builds fixtures with an unset `Typeflag`) still passing.
- `golangci-lint`'s version was pinned in `.github/workflows/ci.yml` in the
  previous round but not in `.github/workflows/release.yml` — now pinned
  there too (`v1.62.2`, matching `ci.yml`) for consistency.

## Documentation

- [Roadmap.md](Roadmap.md)'s `pokkum rollback` entry claimed it reads "from
  build history"; the actual implementation is a manifest `image:` regex
  rewrite with a required `--to` flag and no history store anywhere in the
  codebase. Wording corrected, and a genuine "history-aware rollback"
  feature added to the Backlog section for anyone who wants to build it.

## What was already fine

A prior audit round flagged `cosign.Signer` and `registry.Adapter` as
missing the compile-time `var _ ports.X = (*Y)(nil)` interface assertion
idiom. Re-reading both files directly showed both already had it, inside a
multi-line `var (...)` block — the earlier automated grep pattern
(`var _ ports\.`) only matched the single-line form and missed it. No code
change was needed; noted here so the false alarm doesn't get re-investigated.

## Base image signature verification now covers real upstream distroless/Chainguard signatures (keyless Sigstore)

**Files:** [internal/adapters/baseimage/resolver.go](internal/adapters/baseimage/resolver.go), [internal/adapters/sigstore/](internal/adapters/sigstore/) (new package), [internal/ports/keyless.go](internal/ports/keyless.go) (new), [internal/ports/baseimage.go](internal/ports/baseimage.go).

The static-key verifier fixed in the "Base image Cosign signature verification was non-functional" section above could not structurally check keyless Sigstore signatures (Fulcio short-lived certificates + Rekor transparency log, no fixed public key). Real upstream `distroless` and `chainguard` images sign with keyless Sigstore, so their signatures were never actually verified: `VerifySignature` defaulting to `true` for those presets either required `--no-verify-base` as a workaround or failed with `core.ErrBaseSignatureInvalid`.

A new `ports.KeylessVerifier` interface and `internal/adapters/sigstore` adapter implementation now handle keyless signatures. The adapter uses `github.com/sigstore/sigstore-go` to verify:
- The certificate chain against Fulcio's root CA.
- The certificate's OIDC issuer and Subject Alternative Name (SAN) match expected identities (e.g., Google OIDC for distroless, GitHub OIDC for Chainguard).
- The signature's inclusion in Rekor, the transparency log.

Against an embedded offline trust root snapshot (no live network calls), enabling verification in `--offline`/`--hermetic` build modes.

The resolver (`internal/adapters/baseimage/resolver.go`) determines verification mode (static-key vs. keyless) from the base image preset/flag *before* fetching any signature material — never inferred from what's discovered on the wire. This prevents downgrade attacks (e.g., if an operator mistakenly sets `POKKUM_BASE_IMAGE_PUBKEY` while a preset defaults to keyless, resolution fails explicitly telling them to pass `--base-verify-mode=static-key` if that's intended).

New CLI flags on `pokkum build`:
- `--base-verify-mode {auto|keyless|static-key}` — selects verification mode (default `auto`: `distroless`/`chainguard` presets use keyless by default, `custom` preset uses static-key by default).
- `--base-keyless-identity <SAN>` — overrides the expected certificate SAN for keyless verification.
- `--base-keyless-issuer <issuer URL>` — overrides the expected OIDC issuer for keyless verification.
- `--sigstore-trusted-root <path>` — overrides the embedded trust root snapshot (e.g. for a private Sigstore deployment).

Verified default identities (confirmed by decoding real live Sigstore signatures on 2026-08-12; re-verification procedure is documented on `ports.BaseImagePreset.DefaultKeylessIdentity` in `internal/ports/baseimage.go`):
- `distroless`: OIDC issuer `https://accounts.google.com`, certificate SAN `keyless@distroless.iam.gserviceaccount.com`.
- `chainguard`: OIDC issuer `https://token.actions.githubusercontent.com`, certificate SAN `https://github.com/chainguard-images/images/.github/workflows/release.yaml@refs/heads/main`.

New tests: `internal/adapters/sigstore` includes hermetic tests against real captured signature fixtures (no network, doesn't expire — verification uses Rekor entry's recorded time), plus `internal/adapters/baseimage/resolver_network_test.go`'s `TestResolve_LiveKeylessVerification` testing against live upstream images. The resolver level also has a test proving the anti-downgrade guarantee: an image signed only with a static key, resolved under forced keyless mode, correctly fails rather than silently falling back.

**Independent re-verification** (a second audit pass, adversarial by design given this codebase's history of security features that looked real but weren't): confirmed the verifier calls real `sigstore-go` APIs for chain building, SCT checking, and Rekor inclusion — nothing hand-rolled; confirmed `req.ChainPEM` (the attacker-suppliable chain annotation) is referenced in zero non-test verification-path lines; confirmed the empty-identity refusal runs before any Sigstore call; confirmed the verifier uses the Rekor entry's integrated time rather than `time.Now()` (proven empirically — a real captured cert whose 10-minute validity window expired days before this check still verifies); and confirmed `TestResolve_LiveKeylessVerification` genuinely hits the live `distroless` and `chainguard` registries with the exact zero-extra-flags shape of a plain `pokkum build` and both verify. Three minor, non-security items surfaced and are now documented rather than silently left:
- `pokkum base update`/`base check` pin digests into `pokkum.lock` without running verification (trust-on-first-use) — `pokkum build` re-verifies the locked digest at build time regardless, so this isn't a bypass, but see [Vocabulary.md](Vocabulary.md) §14.
- Setting only one of `--base-keyless-identity`/`--base-keyless-issuer` (not both) fails with a generic "must specify Issuer criteria" error instead of falling back to the preset's default for the unset half — fail-closed and safe, just a confusing message for a plausible mistake. See [Vocabulary.md](Vocabulary.md) §3.
- The verification cache keys on the trusted-root file *path*, not its contents, so a mid-process edit of a custom `--sigstore-trusted-root` file could theoretically serve a stale cached result. Low real-world risk (the file doesn't change during a single build), noted for completeness.

## Rollback history (manifest-based, one hop deep)

**Files:** [cmd/pokkum/rollback.go](cmd/pokkum/rollback.go), [internal/adapters/k8s/resolver.go](internal/adapters/k8s/resolver.go), [internal/ports/k8s.go](internal/ports/k8s.go).

Previously `pokkum rollback` required `--to=<ref>` unconditionally — the caller had to already know what to roll back to. Now `pokkum rollback -f <manifest>` works with no `--to`: `resolve`/`apply` write the displaced image ref into a `pokkum.dev/previous-image` manifest annotation whenever they overwrite an already-concrete `image:` value (not a fresh `pokkum://` reference), and `rollback` reads that annotation when `--to` is omitted. `rollback` itself also writes the annotation on every run — the ref it just replaced becomes the new "previous" — so it's self-toggling: running it twice in a row swaps back to where you started.

This is genuinely real (verified independently): `setAnnotation`/`getAnnotation`-style YAML-node mutation in `resolver.go`, real regex-based annotation read/write in `rollback.go`, and tests that assert exact rewritten content and prove a real two-step toggle round-trip, not just "no error." `MarkFlagRequired("to")` is gone from the flag definition.

**Scope note:** this is one hop deep by design, not full history. A second `rollback` call undoes the *first* rollback, it can't reach an arbitrary earlier generation — that would need a real build-history store, which doesn't exist. The Roadmap Backlog item is retitled "Multi-Generation Rollback History" to reflect that the one-hop case is now done and only the deeper case remains open.

## OCI annotations now auto-populate from git

**Files:** [cmd/pokkum/git_metadata.go](cmd/pokkum/git_metadata.go) (new), [cmd/pokkum/build.go](cmd/pokkum/build.go).

Previously `org.opencontainers.image.*` annotations only ever appeared if a user passed `--image-label` explicitly — no git integration existed despite Roadmap.md claiming there was one. Now `discoverGitMetadata` runs unconditionally on every `pokkum build` and populates three of the four standard keys:
- `org.opencontainers.image.revision` ← `git rev-parse HEAD` (or `GITHUB_SHA` in CI, checked first).
- `org.opencontainers.image.source` ← `git config --get remote.origin.url` (or `GITHUB_SERVER_URL`/`GITHUB_REPOSITORY` in CI).
- `org.opencontainers.image.version` ← `git describe --tags --always --dirty` (or the CI ref name).

An explicit `--image-label org.opencontainers.image.revision=...` (etc.) always overrides the auto-populated value — checked before auto-population runs, confirmed by `TestDiscoverGitMetadata_ExplicitLabelPrecedence`.

**One real gap remains:** outside a git repository, or if `git` isn't on
`PATH`, every git call fails and is swallowed silently — the build succeeds
with `revision`/`source`/`version` simply absent, no warning printed. There's
no opt-out flag either, so this silence is the only signal; a CI system with
an unusually shallow/detached checkout would degrade quietly rather than
loudly. The existing tests only cover the CI-env-var path, not a real `git`
shell-out, so this path is exercised in production but not in the test
suite.

### `org.opencontainers.image.created` (follow-up fix)

`.created` was initially added with its own independent resolution
(`SOURCE_DATE_EPOCH` env var → `git log -1 --format=%cI` in the project
directory), which fixed the missing-annotation gap but introduced a subtler
one: `cmd/pokkum/build.go` already resolves a build timestamp for the rest
of the pipeline via `config.Loader.ResolveBuildTimestamp()` (`SOURCE_DATE_EPOCH`
env var → `git log -1 --pretty=%ct` run in the **CLI process's own working
directory**, not the project directory) — a second, independent resolution
with a different git working directory is a real way for the `.created`
annotation to end up describing a different instant than what's actually
baked into the image's layer mtimes and config, for any invocation where
the CLI isn't run from inside the target project's own repository (e.g.
`pokkum build ../some-other-app`).

Fixed by removing the independent resolution entirely: `runBuild` now
resolves `SOURCE_DATE_EPOCH` once, before label discovery, and passes that
single `time.Time` into `discoverGitMetadata`, which sets `.created` to
exactly that value (or leaves it unset if the timestamp is the Go zero
value — never fabricating a `time.Now()` fallback, matching the project's
"adapters must never call `time.Now()`" invariant). One value, one source
of truth, used everywhere. New tests: `TestDiscoverGitMetadata_EnvVars`
asserts `.created` equals the passed-in timestamp exactly;
`TestDiscoverGitMetadata_ZeroTimestampLeavesCreatedUnset` asserts no
fabricated fallback; `TestDiscoverGitMetadata_ExplicitLabelPrecedence`
covers `.created` alongside `.revision` for explicit-override precedence.

### `--registry-config` and base-image TOFU pinning: confirmed as intended scope

Two items previously flagged as caveats are deliberate design decisions,
not gaps: `--registry-config` stays a generic `docker config.json` reader
rather than gaining ECR/GCR/ACR-specific credential-helper code, to hold
the line on Pokkum's zero-dependency design rather than vendor
cloud-provider SDKs. `pokkum base update`/`base check` pinning digests into
`pokkum.lock` without running verification is accepted as trust-on-first-use,
because `pokkum build` independently re-verifies the locked digest's real
signature (static-key or keyless) at build time regardless — the lockfile
entry is never trusted on its own.

## Dependency vulnerabilities closed

`govulncheck` previously flagged two dependency-tree vulnerabilities (both
unreachable from Pokkum's own code, but worth closing rather than carrying):
`go.opentelemetry.io/otel`'s baggage-parsing issue (GO-2026-5158), fixed by
bumping to v1.45.0; and the unmaintained `golang.org/x/crypto/openpgp`
package (GO-2026-5932), fixed via a `replace` directive in `go.mod` pointing
at the actively-maintained `github.com/ProtonMail/go-crypto/openpgp` fork —
confirmed no remaining import of the original package anywhere in the
codebase. `govulncheck ./...` now reports zero vulnerabilities, reachable or
not.

## Verification

After all of the above: `gofmt -l .` clean, `go vet ./...` clean,
`golangci-lint run ./...` clean, `make check-arch` passes (hexagonal purity
and utility-package naming convention both hold), `go build ./...` clean,
`govulncheck ./...` reports zero vulnerabilities, and `go test ./...` passes
in full, including `tests/integration` and live-network tests against the
real `distroless`/`chainguard` registries.
