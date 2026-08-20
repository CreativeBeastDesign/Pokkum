#!/bin/sh
#
# Pokkum installer.
#
#   curl -fsSL https://raw.githubusercontent.com/CreativeBeastDesign/pokkum/main/install.sh | sh
#
# Installs the pokkum CLI from a GitHub release: detects your OS/architecture,
# downloads the matching archive, VERIFIES its SHA-256 against the release's
# checksums.txt, and installs the binary.
#
# The checksum verification is the point, not a nicety. Pokkum's whole purpose is
# producing signed, verifiable images, and a `curl | sh` that installs whatever
# bytes arrive would make the installer the weakest link in that chain. If the
# checksum does not match, this script refuses to install and says so.
#
# Environment overrides:
#   POKKUM_VERSION      version to install (default: latest release), e.g. v1.0.1
#   POKKUM_INSTALL_DIR  destination directory (default: /usr/local/bin)
#
# POSIX sh on purpose: this runs under whatever /bin/sh the machine has.

set -eu

REPO="CreativeBeastDesign/pokkum"
INSTALL_DIR="${POKKUM_INSTALL_DIR:-/usr/local/bin}"

die() {
	printf 'pokkum installer: %s\n' "$1" >&2
	exit 1
}

info() {
	printf '  %s\n' "$1"
}

need() {
	command -v "$1" >/dev/null 2>&1 || die "'$1' is required but was not found on PATH."
}

need uname
need tar
need mktemp

# One of curl or wget; prefer curl since the documented invocation already uses it.
if command -v curl >/dev/null 2>&1; then
	download() { curl -fsSL "$1" -o "$2"; }
	fetch_stdout() { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
	download() { wget -qO "$2" "$1"; }
	fetch_stdout() { wget -qO- "$1"; }
else
	die "either 'curl' or 'wget' is required."
fi

# sha256: coreutils on Linux, shasum on macOS. Without one of these we cannot
# verify, and installing unverified bytes is not an option this script offers.
if command -v sha256sum >/dev/null 2>&1; then
	sha256_of() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
	sha256_of() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
	die "neither 'sha256sum' nor 'shasum' was found, so the download cannot be verified. Install one, or download a release archive manually from https://github.com/$REPO/releases."
fi

# --- platform detection -----------------------------------------------------
os_raw="$(uname -s)"
case "$os_raw" in
Darwin) os="darwin" ;;
Linux) os="linux" ;;
*) die "unsupported operating system '$os_raw'. Released builds cover darwin and linux; build from source with 'go build ./cmd/pokkum'." ;;
esac

arch_raw="$(uname -m)"
case "$arch_raw" in
x86_64 | amd64) arch="amd64" ;;
aarch64 | arm64) arch="arm64" ;;
*) die "unsupported architecture '$arch_raw'. Released builds cover amd64 and arm64." ;;
esac

# --- version resolution -----------------------------------------------------
if [ -n "${POKKUM_VERSION:-}" ]; then
	version="$POKKUM_VERSION"
else
	# Resolve the latest tag without requiring jq: the releases/latest payload
	# carries exactly one "tag_name" field.
	version="$(fetch_stdout "https://api.github.com/repos/$REPO/releases/latest" |
		sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -n 1)"
	[ -n "$version" ] || die "could not determine the latest release version from the GitHub API. Set POKKUM_VERSION to install a specific version, e.g. POKKUM_VERSION=v1.0.1."
fi

# Release archives are named without the leading "v" (pokkum_1.0.1_linux_amd64.tar.gz),
# while the git tag carries it. Accept either spelling from the caller.
bare_version="${version#v}"

archive="pokkum_${bare_version}_${os}_${arch}.tar.gz"
base_url="https://github.com/$REPO/releases/download/v${bare_version}"

printf 'Installing pokkum %s (%s/%s)\n' "v$bare_version" "$os" "$arch"

tmp="$(mktemp -d)"
# shellcheck disable=SC2064 # expand $tmp now, deliberately: it must be removed
# even if a later assignment changes the variable.
trap "rm -rf '$tmp'" EXIT INT TERM

# --- download ---------------------------------------------------------------
info "downloading $archive"
download "$base_url/$archive" "$tmp/$archive" ||
	die "could not download $base_url/$archive — check that version v$bare_version exists and publishes a build for $os/$arch."

info "downloading checksums.txt"
download "$base_url/checksums.txt" "$tmp/checksums.txt" ||
	die "could not download the checksums file for v$bare_version, so the archive cannot be verified. Refusing to install unverified bytes."

# --- verify -----------------------------------------------------------------
expected="$(sed -n "s/^\([0-9a-f]\{64\}\)[[:space:]][[:space:]]*${archive}\$/\1/p" "$tmp/checksums.txt" | head -n 1)"
[ -n "$expected" ] || die "checksums.txt for v$bare_version contains no entry for $archive. Refusing to install unverified bytes."

actual="$(sha256_of "$tmp/$archive")"
if [ "$actual" != "$expected" ]; then
	printf 'pokkum installer: CHECKSUM MISMATCH for %s\n  expected %s\n  actual   %s\nRefusing to install. The download may be corrupt or tampered with.\n' \
		"$archive" "$expected" "$actual" >&2
	exit 1
fi
info "sha256 verified"

# --- unpack -----------------------------------------------------------------
tar -xzf "$tmp/$archive" -C "$tmp" || die "could not extract $archive."
[ -f "$tmp/pokkum" ] || die "the archive did not contain a 'pokkum' binary — the release layout may have changed."
chmod +x "$tmp/pokkum"

# --- install ----------------------------------------------------------------
# Only reach for sudo when the destination is genuinely not writable, and only
# when a TTY exists to prompt on — a piped `curl | sh` cannot answer a password
# prompt, so tell the user what to run instead of hanging.
if [ -w "$INSTALL_DIR" ] || { [ ! -e "$INSTALL_DIR" ] && mkdir -p "$INSTALL_DIR" 2>/dev/null; }; then
	mv "$tmp/pokkum" "$INSTALL_DIR/pokkum"
elif command -v sudo >/dev/null 2>&1 && [ -t 0 ]; then
	info "$INSTALL_DIR is not writable; elevating with sudo"
	sudo mkdir -p "$INSTALL_DIR"
	sudo mv "$tmp/pokkum" "$INSTALL_DIR/pokkum"
else
	die "$INSTALL_DIR is not writable. Re-run with a writable destination, e.g. POKKUM_INSTALL_DIR=\"\$HOME/.local/bin\", or install manually from https://github.com/$REPO/releases."
fi

printf 'Installed pokkum to %s/pokkum\n' "$INSTALL_DIR"

case ":$PATH:" in
*":$INSTALL_DIR:"*) ;;
*) printf '\nNote: %s is not on your PATH. Add it, e.g.\n  export PATH="%s:$PATH"\n' "$INSTALL_DIR" "$INSTALL_DIR" ;;
esac

printf '\nVerify the install:\n  pokkum --version\n'
