#!/usr/bin/env bash
#
# Regression guard: assert docs/Roadmap.md, docs/Shipped.md, docs/Features.md,
# and docs/items/*.md are byte-for-byte what `go run ./scripts/gen-docs`
# produces from docs/roadmap/*.yaml right now.
#
# Why this exists: docs/roadmap/*.yaml is the single machine-readable source
# of truth for "what's done / what's next" (see scripts/gen-docs's doc
# comment) specifically to replace up to eight hand-maintained markdown files
# that had drifted from each other and from the code repeatedly — this
# project's single most-repeated failure mode (see Lessons.md). A generator
# that can be quietly hand-edited around recreates exactly that drift, just
# one layer removed: the generated file would look authoritative while
# silently disagreeing with its own source. This guard makes that
# impossible to do silently — same pattern as scripts/check-build-flags.sh,
# just checking "regeneration is a no-op" instead of "a flag is present".
#
# Approach: regenerate in place, then ask git whether anything changed.
# This intentionally mutates docs/ as a side effect (like `go generate` +
# `git diff --exit-code`, a common Go idiom) — safe by construction, since a
# clean tree's regenerated output is expected to be identical to what's
# already checked in. A hand-edit to a generated file, a stale item page, or
# a docs/roadmap/*.yaml edit nobody regenerated from all show up here as a
# non-empty diff.
#
# `git status --porcelain` (not `git diff --exit-code`) is used to also
# catch newly-created or deleted files under docs/items/ — a renamed item id
# produces exactly that shape (new file untracked, old file deleted) and
# `git diff` alone on an explicit path list can miss a brand-new untracked
# file.
#
# Usage: bash scripts/check-docs-freshness.sh   # exit 0 clean, 1 on drift

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1

WATCHED_PATHS=(docs/Roadmap.md docs/Shipped.md docs/Features.md docs/items)

echo "== Roadmap docs freshness guard =="
echo "Regenerating from docs/roadmap/*.yaml (go run ./scripts/gen-docs)..."

if ! go run ./scripts/gen-docs; then
  echo "FAIL: gen-docs itself failed (validation error in docs/roadmap/*.yaml, or a generation bug)." >&2
  echo "      Run 'go run ./scripts/gen-docs' directly to see the full error." >&2
  exit 1
fi

diff_output="$(git status --porcelain -- "${WATCHED_PATHS[@]}")"

if [ -z "$diff_output" ]; then
  echo "PASS: docs/Roadmap.md, docs/Shipped.md, docs/Features.md, and docs/items/*.md exactly match docs/roadmap/*.yaml."
  exit 0
else
  echo "FAIL: regenerating docs from docs/roadmap/*.yaml produced a diff — a generated file was hand-edited," >&2
  echo "      or docs/roadmap/*.yaml changed without regenerating. Run 'make docs' and commit the result." >&2
  echo "" >&2
  echo "$diff_output" >&2
  echo "" >&2
  git --no-pager diff -- "${WATCHED_PATHS[@]}" >&2
  exit 1
fi
