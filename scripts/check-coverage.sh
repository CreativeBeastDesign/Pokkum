#!/usr/bin/env bash
#
# Coverage floor regression guard.
#
# Reads a go test coverage profile (produced with -covermode=atomic
# -coverpkg=./... -coverprofile=<file>) and fails if the total statement
# coverage across the module drops below COVERAGE_FLOOR.
#
# The floor is intentionally a "must not regress much below today" ratchet
# rather than an aspirational target: it was set to the real measured
# coverage at the time this guard was introduced, minus a couple of points
# of slack, specifically so it does not immediately break CI. See
# Lessons.md / the CI hardening PR that introduced this script for the
# measured baseline. Raise FLOOR over time as coverage genuinely improves —
# never bump it up speculatively past what has actually been measured.
#
# Usage:
#   bash scripts/check-coverage.sh <coverage-profile-path>
#   COVERAGE_FLOOR=75.0 bash scripts/check-coverage.sh coverage.out

set -uo pipefail

# Baseline: `go test -short -covermode=atomic -coverpkg=./... ./...` measured
# 77.8% total statements on 2026-08-20, up from 75.5% on 2026-08-18. Floor is
# set ~2.8 points below the measured number.
#
# The 2026-08-20 re-measurement matters for a reason beyond the number: between
# those two dates this guard was not measuring anything. A dead golangci-lint
# step failed early in the same CI job, and GitHub skipped every step behind it —
# including the coverage run and this check — for six consecutive runs on main.
# The floor stayed at 73.0 while nothing evaluated it. Treat a floor that has not
# failed in a long time as a claim to re-verify, not as evidence of headroom.
FLOOR="${COVERAGE_FLOOR:-75.0}"
PROFILE="${1:-coverage.out}"

if [ ! -f "$PROFILE" ]; then
  echo "FAIL: coverage profile not found at '$PROFILE'" >&2
  echo "      generate it first, e.g.:" >&2
  echo "      go test -covermode=atomic -coverpkg=./... -coverprofile=$PROFILE ./..." >&2
  exit 1
fi

func_output="$(go tool cover -func="$PROFILE" 2>&1)"
if [ $? -ne 0 ]; then
  echo "FAIL: 'go tool cover -func=$PROFILE' failed:" >&2
  echo "$func_output" >&2
  exit 1
fi

total_line="$(echo "$func_output" | tail -1)"
pct="$(echo "$total_line" | grep -oE '[0-9]+\.[0-9]+' | tail -1)"

if [ -z "$pct" ]; then
  echo "FAIL: could not parse total coverage percentage from:" >&2
  echo "      $total_line" >&2
  exit 1
fi

echo "Measured total coverage: ${pct}%"
echo "Configured floor:        ${FLOOR}%"

if awk -v p="$pct" -v f="$FLOOR" 'BEGIN { exit (p+0 < f+0) ? 1 : 0 }'; then
  echo "PASS: coverage ${pct}% meets floor ${FLOOR}%"
  exit 0
else
  echo "FAIL: coverage ${pct}% is below floor ${FLOOR}% — a change reduced test coverage." >&2
  echo "      Add tests for the newly-uncovered code, or if this is a deliberate," >&2
  echo "      justified removal of dead/tested code, lower FLOOR in this script" >&2
  echo "      to the new real measured number (never raise it speculatively)." >&2
  exit 1
fi
