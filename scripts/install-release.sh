#!/usr/bin/env sh
set -eu

REPO="ib-jpg/ez-ssh"
VERSION="${VERSION:-}"

fetch() {
  url="$1"
  dest="$2"
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" -o "$dest"
  elif command -v wget >/dev/null 2>&1; then
    wget -qO "$dest" "$url"
  else
    echo "Error: curl or wget is required." >&2
    exit 1
  fi
}

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
  linux|darwin) ;;
  *)
    echo "Error: unsupported OS '$OS'. Supported: linux, darwin." >&2
    exit 1
    ;;
esac

RAW_ARCH="$(uname -m)"
case "$RAW_ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  i386|i686) ARCH="386" ;;
  *)
    echo "Error: unsupported architecture '$RAW_ARCH'." >&2
    exit 1
    ;;
esac

if [ "$OS" = "darwin" ] && [ "$ARCH" = "386" ]; then
  echo "Error: darwin/386 is not supported." >&2
  exit 1
fi

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

if [ -z "$VERSION" ]; then
  API_JSON="${TMP_DIR}/latest.json"
  fetch "https://api.github.com/repos/${REPO}/releases/latest" "$API_JSON"
  VERSION="$(sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' "$API_JSON" | head -n 1)"
  if [ -z "$VERSION" ]; then
    echo "Error: could not resolve latest release version from GitHub." >&2
    exit 1
  fi
fi

VER="${VERSION#v}"
ARCHIVE="ez-ssh_${VER}_${OS}_${ARCH}.tar.gz"
BASE_URL="https://github.com/${REPO}/releases/download/v${VER}"

echo "Downloading ez-ssh v${VER} (${OS}/${ARCH})..."
fetch "${BASE_URL}/${ARCHIVE}" "${TMP_DIR}/${ARCHIVE}"
fetch "${BASE_URL}/checksums.txt" "${TMP_DIR}/checksums.txt"

EXPECTED_SHA="$(awk -v file="$ARCHIVE" '$2 == file { print $1; exit }' "${TMP_DIR}/checksums.txt")"
if [ -z "$EXPECTED_SHA" ]; then
  echo "Error: checksum for ${ARCHIVE} not found in checksums.txt." >&2
  exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
  ACTUAL_SHA="$(sha256sum "${TMP_DIR}/${ARCHIVE}" | awk '{ print $1 }')"
elif command -v shasum >/dev/null 2>&1; then
  ACTUAL_SHA="$(shasum -a 256 "${TMP_DIR}/${ARCHIVE}" | awk '{ print $1 }')"
else
  echo "Error: sha256sum or shasum is required for verification." >&2
  exit 1
fi

if [ "$EXPECTED_SHA" != "$ACTUAL_SHA" ]; then
  echo "Error: SHA256 verification failed for ${ARCHIVE}." >&2
  echo "Expected: ${EXPECTED_SHA}" >&2
  echo "Actual:   ${ACTUAL_SHA}" >&2
  exit 1
fi

tar -xzf "${TMP_DIR}/${ARCHIVE}" -C "${TMP_DIR}"

if [ -z "${INSTALL_DIR:-}" ]; then
  if [ -w "/usr/local/bin" ] || [ "$(id -u)" -eq 0 ]; then
    INSTALL_DIR="/usr/local/bin"
  else
    INSTALL_DIR="${HOME}/.local/bin"
  fi
fi

mkdir -p "$INSTALL_DIR"
TARGET="${INSTALL_DIR}/ez-ssh"
cp "${TMP_DIR}/ez-ssh" "$TARGET"
chmod 0755 "$TARGET"

echo "Installed ez-ssh v${VER} to ${TARGET}"
case ":${PATH}:" in
  *":${INSTALL_DIR}:"*) ;;
  *)
    echo "Note: add ${INSTALL_DIR} to your PATH (e.g. export PATH=\"${INSTALL_DIR}:\$PATH\")."
    ;;
esac

"$TARGET" --version
