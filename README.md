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
- Archive downloads (`.zip`, `.tar.gz`, `.tgz`) are extracted into each tool folder, overwriting files in place without deleting unrelated existing files
- Direct binary downloads (non-archive URLs) are stored as executable files inside each tool folder as well
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
- On boot, the manager reconciles registered tools and starts any tool that is not already running
- Token-based API security (all endpoints protected except manager version check)
- Lockdown mode after repeated invalid-token attempts (10); unlock only via local CLI
- Built-in web UI at `/web` for live inspection and common tool operations

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
- Service runs as `root`, and managed tools run as `root` through PM2.
- Installer normalizes the service file for self-update compatibility (`StartLimitIntervalSec` in `[Unit]`, no `NoNewPrivileges`).

## Runtime Configuration
Use CLI flags or env vars for manager runtime only.

Flags:
- `--listen-addr` default `:10000`
- `--data-dir` default `/hubfly-tool-manager/data`
- `--backups-dir` default `/hubfly-tool-manager/backups`
- `--tools-dir` default `/hubfly-tool-manager/tools`
- `--logs-dir` default `/hubfly-tool-manager/logs`
- `--token-file` default `/hubfly-tool-manager/.token`
- `--lockdown-file` default `/hubfly-tool-manager/.lockdown.json`
- `--session-secret-file` default `/hubfly-tool-manager/.session-secret`
- `--pm2-bin` default `pm2`
- `--git-bin` default `git`
- `--command-timeout-secs` default `90`
- `--restart-on-boot` default `true`

Equivalent env vars:
- `HTM_LISTEN_ADDR`
- `HTM_DATA_DIR`
- `HTM_BACKUPS_DIR`
- `HTM_TOOLS_DIR`
- `HTM_LOGS_DIR`
- `HTM_TOKEN_FILE`
- `HTM_LOCKDOWN_FILE`
- `HTM_SESSION_SECRET_FILE`
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

At startup, the manager checks every registered tool and starts any tool whose PM2 state is not already healthy. Tools already `online` are left untouched.

## HTTP API
Default base URL: `http://127.0.0.1:10000`

Public version check (no token required):
```bash
curl -s http://127.0.0.1:10000/version
```

Web UI:
```bash
open http://127.0.0.1:10000/web
```
The page loads without auth, shows a dedicated login screen, then creates a signed session cookie after the token is validated. Browser writes use CSRF protection. CLI/API bearer-token auth still works.

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
    "download_url": "https://github.com/hubfly-space/hubfly-cmonitor/releases/latest/download/hubfly-cmonitor_linux_amd64.zip",
    "version_command": ["{binary}", "version"],
    "args": []
  }'
```
curl -s http://127.0.0.1:10000/tools -H "Authorization: Bearer testing"
curl -s -X POST http://127.0.0.1:10000/tools/hubfly-cmonitor/start -H "Authorization: Bearer testing"
curl -s -X POST http://127.0.0.1:10000/tools/hubfly-cmonitor/provision -H "Authorization: Bearer testing"
curl -s -X POST http://127.0.0.1:10000/tools/hubfly-cmonitor/cleanup -H "Authorization: Bearer testing"

hubfly-reverse-proxy
```bash
curl -s -X POST http://127.0.0.1:10000/tools/register \
  -H "Authorization: Bearer testing" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "hubfly-reverse-proxy",
    "download_url": "https://github.com/hubfly-space/hubfly-reverse-proxy/releases/latest/download/hubfly-linux-amd64.zip",
    "version_command": ["{binary}", "version"],
    "args": []
  }'
```

hubfly-scale
```bash
curl -s -X POST http://127.0.0.1:10000/tools/register \
  -H "Authorization: Bearer testing" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "hubfly-scale",
    "download_url": "https://github.com/hubfly-space/hubfly-scale/releases/latest/download/hubfly-scale.zip",
    "version_command": ["{binary}", "version"],
    "args": []
  }'
```


hubfly-storage
```bash
curl -s -X POST http://127.0.0.1:10000/tools/register \
  -H "Authorization: Bearer testing" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "hubfly-storage",
    "download_url": "https://github.com/hubfly-space/hubfly-storage/releases/latest/download/hubfly-storage-linux-amd64.zip",
    "version_command": ["{binary}", "version"],
    "args": []
  }'
```
filebrowser
```bash
curl -s -X POST http://127.0.0.1:10000/tools/register \
  -H "Authorization: Bearer testing" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "filebrowser",
    "download_url": "https://github.com/hubfly-space/filebrowser/releases/latest/download/filebrowser",
    "version_command": ["{binary}", "version"],
    "args": ["--port","10001"]
  }'
  
```


hubfly-builder
```bash
curl -s -X POST http://127.0.0.1:10000/tools/register \
  -H "Authorization: Bearer testing" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "hubfly-builder",
    "download_url": "https://github.com/hubfly-space/hubfly-builder/releases/latest/download/hubfly-builder",
    "version_command": ["{binary}", "version"],
    "args": []
  }'
  
```

List/status:
```bash
curl -s http://127.0.0.1:10000/tools -H "Authorization: Bearer $TOKEN"
curl -s http://127.0.0.1:10000/tools/Hubfly%20Scale -H "Authorization: Bearer $TOKEN"
curl -s http://127.0.0.1:10000/tools/Hubfly%20Scale/version -H "Authorization: Bearer $TOKEN"
curl -s "http://127.0.0.1:10000/tools?extra=docker-engine" -H "Authorization: Bearer $TOKEN"
```
`GET /tools/{name}` now includes runtime status plus stored database config (`download_url`, `checksum`, `args`, `version_command`, `tool_dir`, `binary_path`, `created_at`, `db_updated_at`).
`GET /tools?extra=docker-engine` appends a synthetic Docker Engine status entry based on live system checks only; it is not stored or tracked in SQLite.

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
Self-update now preserves runtime state before replacing application files:
- `/hubfly-tool-manager/data`
- `/hubfly-tool-manager/backups`
- `/hubfly-tool-manager/tools`
- `/hubfly-tool-manager/logs`
- `/hubfly-tool-manager/configs`
- `/hubfly-tool-manager/.token`
- `/hubfly-tool-manager/.lockdown.json`
- `/hubfly-tool-manager/.session-secret`

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

## Export And Import To Another Server

If you want to move the full manager state to another server, copy the runtime state, not just the binaries.

### What to export

These paths contain the important state:
- `/hubfly-tool-manager/data` for the SQLite database with registered tools/history
- `/hubfly-tool-manager/tools` for downloaded binaries and per-tool files
- `/hubfly-tool-manager/backups` for rollback snapshots
- `/hubfly-tool-manager/logs` if you also want existing logs
- `/hubfly-tool-manager/configs` if you use shared manager-side config files
- `/hubfly-tool-manager/.token`
- `/hubfly-tool-manager/.lockdown.json`
- `/hubfly-tool-manager/.session-secret`

### Create a full export archive

Run this on the source server:

```bash
systemctl stop hubfly-tool-manager
tar -C /hubfly-tool-manager -czf /root/hubfly-tool-manager-export.tar.gz \
  data tools backups logs configs \
  .token .lockdown.json .session-secret
systemctl start hubfly-tool-manager
```

If `configs` or `logs` do not exist on that server, remove them from the tar command.

### Import on a new server

1. Install the same or newer HTM release on the target server.
2. Stop the service.
3. Extract the archive into `/hubfly-tool-manager`.
4. Start the service again.

Example:

```bash
systemctl stop hubfly-tool-manager
tar -C /hubfly-tool-manager -xzf /root/hubfly-tool-manager-export.tar.gz
chown -R root:root /hubfly-tool-manager
systemctl start hubfly-tool-manager
```

### Verify after import

```bash
htm health
htm list
pm2 status
```

If PM2 is empty but `htm list` still shows tools, restart the manager once and it will reconcile registered tools back into PM2 on boot.

### API-only import with curl

If you only want to recreate the registered tool definitions on another server, you can import them over HTTP.

This copies:
- `name`
- `download_url`
- `checksum`
- `args`
- `version_command`

This does not copy:
- existing downloaded binaries
- tool-local files under `/hubfly-tool-manager/tools`
- backups
- logs
- version history

You need `jq` for this.

Source and target example:

```bash
SRC_URL="http://10.0.0.10:10000"
SRC_TOKEN="SOURCE_TOKEN"
DST_URL="http://10.0.0.20:10000"
DST_TOKEN="TARGET_TOKEN"

curl -fsSL "$SRC_URL/tools" \
  -H "Authorization: Bearer $SRC_TOKEN" \
| jq -r '.tools[].name' \
| while IFS= read -r name; do
    encoded_name="$(printf '%s' "$name" | jq -sRr @uri)"
    payload="$(curl -fsSL "$SRC_URL/tools/$encoded_name" \
      -H "Authorization: Bearer $SRC_TOKEN" \
      | jq '{
          name,
          download_url,
          checksum,
          args,
          version_command
        }')"

    curl -fsSL -X POST "$DST_URL/tools/register" \
      -H "Authorization: Bearer $DST_TOKEN" \
      -H 'Content-Type: application/json' \
      -d "$payload"
    echo
  done
```

After importing the definitions, start or provision them on the target:

```bash
curl -fsSL "$DST_URL/tools" \
  -H "Authorization: Bearer $DST_TOKEN" \
| jq -r '.tools[].name' \
| while IFS= read -r name; do
    curl -fsSL -X POST "$DST_URL/tools/$(printf '%s' "$name" | jq -sRr @uri)/provision" \
      -H "Authorization: Bearer $DST_TOKEN"
    echo
  done
```

If you want the exact runtime state, files, backups, and existing binaries too, use the tar archive method above instead of the API-only import.

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

Verify release API reachability:
```bash
curl -fsSL https://api.github.com/repos/hubfly-space/hubfly-tool-manager/releases/latest | head -c 400
curl -I https://github.com/hubfly-space/hubfly-tool-manager/releases/latest
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
