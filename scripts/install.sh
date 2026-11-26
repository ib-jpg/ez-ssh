#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALL_DIR="${HOME}/.local/bin"
TARGET="${INSTALL_DIR}/ez-ssh"

VERSION="${VERSION:-}"
if [ -z "${VERSION}" ] && command -v git >/dev/null 2>&1 && [ -d "${ROOT_DIR}/.git" ]; then
  VERSION="$(git -C "${ROOT_DIR}" describe --tags --exact-match 2>/dev/null || true)"
fi
VERSION="${VERSION:-1.0.0}"
VERSION="${VERSION#v}"

mkdir -p "${INSTALL_DIR}"

echo "Building static ez-ssh v${VERSION} binary..."
cd "${ROOT_DIR}"
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o "${TARGET}" ./cmd/ez-ssh
chmod 0755 "${TARGET}"

echo "Installed: ${TARGET}"
"${TARGET}" --version
