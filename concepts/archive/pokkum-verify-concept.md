# Pokkum Verify Concept: `pokkum verify --rebuild`

Status: PROPOSAL — most complex of the 2026-08 High-priority features (`docs/archive/AdditionalFeatures.md`)

## 1. What & Why

`pokkum verify` answers: **"does the image in my registry/cluster provably correspond to this git commit?"** — by independently rebuilding from source and comparing digests. Signatures prove *who* pushed an image; rebuild-verification proves *what* it was built from. Only a reproducible builder can offer this; it's Pokkum's structural advantage over every Dockerfile-based tool.

```bash
# Verify a registry image against the commit recorded in its provenance
pokkum verify ghcr.io/example/my-app@sha256:abc...

# Verify what's actually running in the cluster
pokkum verify deployment/my-app -n prod

# Verify against an explicit source ref (no/untrusted provenance)
pokkum verify ghcr.io/example/my-app@sha256:abc... --source ./my-app@a1b2c3d
```

## 2. Trust Model (get this right first)

- The verifier does NOT trust the original builder. That's the point.
- The verifier DOES trust: its own toolchain (its Bun/Pokkum binaries), its git remote (commit content addressed by SHA — git SHA-1 collision risk accepted, same as everyone), and the base image *registry content by digest* (digest is self-verifying).
- **Critical subtlety:** provenance is *attacker-suggestible input*, not trusted data. The SLSA statement tells you which commit and toolchain to rebuild with — if you rebuild from what the attacker's provenance says and it matches, you've proven the image matches *that* commit, which is still the correct, useful claim ("image ⇔ commit X"). What you must NOT do is print "verified ✓" implying commit X is the one the user expects. Always print the resolved commit SHA + repo prominently, and support `--expect-source <repo>@<ref>` for CI to assert it.
- Verify Cosign signature and DSSE envelope on the provenance *before* using it (adapters exist: `internal/adapters/cosign`, `internal/adapters/dsse`) — but treat signature verification as a separate, orthogonal check in the report, not a gate for rebuild.

## 3. Verification Levels (the core design decision)

The naive design — rebuild, compare manifest digest, done — fails structurally on your own documented known-limit: **compressed layer digests depend on Go's `compress/flate` at `gzip.BestSpeed`, which changes across Go releases.** A verifier built with Go 1.24 can legitimately fail to byte-match layers from a Pokkum built with Go 1.23. Design around it with explicit levels:

- **L1 — Exact:** rebuilt OCI index digest == remote digest. Strongest claim; requires identical Pokkum binary (or at least identical Go compress/flate) and identical Bun. Expected in CI where the org pins Pokkum versions.
- **L2 — Semantic:** compare per-platform **image config digests** and **uncompressed layer diffIDs** (`rootfs.diff_ids`). DiffIDs are hashes of uncompressed tar bytes — deterministic regardless of Go version. If all diffIDs + configs match but compressed digests differ, the image content is identical; only the gzip framing differs. Report `VERIFIED (L2 / content-identical)`.
- **L3 — Explained mismatch:** any diffID differs → pull both layer versions, untar, file-level diff, and report *which files* differ (this is 80% shared machinery with `pokkum repro doctor` — build them together).

Exit codes: `0` = L1/L2 verified, `1` = mismatch (with L3 report), `2` = cannot verify (missing provenance/toolchain/inputs). Never conflate 1 and 2.

## 4. Architecture

Hexagonal placement:

- `cmd/pokkum/verify.go` — CLI, flag parsing, report rendering (`--output=json` from day one; CI is the primary consumer).
- `internal/core/verify.go` — orchestration: fetch → attest-check → plan rebuild → rebuild → compare → report. Reuses the existing build pipeline (`internal/core/pipeline.go`) with a **pinned-input override struct** (see §5).
- New port `ports.ProvenanceResolver` — fetches and validates SLSA/DSSE/Cosign artifacts for an image ref (adapter over existing registry + cosign + dsse adapters).
- New port `ports.ImageComparator` — L1/L2/L3 comparison given two image sources (remote ref vs local tarball). Adapter uses `go-containerregistry` for both sides.
- Rebuild output goes to a local OCI tarball (`--tarball` path already exists) — **never push during verify**.
- K8s source resolution (`deployment/foo`) reuses `internal/adapters/k8s` to extract `image: ...@sha256:...` from live manifests via kubectl.

### Data needed from provenance (M0 prerequisite)

Rebuild requires these to be recorded at build time in the SLSA statement (`internal/adapters/slsa/generator.go`). Audit what's already there; add what's missing **now**, because you can only verify images built after the fields exist:

- source repo URI + commit SHA (exists — `resolvedDependencies`)
- base image ref **pinned by digest** (exists per ARCHITECTURE §6 — confirm it's the digest, not the tag)
- Bun version + binary SHA256 (partially — ensure exact version string AND binary hash)
- lockfile SHA256s (exists — `slsa/lockfile.go`)
- Pokkum version + commit, **Go runtime version it was compiled with** (`runtime.Version()`), builder OS/arch
- full effective build config: platforms, base preset, SBOM format, telemetry flags, `.pokkumignore` hash — anything that changes bytes

## 5. Rebuild Procedure

1. **Resolve claim**: fetch remote index + per-platform manifests/configs; fetch + signature-check provenance; extract rebuild inputs. If provenance is absent: require `--source repo@ref` and all pinned inputs derivable from the repo itself, else exit 2.
2. **Toolchain gate**: compare local Bun version/hash against provenance. On mismatch: fail with exit 2 and the exact required version — do NOT silently build with a different Bun (guaranteed false mismatch). Optional `--fetch-toolchain` downloads the pinned Bun (shared machinery with the `bunruntime` adapter from `pokkum-layer-caching-concept.md` §4.2). Compare Go/Pokkum version to *predict* L1 vs L2 up front and say so in the report.
3. **Clean source**: `git worktree add --detach <tmp> <commit>` from a fresh/`--reference` clone. Never build from the user's working tree (dirty state, untracked files, local `.env`). Verify lockfile hashes against provenance **before** install.
4. **Frozen install**: `bun install --frozen-lockfile`. Fail hard if the lockfile would change.
5. **Pinned build**: run the standard pipeline with overrides: base image by provenance digest (not tag), same platforms, same flags, `SOURCE_DATE_EPOCH` from the commit (automatic — derived data), output to tarball, no push, no `--local`.
6. **Compare** (§3) and render the report: resolved commit, repo, toolchain match table, per-layer verdict, final level.

## 6. Pitfalls & Gotchas (ranked by how likely they are to burn you)

1. **Go-version digest drift (structural).** Covered by L2. Do not skip L2 thinking L1 is enough — the first time a user upgrades Pokkum, every old image would "fail" verification. Test explicitly: build a fixture tarball, re-compress its layers with different flate output, assert L2 passes.
2. **Cross-OS builds are NOT verified-reproducible.** `bun run build` executes Vite/SvelteKit on the host; macOS (case-insensitive APFS, different platform-native rollup/esbuild binaries) vs Linux can produce differing output in edge cases (file ordering was already patched once — `assets.generated.ts`). Record builder OS/arch in provenance; when verifier OS ≠ builder OS, warn loudly and treat mismatch as "inconclusive" (exit 2), not "tampered" (exit 1). Recommend Linux-CI-to-Linux-CI as the supported verification path for v1; macOS→Linux verification is a stretch goal.
3. **Unsigned/absent provenance ≠ failure to verify content.** `--source` mode must work without any attestation, or the feature is useless for images built before M0 fields existed. But be exit-code honest: content verified, provenance unverifiable.
4. **Base image by tag would be a time bomb.** If build-time provenance recorded only `distroless/cc-debian12:nonroot` (tag), verification breaks the day Google pushes a new tag. The rebuild must consume the *digest* from provenance, and `pokkum build` must record it. Audit this first — it's a silent M0 blocker.
5. **The verifier's own registry fetch must be digest-addressed end-to-end.** Fetch remote manifests by digest, not tag — the tag can move between "resolve claim" and "compare" (TOCTOU).
6. **`bun install` non-hermeticity.** postinstall scripts can embed timestamps/paths/arch into `node_modules`, which flows into the bundle. Mitigations: lockfile hash gate (already), and document that packages with non-deterministic postinstalls break verification (detect via repro-doctor). Consider `--ignore-scripts` as a build-time option to recommend.
7. **Bun patch drift.** Bun `1.2.18` vs `1.2.19` produce different binaries — exact-match the version, never "compatible range". Also record whether `-baseline` variant was used (different binary → different bytes).
8. **Config drift.** Any flag that changes bytes (`--base`, `--platform`, telemetry injection, SBOM embedded vs attached) must come from provenance, not the verifier's defaults. Easy to miss one and chase phantom mismatches — centralize as a single `PinnedBuildInputs` struct consumed by the pipeline, and make the pipeline *refuse* to run in verify mode with any unpinned input.
9. **SBOM/attestation artifacts must be excluded from comparison scope.** You are comparing the *image index and image manifests*, not signature/SBOM referrer artifacts (which are inherently non-reproducible — signatures embed timestamps). Define the comparison boundary explicitly.
10. **Git worktree hygiene.** Clean up temp worktrees on all error paths (`defer`), or verify runs will litter; and `git worktree add` from a bare/`--filter=blob:none` clone can be slow on large repos — offer `--repo <existing-clone>` reuse with a mandatory `git status --porcelain` clean check.
11. **Concurrent tags/multi-arch partial pulls.** Compare the full index (all platforms built), not just the host's platform, or verification passes on amd64 while arm64 was tampered.
12. **Don't let verify mutate caches used by builds.** Share content-addressed blob caches read-only; a poisoned verify-fetch must not be able to inject blobs a later `pokkum build` would reuse without hash-checking. (Content addressing gives you this — just never store by anything other than verified digest.)

## 7. Report Format (sketch)

```
pokkum verify ghcr.io/example/my-app@sha256:abc123...

Source        github.com/example/my-app @ a1b2c3d  (from provenance, signature: VALID keyless:ci@github)
Toolchain     bun 1.2.18 ✓   pokkum v0.3.1 (local v0.3.2 — expect L2)   builder linux/amd64 ✓
Base image    gcr.io/distroless/cc-debian12@sha256:def... ✓

Rebuild       linux/amd64 ✓   linux/arm64 ✓   (2m41s)

Compare       index digest       MISMATCH (expected under toolchain skew)
              image configs      MATCH (2/2)
              layer diffIDs      MATCH (6/6)

VERDICT       VERIFIED (L2 — content-identical; compressed-layer framing differs: Go 1.23 vs 1.24)
```

JSON variant mirrors this structure 1:1 (stable, versioned schema — same schema family as the `--output=json` build results feature).

## 8. Implementation Plan

- **M0 — Provenance completeness (do first, smallest, unblocks everything).** Extend `slsa/generator.go` with the §4 field list (Go version, builder OS/arch, Bun binary hash, base digest confirmation, effective-config capture, `.pokkumignore` hash). Add a fixture test asserting the statement contains every field verify will need. *Ship this even before starting verify itself — every image built without it is unverifiable forever.*
- **M1 — Attestation-check mode (no rebuild).** `pokkum verify <ref> --no-rebuild`: fetch, signature/DSSE validation, print provenance summary + toolchain-skew prediction. Delivers user value early and forces the `ProvenanceResolver` port into shape.
- **M2 — Rebuild + L1/L2 compare.** `PinnedBuildInputs` override struct through `pipeline.go`; clean-worktree checkout; frozen install; tarball output; `ImageComparator` for index/config/diffID comparison; exit codes. Integration test against the local ephemeral registry (`pkg/registry.New()` — already planned in Roadmap v1.0) doing build → push → verify round-trip.
- **M3 — L3 explanation + repro-doctor extraction.** Layer untar + file diff with cause heuristics (timestamp patterns, `version.json`, path embedding). Factor as a shared `internal/adapters/layerdiff` so `pokkum repro doctor` is mostly CLI glue over it.
- **M4 — K8s + CI ergonomics.** `deployment/foo` source, `--expect-source`, `--output=json`, GitHub Action example (scheduled verification of prod images), docs incl. the trust-model section verbatim.

### Test strategy notes

- Golden-path e2e: build fixture (`testdata/fixtures/sveltekit-basic`) → verify → L1.
- Toolchain-skew e2e: re-gzip fixture layers with different settings → L2.
- Tamper e2e: inject one file into a layer of the pushed image → exit 1 with the file named in the L3 report.
- Provenance-lies e2e: provenance pointing at a different commit → rebuild mismatch → exit 1, and `--expect-source` mismatch → immediate failure before rebuild.
- OS-skew: darwin-built provenance verified on linux → exit 2 "inconclusive", not 1.

## 9. Scope Cuts for v1

- No automatic toolchain download (`--fetch-toolchain` can land later with `bunruntime`); fail with clear instructions instead.
- Linux↔Linux only as the *supported* claim; other combos warn + inconclusive.
- No rebuild parallelization across platforms; correctness first.
- No verification of SBOM/signature artifacts' contents beyond signature validity (they're outside the reproducibility boundary).
