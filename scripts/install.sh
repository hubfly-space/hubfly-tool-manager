#!/usr/bin/env bash
set -euo pipefail

if [[ $EUID -ne 0 ]]; then
  echo "Run as root (use: sudo bash install.sh)"
  exit 1
fi

REPO="${HUBFLY_REPO:-hubfly-tools/hubfly-tool-manager}"
INSTALL_DIR="/hubfly-tool-manager"
BIN_DIR="$INSTALL_DIR/bin"
SERVICE_FILE="/etc/systemd/system/hubfly-tool-manager.service"
ARCH_RAW="$(uname -m)"

case "$ARCH_RAW" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $ARCH_RAW"
    exit 1
    ;;
esac

if ! command -v curl >/dev/null 2>&1; then
  echo "curl is required"
  exit 1
fi
if ! command -v tar >/dev/null 2>&1; then
  echo "tar is required"
  exit 1
fi
if ! command -v sha256sum >/dev/null 2>&1; then
  echo "sha256sum is required"
  exit 1
fi

TAG="${HUBFLY_VERSION:-}"
if [[ -z "$TAG" ]]; then
  TAG="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
fi

if [[ -z "$TAG" ]]; then
  echo "Failed to resolve release tag. Set HUBFLY_VERSION manually."
  exit 1
fi

ASSET="hubfly-tool-manager_linux_${ARCH}.tar.gz"
BASE_URL="https://github.com/$REPO/releases/download/$TAG"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT

echo "Installing $REPO $TAG for $ARCH into $INSTALL_DIR"

curl -fL "$BASE_URL/$ASSET" -o "$TMP_DIR/$ASSET"
curl -fL "$BASE_URL/checksums.txt" -o "$TMP_DIR/checksums.txt"

EXPECTED="$(grep "  $ASSET$" "$TMP_DIR/checksums.txt" | awk '{print $1}')"
if [[ -z "$EXPECTED" ]]; then
  echo "Could not find checksum for $ASSET"
  exit 1
fi
ACTUAL="$(sha256sum "$TMP_DIR/$ASSET" | awk '{print $1}')"
if [[ "$EXPECTED" != "$ACTUAL" ]]; then
  echo "Checksum mismatch for $ASSET"
  exit 1
fi

mkdir -p "$INSTALL_DIR" "$BIN_DIR" "$INSTALL_DIR/data" "$INSTALL_DIR/backups" "$INSTALL_DIR/tools"
tar -C "$INSTALL_DIR" -xzf "$TMP_DIR/$ASSET"
chmod +x "$BIN_DIR/hubfly-tool-manager" "$BIN_DIR/htm"

if ! id -u hubfly >/dev/null 2>&1; then
  useradd --system --home "$INSTALL_DIR" --shell /usr/sbin/nologin hubfly
fi
chown -R hubfly:hubfly "$INSTALL_DIR"

install -m 0644 "$INSTALL_DIR/hubfly-tool-manager.service" "$SERVICE_FILE"
systemctl daemon-reload
systemctl enable --now hubfly-tool-manager

systemctl --no-pager --full status hubfly-tool-manager || true

echo "Installed."
echo "Service: systemctl status hubfly-tool-manager"
echo "CLI: $BIN_DIR/htm"
