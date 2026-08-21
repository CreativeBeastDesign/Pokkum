# Handoff: testing Pokkum against a real SvelteKit project

A brief for a **fresh session** (or a human) with no prior context, pointed at a real
SvelteKit project. It exists because Pokkum's own test suite cannot answer the only
question that matters — *does this work on somebody's actual app* — and because the
project's history says plainly that running things finds what reading them cannot: a
real `--strategy=static` boot test once surfaced six independent bugs in one sitting.

Pair this with [`paranoid-testing-guide.md`](paranoid-testing-guide.md), which already
covers 25 areas in depth. **This document is the driver and the gap list, not a
replacement.** Do not duplicate its sections.

---

## 0. Ground rules

Read these before running anything. They are not boilerplate; several exist because
something went wrong.

1. **Never modify the user's project.** Pokkum's core invariant is zero mutation of user
   source (`CLAUDE.md` §2). If a test seems to need an edit, copy the project to a temp
   directory and work there. A stray file in their repo is a bug you introduced.
2. **Ask before anything outward-facing.** Pushing to a registry the user did not
   nominate, publishing, deleting a release or tag, `npm publish` — stop and ask. A local
   registry or `--local` is almost always sufficient.
3. **Scope to what was asked.** If the user asks for one area, test that area and report.
   Do not run all 25 guide sections unbidden — they may want to run the rest themselves.
4. **A green command is not a passing test.** Read the output. Ask what it would print if
   the feature did nothing at all, and whether you could tell the difference. Several
   incidents in `Lessons.md` are exactly this shape.
5. **Report what you actually observed**, with the command and its real output. If you
   could not test something, say so explicitly rather than omitting it.

---

## 1. Environment inventory (do this first, report it)

Findings are only interpretable against a known environment.

```bash
pokkum --version            # or: go build -o /tmp/pk ./cmd/pokkum
node --version; bun --version
docker version --format '{{.Server.Version}}'   # needed only for --local and Kind
kind get clusters; kubectl config current-context
cosign version              # independent verification; >= v3.1.0 for --use-signing-config
```

Then record: the project's SvelteKit and adapter versions, its `svelte.config.js`
adapter, and whether it prerenders. `--runtime=node` supports `--strategy=layered`
**only** — check that before planning runtime tests.

---

## 2. Drive the existing guide

`paranoid-testing-guide.md` covers, with independent verification steps:
preflight, first build, image inspection, OCI annotations, SBOM, SLSA provenance, cosign
signatures, base-image verification, Sigstore trust root, runtime boot, reproducibility,
`pokkum history`/`explain`/`adopt`/`doctor`, `--require-env`, `--mirror-registry` (on `pokkum base update`, not `build`),
`--static`, and `--runtime=node`.

Execute it **as written**, including its `jq` and `cosign` invocations. When a documented
command fails, root-cause the *documented claim* rather than patching an errata line —
that discipline previously found four real issues in one session, one of which was in the
guide itself.

---

## 3. Gaps the guide does not cover

Each item below is untested territory. Commands use flags verified against
`pokkum build --help`.

### 3.1 `--local` (the only path needing a Docker daemon)

Pokkum's pitch is "no daemon"; `--local` is the documented exception, and nothing tests it.

```bash
pokkum build --local -t local-test .
docker images | grep local-test
docker run --rm -p 3000:3000 -e POKKUM_PROBE_PORT=8081 <image>   # then curl /healthz, /readyz
```

Expect a warning that daemon load drops OCI annotations, naming the dropped
`pokkum.dev/*` keys. Confirm the warning names them rather than saying "some".

### 3.2 Secret guard (new, and security-relevant)

```bash
pokkum build --local .                       # on a project containing a fake secret
pokkum build --local --show-secret-values .  # values revealed instead of redacted
```

Check: each finding reports **file, line and column**; values are redacted by default;
the message names both remedies (the inline `pokkum:allow-secret` marker and
`security.allow_secret_patterns`). Then verify the marker works — add
`// pokkum:allow-secret` on or directly above the offending line and confirm that finding
alone disappears while others remain.

Known gap, do not re-report: unquoted assignments (`.env` style `KEY=value`, `.npmrc`
`_authToken=`) are **not** matched — only quoted values are.

### 3.3 Hermetic builds

```bash
pokkum build --hermetic --local .
pokkum build --hermetic --hermetic-mount-isolation --local .   # Linux only
```

Mount isolation needs privileges most machines withhold; on macOS it is unavailable
entirely. If it refuses, that is expected — report the message, not a failure.

### 3.4 Asset overlay and precompression

`--asset-overlay-from` with explicit refs; check precompressed sidecars
(`.br`/`.gz`) appear beside assets and that a second build does not rewrite them.

### 3.5 `pokkum upgrade`

```bash
pokkum upgrade --check          # must not modify anything
pokkum upgrade --offline        # must fail closed, not silently no-op
```

This path verifies a cosign signature over the release binary. Confirm an unsigned or
tampered payload is refused, and that "no signature" and "bad signature" read differently.

---

## 4. Live Kubernetes with Kind

The guide stops at `kubectl --dry-run`. With a Kind cluster the whole path is testable.

```bash
kind create cluster --name pokkum-test
pokkum build --local -t k8s-test .
kind load docker-image <image> --name pokkum-test        # no registry needed
pokkum resolve -f deployment.yaml --security-context --network-policy --resource-defaults
pokkum apply -f deployment.yaml
kubectl get pods,networkpolicy,poddisruptionbudget
```

Verify against the cluster, not the YAML Pokkum printed:

```bash
kubectl get deploy <name> -o jsonpath='{.spec.template.spec.securityContext}'
kubectl get deploy <name> -o jsonpath='{.spec.template.spec.containers[0].image}'   # digest-pinned?
kubectl logs deploy/<name> -c <container>            # PID 1 supervisor output
kubectl exec deploy/<name> -- /pokkum/init --version 2>/dev/null || true
```

Then `pokkum rollback` and confirm the cluster returns to the previous revision — checked
with `kubectl rollout history`, not Pokkum's own claim.

Expect `runAsNonRoot: true`, `seccompProfile: RuntimeDefault`,
`allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, plus a NetworkPolicy and a
PodDisruptionBudget.

---

## 5. Reporting

For each finding:

- the exact command and its real output (trimmed, not paraphrased);
- what you expected and why (cite the flag's help text, a doc line, or a code path);
- whether it is already known — check `docs/Roadmap.md` open items and `Lessons.md`
  before reporting anything as new;
- severity in terms of user impact, not internals.

Say explicitly what you did **not** test and why. An unqualified "everything passed" is
not a useful report, because it hides which of the 25+ areas actually ran.

---

## 6. Cleanup

```bash
kind delete cluster --name pokkum-test
docker rmi <test images>
```

Remove any temp copies of the user's project. Confirm their working tree is untouched
(`git status` in *their* repo, not Pokkum's).
