#!/usr/bin/env bash
#
# Discover and run every FuzzXxx target in the module for a short amount of
# time each. Intended for a scheduled/nightly CI job (see
# .github/workflows/fuzz.yml), never for `make verify` or a PR gate — fuzzing
# the whole target set even briefly is too slow to run on every push.
#
# Deliberately discovers targets via `go test -list` rather than hardcoding a
# list, so newly-added FuzzXxx funcs are picked up automatically without
# touching this script.
#
# Usage:
#   bash scripts/run-fuzz.sh                    # 30s per target (default)
#   FUZZTIME=2m bash scripts/run-fuzz.sh         # override per-target budget
#   FUZZ_PACKAGES="./internal/..." bash scripts/run-fuzz.sh   # narrow scope

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

FUZZTIME="${FUZZTIME:-30s}"
FUZZ_PACKAGES="${FUZZ_PACKAGES:-./...}"

echo "== Discovering FuzzXxx targets in ${FUZZ_PACKAGES} =="

fail=0
found_any=0

# go list ./... enumerates packages; go test -list '^Fuzz' <pkg> lists any
# Fuzz-prefixed top-level func in that package without compiling/running it.
while IFS= read -r pkg; do
  [ -z "$pkg" ] && continue

  targets="$(go test -list '^Fuzz' "$pkg" 2>/dev/null | grep -E '^Fuzz[A-Za-z0-9_]*$' || true)"
  [ -z "$targets" ] && continue

  while IFS= read -r target; do
    [ -z "$target" ] && continue
    found_any=1
    echo ""
    echo "-- Fuzzing ${target} in ${pkg} for ${FUZZTIME} --"
    if ! go test -run='^$' -fuzz="^${target}\$" -fuzztime="${FUZZTIME}" "$pkg"; then
      echo "FAIL: ${target} in ${pkg} found a failing input or crashed." >&2
      fail=1
    fi
  done <<< "$targets"
done < <(go list "$FUZZ_PACKAGES")

echo ""
if [ "$found_any" -eq 0 ]; then
  echo "No FuzzXxx targets found under ${FUZZ_PACKAGES}. Nothing to run."
  exit 0
fi

if [ "$fail" -eq 0 ]; then
  echo "PASS: all discovered fuzz targets survived ${FUZZTIME} each."
  exit 0
else
  echo "FAIL: one or more fuzz targets found a failing input. See output above; failing" >&2
  echo "      seed corpus entries are written under the package's testdata/fuzz/<Target>/" >&2
  echo "      directory and should be committed as regression inputs once fixed." >&2
  exit 1
fi
