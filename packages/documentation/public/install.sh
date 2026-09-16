#!/bin/sh
# wp-ssh-bridge installer
#
# Downloads the newest wp-ssh-bridge release from GitHub, verifies its SHA-256
# checksum, and unpacks the binary.
#
#   curl -fsSL https://wp-ssh-bridge.citation.media/install.sh | sh
#
# Options (after `sh -s --`, for example `| sh -s -- --dir .ddev/bin`):
#   --dir <path>      Install into this directory. Defaults to .ddev/bin when the
#                     current directory is a DDEV project, otherwise to the
#                     current directory.
#   --global          Install into /usr/local/bin (uses sudo when needed).
#   --version <tag>   Install a specific release, for example v0.5.2.
#   -h, --help        Show this help.
#
# Environment:
#   WP_SSH_BRIDGE_VERSION   Same as --version.
#   WP_SSH_BRIDGE_REPO      GitHub repository to read releases from.
set -eu

# Releases are published as GitHub release assets. Point WP_SSH_BRIDGE_REPO at
# a fork to install from somewhere else.
GITHUB_REPO="${WP_SSH_BRIDGE_REPO:-Citation-Media/wp-ssh-bridge}"
VERSION="${WP_SSH_BRIDGE_VERSION:-}"
TARGET_DIR=""
GLOBAL=0

usage() {
  cat <<'USAGE'
wp-ssh-bridge installer

Downloads the latest wp-ssh-bridge release for this machine, verifies the
SHA-256 checksum, and unpacks the binary.

  curl -fsSL https://wp-ssh-bridge.citation.media/install.sh | sh

Options (after `sh -s --`, for example `| sh -s -- --dir .ddev/bin`):
  --dir <path>      Install into this directory. Defaults to .ddev/bin when the
                    current directory is a DDEV project, otherwise to the
                    current directory.
  --global          Install into /usr/local/bin (uses sudo when needed).
  --version <tag>   Install a specific release, for example v0.5.2.
                    Without it, the newest release is resolved automatically.
  -h, --help        Show this help.

Environment:
  WP_SSH_BRIDGE_VERSION   Same as --version.
  WP_SSH_BRIDGE_REPO      GitHub repository to read releases from.
USAGE
}

die() {
  printf 'wp-ssh-bridge installer: %s\n' "$*" >&2
  exit 1
}

while [ $# -gt 0 ]; do
  case "$1" in
    --dir) [ $# -ge 2 ] || die "--dir needs a path"; TARGET_DIR="$2"; shift 2 ;;
    --dir=*) TARGET_DIR="${1#--dir=}"; shift ;;
    --global) GLOBAL=1; shift ;;
    --version) [ $# -ge 2 ] || die "--version needs a tag"; VERSION="$2"; shift 2 ;;
    --version=*) VERSION="${1#--version=}"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown option: $1 (see --help)" ;;
  esac
done

case "$(uname -s)" in
  Darwin) OS=darwin ;;
  Linux) OS=linux ;;
  *) die "unsupported operating system: $(uname -s)" ;;
esac

case "$(uname -m)" in
  arm64|aarch64) ARCH=arm64 ;;
  x86_64|amd64) ARCH=amd64 ;;
  *) die "unsupported architecture: $(uname -m)" ;;
esac

# Prefer curl, fall back to wget. Both are used quietly with failures surfaced.
if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q "$1" -O "$2"; }
else
  die "curl or wget is required"
fi

command -v tar >/dev/null 2>&1 || die "tar is required"

if command -v sha256sum >/dev/null 2>&1; then
  checksum() { sha256sum "$1" | awk '{print $1}'; }
elif command -v shasum >/dev/null 2>&1; then
  checksum() { shasum -a 256 "$1" | awk '{print $1}'; }
else
  die "sha256sum or shasum is required to verify the download"
fi

TMP="$(mktemp -d 2>/dev/null || mktemp -d -t wp-ssh-bridge)"
trap 'rm -rf "$TMP"' EXIT INT TERM

# Every release attaches each archive twice: under a versioned name, and under
# a version-free one that /releases/latest/download/ always resolves to the
# newest release. So the default install is a plain download with no version to
# look up first, and a pinned install addresses the tag directly. The published
# checksums.txt lists both names for the same digest.
if [ -n "$VERSION" ]; then
  # A version the caller supplied may omit the tag's leading "v".
  case "$VERSION" in v*) ;; *) VERSION="v$VERSION" ;; esac
  ARCHIVE="wp-ssh-bridge_${VERSION}_${OS}_${ARCH}.tar.gz"
  BASE="https://github.com/${GITHUB_REPO}/releases/download/${VERSION}"
  printf 'Downloading wp-ssh-bridge %s for %s/%s\n' "$VERSION" "$OS" "$ARCH"
else
  ARCHIVE="wp-ssh-bridge_${OS}_${ARCH}.tar.gz"
  BASE="https://github.com/${GITHUB_REPO}/releases/latest/download"
  printf 'Downloading the latest wp-ssh-bridge for %s/%s\n' "$OS" "$ARCH"
fi
fetch "$BASE/$ARCHIVE" "$TMP/$ARCHIVE" || die "download failed: $BASE/$ARCHIVE"
fetch "$BASE/checksums.txt" "$TMP/checksums.txt" || die "download failed: $BASE/checksums.txt"

EXPECTED="$(grep " ${ARCHIVE}\$" "$TMP/checksums.txt" | awk '{print $1}' | head -n 1)"
[ -n "$EXPECTED" ] || die "no checksum for $ARCHIVE in checksums.txt"
ACTUAL="$(checksum "$TMP/$ARCHIVE")"
[ "$EXPECTED" = "$ACTUAL" ] || die "checksum mismatch for $ARCHIVE (expected $EXPECTED, got $ACTUAL)"

tar -C "$TMP" -xzf "$TMP/$ARCHIVE"
[ -f "$TMP/wp-ssh-bridge" ] || die "archive did not contain a wp-ssh-bridge binary"
chmod +x "$TMP/wp-ssh-bridge"

if [ "$GLOBAL" = 1 ]; then
  DEST=/usr/local/bin
  if [ -w "$DEST" ]; then
    install -m 755 "$TMP/wp-ssh-bridge" "$DEST/wp-ssh-bridge"
  else
    printf 'Installing to %s requires sudo\n' "$DEST"
    sudo install -m 755 "$TMP/wp-ssh-bridge" "$DEST/wp-ssh-bridge"
  fi
else
  if [ -z "$TARGET_DIR" ]; then
    if [ -f ".ddev/config.yaml" ]; then
      TARGET_DIR=".ddev/bin"
    else
      TARGET_DIR="."
    fi
  fi
  DEST="${TARGET_DIR%/}"
  [ -n "$DEST" ] || DEST="."
  mkdir -p "$DEST"
  install -m 755 "$TMP/wp-ssh-bridge" "$DEST/wp-ssh-bridge"
fi

printf 'Installed %s/wp-ssh-bridge (%s)\n' "$DEST" "$("$DEST/wp-ssh-bridge" version --short 2>/dev/null || printf '%s' "${VERSION:-unknown version}")"
printf '\nNext: run "%s/wp-ssh-bridge init" in your project, then "%s/wp-ssh-bridge pull --silent".\n' "$DEST" "$DEST"
