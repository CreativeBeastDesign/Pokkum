#!/usr/bin/env bash
#
# Builds one identical static SvelteKit app four ways and reports what each costs:
#   1. Nginx on Debian (nginx:latest)
#   2. Nginx on Alpine (nginx:alpine)
#   3. Caddy on Alpine (caddy:alpine)
#   4. Pokkum (--strategy=static on chainguard/static with pokkum-static PID 1)
#
# Usage:
#   ./run.sh                  # build all four and print the table
#   ./run.sh --keep           # leave images behind
#   ./run.sh --no-scan        # skip the CVE scan (the slow part)
#   ./run.sh <project-dir>    # point to a custom static project
#
set -euo pipefail

cd "$(dirname "$0")"

KEEP=0
SCAN=1
APP_DIR="app"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --keep) KEEP=1; shift ;;
    --no-scan) SCAN=0; shift ;;
    -h|--help)
      echo "Usage: ./run.sh [--keep] [--no-scan] [project-dir]"
      exit 0
      ;;
    *)
      if [ -d "$1" ]; then
        APP_DIR="$1"
        shift
      else
        echo "unknown option or path: $1" >&2
        exit 2
      fi
      ;;
  esac
done

NGINX_TAG="pokkum-bench-static-nginx:local"
ALPINE_TAG="pokkum-bench-static-alpine:local"
CADDY_TAG="pokkum-bench-static-caddy:local"
POKKUM_REPO="pokkum-bench-static-pokkum"
POKKUM_TAG="$POKKUM_REPO:local"

export POKKUM_DOCKER_REPO="$POKKUM_REPO"

RESULTS="results"
mkdir -p "$RESULTS"

# ---------------------------------------------------------------------------
# Preflight checks
# ---------------------------------------------------------------------------
need() {
  command -v "$1" >/dev/null 2>&1 || { echo "error: $1 is required but not on PATH${2:+ ($2)}" >&2; exit 1; }
}
need docker "Docker is required to build Dockerfile variants and inspect images"
need npm "npm is required to build Dockerfile variants"
need bun "bun is required by Pokkum"

# Check for Pokkum binary (either repo root ../../pokkum or PATH)
if [ -x "../../pokkum" ]; then
  POKKUM_BIN="../../pokkum"
elif command -v pokkum >/dev/null 2>&1; then
  POKKUM_BIN="pokkum"
else
  echo "error: pokkum binary not found. Build it with 'make build' in the repo root." >&2
  exit 1
fi

SCANNER=""
if [ "$SCAN" = "1" ]; then
  if command -v trivy >/dev/null 2>&1; then SCANNER="trivy"
  elif command -v grype >/dev/null 2>&1; then SCANNER="grype"
  else
    echo "note: neither trivy nor grype found — CVE columns will read 'n/a'." >&2
  fi
fi

export SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-1700000000}"

# ---------------------------------------------------------------------------
# Measurement helpers
# ---------------------------------------------------------------------------
image_size_mb() {
  local bytes
  bytes="$(docker image inspect "$1" --format '{{.Size}}' 2>/dev/null || echo 0)"
  awk -v b="$bytes" 'BEGIN { printf "%.1f", b/1024/1024 }'
}

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

has_shell() {
  if docker run --rm --entrypoint /bin/sh "$1" -c 'exit 0' >/dev/null 2>&1; then
    echo "yes"
  else
    echo "no"
  fi
}

os_packages() {
  case "$SCANNER" in
    trivy) { trivy image --quiet --scanners vuln --list-all-pkgs --format json "$1" 2>/dev/null \
             | grep -c '"SrcName"' || true ; } | tr -d ' ' ;;
    *) echo "n/a" ;;
  esac
}

count_lines() {
  local total=0
  for f in "$@"; do
    if [ -f "$f" ]; then
      local n
      n=$(grep -cvE '^\s*(#|$)' "$f" || echo 0)
      total=$((total + n))
    fi
  done
  echo "$total"
}

log() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }

# ---------------------------------------------------------------------------
# Lockfiles & server config staging
# ---------------------------------------------------------------------------
if [ ! -f "$APP_DIR/package-lock.json" ]; then
  log "Generating package-lock.json in $APP_DIR"
  ( cd "$APP_DIR" && npm install --package-lock-only --silent >/dev/null 2>&1 || true )
fi
if [ ! -f "$APP_DIR/bun.lock" ] && [ ! -f "$APP_DIR/bun.lockb" ]; then
  log "Generating bun.lock in $APP_DIR"
  ( cd "$APP_DIR" && bun install --silent >/dev/null 2>&1 || true )
fi

cp nginx.conf "$APP_DIR/" 2>/dev/null || true
cp Caddyfile "$APP_DIR/" 2>/dev/null || true

# ---------------------------------------------------------------------------
# Builds
# ---------------------------------------------------------------------------
log "Variant 1/4: Nginx (Debian)"
docker build -q -f Dockerfile.nginx -t "$NGINX_TAG" "$APP_DIR" >/dev/null

log "Variant 2/4: Nginx (Alpine)"
docker build -q -f Dockerfile.alpine -t "$ALPINE_TAG" "$APP_DIR" >/dev/null

log "Variant 3/4: Caddy (Alpine)"
docker build -q -f Dockerfile.caddy -t "$CADDY_TAG" "$APP_DIR" >/dev/null

log "Variant 4/4: Pokkum (--strategy=static, build 1 of 2)"
rm -rf "$RESULTS/oci-a" "$RESULTS/oci-b"
"$POKKUM_BIN" build "$APP_DIR" --strategy=static --to-oci-layout "$RESULTS/oci-a" --tag local >/dev/null

log "Variant 4/4: Pokkum (--strategy=static, build 2 of 2 for reproducibility check)"
"$POKKUM_BIN" build "$APP_DIR" --strategy=static --to-oci-layout "$RESULTS/oci-b" --tag local >/dev/null

log "Variant 4/4: loading Pokkum image into local Docker daemon"
"$POKKUM_BIN" build "$APP_DIR" --strategy=static --local --tag local >/dev/null

# ---------------------------------------------------------------------------
# Reproducibility
# ---------------------------------------------------------------------------
digest_of_layout() {
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

# ---------------------------------------------------------------------------
# Measurements
# ---------------------------------------------------------------------------
log "Measuring image sizes"
SIZE_NGINX=$(image_size_mb "$NGINX_TAG")
SIZE_ALPINE=$(image_size_mb "$ALPINE_TAG")
SIZE_CADDY=$(image_size_mb "$CADDY_TAG")
SIZE_POKKUM=$(image_size_mb "$POKKUM_TAG")

log "Measuring shells in images"
SHELL_NGINX=$(has_shell "$NGINX_TAG")
SHELL_ALPINE=$(has_shell "$ALPINE_TAG")
SHELL_CADDY=$(has_shell "$CADDY_TAG")
SHELL_POKKUM=$(has_shell "$POKKUM_TAG")

log "Scanning packages and vulnerabilities"
PKGS_NGINX=$(os_packages "$NGINX_TAG")
PKGS_ALPINE=$(os_packages "$ALPINE_TAG")
PKGS_CADDY=$(os_packages "$CADDY_TAG")
PKGS_POKKUM=$(os_packages "$POKKUM_TAG")

CVES_NGINX=$(cve_count "$NGINX_TAG")
CVES_ALPINE=$(cve_count "$ALPINE_TAG")
CVES_CADDY=$(cve_count "$CADDY_TAG")
CVES_POKKUM=$(cve_count "$POKKUM_TAG")

LINES_NGINX=$(count_lines Dockerfile.nginx nginx.conf)
LINES_ALPINE=$(count_lines Dockerfile.alpine nginx.conf)
LINES_CADDY=$(count_lines Dockerfile.caddy Caddyfile)

# ---------------------------------------------------------------------------
# Report Output
# ---------------------------------------------------------------------------
OUT="$RESULTS/results.md"
{
  echo "| | Nginx (Debian) | Nginx (Alpine) | Caddy (Alpine) | Pokkum (\`--strategy=static\`) |"
  echo "|---|---|---|---|---|"
  echo "| Image size (MB) | $SIZE_NGINX | $SIZE_ALPINE | $SIZE_CADDY | **$SIZE_POKKUM** |"
  echo "| OS packages | $PKGS_NGINX | $PKGS_ALPINE | $PKGS_CADDY | **$PKGS_POKKUM** |"
  echo "| HIGH+CRITICAL CVEs | $CVES_NGINX | $CVES_ALPINE | $CVES_CADDY | **$CVES_POKKUM** |"
  echo "| Shell in the image | $SHELL_NGINX | $SHELL_ALPINE | $SHELL_CADDY | **$SHELL_POKKUM** |"
  echo "| Base distribution | Debian GNU/Linux | Alpine Linux | Alpine Linux | **chainguard/static** (libc-free) |"
  echo "| PID 1 Process | nginx (master) | nginx (master) | caddy | **pokkum-static** (Go) |"
  echo "| Precompressed sidecars (.br/.gz/.zst) | manual | manual | manual | **automatic** (\`precompressutils\`) |"
  echo "| Reproducible build | no (by construction) | no (by construction) | no (by construction) | $REPRO |"
  echo "| SBOM / SLSA Provenance | no | no | no | **yes** |"
  echo "| Lines of build & server config | $LINES_NGINX | $LINES_ALPINE | $LINES_CADDY | **0** |"
  echo
  echo "Scanner: ${SCANNER:-none (CVE and package columns not measured)}. "
  echo "SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH. Generated by \`benchmarks/static-three-way/run.sh\`."
} > "$OUT"

log "Benchmark Results"
cat "$OUT"
echo
echo "Written to $OUT"

if [ "$KEEP" = "0" ]; then
  docker rmi -f "$NGINX_TAG" "$ALPINE_TAG" "$CADDY_TAG" "$POKKUM_TAG" >/dev/null 2>&1 || true
fi
