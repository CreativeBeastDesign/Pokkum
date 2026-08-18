# Overnight run — findings log

Bugs and defects discovered while working the overnight queue of 2026-08-18/19.
Each entry records what was found, where, whether it was fixed, and how it was
confirmed. Root causes and preventative rules for the substantial ones also go
to `Lessons.md` (the project's permanent incident log) and, where they imply a
new invariant, to Serena's `mem:self_review_checklist`. This file is the
chronological working record for this run specifically.

**Queue:** `pipeline_test.go` mock asserts · `--strategy=static` fixture + boot
smoke test · composition-root refactor for the allowlisted adapter→adapter
imports · `populateInputsFromSLSA` repository fallback · `--runtime=node` ·
Sigstore TUF root refresh.

---

## Findings

### 1. `populateInputsFromSLSA` filled the source repo from the target image repo

**Found:** carried over from the `--expect-source` work (`91dc3cd`), which flagged it in code comments and deliberately left it out of scope.
**Where:** `internal/adapters/provenance/resolver.go`, the `ep["repository"]` fallback.
**What:** SLSA external parameters' `repository` is the *target image* repository — where the image was pushed. It was being used to fill `PinnedInputs.Repo`, which means the *git source* repository and is what `pokkum verify` displays as the build's source. So on a statement with no `source-code` dependency, an operator saw `Source Repo: ghcr.io/acme/app` and could reasonably conclude the source had been verified as coming from there, when all that field proved was where the image was pushed.
**Severity:** correctness / misleading output, not a security hole — confirmed: the fallback never set `SourceProvenance`, so `--expect-source`'s gate (which requires `SourceProvenanceVerified`) rejected either way, before and after.
**Fixed:** fallback removed rather than relocated. `Repo` now stays empty and `pokkum verify` prints `(unrecorded)`, which is honest. Every reader of `PinnedInputs.Repo` was checked to handle the empty case. Pinned by a new test asserting that a verified statement with no `source-code` dependency leaves `Repo` unpopulated.
**Note:** an empty field the operator must interpret beats a populated field that means something other than its name.

_(appended as work proceeds)_
