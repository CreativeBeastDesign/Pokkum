# Pokkum Repro Doctor Concept: `pokkum repro doctor`

Status: PROPOSAL — companion to `pokkum-verify-concept.md` (shares the layer-diff machinery, verify M3)

## 1. What & Why

README known-limit, verbatim: *"Without this, builds are **not** reproducible, and nothing will warn you — the images simply differ."* Reproducibility is Pokkum's core promise and its most fragile property: one unpinned `kit.version.name`, one dependency with a sloppy postinstall, one adapter regression, and the promise silently breaks — taking `pokkum verify`, digest-based idempotent pushes, and layer caching down with it.

`pokkum repro doctor` turns "the digests differ, good luck" into "**stage 2 diverges: `client/_app/version.json` contains a build timestamp → pin `kit.version.name`, see README**".

```bash
pokkum repro doctor ./my-app                 # static checks + double build + bisect + explain
pokkum repro doctor ./my-app --fast          # static checks only, no build (seconds)
pokkum repro doctor ./my-app --against a.tar # compare one fresh build against an existing artifact
pokkum repro doctor ./my-app --perturb       # also vary build path/env to catch machine-dependence
```

Exit codes: `0` reproducible, `1` not reproducible (with diagnosis), `2` could not complete. Same discipline as verify: never conflate 1 and 2.

## 2. The Core Design: Stage-Level Bisection

**The key insight — and the thing naive implementations get wrong:** diffing two final images tells you *that* they differ, not *why*. "Layer 1 diffID mismatch" on a ~90 MB `bun build --compile` binary is unactionable; binary-diffing a compiled Bun executable is noise. The fix is to snapshot **every intermediate stage of the pipeline** and find the *first* stage where the two builds diverge — divergence at stage N with identical stage N−1 inputs localizes the cause to exactly one pipeline step.

Stage snapshot points (mirroring `internal/core/pipeline.go` / ARCHITECTURE §2):

- **S1 — Adapter output**: content-hash tree of the SvelteKit build dir (`bun run build` output), per file.
- **S2 — Post-stabilization**: same tree after Pokkum's determinism fixes (asset sorting, `SOURCE_DATE_EPOCH` application).
- **S3 — Compiled artifacts**: SHA-256 of each per-platform binary (exe path) or bundle output files (layered path).
- **S4 — Layer tars**: uncompressed diffIDs per layer per platform.
- **S5 — Compressed blobs**: gzip'd layer digests.
- **S6 — Config & manifests**: image config JSON, per-platform manifests, index.

Divergence table → diagnosis domain:

| First divergent stage | Meaning | Typical cause |
|---|---|---|
| S1 | App/toolchain build is non-deterministic | `Date.now()` in config, `Math.random()` at build time, unpinned `kit.version.name`, non-deterministic postinstall output, Vite plugin misbehavior |
| S2 (S1 identical) | Pokkum's own stabilization is non-deterministic | Bug in Pokkum (sort fix regressed, epoch not applied) |
| S3 (S2 identical) | Compiler non-determinism | Bun `--compile` embedding something (outfile name variance is already handled — a new one means Bun regression) |
| S4 (S3 identical) | Packager non-determinism | Tar header bug in `internal/adapters/packager` (mtime/uid/order) |
| S5 (S4 identical) | Compression non-determinism | gzip settings drift — should be impossible with zeroed headers; if seen, Pokkum bug or Go version changed mid-run |
| S6 (S5 identical) | Metadata non-determinism | Timestamp/annotation leaking into config or manifest |

Implementation: a `ports.StageRecorder` interface the pipeline calls at each snapshot point (no-op in normal builds, recording in doctor/verify runs). This keeps the instrumentation out of the domain logic and reuses the pipeline verbatim — the doctor must run the *real* pipeline, not a parallel reimplementation that could itself diverge.

## 3. Diagnosis Phases

### Phase 0 — Static checks (`--fast`, runs always)

No build; catches the top offenders in seconds:

- `kit.version.name` pinned to `SOURCE_DATE_EPOCH`? (the #1 footgun; note the virtual-config injector already pins it when it controls the config — check the *effective* config, not just the file)
- Git repo present + at least one commit (else `SOURCE_DATE_EPOCH` derivation fails/falls back)? Dirty working tree warning (uncommitted changes make "reproducible from commit X" claims meaningless).
- Lockfile present and committed?
- Known non-deterministic packages in the dependency tree (curated list: packages with timestamp-embedding postinstalls or build-time randomness).
- Direct source scan for build-time `Date.now()` / `new Date()` / `Math.random()` in `svelte.config.js`, `vite.config.*`, and hooks files (cheap regex-level scan, advisory only).
- `.pokkumignore` sanity (source maps excluded, `.env*` excluded).

### Phase 1 — Double build + stage compare

Build twice into **fresh, separate temp directories with cold caches** (see pitfall 2), record all stages, produce the first-divergence verdict. No pushes; tarball outputs are discarded unless `--keep`.

### Phase 2 — File-level diff at the divergent stage

For the first divergent stage, diff the actual content (tree diff for S1/S2, tar-entry diff for S4 — header fields *and* content separately, so "same bytes, different mtime" is distinguished from "different bytes"). This is the shared `internal/adapters/layerdiff` component from verify M3.

### Phase 3 — Cause heuristics

Table-driven rules over the file-level diff, each with: matcher, confidence, explanation, fix, doc link. Initial catalog:

- `version.json` content differs → unpinned `kit.version.name` → pin it (README recipe).
- Differing 10/13-digit integers near the build wall-clock time → embedded timestamp → identify the emitting file/package.
- Absolute paths containing the temp build dir / `$HOME` → path embedding → report the package whose output contains it (this is also what `--perturb` exists to force out).
- Same file *set*, different *order* (S1, or tar entry order in S4) → iteration-order bug → if in `assets.generated.ts`: adapter regression (Pokkum's sort fix should have caught it — fail loudly per existing policy); if in Pokkum's tar: packager bug.
- Tar headers differ but content identical → mtime/uid/mode not pinned → packager bug or epoch not propagated.
- High-entropy differing strings of equal length → random IDs (UUIDs, nonces) generated at build time → locate emitting module.
- Only S5 differs → gzip framing → check Go version consistency (and point at verify L2 semantics).

Unmatched diffs render as a plain file-level report — the heuristics are an explanation layer, never a filter that hides raw evidence.

## 4. `--perturb` Mode (catching machine-dependence)

A same-machine double build **cannot** detect environment embedding — both builds see the same `$HOME`, path, TZ, and locale, so embedded values are identical and digests match, then break on the CI machine. Reprotest-style variations, applied to the second build only:

- Different build/temp directory path (catches path embedding — the common one).
- Scrubbed/altered non-allowlisted env vars (catches env inlining; complements the Secret-Inlining Guard).
- `TZ` and `LANG`/`LC_ALL` changed (catches locale-dependent sorting/formatting — note `sort` collation differences are a classic).

Not in v1: filesystem readdir-order variation (needs a shim FS; Linux-only via FUSE — disproportionate) and clock variation (`SOURCE_DATE_EPOCH` pinning should make wall-clock irrelevant; a differing result under normal runs already exposes clock reliance).

## 5. Pitfalls & Gotchas

1. **Diffing the compiled binary is a dead end.** An S3 divergence with identical S2 must be reported as "Bun compile step is non-deterministic (bug — report upstream with these inputs)", not as a 90 MB hex diff. Invest in stage bisection, not binary diffing. (On the layered path from `pokkum-layer-caching-concept.md`, S3 outputs are JS text — diffable — one more quiet advantage of that design.)
2. **Caches can mask or cause non-determinism.** Vite/`.svelte-kit` caches shared between the two builds can make a non-deterministic build *look* stable (second build reuses first build's artifacts). Both builds need isolated project copies with cold caches. Corollary: copying the project must preserve `node_modules` determinism — run `bun install --frozen-lockfile` per copy rather than copying `node_modules` (slower, honest) with a `--reuse-install` opt-out.
3. **Double-build ≠ cross-machine reproducibility.** Be explicit in the report: a clean doctor run without `--perturb` proves *local* stability only. Print which claim was tested. Otherwise users will file "doctor passed but CI digest differs" bugs — which is precisely the path-embedding case `--perturb` exists for.
4. **The doctor must not have its own determinism bugs.** Report ordering, map iteration, temp naming — all sorted/stable, or diagnosing the diagnoser becomes a rabbit hole. Same discipline as the packager.
5. **Timestamp heuristic false positives.** Any 10-digit number near the epoch (content hash fragments, sizes) can look like a timestamp. Require corroboration: the value must differ between the two builds AND both values must fall within each build's wall-clock window. Confidence-label every heuristic hit.
6. **Minified one-line JS diffs are unreadable.** Don't try to pretty-print/diff minified bundles semantically in v1; report byte ranges + the nearest identifiable module path (bundle comments/sourcemap file names if present). Building with sourcemaps "just for diagnosis" changes the artifact under test — never alter build inputs between doctor and real builds.
7. **Memory on big asset trees.** Stream tar comparison entry-by-entry (both tars are sorted — a linear merge-walk works); never load layers whole. SvelteKit `client/` dirs can be GBs on media-heavy sites.
8. **`--against` semantics.** Comparing a fresh build against an old tarball conflates "non-deterministic" with "source/toolchain changed since". Require a git ref for the fresh build, warn on dirty tree, and print toolchain versions of both sides (the tarball side comes from its embedded provenance/annotations if present — M0 of the verify concept pays off here too).
9. **Runtime cost honesty.** Full doctor = 2× build (~2× `bun build --compile` per platform on the exe path). Default to a single platform (`--platform linux/amd64`) for doctor runs — cross-platform non-determinism is almost always platform-symmetric — with `--all-platforms` for the paranoid.
10. **Phase-0-only green is not a guarantee.** `--fast` must exit 0 with an explicit "static checks only — run a full doctor to test the build" note, or users will treat it as certification.

## 6. Report Sketch

```
pokkum repro doctor ./my-app

Static checks     kit.version.name pinned ✓   lockfile committed ✓   git clean ✓
                  dependency scan: 1 advisory (node-sass: known non-deterministic postinstall)

Build A           2m14s   Build B   2m11s   (isolated dirs, cold caches, platform linux/amd64)

Stages            S1 adapter output      DIVERGED (1 file)
                  S2–S6                  (not evaluated — bisection stops at first divergence)

Diagnosis         client/_app/version.json
                    A: {"version":"1754923312021"}
                    B: {"version":"1754923391847"}
                  Cause (high confidence): SvelteKit kit.version.name defaulting to Date.now()
                  → the effective config pin was bypassed: svelte.config.js exports a function,
                    which the virtual-config injector cannot transform. Pin manually (README
                    "Reproducibility requires one line").

VERDICT           NOT REPRODUCIBLE (1 cause, high confidence)   [local-stability test; --perturb not run]
```

JSON output mirrors it (same versioned schema family as verify / `--output=json`).

## 7. Implementation Plan

- **M1 — Stage recorder + bisection core.** `ports.StageRecorder` + pipeline instrumentation at S1–S6; `internal/core/repro.go` double-build orchestration (isolated copies, cold caches); first-divergence verdict with raw hash table output. No heuristics yet — this alone is already actionable ("S4 diverged: packager").
- **M2 — `layerdiff` + Phase-2 file diffs.** Build `internal/adapters/layerdiff` (tree diff, tar merge-walk diff separating header vs content) — shared deliverable with `pokkum verify` M3; wire into the divergent-stage report.
- **M3 — Heuristics catalog + static checks.** Phase 0 checks and the Phase 3 rule table (each rule: fixture + test, see below); `--fast` mode.
- **M4 — `--perturb`, `--against`, JSON output, docs.** Perturbation harness (path/env/TZ), tarball comparison mode, `--output=json`, README section replacing the bare "nothing will warn you" caveat with "run `pokkum repro doctor`".

### Test strategy

Fixtures with *seeded* non-determinism, each asserting the exact diagnosis:

- `fixture-unpinned-version`: no version pin → S1 divergence, `version.json` rule fires.
- `fixture-datenow-config`: `Date.now()` in a vite plugin → S1, timestamp rule.
- `fixture-path-embed`: plugin writing `process.cwd()` into output → passes plain doctor, fails under `--perturb` (this test guards pitfall 3).
- `fixture-packager-regression`: unit-level — feed the packager an unsorted file map, assert S4 rule.
- Green path: `testdata/fixtures/sveltekit-basic` → exit 0 (this doubles as the reproducibility e2e already in `tests/integration/reproducibility_e2e_test.go` — consolidate rather than duplicate).

## 8. Scope Cuts for v1

- Single platform by default; `--all-platforms` opt-in.
- No FS-ordering or clock perturbation.
- No semantic diffing of minified bundles.
- Heuristics limited to the curated catalog; everything else falls through to the raw file-level report.
