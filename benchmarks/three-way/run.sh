#!/usr/bin/env bash
#
# Builds one identical SvelteKit app three ways and reports what each costs.
#
# Everything it measures is measured the same way for all three variants, with
# the same external tools, against the same source tree. Nothing here is
# Pokkum-specific except the third build command.
#
#   ./run.sh                  # build all three and print the table
#   ./run.sh --keep           # leave the images behind for your own poking
#   ./run.sh --no-scan        # skip the CVE scan (it is the slow part)
#
set -euo pipefail

cd "$(dirname "$0")"

KEEP=0
SCAN=1
for arg in "$@"; do
  case "$arg" in
    --keep) KEEP=1 ;;
    --no-scan) SCAN=0 ;;
    -h|--help) sed -n '2,12p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown flag: $arg" >&2; exit 2 ;;
  esac
done

NAIVE_TAG="pokkum-bench-naive:local"
TUNED_TAG="pokkum-bench-tuned:local"
POKKUM_REPO="pokkum-bench-pokkum"
POKKUM_TAG="$POKKUM_REPO:local"

# Pokkum requires a destination repository in every output mode, including the
# local ones — the image has to be addressable by *something*. Nothing is
# pushed anywhere by this script.
export POKKUM_DOCKER_REPO="$POKKUM_REPO"

RESULTS="results"
mkdir -p "$RESULTS"

# ---------------------------------------------------------------------------
# Preflight. Fail with a specific, actionable message rather than a stack of
# "command not found" further down.
# ---------------------------------------------------------------------------
need() {
  command -v "$1" >/dev/null 2>&1 || { echo "error: $1 is required but not on PATH${2:+ ($2)}" >&2; exit 1; }
}
need docker "the two Dockerfile variants have to be built by something"
need pokkum "install it: https://github.com/CreativeBeastDesign/pokkum#installation--setup"
need npm "the two Dockerfile variants are npm-based"
need bun "pokkum resolves dependencies through bun"

# An INDEPENDENT scanner, deliberately. Pokkum ships its own (`pokkum scan`),
# and a comparison Pokkum wins using Pokkum's own scanner is worth nothing.
SCANNER=""
if [ "$SCAN" = "1" ]; then
  if command -v trivy >/dev/null 2>&1; then SCANNER="trivy"
  elif command -v grype >/dev/null 2>&1; then SCANNER="grype"
  else
    echo "note: neither trivy nor grype found — CVE columns will read 'not measured'." >&2
    echo "      install one of them for the comparison's most interesting column." >&2
  fi
fi

# Fixed epoch so the reproducibility check measures the packaging step rather
# than the clock. All three builds get it; only one of them uses it.
export SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-1700000000}"

# ---------------------------------------------------------------------------
# Measurement helpers
# ---------------------------------------------------------------------------

# image_size_mb <tag> — the size Docker reports for the local image.
image_size_mb() {
  local bytes
  bytes="$(docker image inspect "$1" --format '{{.Size}}' 2>/dev/null || echo 0)"
  awk -v b="$bytes" 'BEGIN { printf "%.1f", b/1024/1024 }'
}

# cve_count <tag> <severity-filter> — counts findings at or above a severity.
# Prints "n/a" when no scanner is available, never 0: "not measured" and
# "measured, found nothing" are different facts and must not print the same.
cve_count() {
  local tag="$1"
  case "$SCANNER" in
    trivy)
      { trivy image --quiet --scanners vuln --severity HIGH,CRITICAL --format json "$tag" 2>/dev/null \
        | grep -c '"VulnerabilityID"' || true ; } | tr -d ' ' ;;
    grype)
      { grype "$tag" -o json --quiet 2>/dev/null \
        | grep -cE '"severity": *"(High|Critical)"' || true ; } | tr -d ' ' ;;
    *) echo "n/a" ;;
  esac
}

# has_shell <tag> — whether an attacker landing RCE finds a shell waiting.
has_shell() {
  if docker run --rm --entrypoint /bin/sh "$1" -c 'exit 0' >/dev/null 2>&1; then
    echo "yes"
  else
    echo "no"
  fi
}

# os_packages <tag> — how many OS packages the runtime layer carries. Every one
# is a thing that can get a CVE next Tuesday.
os_packages() {
  case "$SCANNER" in
    trivy) { trivy image --quiet --scanners vuln --list-all-pkgs --format json "$1" 2>/dev/null \
             | grep -c '"SrcName"' || true ; } | tr -d ' ' ;;
    *) echo "n/a" ;;
  esac
}

log() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

# zero_default turns an empty measurement into an explicit 0. `grep -c` with no
# match legitimately yields 0, and a distroless image genuinely having zero of
# something is the most interesting cell in the table — it must not render as a
# blank that reads like a missing measurement.
zero_default() { local v; v="$(cat)"; echo "${v:-0}"; }

# ---------------------------------------------------------------------------
# Lockfiles. The two Dockerfile variants are npm-based and the tuned one uses
# `npm ci`, which requires a package-lock.json; Pokkum resolves through bun and
# wants a bun.lock. Both are generated from the SAME package.json, so this is
# each ecosystem's resolution of one dependency set, not two different sets.
# Generated rather than committed so the comparison always reflects what those
# tools do today.
# ---------------------------------------------------------------------------
if [ ! -f app/package-lock.json ]; then
  log "Generating package-lock.json (needed by npm ci in the tuned variant)"
  ( cd app && npm install --package-lock-only --silent >/dev/null )
fi
if [ ! -f app/bun.lock ] && [ ! -f app/bun.lockb ]; then
  log "Generating bun.lock (needed by pokkum)"
  ( cd app && bun install --silent >/dev/null )
fi

# ---------------------------------------------------------------------------
# Build 1 & 2 — the Dockerfiles
# ---------------------------------------------------------------------------
log "Variant 1/3: naive Dockerfile"
docker build -q -f Dockerfile.naive -t "$NAIVE_TAG" app >/dev/null

log "Variant 2/3: tuned multi-stage Dockerfile"
docker build -q -f Dockerfile.tuned -t "$TUNED_TAG" app >/dev/null

# ---------------------------------------------------------------------------
# Build 3 — Pokkum. Twice, into an OCI layout, so the digests of two
# independent builds of identical source can be compared byte-for-byte.
# ---------------------------------------------------------------------------
log "Variant 3/3: pokkum (build 1 of 2)"
rm -rf "$RESULTS/oci-a" "$RESULTS/oci-b"
pokkum build app --to-oci-layout "$RESULTS/oci-a" --tag local >/dev/null

log "Variant 3/3: pokkum (build 2 of 2, to compare digests)"
pokkum build app --to-oci-layout "$RESULTS/oci-b" --tag local >/dev/null

# Load it into Docker too, so size, shell and CVE columns are measured with
# exactly the same commands as the other two variants.
log "Variant 3/3: loading into the local daemon for measurement"
pokkum build app --local --tag local >/dev/null

# ---------------------------------------------------------------------------
# Reproducibility: do two independent builds of identical source agree?
# ---------------------------------------------------------------------------
digest_of_layout() {
  # The index digest is what a registry would address the image by.
  if command -v jq >/dev/null 2>&1; then
    jq -r '.manifests[0].digest' "$1/index.json" 2>/dev/null || echo "unknown"
  else
    grep -o 'sha256:[0-9a-f]\{64\}' "$1/index.json" | head -1 || echo "unknown"
  fi
}
DIGEST_A="$(digest_of_layout "$RESULTS/oci-a")"
DIGEST_B="$(digest_of_layout "$RESULTS/oci-b")"
if [ "$DIGEST_A" = "$DIGEST_B" ] && [ "$DIGEST_A" != "unknown" ]; then
  REPRO="**yes** (\`${DIGEST_A:0:19}…\`)"
else
  REPRO="no"
fi

# The Dockerfile variants are not built twice: `npm install`/`npm ci` resolve
# against the network and the image is stamped with the wall clock, so the
# answer is known in advance and spending two more builds to confirm it would
# be theatre. Stated, not measured — and labelled as such in the table.

# ---------------------------------------------------------------------------
# Report
# ---------------------------------------------------------------------------
OUT="$RESULTS/results.md"
{
  echo "| | Naive Dockerfile | Tuned multi-stage | Pokkum |"
  echo "|---|---|---|---|"
  echo "| Image size (MB) | $(image_size_mb "$NAIVE_TAG") | $(image_size_mb "$TUNED_TAG") | $(image_size_mb "$POKKUM_TAG") |"
  echo "| OS packages | $(os_packages "$NAIVE_TAG") | $(os_packages "$TUNED_TAG") | $(os_packages "$POKKUM_TAG") |"
  echo "| HIGH+CRITICAL CVEs | $(cve_count "$NAIVE_TAG") | $(cve_count "$TUNED_TAG") | $(cve_count "$POKKUM_TAG") |"
  echo "| Shell in the image | $(has_shell "$NAIVE_TAG") | $(has_shell "$TUNED_TAG") | $(has_shell "$POKKUM_TAG") |"
  echo "| Reproducible build | no (by construction) | no (by construction) | $REPRO |"
  echo "| SBOM | no | no | yes |"
  echo "| SLSA provenance | no | no | yes |"
  echo "| Lines of build config you maintain | $(grep -cvE '^\s*(#|$)' Dockerfile.naive) | $(grep -cvE '^\s*(#|$)' Dockerfile.tuned) | 0 |"
  echo
  echo "Scanner: ${SCANNER:-none (CVE and package columns not measured)}. "
  echo "SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH. Generated by \`benchmarks/three-way/run.sh\`."
} > "$OUT"

log "Results"
cat "$OUT"
echo
echo "Written to $OUT — paste it anywhere, or re-run it yourself and disagree."

if [ "$KEEP" = "0" ]; then
  docker rmi -f "$NAIVE_TAG" "$TUNED_TAG" "$POKKUM_TAG" >/dev/null 2>&1 || true
fi
