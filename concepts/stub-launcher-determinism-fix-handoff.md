# Handoff: Fix Non-Deterministic Stub-Launcher Binary

**Status:** Confirmed real bug, not yet fixed. Flagged as code-review finding #10 (2026-08-17) against the Option A (compiled stub launcher) implementation, then verified empirically during the fix pass — see below. Deliberately left unfixed there because a proper fix is real engineering, not a minimal edit.

**Read first:** `concepts/layered-strategy-runtime-hardening-concept.md` §1 (the design doc Option A was built from) and `CLAUDE.md`'s "Bit-for-Bit OCI Reproducibility" invariant — this bug is a direct violation of that invariant, which the rest of the codebase treats as load-bearing (every other embedded binary/layer in Pokkum is either downloaded-and-checksum-pinned or built deterministically from `SOURCE_DATE_EPOCH`-pinned inputs).

---

## 1. What's confirmed

`internal/adapters/bunruntime/resolver.go`'s `compileStub` (currently ~line 302) compiles a minimal entrypoint stub with:

```go
entryFile := filepath.Join(tmpDir, "stub-entry.ts")
stubCode := "const p = \"/app/server/\" + \"index.js\";\nawait import(p);\n"
...
cmd := exec.CommandContext(ctx, "bun", "build", "--compile", "--target="+targetName, "--outfile="+tmpBinary, entryFile)
```

I reproduced this exact invocation directly in a shell, twice, with byte-identical input:

```bash
mkdir -p /tmp/stub-spike && cd /tmp/stub-spike
printf 'const p = "/app/server/" + "index.js";\nawait import(p);\n' > stub-entry.ts
bun build --compile --target=bun-linux-x64 --outfile=bun-stub-x64 stub-entry.ts
bun build --compile --target=bun-linux-x64 --outfile=bun-stub-x64-run2 stub-entry.ts
shasum -a 256 bun-stub-x64 bun-stub-x64-run2
```

Result: **two different SHA256 hashes.** `file bun-stub-x64` shows `BuildID[sha1]=a9a0d18db4f98a86ad4778800c5fa46943f81b2e`, and the second run's build ID differs too — strongly suggesting the ELF `.note.gnu.build-id` section (a per-link, typically content-and-timestamp-derived identifier most linkers emit by default) is at least one source of the variance. This was tested with `bun` 1.3.14 on macOS, cross-compiling for `bun-linux-x64`.

**Not yet confirmed:** whether the build-id is the *only* source of variance, or whether other regions of the binary also differ between runs. Do this first (see §3, step 1) before committing to a fix — don't assume.

## 2. Why this matters

- `compileStub` never references `req.SourceDateEpoch` (zero occurrences anywhere in `resolver.go`) despite `ports.BunResolverRequest` carrying it (see the call site in `internal/core/pipeline.go` around line 947: `SourceDateEpoch: req.SourceDateEpoch` is passed in and currently ignored downstream).
- No test in `resolver_test.go` invokes the real `bun` compiler at all — `TestResolver_StubLauncher_BypassedByCustomBinary`, `_CachedHit`, and `_AdversarialTargetAndCacheIsolation` all exercise cache-hit/isolation logic with fake pre-seeded binaries, never a real compile. This is why the bug shipped undetected.
- Every other binary Pokkum embeds is either downloaded with a pinned SHA256 (`pinnedReleaseChecksums` in this same file) or built from a fully `SOURCE_DATE_EPOCH`-pinned, sorted, deterministic pipeline (`internal/adapters/packager`, `internal/adapters/attestutils`). The stub launcher is the first embedded artifact compiled from scratch at build time with no determinism guarantee at all — if it ships non-deterministic, `--stub-launcher` images will fail `pokkum verify --rebuild` and `TestRealBuildIsReproducibleAcrossRuns`-style double-build digest checks, for reasons that will look mysterious without this context.

## 3. Suggested approach

**Step 1 — isolate the actual diff, don't guess.** Compile the stub twice (as above, or via a throwaway Go test), then diff the two binaries byte-for-byte *excluding* the build-id section (`debug/elf` can locate `.note.gnu.build-id`'s file offset/size; zero both copies' bytes in that range and compare what's left). This tells you definitively whether build-id is the whole story or whether something else — chunk ordering, embedded timestamps elsewhere, ASLR-related padding — also varies. The fix design depends entirely on this answer.

**Step 2 — pick a normalization strategy based on step 1's answer.** Leading candidate, assuming build-id is the (or the main) culprit:

- **Patch the ELF binary directly, in pure Go, without depending on a host `strip`/`objcopy` tool.** `internal/adapters/striputils` (existing package, `debug/elf` for reading) already hit the exact portability wall this needs to avoid: its `StripELFFile` requires `llvm-strip`/`strip` on `$PATH` and **explicitly documents failing on plain macOS dev hosts** ("Xcode's Mach-O-only tool... exits with 'unrecognized option'"; see `Roadmap.md`'s "ELF Native Addon Stripping" entry for the shipped fix — a `Warn`-level log when no tool is found, not a real strip). Reusing `StripELFFile` as-is would very likely just skip on this machine and any other macOS dev host, silently leaving the bug in place there while maybe working in Linux CI — not good enough for something CLAUDE.md treats as an invariant, not a best-effort optimization. Also: `--strip-unneeded` strips debug symbols/relocations, not necessarily the build-id note specifically (that section isn't "debug info" in the sense strip tools usually mean) — verify this empirically before assuming reuse would even work when a tool *is* present.
  - The more robust, portable option: since Go's `debug/elf` package can locate section headers (name, file offset, size) for reading, a small self-contained function can open the compiled binary, find `.note.gnu.build-id` by name, and zero (or otherwise deterministically overwrite) its content bytes in place — no external tool dependency, works identically on every host. `debug/elf` itself is read-only (no section-write API), so this means manual `os.File` byte patching at the located offset, not a `debug/elf` write call. Keep the section's *size* unchanged (don't try to remove the section entirely — that shifts every subsequent offset and risks corrupting the ELF, which is a much harder, riskier operation) — zeroing content in place is the safe version of this fix.
- Only if step 1 shows build-id isn't the whole story: investigate whether `bun build --compile` has an env var or internal flag for reproducible output (checked `bun build --help` already during the original review — no such flag is currently listed; worth checking bun's GitHub issues/changelog for anything newer, and worth asking upstream if genuinely stuck).

**Step 3 — add the regression test that should have existed from the start.** Guard it exactly like this repo's other real-`bun` tests (see `tests/integration/reproducibility_e2e_test.go`'s `TestRealBuildIsReproducibleAcrossRuns`): skip under `testing.Short()`, skip when `exec.LookPath("bun")` fails. The test itself: call `Resolver.Resolve` twice with identical `(version, variant, platform, StubLauncher: true)` into two different cache dirs (or clear the cache between calls), assert identical `SHA256`. This belongs in `internal/adapters/bunruntime/resolver_test.go` alongside the existing `StubLauncher` tests.

**Step 4 — thread `SourceDateEpoch` through if the eventual design needs it.** Not obviously required if the ELF-patching approach in step 2 works (it doesn't need a timestamp, just needs to zero a fixed section), but if the investigation in step 1 turns up an *embedded timestamp* (not just build-id randomness) as a second source of variance, that's exactly what `SOURCE_DATE_EPOCH` exists to pin — follow the pattern already established in `internal/adapters/packager`/`striputils.StripELFFile`'s `modTime` parameter (pin file mtimes to the epoch) rather than inventing a new mechanism.

## 4. Files involved

| File | Role |
|---|---|
| `internal/adapters/bunruntime/resolver.go` | `compileStub` (~line 302) is where the fix goes — likely a new step after the `bun build --compile` call and before `computeFileSHA256`, patching `tmpBinary` in place before it's renamed to `binaryPath`. |
| `internal/adapters/bunruntime/resolver_test.go` | Where the new determinism regression test belongs (step 3). |
| `internal/adapters/striputils/striputils.go` | Reference for this codebase's existing ELF-handling conventions (`debug/elf` usage, `IsELFBinary`) and the exact portability trap to avoid repeating (host-strip-tool dependency). Do not assume this package's `StripELFFile` is directly reusable — verify per step 2's notes above. |
| `internal/adapters/attestutils/` | A second reference point: this package already computes deterministic content hashes over build output for the (already-shipped) Option C startup-attestation feature — same problem class ("make build output verifiably identical across runs"), worth a skim for any directly reusable patterns even though its inputs (a directory tree) differ from this task's (a single ELF binary). |
| `concepts/layered-strategy-runtime-hardening-concept.md` | Original Option A design doc; §1.3's "build-time assertion, not a one-time fact to trust forever" framing directly motivates step 3. |

## 5. Acceptance criteria

- [ ] Step 1's investigation is documented somewhere (a `Lessons.md` entry fits this repo's convention) — what exactly varies between two compiles, not just "it's probably the build-id."
- [ ] `compileStub` (or a helper it calls) produces byte-identical output across repeated compiles of the same `(version, variant, platform)` on the same host — verified by the new test in step 3, which must actually pass, not be skip-guarded into never running in CI.
- [ ] The fix works without depending on any host tool beyond `bun` itself — no new `strip`/`objcopy` dependency that would silently no-op on a host lacking it (the exact failure mode already documented for `striputils` on macOS).
- [ ] `make verify` (or at minimum `gofmt`, `go vet`, `golangci-lint run ./...`, `go test ./internal/adapters/bunruntime/...`) passes.
- [ ] `Roadmap.md`'s "2. Layered-Strategy Runtime Hardening" section gets a note that Option A's determinism gap is closed (it currently doesn't mention this gap at all, since it was found after that section was last written — add it as a finding-and-fix pair, matching the style of the "Layered-Strategy Real-Build Correctness" section's entries).
