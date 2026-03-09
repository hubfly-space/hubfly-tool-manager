# Hubfly Tool Manager

A stable PM2-based manager for binary tools.

Now fully database-driven:
- No tool definitions in `config.json`
- Tools are registered via API/CLI and stored in SQLite
- Each tool gets its own folder: `/hubfly-tool-manager/tools/<name-with-spaces-replaced-by-dash>`

## Features
- Register new tools with:
  - binary download URL
  - optional SHA256 checksum
  - version command
  - runtime args
- PM2 lifecycle management:
  - start/stop/restart/provision/update
  - handles tools not yet registered in PM2
- Manual updates only (no background updater)
- Backup before update and rollback (keep last 3 backups)
- Rollback to latest/specific backup
- Cleanup endpoint for one tool only (tool dir + backups + db rows)
- SQLite history of tool versions/updates
- Manager self-update endpoint (not stored in tool version history)
- Manager runs as a `systemd` service (not PM2) with automatic restart
- Token-based API security (all endpoints protected except manager version check)
- Lockdown mode after repeated invalid-token attempts (10); unlock only via local CLI

## Quick Install (One Line, Linux)

```bash
curl -fsSL https://raw.githubusercontent.com/hubfly-space/hubfly-tool-manager/main/scripts/install.sh | sudo bash
```

Optional env overrides for installer:
- `HUBFLY_REPO=owner/repo` (if your GitHub repo path is different)
- `HUBFLY_VERSION=vX.Y.Z` (install a specific release instead of latest)

Installer also exposes binaries globally:
- `/usr/local/bin/htm`
- `/usr/local/bin/hubfly-tool-manager`
- Installer performs strict preflight checks for `node`, `npm`, and `pm2`; it stops with an error if any are missing
- `node`, `npm`, and `pm2` must be installed system-wide (PATH-visible to non-interactive `sudo bash`; shell-only `nvm` installs are not enough)
  - installer also attempts to detect common nvm node bin paths (for root/home users) and include them automatically
  - installer will reject `/root/.nvm/...` paths because the service runs as `hubfly`
- Installer adds `/etc/sudoers.d/hubfly-tool-manager` so service user `hubfly` can run:
  - `systemctl daemon-reload`
  - `systemctl restart hubfly-tool-manager`
  Required for self-update to complete.
- Installer normalizes the service file for self-update compatibility (`StartLimitIntervalSec` in `[Unit]`, no `NoNewPrivileges`).

## Runtime Configuration
Use CLI flags or env vars for manager runtime only.

Flags:
- `--listen-addr` default `:10000`
- `--data-dir` default `/hubfly-tool-manager/data`
- `--backups-dir` default `/hubfly-tool-manager/backups`
- `--tools-dir` default `/hubfly-tool-manager/tools`
- `--token-file` default `/hubfly-tool-manager/.token`
- `--pm2-bin` default `pm2`
- `--git-bin` default `git`
- `--command-timeout-secs` default `90`
- `--restart-on-boot` default `false`

Equivalent env vars:
- `HTM_LISTEN_ADDR`
- `HTM_DATA_DIR`
- `HTM_BACKUPS_DIR`
- `HTM_TOOLS_DIR`
- `HTM_TOKEN_FILE`
- `HTM_PM2_BIN`
- `HTM_GIT_BIN`
- `HTM_COMMAND_TIMEOUT_SECS`
- `HTM_RESTART_ON_BOOT`

## Build

```bash
go build -o bin/hubfly-tool-manager ./cmd/server
go build -o bin/htm ./cmd/htm
```

## Run

```bash
./bin/hubfly-tool-manager
```

Production (recommended):
```bash
sudo systemctl enable --now hubfly-tool-manager
sudo systemctl status hubfly-tool-manager
```

## HTTP API
Default base URL: `http://127.0.0.1:10000`

Public version check (no token required):
```bash
curl -s http://127.0.0.1:10000/version
```

Initialize token locally (overwrites existing token):
```bash
htm init
# or
htm init 'YOUR_SECRET_TOKEN'
```

After initialization, all other endpoints require token via `Authorization: Bearer <TOKEN>` (or `X-HTM-Token`).
After 10 invalid-token attempts, service enters lockdown mode for protected endpoints.
Clear lockdown locally:
```bash
htm unlock
```

Health:
```bash
TOKEN="$(cat /hubfly-tool-manager/.token)"
curl -s http://127.0.0.1:10000/health -H "Authorization: Bearer $TOKEN"
```
Response includes manager `version` (in release builds this is injected from the Git tag).

Register tool:
```bash
curl -s -X POST http://127.0.0.1:10000/tools/register \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "Hubfly Scale",
    "download_url": "https://example.com/releases/hubfly-scale",
    "checksum": "sha256:abc123...",
    "version_command": ["{binary}", "version"],
    "args": ["serve", "--port", "9010"]
  }'
```


Basic Tools:
hubfly-cmonitor
```bash
curl -s -X POST http://127.0.0.1:10000/tools/register \
  -H "Authorization: Bearer testing" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "hubfly-cmonitor",
    "download_url": "https://github.com/hubfly-space/hubfly-cmonitor/releases/latest/download/hubfly-cmonitor_v1.0.0_linux_amd64.zip",
    "version_command": ["{binary}", "version"],
    "args": []
  }'
```



List/status:
```bash
curl -s http://127.0.0.1:10000/tools -H "Authorization: Bearer $TOKEN"
curl -s http://127.0.0.1:10000/tools/Hubfly%20Scale -H "Authorization: Bearer $TOKEN"
curl -s http://127.0.0.1:10000/tools/Hubfly%20Scale/version -H "Authorization: Bearer $TOKEN"
```

Lifecycle:
```bash
curl -s -X POST http://127.0.0.1:10000/tools/Hubfly%20Scale/start -H "Authorization: Bearer $TOKEN"
curl -s -X POST http://127.0.0.1:10000/tools/Hubfly%20Scale/stop -H "Authorization: Bearer $TOKEN"
curl -s -X POST http://127.0.0.1:10000/tools/Hubfly%20Scale/restart -H "Authorization: Bearer $TOKEN"
curl -s -X POST http://127.0.0.1:10000/tools/Hubfly%20Scale/provision -H "Authorization: Bearer $TOKEN"
curl -s -X POST http://127.0.0.1:10000/tools/Hubfly%20Scale/update -H "Authorization: Bearer $TOKEN"
```

Update tool metadata and immediately update/restart tool:
```bash
curl -s -X POST http://127.0.0.1:10000/tools/Hubfly%20Scale/configure-update \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "download_url":"https://example.com/releases/hubfly-scale-v2",
    "checksum":"sha256:abc123",
    "version_command":["{binary}","version"],
    "args":["serve","--port","9011"]
  }'
```

Backups/rollback:
```bash
curl -s http://127.0.0.1:10000/tools/Hubfly%20Scale/backups -H "Authorization: Bearer $TOKEN"
curl -s -X POST http://127.0.0.1:10000/tools/Hubfly%20Scale/rollback -H "Authorization: Bearer $TOKEN"
curl -s -X POST http://127.0.0.1:10000/tools/Hubfly%20Scale/rollback \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"backup_id":"20260308T010203Z"}'
```

History:
```bash
curl -s "http://127.0.0.1:10000/tools/Hubfly%20Scale/history?limit=10" -H "Authorization: Bearer $TOKEN"
```

Cleanup one tool:
```bash
curl -s -X POST http://127.0.0.1:10000/tools/Hubfly%20Scale/cleanup -H "Authorization: Bearer $TOKEN"
```

Self update:
```bash
curl -s -X POST http://127.0.0.1:10000/self/update -H "Authorization: Bearer $TOKEN"
```
Self-update runs `systemctl daemon-reload` then `systemctl restart hubfly-tool-manager` with direct and `sudo` fallback attempts.
The endpoint returns immediately (`202 Accepted`) and executes update/restart asynchronously.
Self-update and release downloads use retry/backoff with explicit TLS/connect timeouts to tolerate transient network errors.

## CLI
Default server: `HTM_SERVER=http://127.0.0.1:10000`

```bash
htm init "YOUR_SECRET_TOKEN"
htm manager-version
htm health
htm unlock
htm register --name "Hubfly Scale" --url "https://example.com/releases/hubfly-scale" --checksum "sha256:abc123" --version-cmd "{binary},version" --args "serve,--port,9010"
htm list
htm status "Hubfly Scale"
htm version "Hubfly Scale"
htm start "Hubfly Scale"
htm update "Hubfly Scale"
htm configure-update "Hubfly Scale" --url "https://example.com/releases/hubfly-scale-v2"
htm backups "Hubfly Scale"
htm rollback "Hubfly Scale"
htm cleanup "Hubfly Scale"
htm self-update
```

## Tool Folder Layout
For `name = "Hubfly Scale"`, tool directory is:

```text
/hubfly-tool-manager/tools/hubfly-scale/
```

Expected managed files in that folder:
- downloaded binary
- optional `config.json`
- optional `.env`
- optional `configs/`

Backup captures these files if present.

## GitHub Release Workflow

The repository includes [release-linux.yml](/home/bonheur/Desktop/Projects/hubfly-tools/hubfly-tool-manager/.github/workflows/release-linux.yml), which on each `v*` tag:
- builds Linux binaries (`amd64`, `arm64`)
- creates tarballs
- generates `checksums.txt`
- publishes GitHub Release assets

## Troubleshooting

If update/install/runtime is not working as expected, use the commands below.

### Self-update did not change version

Check health/version before and after:
```bash
htm health
htm self-update
htm health
```

Read service logs:
```bash
journalctl -u hubfly-tool-manager -n 200 --no-pager
journalctl -u hubfly-tool-manager --since "20 min ago" --no-pager | grep -Ei "self-update|daemon-reload|restart|failed|error"
```

Follow logs live while triggering update:
```bash
journalctl -u hubfly-tool-manager -f
# in another shell
htm self-update
```

Verify release API reachability as service user:
```bash
sudo -u hubfly curl -fsSL https://api.github.com/repos/hubfly-space/hubfly-tool-manager/releases/latest | head -c 400
sudo -u hubfly curl -I https://github.com/hubfly-space/hubfly-tool-manager/releases/latest
```

Verify installed binary and active version:
```bash
ls -l /hubfly-tool-manager/bin/hubfly-tool-manager
htm health
```

### Service not listening on port 10000

Check service status and recent logs:
```bash
systemctl status hubfly-tool-manager --no-pager
journalctl -u hubfly-tool-manager -n 120 --no-pager
```

Check listener:
```bash
ss -ltnp | grep 10000
curl -s http://127.0.0.1:10000/version
```

### Auth/lockdown troubleshooting

Initialize token:
```bash
htm init "YOUR_SECRET_TOKEN"
```

If locked after repeated invalid-token attempts:
```bash
htm unlock
```
