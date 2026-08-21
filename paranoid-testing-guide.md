# Paranoid Testing Guide: Verifying Pokkum on a Real SvelteKit Project

This is a "believe nothing" test plan. At every step, Pokkum makes a claim —
build succeeded, image signed, SBOM attached, provenance recorded,
reproducible, verified. This guide cross-checks each claim with an
**independent tool** (`docker`, `cosign`, raw `jq`/`sha256sum`) rather than
trusting Pokkum's own exit code or "Verified: true" output. If Pokkum says
X, this guide's job is to make X observable some other way before you
believe it.

This is written from direct experience auditing this codebase: several past
"passed" states here were previously backed by hardcoded `true` values and
no real check at all (see [docs/archive/fixes-to-v1.md](docs/archive/fixes-to-v1.md)). Those are
fixed now, but the discipline of not trusting a tool's self-report is worth
keeping regardless of which tool it is.

## 0. Prerequisites

```bash
# Required
which bun git go docker cosign

# Recommended but optional (install if you want deeper independent
# inspection than `docker manifest`/`docker inspect` give you)
brew install crane        # or: go install github.com/google/go-containerregistry/cmd/crane@latest
brew install syft grype   # independent SBOM/CVE cross-check tools. Pokkum
                           # itself is zero-dependency and does NOT use Syft
                           # (see §16) — these are useful precisely because
                           # they're a second, differently-implemented
                           # opinion, not because Pokkum shares any code
                           # with them.
```

**Build the embedded PID-1 binaries before building `pokkum` itself — skip
this and step 4 onward silently builds broken images.** `pokkum-init` and
`pokkum-static` are the Go binaries that become PID 1 inside every image
Pokkum produces. They are gitignored, locally-built artifacts consumed via
`go:embed`, not checked-in source: a fresh clone has only a `.gitkeep` in
their `bin/` directories. `go build ./cmd/pokkum` succeeds anyway —
embedding a near-empty directory is legal Go — so the failure doesn't show
up at build time. It shows up only at *runtime*, when the produced image's
entrypoint binary is missing and the container never starts.

```bash
cd /path/to/pokkum
make supervisor static-server
ls internal/adapters/supervisor/bin internal/adapters/staticserver/bin
#   → pokkum-init-linux-{amd64,arm64}.zst and pokkum-static-linux-{amd64,arm64}.zst
```

Build `pokkum` from the exact commit you're testing (don't test against a
possibly-stale installed binary):

```bash
go build -o ./pokkum-test ./cmd/pokkum
./pokkum-test version   # confirm it reports the commit you expect
```

Use `./pokkum-test` (not a system-installed `pokkum`) for every command
below, so you know exactly what code you're testing.

Pick a registry you can push to and inspect independently — GHCR is the
easiest if you already have a GitHub account:

```bash
export POKKUM_DOCKER_REPO=ghcr.io/<you>/pokkum-paranoid-test
docker login ghcr.io -u <you>   # cosign/docker both need real registry auth
```

## 1. Get a real SvelteKit project

Use a project you actually care about, or scaffold a fresh one so you have
a known-clean baseline first:

```bash
npx sv create test-app --template minimal --types ts
cd test-app
bun install
```

If you have an existing SvelteKit app already deployed some other way
(Vercel adapter, Node adapter, hand-written Dockerfile), test with that
too, separately — it exercises `pokkum doctor`'s real-world preflight
checks in a way a fresh scaffold won't.

## 2. Preflight — don't trust "no errors," read what it actually checked

```bash
./pokkum-test doctor .
```

**Verify independently:** open the output and confirm it actually named
specific things (Bun version found, `@jesterkit/exe-sveltekit` present or
not, registry auth reachable) rather than a generic "all checks passed."
If it says the SvelteKit adapter isn't installed, stop here and fix that
first — don't let `pokkum build` silently work around a missing
prerequisite.

## 3. First build — inspect before anything touches a registry

```bash
./pokkum-test build . --print-manifest --output=json > /tmp/manifest.json
```

**Verify independently:**
```bash
jq . /tmp/manifest.json | less
```
Read the actual computed manifest/config JSON yourself. The document's own
top level *is* the report (`repo`/`tags`/`pushed`/`index`/`images`) — there
is no `.data` wrapper here (that's specific to `--print-manifest`; it does
not share a shape with `scan`/`history`/`verify`'s JSON — see §4).
Concretely:
```bash
jq -r '.index.digest' /tmp/manifest.json                                          # index digest
jq -r '.images[].platform' /tmp/manifest.json                                     # platform list
jq -r '.images[0].manifest.layers | length' /tmp/manifest.json                    # layer count, first platform
jq -r '.images[0].config.config.Labels["org.opencontainers.image.base.name"]' /tmp/manifest.json   # resolved base ref
```
Confirm the base image ref, platform list, and layer count match what you
expect — this step touches no registry and pushes nothing, so there's
nothing to trust yet except "did the JSON look sane."

## 4. Real build — capture everything Pokkum claims happened

```bash
IMAGE_REF=$(./pokkum-test build . 2>build.log)
echo "$IMAGE_REF"
```

Keep `build.log` (stderr, structured logs) — you'll cross-check specific
claims from it against independent tooling in the steps below.

**Verify independently, about the command itself, before trusting anything
downstream of it:** `build`'s only stdout output is the single line above —
the published `repo@sha256:...` reference. `internal/core/pipeline.go`
calls this out explicitly in its own comment: "the one line of program
output... nothing else may ever share the stream." `--output=json` does
**not** change this for `build` — confirmed by running it with and without
the flag. That flag only affects `--print-manifest`'s separate JSON path
(§3) and every other subcommand (`scan`, `history`, `verify`, `adopt`, ...),
which do wrap their result in a `{"schema_version", "command", "status",
"data": {...}}` envelope (`internal/ports/output.go`'s `JSONEnvelope`).
Piping `build`'s own stdout through `jq -r '.data.digest'` therefore fails
loudly rather than silently: the plain ref is not JSON at all, so jq never
reaches the key lookup.

```
$ pokkum build . --output=json | jq -r '.data.digest'
jq: parse error: Invalid numeric literal at line 1, column 10   # exit 5
```

(An earlier revision of this guide claimed that pipeline "silently returns
`null`". It does not — verified against jq directly. A silent `null` is what
you get from JSON that parses but lacks the key, e.g. the envelope other
subcommands emit; `build`'s stdout never parses in the first place. The
practical advice below is unchanged, but the reason is the opposite of what
was written: the failure is loud, not quiet.)

Extract the digest from the plain ref instead:

```bash
DIGEST=${IMAGE_REF#*@}
echo "$DIGEST"
```

## 5. Independent image verification — don't trust `docker`-via-Pokkum, use `docker` yourself

```bash
docker pull "$POKKUM_DOCKER_REPO@$DIGEST"
docker inspect "$POKKUM_DOCKER_REPO@$DIGEST" | jq '.[0].Config'
```

**Verify independently:**
- `docker inspect`'s `Config.User` should show a non-root UID (Pokkum
  claims `runAsNonRoot`/UID 65532 — confirm it here, not from a Pokkum log
  line).
- `docker history "$POKKUM_DOCKER_REPO@$DIGEST"` — count the layers
  yourself, cross-check against `pokkum explain "$POKKUM_DOCKER_REPO@$DIGEST"`'s
  real per-layer breakdown for that same image (the layer count and set vary
  by `--strategy` and by which optional layers — client assets, vendor deps,
  native addons, prerendered pages — a given build actually produced).
- If `crane` is installed: `crane manifest "$POKKUM_DOCKER_REPO@$DIGEST" | jq .`
  gives you the raw OCI manifest without Docker's daemon interpreting
  anything for you.

## 6. OCI annotations — cross-check against your own `git log`, not Pokkum's claim

```bash
docker inspect "$POKKUM_DOCKER_REPO@$DIGEST" | jq '.[0].Config.Labels'
```

**Verify independently, by hand:**
```bash
git rev-parse HEAD                              # compare to org.opencontainers.image.revision
git config --get remote.origin.url              # compare to org.opencontainers.image.source
git log -1 --format=%cI                         # compare to org.opencontainers.image.created (should match, modulo SOURCE_DATE_EPOCH override)
```
If you built outside a git repo, confirm these labels are genuinely
*absent* (not blank-string or fabricated) — that's documented behavior
(see [docs/archive/for-users.md](docs/archive/for-users.md)), not a bug, but worth confirming it's
actually absent rather than silently wrong.

## 7. SBOM — don't trust "SBOM generated," read it

```bash
docker buildx imagetools inspect "$POKKUM_DOCKER_REPO@$DIGEST" --format '{{json .}}' | jq .
# or, if attached as an OCI 1.1 referrer (Pokkum's default):
oras discover --format json "$POKKUM_DOCKER_REPO@$DIGEST"   # if you have `oras` installed
```

**Verify independently:** the SBOM should list real package names/versions
that match what's actually in your `package.json`/`bun.lock` — spot-check
3-4 dependencies by name. An SBOM that's present but empty, or that lists
generic placeholders instead of your actual deps, is worse than no SBOM at
all because it looks like coverage that isn't there.

## 8. SLSA provenance — verify with `cosign`, independent of `pokkum verify`

**Note:** `pokkum verify --no-rebuild`'s provenance summary is backed by a
real resolver (`internal/adapters/provenance/resolver.go`) that pulls the
remote manifest, verifies the Cosign signature (static-key or keyless),
extracts and verifies the in-toto DSSE envelope and SLSA v1.0 statement, and
enforces `--expect-source`. This guide used to warrant a "confirmed
finding" banner here because that resolver was a stub returning identical
hardcoded data regardless of the image you pointed it at — that's gone,
but two further rounds of hardening have landed on the now-real resolver
since, and "believe nothing" applies to each of them too:

- A literal fail-open: the resolver could return `SignatureValid: false`
  with a **nil error** for a genuinely signed image, meaning `verify`'s
  rebuild/comparison path had no gate on that field at all — a signed image
  could still be waved through.
- Four further, narrower fail-open defects found in the same pass: a
  missing `Critical.Type` check on the signing claims, `--expect-source`
  matching by `strings.Contains` (so `evil/github.com/acme/app` satisfied
  an expectation of `github.com/acme/app`), SLSA subject matching by
  substring against the digest hex instead of an exact `@<alg>:<hex>`
  suffix, and unbounded reads of registry-supplied content ahead of
  verification.

All are fixed, and every one of these was confirmed to regress its own new
test when reverted — but don't take that on faith either; cross-check with
`cosign` directly, as below, rather than trusting `pokkum verify`'s own
summary alone for this step.

```bash
cosign verify-attestation --type slsaprovenance1 \
  --certificate-identity-regexp '.*' --certificate-oidc-issuer-regexp '.*' \
  "$POKKUM_DOCKER_REPO@$DIGEST" 2>/dev/null | jq '.payload | @base64d | fromjson'
```
(Adjust `--certificate-identity`/`--certificate-oidc-issuer` to your actual
signing identity if you're not using keyless-anything-goes verification for
this check — tightening those two flags to your real CI identity is itself
worth doing once, so you know what "verified" should mean going forward.
This assumes a keyless-signed image; if you signed with a static key
instead, per §9, use `cosign verify-attestation --key <pub.pem>` here
instead of the certificate-identity flags.)

**Verify independently:** read the decoded predicate. Confirm
`builder.id`, `invocation.configSource` (or equivalent), and the Go/Bun
toolchain versions recorded match reality — not just that the attestation
exists and cosign didn't error.

**Use `--type slsaprovenance1`, not `--type slsaprovenance`.** They are
different predicate types: `slsaprovenance` means SLSA **v0.2**, while Pokkum
emits **v1**. Passing the wrong one produces an error that reads like a broken
attestation but is really a type mismatch:
```
$ cosign verify-attestation --type slsaprovenance …
Error: none of the attestations matched the predicate type: slsaprovenance,
       found: https://slsa.dev/provenance/v1

$ cosign verify-attestation --type slsaprovenance1 …
The signatures were verified against the specified public key
```

**Previously-documented gap, now closed — do not go hunting for it.** Earlier
revisions of this guide recorded cosign refusing Pokkum's DSSE layer for want of
a `dev.cosignproject.cosign/signature` annotation. That annotation is now written
(`signatureImage` in `internal/adapters/registry/attestation.go`), and an
adversarial field test confirmed cosign v3.1.3 verifies the attestation cleanly
against a tag-mode registry with **no** OCI 1.1 Referrers support — the same
fallback path ECR and older Harbor/Artifactory take. If `cosign
verify-attestation` fails for you now, check the `--type` above before
suspecting the attestation, and only then check whether the registry served a
referrer or fell back to the legacy `.att` tag:
```bash
crane manifest "$POKKUM_DOCKER_REPO:sha256-${DIGEST#sha256:}.att" >/dev/null && echo "tag-mode fallback was used"
```

## 9. Cosign signature — generate a real key, sign for real, verify with the `cosign` CLI directly

Signing used to be entirely unwired: `--sign` defaulted to `true` and the
pipeline's own validation required both a Cosign signer and a DSSE signer
to be non-nil, but neither signer's `Sign()` was ever actually called. The
SLSA statement was generated, logged, and discarded — no DSSE envelope, no
Cosign signature, no attachment, no push. Every image Pokkum ever produced
before that fix was pushed unsigned, regardless of what `--sign`'s default
implied. That is now wired end to end:

- `--signing-key`/`POKKUM_SIGNING_KEY` supply the private key — PEM text or
  a path to one, ECDSA P-256 or Ed25519, **unencrypted**. This is not the
  same format `cosign generate-key-pair` writes (an encrypted, password-
  protected key) — confirmed by trying to feed one in directly; generate an
  ordinary `openssl`-produced key instead (below).
- `--require-signed` turns a missing key into a CI-gate hard failure.
- Every signature and attestation is dual-published to the index **and**
  every per-platform manifest, so `cosign` and admission controllers like
  Kyverno agree regardless of which one they inspect.
- A post-push self-verification stage fetches the material back from the
  registry and cryptographically verifies it before the build reports
  success — but this guide's whole premise is not stopping at that
  self-report either.

**First, confirm the honest default: no key configured means a legitimately
unsigned image, announced loudly — not silently.**
```bash
./pokkum-test build . 2>&1 | grep -i "not signed\|no signing key"
```
Expect, close to verbatim:
```
level=WARN msg="--sign is enabled (the default) but no signing key is available — this build will push an UNSIGNED image; set POKKUM_SIGNING_KEY or --signing-key to sign, or pass --require-signed to make this an error"
level=WARN msg="IMAGE NOT SIGNED: signing is enabled but no signing key is available — the pushed image carries no signature or provenance attestation; ..."
```
Then confirm `cosign` independently agrees there is genuinely nothing to
check — this is the **absence** case, and it must read differently from a
broken signature (below):
```bash
cosign verify --certificate-identity-regexp '.*' --certificate-oidc-issuer-regexp '.*' \
  "$POKKUM_DOCKER_REPO@$DIGEST" 2>&1 | tail -3
#   → "Error: no signatures found"
```

**Now generate a real, unencrypted key and sign for real:**
```bash
openssl ecparam -genkey -name prime256v1 -noout -out /tmp/pokkum-sign.key
openssl ec -in /tmp/pokkum-sign.key -pubout -out /tmp/pokkum-sign.pub

IMAGE_REF=$(./pokkum-test build . --signing-key=/tmp/pokkum-sign.key 2>build-signed.log)
DIGEST=${IMAGE_REF#*@}
```

**Verify independently, with `cosign` and only the public key — not
`pokkum verify`:**
```bash
cosign verify --key /tmp/pokkum-sign.pub "$POKKUM_DOCKER_REPO@$DIGEST" 2>&1 | tail -10
```
This must come back with a real, cryptographic verification report from
the `cosign` binary itself — a tool this project didn't write. If it
fails, don't fall back to trusting `pokkum verify`'s own report of the
same thing; the point of this step is a second, independently implemented
opinion. Then confirm a *wrong* key fails **differently** from *no* key at
all, so you can tell the two apart when something looks wrong in the field:
```bash
openssl ecparam -genkey -name prime256v1 -noout -out /tmp/wrong.key
openssl ec -in /tmp/wrong.key -pubout -out /tmp/wrong.pub
cosign verify --key /tmp/wrong.pub "$POKKUM_DOCKER_REPO@$DIGEST" 2>&1 | tail -3
#   → "Error: no matching signatures: invalid signature when validating
#      ASN.1 encoded signature" — a broken/wrong-key signature. Compare
#      against "no signatures found" above: these must read differently,
#      or you cannot tell "nobody signed this" apart from "someone tampered
#      with this" by reading cosign's own output.
```

**Dual publication (index + every per-platform manifest):** don't stop at
the index digest —
```bash
crane manifest "$POKKUM_DOCKER_REPO@$DIGEST" | jq -r '.manifests[].digest'
```
and re-run `cosign verify --key /tmp/pokkum-sign.pub` against one of the
returned per-platform digests too. Both must verify independently; a
signature that only covers the index (or only a single platform) would be
a regression on a multi-platform build.

**The `--require-signed` CI gate fails before any work happens, not partway
through a slow push:**
```bash
time (./pokkum-test build . --require-signed 2>&1 | tail -3)
```
Expect a sub-second failure with:
```
--require-signed: signing required but no signing key is available (set POKKUM_SIGNING_KEY or --signing-key)
```
**Verify independently:** confirm this is a `Validate()`-stage rejection —
no Bun invocation, no base image pull, no network call at all — not a
build that starts, does real work, and fails partway through. The
wall-clock time from `time` is the independent evidence here.

Cleanup: `rm -f /tmp/pokkum-sign.key /tmp/pokkum-sign.pub /tmp/wrong.key /tmp/wrong.pub`.

## 10. Base image signature — confirm which mode actually ran, and that "no key" fails closed

The shared placeholder trust anchor is gone, and there is no longer an
"embedded default" to fall back to for static-key verification of any
kind. Until recently, one hardcoded, unattributed P-256 public key was the
last-resort trust anchor for three independent trust domains — base-image
static-key verification, remote-cache verification, and provenance
verification — and its own doc comment admitted it "does not correspond to
any key that actually signs upstream distroless or Chainguard images." A
trust anchor nobody owns is worse than no default, so it was deleted rather
than replaced: all three sites now fail closed, naming the exact env var to
set, and distinguishing "no key configured" from "signature invalid" —
different operator problems needing different fixes.

```bash
./pokkum-test build . --base-verify-mode=static-key --dry-run --log-level=DEBUG 2>&1 | tail -3
```
Expect, verbatim:
```
level=ERROR msg="command failed" error="baseimage: gcr.io/distroless/cc-debian12:nonroot: static-key verification requested but no key is configured; set POKKUM_BASE_IMAGE_PUBKEY to the Cosign public key that signed this base image: base image Cosign signature verification failed"
```
**Verify independently:** there is no "or the embedded default" phrasing
any more, and the build genuinely fails rather than silently passing —
confirmed by exit code, not just log text. The same fix closed a real,
separate fail-open in the provenance resolver used by §8/§9: it used to
return `SignatureValid: false` with a **nil error** for a genuinely signed
image.

For the default (`distroless`/`chainguard`/`distroless-node`) presets,
confirm the log explicitly says keyless verification ran (Fulcio + Rekor),
naming a real issuer/SAN and a real Rekor log ID/index — not just "base
image resolved" with no mention of signature checking at all:
```bash
./pokkum-test build . --print-manifest --log-level=DEBUG 2>&1 | grep -i "keyless\|signature\|verif"
```
Then, separately, run `--no-verify-base` once and confirm the complete
*absence* of every one of those lines, so you know what "not verified"
looks like too — not just what "verified" looks like:
```bash
./pokkum-test build . --no-verify-base --print-manifest --log-level=DEBUG 2>&1 | grep -i "keyless\|signature\|verif"
#   → no output at all
```

**A known gap worth flagging rather than working around:** the `--base`
flag's own help text advertises "distroless [default], chainguard, or
custom reference," but no current CLI flag or `.pokkum.yaml` field actually
lets you supply an arbitrary custom base image reference — `--base=custom`
alone fails with `base image reference is required for preset "custom"`,
and nothing else populates `BuildRequest.BaseImage.Ref` besides the
hardcoded static-strategy default. Confirmed by trying it. This means a
genuinely-successful static-key verification (a self-signed custom base,
with `POKKUM_BASE_IMAGE_PUBKEY` set to the matching public key) cannot be
demonstrated from this CLI today — the distroless/chainguard/distroless-node
presets are all keyless-signed upstream and have no static-key `.sig` to
check against. If you need to exercise the success path, it's covered by
`internal/adapters/baseimage/resolver_test.go`'s unit tests, not by any
command this guide can hand you right now.

## 11. Sigstore trust root coverage — confirm the embedded snapshot covers the log your signature actually landed in

The embedded Sigstore trust root used to be stale in a way that read
exactly like a forgery: its newest anchor dated 2023-04-14 and it covered
one Rekor transparency log, while the live public-good root has covered
**two** since 2025-09-23. Any keyless signature recorded on the newer
`log2025-1` shard failed with "not enough verified log entries from
transparency log: 0 < 1" — a genuine signature, indistinguishable from a
bad one, purely because the snapshot had no key for that shard. This
mattered the moment keyless verification itself became functional (§8/§10)
— the root had never actually been load-bearing before that fix landed,
and was about to be.

**Verify independently which logs the embedded snapshot actually covers —
don't take Pokkum's regenerated-snapshot claim on faith:**
```bash
jq -r '.tlogs[].baseUrl' internal/adapters/sigstore/trusted-root-public-good.json
#   → https://rekor.sigstore.dev
#     https://log2025-1.rekor.sigstore.dev
```
Cross-check against the *live* Sigstore TUF repository, fetched by a real
Sigstore tool rather than a raw guessed URL (TUF targets are
content-addressed; there is no stable plain path to curl):
```bash
cosign initialize
jq -r '.tlogs[].baseUrl' ~/.sigstore/root/tuf-repo-cdn.sigstore.dev/targets/trusted_root.json
```
Both lists should match (modulo the live repository having added more logs
since this guide was last touched — that possibility is the entire point
of this check).

**Confirm the diagnosis is real, not just reassuring text.** A signature
recorded on a log the trust root doesn't cover now fails with an explicit
"most likely a trust-root coverage gap" message rather than a bare
signature-verification error, and it names the logs the root does cover:
```
%s: the Rekor entry in annotation ... was recorded in transparency log ...,
which the ... trusted root does not contain (it covers N log(s): ...).
This is most likely a trust-root coverage gap rather than a bad signature...
```
The message also names the exact refresh command:
```bash
POKKUM_UPDATE_SIGSTORE_TRUSTED_ROOT=1 go test ./internal/adapters/sigstore/ -run TestTrustedRootSnapshot_TracksLiveTUFRepository -count=1
```
This is the real command the error text quotes — it is Go's golden-file
`-update` convention, so the same code path that *detects* drift from the
live repository is the one that *fixes* it, and the two cannot disagree.
Don't run it against a working checkout casually — it rewrites the
embedded snapshot files in place; use a throwaway clone, or `git diff` and
discard, if you're only checking whether it *would* change anything.

If you'd rather pin verification to a specific snapshot instead of the
embedded one, `--sigstore-trusted-root=<file>` exists on both `build` and
`verify`. Confirm it's actually wired, not silently ignored, by feeding it
a deliberately old snapshot and confirming the same coverage-gap error
above reproduces against a signature you know is genuine.

## 12. Runtime verification — does the image actually run?

```bash
docker run -d --name pokkum-paranoid-test -p 3000:3000 -p 8081:8081 "$POKKUM_DOCKER_REPO@$DIGEST"
sleep 2
curl -sf http://localhost:8081/healthz && echo " healthz OK"
curl -sf http://localhost:8081/readyz && echo " readyz OK"
curl -sf http://localhost:3000/ | head -20
docker logs pokkum-paranoid-test
docker stop pokkum-paranoid-test && docker rm pokkum-paranoid-test
```

**Verify independently:** an image that builds, signs, and attests
correctly but doesn't actually serve your app is a false positive on
everything above. This step is the one no cryptographic verification can
substitute for — actually load the page.

## 13. Reproducibility — diff two independent builds yourself

```bash
IMAGE_REF1=$(./pokkum-test build . --tarball /tmp/build1.tar 2>/dev/null)
IMAGE_REF2=$(./pokkum-test build . --tarball /tmp/build2.tar 2>/dev/null)
DIGEST1=${IMAGE_REF1#*@}
DIGEST2=${IMAGE_REF2#*@}
echo "build1: $DIGEST1"
echo "build2: $DIGEST2"
[ "$DIGEST1" = "$DIGEST2" ] && echo "MATCH" || echo "MISMATCH"

# Don't stop at digest equality — diff the actual tarball bytes too:
sha256sum /tmp/build1.tar /tmp/build2.tar
```

**Verify independently:** run this on two different days, or after a
`git commit --amend` that changes nothing but the commit hash, to confirm
`SOURCE_DATE_EPOCH` pinning is doing its job rather than coincidentally
matching because you ran both in the same second. Then, separately, run
`./pokkum-test verify --rebuild` and confirm ITS verdict agrees with your
manual diff above — if they disagree, trust your manual diff and treat
that as a bug report.

## 14. Kubernetes manifest resolution — read the generated YAML, don't skim it

```yaml
# deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-app
spec:
  selector:
    matchLabels: { app: test-app }
  template:
    metadata:
      labels: { app: test-app }
    spec:
      containers:
      - name: main
        image: pokkum://.
        ports: [{ containerPort: 3000 }]
```

```bash
./pokkum-test resolve -f deployment.yaml --network-policy --resource-defaults > resolved.yaml
cat resolved.yaml
```

**Verify independently, by eye:**
- The `PodDisruptionBudget`'s `selector.matchLabels` must show
  `app: test-app` — **not** an empty `{}`. If your manifest has no
  `template.metadata.labels`, confirm the PDB document is simply *absent*
  from the output rather than present-and-wrong.
- The `NetworkPolicy`'s `ingress[].ports` must list the real container port
  (3000 here), not a hardcoded default, and `egress` must NOT contain a
  bare `- {}` (unrestricted) rule.
- The `image:` line must be a `repo@sha256:...` digest, never a mutable
  tag.
- Run `kubectl apply --dry-run=client -f resolved.yaml` (or
  `--dry-run=server` against a real cluster if you have one) to confirm
  the YAML is actually valid Kubernetes, not just valid-looking.

## 15. Rollback — prove the round-trip, don't just run it once

```bash
./pokkum-test rollback -f resolved.yaml   # no --to: should read pokkum.dev/previous-image
grep -A2 "image:" resolved.yaml
grep "pokkum.dev/previous-image" resolved.yaml
./pokkum-test rollback -f resolved.yaml   # run again — should toggle back
grep -A2 "image:" resolved.yaml
```

**Verify independently:** the image ref after the second `rollback` call
should equal what it was before the first one. If it doesn't round-trip,
the annotation-writing logic has a bug — this is cheap to check and easy
to get wrong.

## 16. `pokkum scan` — now real; verify it against a package you know is vulnerable

`pokkum scan` was fixed to genuinely pull and inspect the target (real
enumeration + real OSV.dev queries) rather than return a hardcoded advisory
list — but "believe nothing" applies to the fix itself just as much as to
the original claim. Don't just run it and check the exit code; make it
prove it against something you know the answer to.

**Factual correction to this guide, not a behavior change:** an earlier
version of this section described the enumeration as "real Syft-based."
That was wrong — Pokkum is a zero-dependency tool and has no Syft anywhere
in its dependency graph. Enumeration comes from its own lightweight parsers
(`internal/adapters/scannerutils`, which declares its purpose in its own
package doc: OS package databases — Debian `dpkg` status, Alpine `apk`
database — OS release metadata, and JS/TS lockfiles — `bun.lock`,
`package-lock.json`, `pnpm-lock.yaml` — "avoiding the need for heavy
external catalogers like Anchore Syft") plus real OSV.dev queries. Keeping
`syft`/`grype` installed (§0) as an *independent* cross-check remains
exactly right — that part of the original advice was correct; only the
description of Pokkum's own scanner was not.

```bash
./pokkum-test scan "$POKKUM_DOCKER_REPO@$DIGEST" --output=json | jq .
```

**Verify independently — package enumeration is real:**
```bash
jq '.data.vulnerabilities[].package' <(./pokkum-test scan "$POKKUM_DOCKER_REPO@$DIGEST" --output=json)
# Cross-check the package LIST itself (not just CVE findings) against
# what's actually in the image, independent of pokkum's own scanner:
grype "$POKKUM_DOCKER_REPO@$DIGEST" -o json | jq '.matches[].artifact.name' 2>/dev/null
```

**Verify independently — it actually catches a real CVE, not just a real
package list.** Build a throwaway project with a known-vulnerable
dependency and confirm `pokkum scan` catches it:
```bash
mkdir /tmp/scan-test && cd /tmp/scan-test
echo '{"name":"scan-test","dependencies":{"lodash":"4.17.4"}}' > package.json
../pokkum-test scan . --fail-on=low --output=json | jq '.data.vulnerabilities'
# lodash 4.17.4 has real, long-published CVEs (prototype pollution, etc.) —
# expect a non-empty list and a non-zero exit code.
echo "exit code: $?"
cd - && rm -rf /tmp/scan-test
```

**Verify independently — it fails closed, not clean, when the CVE database
is unreachable.** This was a real bug found in review: a failed OSV.dev
lookup used to silently report "0 vulnerabilities found," indistinguishable
from a genuinely clean scan.
```bash
# Point DNS/network somewhere broken (or just disconnect), then:
./pokkum-test scan . --output=json | jq '.data.incomplete, .data.warnings'
echo "exit code: $?"
# Expect incomplete:true, a non-empty warnings array, and a NON-ZERO exit
# code — not a clean pass. --allow-incomplete should be the only way to
# get a 0 exit code in this state.
./pokkum-test scan . --allow-incomplete --output=json | jq '.data.passed, .data.incomplete'
```

`pokkum scan --toolchain` (Bun/SvelteKit version advisories via OSV.dev)
remains real and works as documented, same as before.

## 17. `--registry-config` — verify the credential helper actually gets invoked

```bash
# A minimal docker-config.json using a credHelper (swap for your real registry/helper):
cat > /tmp/regconfig.json <<'EOF'
{"credHelpers": {"ghcr.io": "desktop"}}
EOF
./pokkum-test build . --registry-config=/tmp/regconfig.json --dry-run --log-level=DEBUG 2>&1 | grep -i "credential\|keychain\|helper"
```

**Verify independently:** temporarily rename/remove the named
`docker-credential-<helper>` binary from `PATH` and re-run — confirm the
build reports a clear error (or falls back to the default keychain) rather
than hanging or silently using no auth at all. Don't just trust a
successful run with the helper present; prove the failure mode is sane too.

## 18. `pokkum adopt` — verify the detection gate and that nothing gets mutated you didn't ask for

```bash
# 1. Confirm it refuses a non-SvelteKit project
mkdir /tmp/not-sveltekit && cd /tmp/not-sveltekit
echo '{"name":"plain-app","dependencies":{"express":"^4.19.0"}}' > package.json
../pokkum-test adopt . ; echo "exit: $?"
# Expect a clear error and an UNCHANGED package.json — verify both:
cat package.json
cd - && rm -rf /tmp/not-sveltekit
```

```bash
# 2. Against a real SvelteKit project using adapter-node/adapter-vercel:
cp -r test-app /tmp/adopt-test && cd /tmp/adopt-test
../pokkum-test adopt . --dry-run
```

**Verify independently:**
- Diff `package.json` before/after a real (non-dry-run) run — confirm only
  the adapter dependency and `pokkum:build` script actually changed, not
  every key reordered. `git diff package.json` is the cleanest way to see
  this if the test project is a git repo.
- Without `--write-config`, confirm `svelte.config.js` is **byte-identical**
  (`diff` it, don't just check the command's own "ConfigUpdated" claim):
  ```bash
  cp svelte.config.js /tmp/before.js
  ../pokkum-test adopt .
  diff /tmp/before.js svelte.config.js && echo "unchanged, as expected without --write-config"
  ```
- With `--write-config`, confirm it now DOES change and still contains a
  valid, buildable config (`../pokkum-test build . --print-manifest` should
  still succeed after).

## 19. `pokkum history` — confirm it reports THIS image's real data, not a template

```bash
./pokkum-test history "$POKKUM_DOCKER_REPO@$DIGEST" --output=json | jq .
```

**Verify independently:** the single most important check here is that the
output actually varies per image — a hardcoded stub would look identical
for every ref. Build and push a second, distinguishable image (different
commit, different tag) and confirm the two `history` outputs differ:
```bash
git commit --allow-empty -m "second commit for history diff test"
IMAGE_REF2=$(./pokkum-test build . 2>/dev/null)
DIGEST2=${IMAGE_REF2#*@}
diff <(./pokkum-test history "$POKKUM_DOCKER_REPO@$DIGEST" --output=json) \
     <(./pokkum-test history "$POKKUM_DOCKER_REPO@$DIGEST2" --output=json)
# Expect a real diff (different git_commit at minimum) — identical output
# here would mean you're looking at a stub again.
```
Also confirm `git_commit`/`git_repo` in the output match your actual
`git rev-parse HEAD`/`git remote get-url origin` at build time — not just
that the fields are non-empty. `pokkum history` deliberately does **not**
verify signatures or SLSA provenance — if its output claims otherwise,
that's a regression; the real verdict on those comes from `pokkum verify`.

## 20. Multi-generation rollback — prove it survives more than one hop

```bash
# Deploy three times in a row against the same manifest to build real history:
for i in 1 2 3; do
  ./pokkum-test resolve -f deployment.yaml > /tmp/resolved-$i.yaml
  cp /tmp/resolved-$i.yaml deployment.yaml   # persist annotations forward, per docs/archive/fixes-to-v1.md's documented workflow requirement
done
./pokkum-test rollback -f deployment.yaml --list
```

**Verify independently:** `--list` should show (at least) two prior
generations, not just one. Roll back two generations (`-g 2`) and confirm
the resulting `image:` matches the digest from `/tmp/resolved-1.yaml`, not
just "some earlier value":
```bash
grep "image:" /tmp/resolved-1.yaml
./pokkum-test rollback -f deployment.yaml -g 2
grep "image:" deployment.yaml
# these two digests must match
```
Known, documented limitation worth confirming rather than assuming: this
only works if annotations are carried forward between `resolve` calls (as
above). If you instead re-resolve a **fresh copy** of the original
`pokkum://`-templated source each time (Pokkum's own recommended default
workflow — never mutating the tracked source), no history accumulates at
all — confirm this too, so you know which regime you're actually in:
```bash
./pokkum-test resolve -f original-template.yaml > /tmp/fresh1.yaml
./pokkum-test resolve -f original-template.yaml > /tmp/fresh2.yaml
diff /tmp/fresh1.yaml /tmp/fresh2.yaml   # expect no history annotation difference — this is the known gap, not a new bug
```

## 21. Runtime Env Contract (`--require-env`) — confirm it actually fails fast at startup

```bash
./pokkum-test build . --require-env=DATABASE_URL,API_KEY
docker run --rm "$POKKUM_DOCKER_REPO@$DIGEST"
```
**Verify independently:** the container must exit non-zero immediately,
with `DATABASE_URL` and `API_KEY` named explicitly in the output — not a
generic crash, and not a silent hang. Then confirm the positive case:
```bash
docker run --rm -e DATABASE_URL=postgres://x -e API_KEY=test "$POKKUM_DOCKER_REPO@$DIGEST"
```
This should start normally. Also confirm no *value* ever got baked into
the image — inspect the pushed image's labels/env and confirm only the
variable *names* appear, never `postgres://x` or any value you supplied at
build time (there shouldn't be a build-time value at all for this feature):
```bash
docker inspect "$POKKUM_DOCKER_REPO@$DIGEST" | jq '.[0].Config.Env, .[0].Config.Labels'
```

## 22. Base Image Escrow / Mirroring (`--mirror-registry`)

Base Image Escrow mirrors upstream base images/indexes and their Cosign `.sig` tags
to a project-controlled registry upon `pokkum base update`. All mirror write operations
are verified and fail-closed: write failures immediately return errors and prevent
recording unwritten `MirrorRef` entries in `pokkum.lock`.

To verify manual mirroring against a live registry:
```bash
./pokkum-test base update --preset distroless --mirror-registry=ghcr.io/you/base-mirror
crane manifest ghcr.io/you/base-mirror:sha256-<digest-from-pokkum.lock>
crane manifest ghcr.io/you/base-mirror:sha256-<digest-from-pokkum.lock>.sig
```
Automated unit tests in `internal/adapters/baseimage/resolver_test.go` (`TestResolve_EscrowMirror_*`)
test multi-platform indexes, single images, signature tag mirroring, fail-closed write errors,
and air-gapped resolution.

**Mirror + signature verification interaction:** combining `--mirror-registry` with
`VerifySignature: true` (the default for `distroless`/`chainguard`) used to fail on ordinary,
successful resolution — the signature's docker-reference claim was checked against the
mirror's repo name instead of the true upstream repo. This is now fixed: the claim is checked
against the upstream repo recorded in the lockfile entry (`entry.Ref`), independent of wherever
the bytes were actually fetched from. Covered by
`TestResolve_EscrowMirror_VerifySignature_SucceedsAgainstUpstreamRepo` (the mirror-preferred
success case, asserted on real digest/ref/pinned-ref fields) and
`TestResolve_EscrowMirror_VerifySignature_TamperedMirrorDigestFailsClosed` (an adversarial test
proving a mirror serving different content under a replayed genuine signature still fails
closed on the independent digest claim). If a future paranoid pass wants to re-check this by
hand: mirror a signed base, then resolve with both flags and confirm it succeeds; separately,
hand-edit a mirrored `.sig` tag to reference a different digest than the mirror actually serves
and confirm resolution still fails closed.

## 23. Static / Prerendered (`--static`) — verify the zero-JS image end-to-end

This exercises the `--static` build path (Roadmap §1): a purely prerendered
SvelteKit site compiled onto a libc-free `chainguard/static` base and served
by Pokkum's own embedded `pokkum-static` Go PID-1 file server — **no Bun
runtime, no compiled executable, no separate supervisor**. "Believe nothing"
matters extra here because the HTTP-serving layer is Pokkum's own code, not
an off-the-shelf nginx, and this is one of the newest surfaces in the
codebase — `--strategy=static` genuinely did not work at all until recently.

**What changed, so you know why the checks below say what they say.**
`pokkum-static` never set `http.Server.Addr` on either of its listeners, so
both fell back to Go's `:http` default and ignored `PORT`/
`POKKUM_PROBE_PORT` entirely — one listener won the race for port 80, the
other died with "address already in use," and no `--strategy=static` image
was ever reachable on its documented ports. `Preflight` also rejected
every correctly-configured `adapter-static` project outright before
`Prepare`'s own strategy-aware check ever ran. And real prerendered output
nests under `prerendered/pages/` (plus sibling `dependencies/`/`data/`
trees), while the code assumed a flat layout — so the directory-exists
check passed while every prerendered route 404'd, and the synthetic test
fixture in use at the time fabricated the same flat shape the bug assumed,
so nothing caught it. All three are fixed, backed by a real fixture
(`testdata/fixtures/sveltekit-static`, built with real `bun`) and a boot
smoke test that actually starts a container and polls it. The embedded
`pokkum-static` blobs consumed via `go:embed` are gitignored, locally-built
artifacts (§0) — CI and `.goreleaser.yaml` now build them explicitly, but
that wasn't always true either; until it was fixed, no CI-built release
could ever have produced a working `--strategy=static` image, only a
developer's own locally-built binary could.

**A related fail-closed fix, worth being aware of even though this guide
gives no command for it today:** if `PORT` and `POKKUM_PROBE_PORT` collapse
to the same value, there is no mux merge that lets one listener cover the
other — the probe listener silently never starts, and `/healthz`/`/readyz`
are served by nothing. `pokkum-static` now rejects this outright at
startup with `"%s and %s must not both be %d: ..."` rather than booting
into an unprobeable container. There is no way to *demonstrate* the
opposite (a working single-port mode) because that mode does not exist —
don't write yourself a command that assumes it does.

### 23a. Preflight — make the CLI tell you what `--static` actually did

```bash
# 1. The shorthand must map to the static strategy and reject the conflicting one.
./pokkum-test build . --static --strategy=exe --print-manifest 2>&1 | head -5
#      → expect a hard error: "--static cannot be combined with --strategy=exe"

# 2. With no --base/--hardened, --static must pick the libc-free static base.
./pokkum-test build . --static --print-manifest --output=json 2>/dev/null \
  | jq -r '.images[0].config.config.Labels["org.opencontainers.image.base.name"]'
#      → cgr.dev/chainguard/static@sha256:... , NOT gcr.io/distroless/cc-debian12
```

**Verify independently:** don't trust the flag description — confirm the base
ref *changed* from what a default `layered` build emits, and confirm the error
in (1) really is an exit-code failure, not a printed warning.

### 23b. Build a genuinely prerendered app

`--static` requires an all-prerendered site (every route static; no SSR-only
endpoints). Use a fixture that is *truly* prerenderable, or force it:

```bash
# Minimal route that is fully static:
#   src/routes/+page.ts  ->  export const prerender = true;
# plus, if the whole site should be static-by-default:
#   src/routes/+layout.ts -> export const prerender = true;
npx sv create static-app --template minimal --types ts
cd static-app && bun install
# Tell the adapter there must be no fallback/SSR surprises:
cat >> svelte.config.js <<'EOF'
const config = {
  // ...existing...
  kit: { prerender: { entries: ['/'] } }
};
EOF

cd /path/to/pokkum
export POKKUM_DOCKER_REPO=ghcr.io/<you>/pokkum-paranoid-test
IMAGE_REF=$(./pokkum-test build /path/to/static-app --static --tag static-v1 2>static.log)
echo "$IMAGE_REF" | tee static-ref.txt
```

If the app has an unprerendered (SSR-only) route, `--static` should **fail the
build** — that is correct guarded behavior, not a bug. If it unexpectedly
succeeds on an SSR-only route, you've found a gap.

### 23c. Inspect the image independently — this is where "no Bun, no supervisor" is proven

```bash
# Pull the exact built digest (read it from static-ref.txt, don't trust memory).
IMAGE=$(cat static-ref.txt)
DIGEST=${IMAGE#*@}
docker pull "$IMAGE"

# Config: entrypoint + no USER change + service port
docker inspect "$IMAGE" --format '{{json .Config.Entrypoint}} {{.Config.ExposedPorts}}'

# Layer history: expect ONLY base + /app/client + /app/prerendered + /pokkum/static.
# There must be NO /usr/local/bin/bun layer and NO /pokkum/init supervisor layer.
docker history "$IMAGE"

# Independent structural look (crane if you installed it):
crane manifest "$IMAGE" | jq '.layers | length'
crane config "$IMAGE" | jq '{entrypoint:.config.Entrypoint, env:.config.Env, created_by:.history[-1].created_by}'
```

**Verify independently:** `crane config` env should include
`POKKUM_STATIC_ROOTS=/app/client:/app/prerendered` and the entrypoint should
be exactly `["/pokkum/static"]` — not wrapped by `/pokkum/init` (unlike
`layered`/`exe`, the static strategy's own binary is PID 1 with no
supervisor at all — confirmed against a real built image) and not `bun`. A
lack of the Bun layer in `docker history` is the single most important
"it's really static" signal; the layer's `created_by` for a bun-runtime
build is literally `pokkum: add /usr/local/bin/bun`, so `docker history
"$IMAGE" | grep -i bun` returning nothing is a real, checkable absence, not
just "I didn't notice it."

### 23d. Runtime — prove `pokkum-static` serves correctly

```bash
docker run -d --rm -p 3000:3000 -p 8081:8081 --name pokkum-static-test "$IMAGE"
sleep 2

BASE=http://127.0.0.1:3000

# 1. Index serves and is HTML
curl -s -D- "$BASE/" -o /tmp/static-index.html | head -1
grep -i '<html' /tmp/static-index.html

# 2. Probe endpoints respond (pokkum-static doubles as the probe server)
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8081/healthz   # → 200
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8081/readyz    # → 200

# 3. Extensionless route (adapter-static's default trailingSlash:'never'
#    prerenders /about to about.html, not a directory) serves as real HTML,
#    not a 404 and not the wrong fallback:
curl -s -o /dev/null -w '%{http_code}\n' "$BASE/about"                  # → 200

# 4. Range request → 206 with correct Content-Range
curl -s -D- -H 'Range: bytes=0-9' "$BASE/" -o /tmp/range.bin | grep -Ei 'HTTP/|content-range'

# 5. Content-Encoding: request gzip and confirm the served bytes are gzip.
#    The build pre-compresses /app/client assets to .gz/.br/.zst; the server
#    must hand back the sidecar, not compress on the fly.
curl -s -D- -H 'Accept-Encoding: gzip' "$BASE/" -o /tmp/served.bin | grep -i 'content-encoding'
file /tmp/served.bin        # → 'gzip compressed data', and NOT the on-the-fly variant

# 6. Unknown route → 404 (pure static site has no fallback by default)
curl -s -o /dev/null -w '%{http_code}\n' "$BASE/does-not-exist"          # → 404
#    The 404 is honest and clean (no dev-marker HTML comment), though the
#    server logs a one-per-process Warn noting that an opt-in SPA fallback
#    exists via POKKUM_STATIC_FALLBACK / -fallback.

# 6b. (Optional) Opt-in SPA fallback — rebuild with a fallback page:
#     a static project whose svelte.config.js sets adapter({ fallback: '200.html' })
#     emits client/200.html; the packager stamps POKKUM_STATIC_FALLBACK=/app/client/200.html.
#     Then an unmatched GET/HEAD returns the shell with 200:
#     curl -s -D- -H 'Accept-Encoding: gzip' "$BASE/unknown-route" | grep -iE 'HTTP|content-type'

docker stop pokkum-static-test
```

**Verify independently:** don't trust `curl` exit 0 on its own — read the actual
`Content-Range` and `Content-Encoding` **headers** you printed above, and
`file` the served body so you know you really got gzip bytes and not the
plain file.

**Conditional GET — verify the 304, don't assume it.** `pokkum-static` sends a
strong content-hash `ETag` and honours `If-None-Match`: a request presenting the
current tag gets a `304` with no body. This was genuinely absent until it was
found by running this very step, so it is worth actually checking rather than
trusting the claim:
```bash
ETAG=$(curl -s -D- -o /dev/null "$BASE/" | grep -i '^etag:' | sed -E 's/^[Ee][Tt][Aa][Gg]: //' | tr -d '\r')
curl -s -o /dev/null -w '%{http_code}\n' -H "If-None-Match: $ETAG" "$BASE/"          # → 304
curl -s -o /dev/null -w '%{http_code}\n' -H 'If-None-Match: "not-the-tag"' "$BASE/"  # → 200
curl -s -o /dev/null -w '%{http_code}\n' -H 'If-None-Match: *' "$BASE/"              # → 304
# A matching conditional beats Range, per RFC 9110 — this must be 304, not 206:
curl -s -o /dev/null -w '%{http_code}\n' -H "If-None-Match: $ETAG" -H 'Range: bytes=0-9' "$BASE/"
```
**Verify independently:** confirm the `304` carries **no body** (`curl -s` should
print nothing for it) while still returning the `ETag` and `Cache-Control`
headers — a 304 that omits its validator is as wrong as one that ships a body.

`If-Modified-Since` is deliberately **not** implemented, and its absence is
correct rather than a gap: in-image file mtimes are pinned to a fixed epoch for
bit-for-bit reproducibility, so a `Last-Modified` derived from them would be a
constant across every build and actively misleading as a validator. The
content-hash `ETag` is the honest one.

### 23e. Reproducibility — static builds must still be bit-for-bit

```bash
./pokkum-test build /path/to/static-app --static --tag static-v1 2>/dev/null > /tmp/static1.txt
./pokkum-test build /path/to/static-app --static --tag static-v2 2>/dev/null > /tmp/static2.txt
diff /tmp/static1.txt /tmp/static2.txt && echo "STATIC REPRODUCIBLE"
```
(The plain `repo@sha256:...` ref is stable across the two `--tag` values
because the digest reflects image content, not the tag — a real mismatch
here would show up as two different digests, not just two different tags.)

### 23f. Layered prerendered pages (the other half of §1)

For a normal `--strategy=layered` build, prerendered pages now live in their
own `/app/prerendered` layer (Roadmap §1, part 1), and the generated
adapter-node `handler.js` is patched to serve them via `POKKUM_PRERENDERED_DIR`.

```bash
# Build a predominantly-prerendered app with the default layered strategy.
IMAGE_REF=$(./pokkum-test build /path/to/app 2>layered.log)
DIGEST=${IMAGE_REF#*@}
IMAGE="$POKKUM_DOCKER_REPO@$DIGEST"
docker pull "$IMAGE"

# 1. A prerendered route is served as real static HTML from /app/prerendered
docker run -d --rm -p 3001:3000 --name pokkum-layered-test "$IMAGE"; sleep 2
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:3001/my-prerendered-page   # → 200
curl -s http://127.0.0.1:3001/my-prerendered-page | grep -i '<html'
docker stop pokkum-layered-test

# 2. The image mount/env point at that layer
crane config "$IMAGE" | jq '.config.Env[] | select(startswith("POKKUM_PRERENDERED_DIR"))'   # → /app/prerendered
docker history "$IMAGE" | grep -i prerendered    # → a layer created_by naming /app/prerendered
```

**Verify independently:** the prerendered page should return the *static
pre-rendered HTML* — not a client-side SPA shell. If it 404s or returns the
empty SPA bootstrap, the handler patch isn't pointing at the right layer (a
version-sensitive gap — see Roadmap §1 follow-ups).

## 24. `--runtime=node` — the second runtime dimension: verify Bun really isn't in the image

`--runtime=node` swaps the embedded, checksum-pinned Bun runtime for the
base image's own Node.js. It's a genuinely new capability with its own
verification surface, not a bolt-on: it changes the base preset, the
entrypoint, the layer set, the toolchain CVE-lookup keying, and what SLSA
provenance records as `externalParameters.runtime` — this guide had
nothing for any of it before now.

### 24a. Preflight — confirm the (runtime × strategy) matrix is a real gate, not a suggestion

```bash
# node supports --strategy=layered only; every other combination must be a
# hard validation error, before any Bun invocation or network call:
./pokkum-test build . --runtime=node --strategy=exe  --dry-run 2>&1 | tail -1
./pokkum-test build . --runtime=node --static        --dry-run 2>&1 | tail -1
./pokkum-test build . --runtime=node --telemetry      --dry-run 2>&1 | tail -1
./pokkum-test build . --runtime=node --base=chainguard --dry-run 2>&1 | tail -1
```
Each must fail validation, in well under a second, with a specific, named
reason — confirmed verbatim against the real CLI:
```
--runtime=node supports --strategy=layered only (exe compiles via `bun build --compile`, which has no Node equivalent; static ships no JS runtime at all), got "exe"
--runtime=node supports --strategy=layered only (... ), got "static"
--telemetry is not yet supported with --runtime=node (the layered telemetry bootstrap is Bun-specific `bun --preload` TypeScript)
base preset "chainguard" ships no Node.js runtime; --runtime=node needs --base=distroless-node (the default) or a custom base image providing /nodejs/bin/node
```
**Verify independently:** these are real, specific error strings naming the
actual conflicting flag — not a generic "invalid combination" that would
just as happily reject something that should work.

### 24b. Build, boot, and prove there is no Bun anywhere in the image

```bash
IMAGE_REF=$(./pokkum-test build . --runtime=node 2>node-build.log)
DIGEST=${IMAGE_REF#*@}
IMAGE="$POKKUM_DOCKER_REPO@$DIGEST"
docker pull "$IMAGE"
```

**Verify independently — the base preset really changed:**
```bash
crane config "$IMAGE" | jq -r '.config.Labels["org.opencontainers.image.base.name"]'
#   → gcr.io/distroless/nodejs24-debian12@sha256:...  (NOT cc-debian12)
```

**Verify independently — the entrypoint execs Node through the same
supervisor, not Bun:**
```bash
crane config "$IMAGE" | jq -r '.config.Entrypoint'
#   → ["/pokkum/init","--","/nodejs/bin/node","/app/server/index.js"]
```

**Verify independently — no Bun layer exists at all** (a bun-runtime build
of the same project has a layer whose `created_by` is literally `pokkum:
add /usr/local/bin/bun`):
```bash
docker history "$IMAGE" | grep -i bun
#   → no output. Build the same project with the default (bun) runtime and
#     diff the two `docker history` outputs to confirm the negative against
#     a real positive, not just an absence you're assuming is meaningful.
```

**Verify independently — the non-default runtime is labeled, and *only*
the non-default one is** (so every pre-existing bun image's labels, and
its pinned digest, stayed byte-identical when this shipped):
```bash
docker inspect "$IMAGE" | jq -r '.[0].Config.Labels["dev.pokkum.runtime"]'
#   → node
```

**Verify independently — it actually boots and serves, not just assembles
correctly:**
```bash
docker run -d --rm -p 3000:3000 -p 8081:8081 --name pokkum-node-test "$IMAGE"
sleep 2
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8081/healthz   # → 200
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8081/readyz    # → 200
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:3000/          # → 200
docker logs pokkum-node-test 2>&1 | grep "path=/nodejs"
docker stop pokkum-node-test
```
Confirm the supervisor's own startup log names the real exec path
(`path=/nodejs/bin/node ...`) — not just that `curl` got a 200, which a
stale cached response or a misrouted proxy could also produce.

### 24c. Known, documented gaps — don't file these as bugs

- **No telemetry.** `--runtime=node --telemetry` is rejected outright
  (§24a) — the layered OTEL bootstrap is Bun-specific (`bun --preload` of a
  TypeScript file); a Node-native equivalent is tracked as follow-up work,
  not silently faked here.
- **Node-core CVEs are not scannable.** `pokkum scan` cannot see Node's own
  version/CVEs inside a `--runtime=node` image: distroless ships Node
  outside `dpkg`, so the OS-package scan has nothing to enumerate it from,
  and the zero-dependency scanner (§16) has no Node-core advisory ecosystem
  to query in the first place. CVE response for the Node runtime itself is
  `pokkum base update` re-pinning the base digest, not `pokkum scan`
  catching it after the fact:
  ```bash
  ./pokkum-test scan "$IMAGE" --output=json | jq '.data.vulnerabilities[] | select(.package | test("node"; "i"))'
  #   → expect no results naming the Node runtime itself (findings for your
  #     own npm dependencies are unaffected and should still show up).
  ```
- **The runtime is part of the remote-cache key.** A build sharing a custom
  `--base` between a bun and a node build must not cache-hit across the
  two — the default presets already differ per runtime (so the base digest
  usually separates them), but a shared custom base makes that protection
  evaporate without this. Unit-tested in
  `internal/adapters/remotecacheutils/remotecacheutils_test.go`; worth a
  manual spot-check if you rely on a shared custom base.

## 25. Cleanup

```bash
docker rmi "$POKKUM_DOCKER_REPO@$DIGEST" 2>/dev/null
rm -f /tmp/build1.tar /tmp/build2.tar /tmp/manifest.json build.log build-signed.log node-build.log layered.log static.log resolved.yaml
rm -f /tmp/regconfig.json /tmp/before.js /tmp/resolved-1.yaml /tmp/resolved-2.yaml /tmp/resolved-3.yaml /tmp/fresh1.yaml /tmp/fresh2.yaml
rm -f /tmp/pokkum-sign.key /tmp/pokkum-sign.pub /tmp/wrong.key /tmp/wrong.pub
rm -f static-ref.txt /tmp/static1.txt /tmp/static2.txt /tmp/static-index.html /tmp/range.bin /tmp/served.bin
rm -f ./pokkum-test
```

---

## Summary checklist

| Claim | Independent verification tool | Step |
|---|---|---|
| Build succeeded | `docker inspect`, `docker history` | 5 |
| Non-root, hardened runtime | `docker inspect .Config.User` | 5 |
| OCI annotations correct | your own `git log`/`git config` | 6 |
| SBOM real and non-empty | manual dependency spot-check | 7 |
| SLSA provenance real | `cosign verify-attestation` (tag-mode caveat noted) | 8 |
| Image signature real, and distinguishable from "legitimately unsigned" | `cosign verify` with a real generated key | 9 |
| Base image verification ran, and fails closed with no key configured | debug logs, explicit mode check, no embedded fallback | 10 |
| Sigstore trust root actually covers the log your signature landed in | `cosign initialize` + `jq` against the live TUF repo | 11 |
| App actually works | `curl` against a running container | 12 |
| Build is reproducible | manual two-build digest + tarball diff | 13 |
| K8s manifests correct | read the YAML, `kubectl --dry-run` | 14 |
| Rollback round-trips | manual toggle-twice check | 15 |
| CVE scan catches a real vulnerability, fails closed on DB outage, enumerates via Pokkum's own parsers (not Syft) | vulnerable-dependency fixture, network-outage test, `grype` cross-check | 16 |
| Registry credential helper actually invoked | remove the helper binary, confirm the failure mode | 17 |
| `adopt` refuses non-SvelteKit projects, mutates only what's expected | `git diff`, byte-diff without `--write-config` | 18 |
| `pokkum history` reflects the real image, not a template | two builds, diff their `history` output | 19 |
| Multi-generation rollback survives >1 hop | `-g 2` lands on the right digest, not "some" digest | 20 |
| Runtime Env Contract fails fast, bakes no values | container exit code + `docker inspect` | 21 |
| Base image mirror actually wrote the blob | `crane manifest` against the mirror, not the log line | 22 |
| `--static` really produced a zero-JS image (no Bun/supervisor, static base, correct ports) | `docker history`, `crane config` entrypoint/env | 23c |
| `pokkum-static` serves correctly (Range/Content-Encoding/probes/extensionless routes/404); ETag present but 304 is a known gap | `curl` headers + `file` on the served body | 23d |
| Prerendered pages served as real static HTML from `/app/prerendered` | `curl` + `crane config` env + `docker history` | 23f |
| `--runtime=node` really ships no Bun, execs `/nodejs/bin/node`, and boots | `docker history`, `crane config`, real boot + curl | 24b |

If every row above checks out via the independent tool, not just Pokkum's
own exit code, you have real evidence — not just Pokkum's word for it.
