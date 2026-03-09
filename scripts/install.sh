#!/usr/bin/env bash
set -euo pipefail

REPO="${HUBFLY_REPO:-hubfly-space/hubfly-tool-manager}"
INSTALL_DIR="/hubfly-tool-manager"
BIN_DIR="$INSTALL_DIR/bin"
SERVICE_NAME="hubfly-tool-manager"
SERVICE_FILE="/etc/systemd/system/${SERVICE_NAME}.service"
SUDOERS_FILE="/etc/sudoers.d/hubfly-tool-manager"
ARCH_RAW="$(uname -m)"
RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
LOG_DIR_DEFAULT="/tmp"
LOG_FILE="$LOG_DIR_DEFAULT/${SERVICE_NAME}-install-${RUN_ID}.log"

mkdir -p "$LOG_DIR_DEFAULT"
touch "$LOG_FILE"

log() {
  local msg="$1"
  echo "[$(date -u +%Y-%m-%dT%H:%M:%SZ)] $msg" | tee -a "$LOG_FILE"
}

run() {
  local msg="$1"
  shift
  log "$msg"
  "$@" 2>&1 | tee -a "$LOG_FILE"
}

normalize_service_file() {
  local file="$1"
  [[ -f "$file" ]] || fail "Service file not found: $file"

  # Ensure restart rate limit key is in [Unit], not [Service].
  if ! awk 'BEGIN{in_unit=0;ok=0} /^\[Unit\]/{in_unit=1;next} /^\[/{in_unit=0} in_unit && /^StartLimitIntervalSec=0$/{ok=1} END{exit ok?0:1}' "$file"; then
    awk '
      BEGIN {inserted=0}
      /^\[Unit\]$/ {print; in_unit=1; next}
      /^\[/ && $0 != "[Unit]" { if (in_unit && !inserted) { print "StartLimitIntervalSec=0"; inserted=1 } in_unit=0 }
      /^StartLimitIntervalSec=0$/ { next }
      { print }
      END { if (in_unit && !inserted) print "StartLimitIntervalSec=0" }
    ' "$file" > "${file}.tmp"
    mv "${file}.tmp" "$file"
  fi

  # Self-update needs controlled privilege escalation; remove NoNewPrivileges if present.
  sed -i '/^NoNewPrivileges=/d' "$file"
}

fail() {
  local msg="$1"
  log "ERROR: $msg"
  log "Install log: $LOG_FILE"
  exit 1
}

cleanup_on_error() {
  local code=$?
  if [[ $code -ne 0 ]]; then
    log "Installation failed with exit code $code"
    log "Install log: $LOG_FILE"
  fi
}
trap cleanup_on_error EXIT

if [[ $EUID -ne 0 ]]; then
  fail "Run as root (use: sudo bash install.sh)"
fi

case "$ARCH_RAW" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) fail "Unsupported architecture: $ARCH_RAW" ;;
esac

for cmd in curl tar sha256sum systemctl; do
  command -v "$cmd" >/dev/null 2>&1 || fail "$cmd is required"
done

# Normalize PATH for non-interactive sudo contexts.
export PATH="/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/snap/bin:${PATH:-}"

resolve_bin() {
  local bin="$1"
  if command -v "$bin" >/dev/null 2>&1; then
    command -v "$bin"
    return 0
  fi
  for p in "/usr/local/bin/$bin" "/usr/bin/$bin" "/bin/$bin" "/snap/bin/$bin"; do
    if [[ -x "$p" ]]; then
      echo "$p"
      return 0
    fi
  done
  return 1
}

ensure_pm2_prereqs() {
  local node_bin npm_bin pm2_bin
  node_bin="$(resolve_bin node || true)"
  npm_bin="$(resolve_bin npm || true)"
  pm2_bin="$(resolve_bin pm2 || true)"

  if [[ -z "$node_bin" || -z "$npm_bin" || -z "$pm2_bin" ]]; then
    log "Current PATH: $PATH"
    [[ -z "$node_bin" ]] && log "node binary not found in PATH or common locations."
    [[ -z "$npm_bin" ]] && log "npm binary not found in PATH or common locations."
    [[ -z "$pm2_bin" ]] && log "pm2 binary not found in PATH or common locations."
    fail "Missing runtime prerequisites. Ensure node, npm, and pm2 are installed system-wide (not shell-only via nvm)."
  fi

  log "node detected: $node_bin"
  log "npm detected: $npm_bin"
  log "pm2 detected: $pm2_bin"
}

TAG="${HUBFLY_VERSION:-}"
if [[ -z "$TAG" ]]; then
  log "Resolving latest release tag from GitHub"
  TAG="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" | sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
fi
[[ -n "$TAG" ]] || fail "Failed to resolve release tag. Set HUBFLY_VERSION manually."

ASSET="hubfly-tool-manager_linux_${ARCH}.tar.gz"
BASE_URL="https://github.com/$REPO/releases/download/$TAG"
TMP_DIR="$(mktemp -d)"
STAGE_DIR="$TMP_DIR/stage"
PRESERVE_DIR="$TMP_DIR/preserve"
mkdir -p "$STAGE_DIR" "$PRESERVE_DIR"

log "Preparing install for $REPO $TAG ($ARCH)"
log "Log file: $LOG_FILE"

run "Downloading release asset" curl -fL "$BASE_URL/$ASSET" -o "$TMP_DIR/$ASSET"
run "Downloading checksums" curl -fL "$BASE_URL/checksums.txt" -o "$TMP_DIR/checksums.txt"

EXPECTED="$(grep "  $ASSET$" "$TMP_DIR/checksums.txt" | awk '{print $1}')"
[[ -n "$EXPECTED" ]] || fail "Could not find checksum for $ASSET"
ACTUAL="$(sha256sum "$TMP_DIR/$ASSET" | awk '{print $1}')"
[[ "$EXPECTED" == "$ACTUAL" ]] || fail "Checksum mismatch for $ASSET"

# Smart re-run cleanup:
# - stop existing service cleanly
# - preserve runtime state (data, backups, tools)
# - remove old app files before reinstall
if [[ -d "$INSTALL_DIR" || -f "$SERVICE_FILE" ]]; then
  log "Existing installation detected. Starting smart cleanup."

  if systemctl list-unit-files | grep -q "^${SERVICE_NAME}\.service"; then
    run "Stopping existing service" systemctl stop "$SERVICE_NAME" || true
    run "Disabling existing service" systemctl disable "$SERVICE_NAME" || true
  fi

  for d in data backups tools; do
    if [[ -d "$INSTALL_DIR/$d" ]]; then
      run "Preserving $d directory" mv "$INSTALL_DIR/$d" "$PRESERVE_DIR/$d"
    fi
  done

  if [[ -d "$INSTALL_DIR" ]]; then
    run "Removing old application files" find "$INSTALL_DIR" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
  fi
else
  log "No previous installation found. Fresh install."
fi

ensure_pm2_prereqs
run "Creating install directories" mkdir -p "$INSTALL_DIR" "$BIN_DIR" "$INSTALL_DIR/data" "$INSTALL_DIR/backups" "$INSTALL_DIR/tools"
run "Extracting release archive" tar -C "$STAGE_DIR" -xzf "$TMP_DIR/$ASSET"
run "Installing release files" cp -a "$STAGE_DIR"/. "$INSTALL_DIR"/
run "Setting executable permissions" chmod +x "$BIN_DIR/hubfly-tool-manager" "$BIN_DIR/htm"
run "Normalizing systemd service file for self-update compatibility" normalize_service_file "$INSTALL_DIR/hubfly-tool-manager.service"

for d in data backups tools; do
  if [[ -d "$PRESERVE_DIR/$d" ]]; then
    run "Restoring preserved $d directory" mv "$PRESERVE_DIR/$d" "$INSTALL_DIR/$d"
  fi
done

if ! id -u hubfly >/dev/null 2>&1; then
  run "Creating system user 'hubfly'" useradd --system --home "$INSTALL_DIR" --shell /usr/sbin/nologin hubfly
fi
run "Applying ownership" chown -R hubfly:hubfly "$INSTALL_DIR"

# Allow service user to reload/restart only this service during self-update.
run "Configuring sudoers for self-update systemctl commands" bash -c "cat > '$SUDOERS_FILE' <<'SUDOEOF'
Defaults:hubfly !requiretty
hubfly ALL=(root) NOPASSWD: /usr/bin/systemctl daemon-reload, /usr/bin/systemctl restart hubfly-tool-manager, /bin/systemctl daemon-reload, /bin/systemctl restart hubfly-tool-manager
SUDOEOF"
run "Validating sudoers file" visudo -cf "$SUDOERS_FILE"
run "Setting sudoers permissions" chmod 0440 "$SUDOERS_FILE"

run "Installing systemd service file" install -m 0644 "$INSTALL_DIR/hubfly-tool-manager.service" "$SERVICE_FILE"
run "Reloading systemd" systemctl daemon-reload
run "Enabling service" systemctl enable "$SERVICE_NAME"
run "Starting service" systemctl restart "$SERVICE_NAME"

run "Linking CLI binaries globally" ln -sf "$BIN_DIR/htm" /usr/local/bin/htm
run "Linking server binary globally" ln -sf "$BIN_DIR/hubfly-tool-manager" /usr/local/bin/hubfly-tool-manager

mkdir -p "$INSTALL_DIR/logs"
run "Archiving installer log" cp "$LOG_FILE" "$INSTALL_DIR/logs/install-${RUN_ID}.log"

run "Service status" systemctl --no-pager --full status "$SERVICE_NAME" || true

log "Installation completed successfully"
log "Service: systemctl status $SERVICE_NAME"
log "CLI: htm"
log "Install log: $INSTALL_DIR/logs/install-${RUN_ID}.log"

rm -rf "$TMP_DIR"
trap - EXIT
