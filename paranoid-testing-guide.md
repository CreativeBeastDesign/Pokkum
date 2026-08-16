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

**Note:** this step used to warrant a "confirmed finding" banner because
`pokkum verify --no-rebuild`'s provenance summary came from a stub that
returned identical hardcoded data (signer identity, signature validity,
SLSA presence) regardless of the image you pointed it at. That stub is
gone: `pokkum verify --no-rebuild` now drives a real resolver
(`internal/adapters/provenance/resolver.go`, Roadmap Tier 0) that pulls the
remote manifest, verifies the Cosign signature (static-key or keyless),
extracts and verifies the in-toto DSSE envelope and SLSA v1.0 statement, and
enforces `--expect-source`. The "believe nothing" rule still applies — this
move from stub to real logic is exactly the class of fix this guide exists
to double-check — so do not trust `pokkum verify`'s own summary alone for
this step; cross-check it against `cosign` directly, as below.

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

## 15. `pokkum scan` — now real; verify it against a package you know is vulnerable

`pokkum scan` was fixed to genuinely pull and inspect the target (real
Syft-based enumeration + real OSV.dev queries) rather than return a
hardcoded advisory list — but "believe nothing" applies to the fix itself
just as much as to the original claim. Don't just run it and check the
exit code; make it prove it against something you know the answer to.

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

## 16. `--registry-config` — verify the credential helper actually gets invoked

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

## 17. `pokkum adopt` — verify the detection gate and that nothing gets mutated you didn't ask for

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

## 18. `pokkum history` — confirm it reports THIS image's real data, not a template

```bash
./pokkum-test history "$POKKUM_DOCKER_REPO@$DIGEST" --output=json | jq .
```

**Verify independently:** the single most important check here is that the
output actually varies per image — a hardcoded stub would look identical
for every ref. Build and push a second, distinguishable image (different
commit, different tag) and confirm the two `history` outputs differ:
```bash
git commit --allow-empty -m "second commit for history diff test"
DIGEST2=$(./pokkum-test build . --output=json | jq -r '.data.digest')
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

## 19. Multi-generation rollback — prove it survives more than one hop

```bash
# Deploy three times in a row against the same manifest to build real history:
for i in 1 2 3; do
  ./pokkum-test resolve -f deployment.yaml > /tmp/resolved-$i.yaml
  cp /tmp/resolved-$i.yaml deployment.yaml   # persist annotations forward, per fixes-to-v1.md's documented workflow requirement
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

## 20. Runtime Env Contract (`--require-env`) — confirm it actually fails fast at startup

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

## 21. Base Image Escrow / Mirroring (`--mirror-registry`)

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

## 22. Static / Prerendered (`--static`) — verify the zero-JS image end-to-end

This exercises the `--static` build path (Roadmap §1): a purely prerendered
SvelteKit site compiled onto a libc-free `chainguard/static` base and served
by Pokkum's own embedded `pokkum-static` Go PID-1 file server — **no Bun
runtime, no compiled executable, no separate supervisor**. "Believe nothing"
matters extra here because the HTTP-serving layer is Pokkum's own code, not
an off-the-shelf nginx, and this is the newest surface in the codebase.

### 22a. Preflight — make the CLI tell you what `--static` actually did

```bash
# 1. The shorthand must map to the static strategy and reject the conflicting one.
./pokkum-test build . --static --strategy=exe --print-manifest 2>&1 | head -5
#      → expect a hard error: "--static cannot be combined with --strategy=exe"

# 2. With no --base/--hardened, --static must pick the libc-free static base.
./pokkum-test build . --static --print-manifest --output=json 2>/dev/null | jq '.data.base' 
#      → expect cgr.dev/chainguard/static (or its resolved digest), NOT distroless cc-debian12
```

**Verify independently:** don't trust the flag description — confirm the base
ref *changed* from what a default `layered` build emits, and confirm the error
in (1) really is an exit-code failure, not a printed warning.

### 22b. Build a genuinely prerendered app

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
./pokkum-test build /path/to/static-app --static --tag static-v1 --output=json 2>static.log | tee static.json
```

If the app has an unprerendered (SSR-only) route, `--static` should **fail the
build** — that is correct guarded behavior, not a bug. If it unexpectedly
succeeds on an SSR-only route, you've found a gap.

### 22c. Inspect the image independently — this is where "no Bun, no supervisor" is proven

```bash
# Pull the exact built digest (read it from static.json, don't trust memory).
DIGEST=$(jq -r '.data.digest // .data.image.digest // .data.image' static.json | sed 's/.*://')
IMAGE="$POKKUM_DOCKER_REPO@$DIGEST"
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
name `/pokkum/static` (not `/pokkum/init`, not `bun`). A lack of the Bun
layer in `docker history` is the single most important "it's really static"
signal.

### 22d. Runtime — prove `pokkum-static` serves correctly

```bash
crane pull "$IMAGE" /tmp/static-img.tar >/dev/null 2>&1
docker run -d --rm -p 3000:3000 -p 8081:8081 --name pokkum-static-test "$IMAGE"
sleep 2

BASE=http://127.0.0.1:3000

# 1. Index serves and is HTML
curl -s -D- "$BASE/" -o /tmp/static-index.html | head -1
grep -i '<html' /tmp/static-index.html

# 2. Probe endpoints respond (pokkum-static doubles as the probe server)
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8081/healthz   # → 200
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8081/readyz    # → 200

# 3. Range request → 206 with correct Content-Range
curl -s -D- -H 'Range: bytes=0-9' "$BASE/" -o /tmp/range.bin | grep -Ei 'HTTP/|content-range'

# 4. Strong ETag present and usable for 304
ETAG=$(curl -s -D- -o /dev/null "$BASE/" | grep -i '^etag:' | tr -d '\r' | awk '{print $2}')
curl -s -o /dev/null -w '%{http_code}\n' -H "If-None-Match: $ETAG" "$BASE/"   # → 304

# 5. Content-Encoding: request gzip and confirm the served bytes are gzip.
#    The build pre-compresses /app/client assets to .gz/.br/.zst; the server
#    must hand back the sidecar, not compress on the fly.
curl -s -D- -H 'Accept-Encoding: gzip' "$BASE/" -o /tmp/served.bin | grep -i 'content-encoding'
file /tmp/served.bin        # → 'gzip compressed data', and NOT the on-the-fly variant

# 6. Unknown route → 404 (pure static site has no fallback unless configured)
curl -s -o /dev/null -w '%{http_code}\n' "$BASE/does-not-exist"          # → 404

docker stop pokkum-static-test
```

**Verify independently:** don't trust `curl` exit 0 on its own — read the actual
`Content-Range`, `ETag`, and `Content-Encoding` **headers** you printed above,
and `file` the served body so you know you really got gzip bytes and not the
plain file. A missing/garbled `Content-Encoding` or a `200` where `206`/`304`
was asked is a real failing claim.

### 22e. Reproducibility — static builds must still be bit-for-bit

```bash
./pokkum-test build /path/to/static-app --static --tag static-v1 --output=json 2>/dev/null | jq -r '.data.digest // .data.image' > /tmp/static1.txt
./pokkum-test build /path/to/static-app --static --tag static-v2 --output=json 2>/dev/null | jq -r '.data.digest // .data.image' > /tmp/static2.txt
diff /tmp/static1.txt /tmp/static2.txt && echo "STATIC REPRODUCIBLE"
```

### 22f. Layered prerendered pages (the other half of §1)

For a normal `--strategy=layered` build, prerendered pages now live in their
own `/app/prerendered` layer (Roadmap §1, part 1), and the generated
adapter-node `handler.js` is patched to serve them via `POKKUM_PRERENDERED_DIR`.

```bash
# Build a predominantly-prerendered app with the default layered strategy.
./pokkum-test build /path/to/app --output=json 2>layered.log | tee layered.json
DIGEST=$(jq -r '.data.digest // .data.image.digest // .data.image' layered.json | sed 's/.*://')
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

## 23. Cleanup

```bash
docker rmi "$POKKUM_DOCKER_REPO@$DIGEST" 2>/dev/null
rm -f /tmp/build1.tar /tmp/build2.tar /tmp/manifest.json build-result.json build.log resolved.yaml
rm -f /tmp/regconfig.json /tmp/before.js /tmp/resolved-1.yaml /tmp/resolved-2.yaml /tmp/resolved-3.yaml /tmp/fresh1.yaml /tmp/fresh2.yaml
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
| CVE scan catches a real vulnerability, fails closed on DB outage | vulnerable-dependency fixture, network-outage test | 15 |
| Registry credential helper actually invoked | remove the helper binary, confirm the failure mode | 16 |
| `adopt` refuses non-SvelteKit projects, mutates only what's expected | `git diff`, byte-diff without `--write-config` | 17 |
| `pokkum history` reflects the real image, not a template | two builds, diff their `history` output | 18 |
| Multi-generation rollback survives >1 hop | `-g 2` lands on the right digest, not "some" digest | 19 |
| Runtime Env Contract fails fast, bakes no values | container exit code + `docker inspect` | 20 |
| Base image mirror actually wrote the blob | `crane manifest` against the mirror, not the log line | 21 |
| `--static` really produced a zero-JS image (no Bun/supervisor, static base) | `docker history`, `crane config` entrypoint/env | 22c |
| `pokkum-static` serves correctly (Range/ETag/Content-Encoding/probes/404) | `curl` headers + `file` on the served body | 22d |
| Prerendered pages served as real static HTML from `/app/prerendered` | `curl` + `crane config` env + `docker history` | 22f |

If every row above checks out via the independent tool, not just Pokkum's
own exit code, you have real evidence — not just Pokkum's word for it.
