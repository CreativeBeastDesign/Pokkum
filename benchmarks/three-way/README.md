# Three ways to containerise the same SvelteKit app

One app. Three builds. Same source tree, same machine, same measuring tape.

The point is not that Pokkum wins — you can read the script and check that yourself, which is the whole idea. The point is to make the trade-off **concrete**, because "distroless, reproducible, SBOM" means very little until it is a number next to another number.

```bash
./run.sh
```

That builds all three variants, measures them identically, and writes a markdown table to `results/results.md` that you can paste anywhere. It takes a few minutes, most of it the CVE scan (`--no-scan` skips it).

## What it looked like on one machine

Measured on an M-series Mac, 2026-09-01, with trivy. **These are one data point, not a specification** — run it yourself and you will get different numbers.

| | Naive Dockerfile | Tuned multi-stage | Pokkum |
|---|---|---|---|
| Image size (MB) | 1138.5 | 165.3 | **137.2** |
| OS packages | 413 | 19 | **11** |
| HIGH+CRITICAL CVEs | 479 | 13 | **0** |
| Shell in the image | yes | yes | **no** |
| Reproducible build | no (by construction) | no (by construction) | **yes** |
| SBOM | no | no | yes |
| SLSA provenance | no | no | yes |
| Lines of build config you maintain | 8 | 18 | **0** |

The two rows worth dwelling on are not the size one. **OS packages** is the number that predicts next year: 413 packages is 413 things that can get a CVE, and the naive image's 479 HIGH+CRITICAL findings are almost all in an OS nobody chose deliberately — they came with `FROM node:22`. And **shell in the image** is the difference between an RCE that gets an interactive prompt and one that does not.

The tuned Dockerfile does most of the work: 1138MB to 165MB, 479 CVEs to 13. That is the honest comparison, and it is why `Dockerfile.tuned` exists rather than only the naive one. What it cannot do — because alpine *is* a shell and a package manager — is reach zero.

## What gets built

| Variant | What it represents |
|---|---|
| [`Dockerfile.naive`](Dockerfile.naive) | The Dockerfile most people write first — the shape in the majority of SvelteKit deployment blog posts. It works fine; the question is what it costs. |
| [`Dockerfile.tuned`](Dockerfile.tuned) | The Dockerfile someone who knows what they're doing writes. Multi-stage, alpine, production-only deps, non-root, `tini` as PID 1. **This is the fair comparison.** |
| `pokkum build` | No Dockerfile at all. |

All three build [`app/`](app) — a small but not trivial SvelteKit app: one SSR route, one prerendered route, an API endpoint, and a real npm dependency, so the image genuinely has to ship `node_modules` content rather than only static files.

## What gets measured

| Measurement | How | Why it matters |
|---|---|---|
| Image size | `docker image inspect` | The boring one everybody quotes. |
| OS packages | trivy | Every package is a thing that can get a CVE next Tuesday. This is the number that predicts your future, more than today's CVE count does. |
| HIGH+CRITICAL CVEs | trivy or grype | Today's snapshot. |
| Shell in the image | `docker run --entrypoint /bin/sh` | Whether someone who lands RCE finds a shell waiting for them. |
| Reproducible build | two independent Pokkum builds, index digests compared | Whether "the image built from commit `abc123`" is a thing that has one answer. |
| SBOM / provenance | present or not | |
| Lines of build config you maintain | `grep -cv '^#'` | The one nobody counts. It is not zero-cost to own a Dockerfile per project, forever. |

## Methodology, and where it is unfair

Stated openly, because a benchmark whose limitations you have to discover yourself is marketing:

- **The scanner is deliberately not Pokkum's.** Pokkum ships `pokkum scan`, and a comparison Pokkum wins using its own scanner would be worth nothing. `run.sh` uses trivy or grype and refuses to substitute its own when neither is installed — the CVE columns read `n/a`, never `0`. "Not measured" and "measured, found nothing" are different facts.
- **The Dockerfile variants are not built twice.** Their reproducibility row says "no (by construction)", not "measured no". `npm install`/`npm ci` resolve against the network at build time and the image is stamped with the wall clock; spending two more builds to confirm a known answer would be theatre. If you disagree, build them twice and compare — the row is labelled so you know it wasn't measured.
- **`SOURCE_DATE_EPOCH` is pinned for all three.** Only one of them uses it. That is the point, but it is fixed for everybody so nothing is being handicapped.
- **SvelteKit's `kit.version.name` is pinned in [`app/svelte.config.js`](app/svelte.config.js).** Left at its default of `Date.now()`, it renames every hashed client chunk on every build and no packaging tool could rescue any of the three. Pinning it for all three keeps the reproducibility row about the packaging step, which is the thing under comparison.
- **Sizes are local Docker sizes, not compressed registry sizes.** Consistent across all three, but not the number your registry bill sees.
- **One app is one data point.** A larger app with native addons or a big `static/` tree shifts every row. Point `run.sh` at your own app — change the path in the three build commands — and the numbers will be more useful to you than these are.
- **The Pokkum row uses a warm-up build.** The first build in a tree with no `pokkum.lock` records the base image in the image config by its **tag**; every later build, reading the lockfile, records it by **digest**. That one annotation cascades into the config, manifest and index digests, so build 1 and build 2 of identical source disagree while builds 2, 3, 4… all agree. That is a real defect in Pokkum, not a measurement artefact — reproduce it with `rm app/pokkum.lock` and two builds — but the row asks "does Pokkum produce the same image twice", and answering it with a first-run-only quirk would misreport steady-state behaviour as the general case. So the benchmark warms past it and says so here rather than quietly getting a nicer number.
- **Don't read the `revision` annotation across a commit.** Pokkum stamps the surrounding repository's git commit into `org.opencontainers.image.revision`, so two builds straddling a commit differ for that reason alone. That is correct behaviour. The two measured builds run back to back, so it cannot affect them — but it will bite you if you compare builds by hand across a `git commit`.
- **Cold-start time is not measured.** It varies more with your host than with the image, and a number that noisy would be worse than no number.

## Running it against your own app

Replace `app/` with your project, or edit the three build invocations in `run.sh` to point elsewhere. The two Dockerfiles assume `npm` and `@sveltejs/adapter-node`; Pokkum handles an `adapter-node` project as-is, without modifying your source tree, which is why all three can share one directory in the first place.

## Requirements

- `docker`
- `pokkum` ([install](https://github.com/CreativeBeastDesign/pokkum#installation--setup))
- `trivy` or `grype`, optional but it is the most interesting column
- `jq`, optional (there is a `grep` fallback)
