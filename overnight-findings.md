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

_(appended as work proceeds)_
