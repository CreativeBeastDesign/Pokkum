#!/usr/bin/env bash
#
# Regression guard: assert every release build path enforces the compiler
# build optimization flags.
#
# Roadmap "Compiler Build Optimization Flags" (#132):
#   -trimpath -ldflags="-s -w"
# across both release build paths:
#   1. Makefile `build` / `supervisor` targets (local + `make` dev builds)
#   2. .goreleaser.yaml        (official `pokkum upgrade` release pipeline)
#
# The release paths (Makefile build, goreleaser) must also set
# the -X main.version/commit/buildDate ldflags so `pokkum version` reports
# real metadata, and so both paths stay in parity.
#
# Failing this check means an edit silently dropped the size optimization —
# fail loudly rather than shipping a fatter, unstripped release binary.
#
# Usage: bash scripts/check-build-flags.sh   # exit 0 on success, 1 on failure

set -u

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fail=0

# check_present <label> <file> <grep-pattern>
# The literal -- before "$pattern" tells grep to stop option parsing, so
# patterns that begin with '-' (e.g. -trimpath, -s -w, -X) are matched literally.
check_present() {
  local label="$1" file="$2" pattern="$3"
  if grep -qE -- "$pattern" "$ROOT/$file"; then
    echo "ok   : $label ($file)"
  else
    echo "FAIL : $label — $file does not contain '$pattern'" >&2
    fail=1
  fi
}

echo "== Compiler build optimization flags regression guard =="

# --- trimpath everywhere ---
check_present "trimpath (Makefile build)"         "Makefile"                          "-trimpath"
check_present "trimpath (.goreleaser.yaml)"       ".goreleaser.yaml"                  "-trimpath"

# --- strip & no-DWARF (-s -w) everywhere ---
check_present "-s -w (Makefile build)"            "Makefile"                          "-s -w"
check_present "-s -w (Makefile supervisor)"       "Makefile"                          "-ldflags \"-s -w\""
check_present "-s -w (.goreleaser.yaml)"          ".goreleaser.yaml"                  "-s -w"

# --- version metadata parity on release-capable paths ---
check_present "-X main.version (Makefile build)"    "Makefile"                          "-X main.version"
check_present "-X main.version (.goreleaser.yaml)"  ".goreleaser.yaml"                 "-X main.version"

echo ""
if [ "$fail" -eq 0 ]; then
  echo "PASS: compiler build optimization flags enforced in all release paths."
  exit 0
else
  echo "FAIL: one or more release build paths dropped a compiler build optimization flag." >&2
  exit 1
fi
