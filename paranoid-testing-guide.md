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
no real check at all (see [fixes-to-v1.md](fixes-to-v1.md)). Those are
fixed now, but the discipline of not trusting a tool's self-report is worth
keeping regardless of which tool it is.

## 0. Prerequisites

```bash
# Required
which bun git go docker cosign

# Recommended but optional (install if you want deeper independent
# inspection than `docker manifest`/`docker inspect` give you)
brew install crane        # or: go install github.com/google/go-containerregistry/cmd/crane@latest
brew install syft grype   # independent SBOM/CVE cross-check, unrelated to Pokkum's own syft usage
```

Build `pokkum` from the exact commit you're testing (don't test against a
possibly-stale installed binary):

```bash
cd /path/to/pokkum
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
jq '.data' /tmp/manifest.json | less
```
Read the actual computed manifest/config JSON yourself. Confirm the base
image ref, platform list, and layer count match what you expect — this
step touches no registry and pushes nothing, so there's nothing to trust
yet except "did the JSON look sane."

## 4. Real build — capture everything Pokkum claims happened

```bash
./pokkum-test build . --output=json 2>build.log | tee build-result.json
```

Keep `build.log` (stderr, structured logs) and `build-result.json` (the
JSON result envelope) — you'll cross-check specific claims from both
against independent tooling in the steps below.

```bash
DIGEST=$(jq -r '.data.digest' build-result.json)
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
  yourself, cross-check against the 5-layer architecture Pokkum's docs
  describe (base, Bun runtime, supervisor, app server/client, vendor,
  native — depending on `--strategy`).
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
(see [for-users.md](for-users.md)), not a bug, but worth confirming it's
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

```bash
cosign verify-attestation --type slsaprovenance \
  --certificate-identity-regexp '.*' --certificate-oidc-issuer-regexp '.*' \
  "$POKKUM_DOCKER_REPO@$DIGEST" 2>/dev/null | jq '.payload | @base64d | fromjson'
```
(Adjust `--certificate-identity`/`--certificate-oidc-issuer` to your actual
signing identity if you're not using keyless-anything-goes verification for
this check — tightening those two flags to your real CI identity is itself
worth doing once, so you know what "verified" should mean going forward.)

**Verify independently:** read the decoded predicate. Confirm
`builder.id`, `invocation.configSource` (or equivalent), and the Go/Bun
toolchain versions recorded match reality — not just that the attestation
exists and cosign didn't error.

## 9. Cosign signature — verify with the `cosign` CLI directly, not `pokkum verify`

```bash
cosign verify --certificate-identity-regexp '.*' --certificate-oidc-issuer-regexp '.*' \
  "$POKKUM_DOCKER_REPO@$DIGEST" 2>&1 | tail -20
```

**Verify independently:** this must come back with a real, cryptographic
"Verified OK" from the `cosign` binary itself — a tool this project didn't
write. If it fails, don't fall back to trusting `pokkum verify`'s own
report of the same thing; the point of this step is having a second,
independently-implemented opinion.

## 10. Base image signature — confirm which mode actually ran, don't assume

```bash
./pokkum-test build . --base distroless --print-manifest --log-level=DEBUG 2>&1 | grep -i "keyless\|signature\|verif"
```

**Verify independently:** the log should explicitly say keyless
verification ran (Fulcio + Rekor) for the `distroless`/`chainguard`
presets, not just "base image resolved" with no mention of signature
checking. If you're on a `custom` base, confirm it explicitly names
static-key mode instead and that `POKKUM_BASE_IMAGE_PUBKEY` (or the
embedded default) is what it actually checked against — don't assume
verification ran just because the build didn't error; also try
`--no-verify-base` once on a scratch build and confirm the *absence* of
those log lines, so you know what "not verified" looks like too and can
tell the difference.

## 11. Runtime verification — does the image actually run?

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

## 12. Reproducibility — diff two independent builds yourself

```bash
DIGEST1=$(./pokkum-test build . --tarball /tmp/build1.tar --output=json | jq -r '.data.digest')
DIGEST2=$(./pokkum-test build . --tarball /tmp/build2.tar --output=json | jq -r '.data.digest')
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

## 13. Kubernetes manifest resolution — read the generated YAML, don't skim it

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

## 14. Rollback — prove the round-trip, don't just run it once

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

## 15. `pokkum scan` — know what it does and doesn't cover today

```bash
./pokkum-test scan "$POKKUM_DOCKER_REPO@$DIGEST" --output=json | jq .
```

**Important, verified fact about the current implementation:** as of this
writing, `pokkum scan <image-or-tarball>` does **not** actually pull or
inspect the image — it returns the same small, hardcoded Bun/SvelteKit
advisory list regardless of what image you point it at. It does not catch
OS-package CVEs (e.g. a `libssl3` CVE in the base image) — see
[Roadmap.md](Roadmap.md)'s "Next Best Steps" section. **If OS-level CVEs
in your base image are what you actually care about — and if that's why
you're testing this, they probably are — don't rely on `pokkum scan` for
that yet.** Cross-check independently instead:

```bash
grype "$POKKUM_DOCKER_REPO@$DIGEST"          # if you installed it in step 0
# or
docker scout cves "$POKKUM_DOCKER_REPO@$DIGEST"   # Docker Desktop's built-in scanner
```

`pokkum scan --toolchain` (Bun/SvelteKit version advisories via OSV.dev) is
real and does work as documented — the gap is specifically the
image/tarball OS-package path.

## 16. Cleanup

```bash
docker rmi "$POKKUM_DOCKER_REPO@$DIGEST" 2>/dev/null
rm -f /tmp/build1.tar /tmp/build2.tar /tmp/manifest.json build-result.json build.log resolved.yaml
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
| SLSA provenance real | `cosign verify-attestation` | 8 |
| Image signature real | `cosign verify` | 9 |
| Base image verification ran | debug logs, explicit mode check | 10 |
| App actually works | `curl` against a running container | 11 |
| Build is reproducible | manual two-build digest + tarball diff | 12 |
| K8s manifests correct | read the YAML, `kubectl --dry-run` | 13 |
| Rollback round-trips | manual toggle-twice check | 14 |
| CVE scanning covers what you need | `grype`/`docker scout`, not `pokkum scan` alone (yet) | 15 |

If every row above checks out via the independent tool, not just Pokkum's
own exit code, you have real evidence — not just Pokkum's word for it.
