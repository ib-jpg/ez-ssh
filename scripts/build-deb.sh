#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${1:-${VERSION:-}}"
if [ -z "${VERSION}" ] && command -v git >/dev/null 2>&1 && [ -d "${ROOT_DIR}/.git" ]; then
  VERSION="$(git -C "${ROOT_DIR}" describe --tags --exact-match 2>/dev/null || true)"
fi
VERSION="${VERSION:-1.0.0}"
VERSION="${VERSION#v}"

DEB_ARCH="${2:-${ARCH:-$(dpkg --print-architecture)}}"
case "${DEB_ARCH}" in
  amd64|x86_64)
    DEB_ARCH="amd64"
    GOARCH="amd64"
    ;;
  arm64|aarch64)
    DEB_ARCH="arm64"
    GOARCH="arm64"
    ;;
  i386|386)
    DEB_ARCH="i386"
    GOARCH="386"
    ;;
  *)
    echo "Unsupported Debian architecture: ${DEB_ARCH}" >&2
    exit 1
    ;;
esac

command -v dpkg-deb >/dev/null 2>&1 || { echo "dpkg-deb is required" >&2; exit 1; }
command -v go >/dev/null 2>&1 || { echo "go is required" >&2; exit 1; }

STAGE="$(mktemp -d)"
trap 'rm -rf "${STAGE}"' EXIT

mkdir -p \
  "${STAGE}/DEBIAN" \
  "${STAGE}/usr/bin" \
  "${STAGE}/usr/share/man/man1" \
  "${STAGE}/usr/share/doc/ez-ssh" \
  "${ROOT_DIR}/dist"
find "${STAGE}" -type d -exec chmod 0755 {} +

cd "${ROOT_DIR}"
echo "Building ez-ssh ${VERSION} for Debian/Ubuntu (${DEB_ARCH})..."
CGO_ENABLED=0 GOOS=linux GOARCH="${GOARCH}" go build -trimpath \
  -ldflags="-s -w -X main.version=${VERSION}" \
  -o "${STAGE}/usr/bin/ez-ssh" ./cmd/ez-ssh
chmod 0755 "${STAGE}/usr/bin/ez-ssh"

gzip -9 -n -c "${ROOT_DIR}/docs/ez-ssh.1" > "${STAGE}/usr/share/man/man1/ez-ssh.1.gz"
chmod 0644 "${STAGE}/usr/share/man/man1/ez-ssh.1.gz"
install -m 0644 "${ROOT_DIR}/LICENSE" "${STAGE}/usr/share/doc/ez-ssh/copyright"
install -m 0644 "${ROOT_DIR}/README.md" "${STAGE}/usr/share/doc/ez-ssh/README.md"

INSTALLED_SIZE="$(du -ks "${STAGE}/usr" | awk '{print $1}')"

cat > "${STAGE}/DEBIAN/control" <<EOF
Package: ez-ssh
Version: ${VERSION}
Section: net
Priority: optional
Architecture: ${DEB_ARCH}
Maintainer: Ibrahim Irsyad <ibrairsyd0406@gmail.com>
Recommends: openssh-client
Installed-Size: ${INSTALLED_SIZE}
Homepage: https://github.com/ib-jpg/ez-ssh
Description: Local, zero-cloud SSH and SFTP Manager TUI
 ez-ssh is a fast, zero-cloud SSH and SFTP Terminal User Interface (TUI)
 built in Go with Bubble Tea, Lip Gloss, and pure-Go SQLite.
EOF

OUT_DEB="${ROOT_DIR}/dist/ez-ssh_${VERSION}_${DEB_ARCH}.deb"
dpkg-deb --root-owner-group --build "${STAGE}" "${OUT_DEB}"
echo "Created ${OUT_DEB}"
