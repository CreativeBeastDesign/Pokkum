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
static key pair, but it does **not** verify real upstream distroless or
Chainguard image signatures out of the box — see
[unfixed-limitation.md](unfixed-limitation.md).

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

## Verification

After all of the above: `gofmt -l .` clean, `go vet ./...` clean,
`golangci-lint run ./...` clean, `make check-arch` passes (hexagonal purity
and utility-package naming convention both hold), `go build ./...` clean,
and `go test ./...` passes in full, including `tests/integration`.
