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
- `--pm2-bin` default `pm2`
- `--git-bin` default `git`
- `--command-timeout-secs` default `90`
- `--restart-on-boot` default `false`

Equivalent env vars:
- `HTM_LISTEN_ADDR`
- `HTM_DATA_DIR`
- `HTM_BACKUPS_DIR`
- `HTM_TOOLS_DIR`
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

Health:
```bash
curl -s http://127.0.0.1:10000/health
```
Response includes manager `version` (in release builds this is injected from the Git tag).

Register tool:
```bash
curl -s -X POST http://127.0.0.1:10000/tools/register \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "Hubfly Scale",
    "download_url": "https://example.com/releases/hubfly-scale",
    "checksum": "sha256:abc123...",
    "version_command": ["{binary}", "version"],
    "args": ["serve", "--port", "9010"]
  }'
```

List/status:
```bash
curl -s http://127.0.0.1:10000/tools
curl -s http://127.0.0.1:10000/tools/Hubfly%20Scale
curl -s http://127.0.0.1:10000/tools/Hubfly%20Scale/version
```

Lifecycle:
```bash
curl -s -X POST http://127.0.0.1:10000/tools/Hubfly%20Scale/start
curl -s -X POST http://127.0.0.1:10000/tools/Hubfly%20Scale/stop
curl -s -X POST http://127.0.0.1:10000/tools/Hubfly%20Scale/restart
curl -s -X POST http://127.0.0.1:10000/tools/Hubfly%20Scale/provision
curl -s -X POST http://127.0.0.1:10000/tools/Hubfly%20Scale/update
```

Update tool metadata and immediately update/restart tool:
```bash
curl -s -X POST http://127.0.0.1:10000/tools/Hubfly%20Scale/configure-update \
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
curl -s http://127.0.0.1:10000/tools/Hubfly%20Scale/backups
curl -s -X POST http://127.0.0.1:10000/tools/Hubfly%20Scale/rollback
curl -s -X POST http://127.0.0.1:10000/tools/Hubfly%20Scale/rollback \
  -H 'Content-Type: application/json' \
  -d '{"backup_id":"20260308T010203Z"}'
```

History:
```bash
curl -s "http://127.0.0.1:10000/tools/Hubfly%20Scale/history?limit=10"
```

Cleanup one tool:
```bash
curl -s -X POST http://127.0.0.1:10000/tools/Hubfly%20Scale/cleanup
```

Self update:
```bash
curl -s -X POST http://127.0.0.1:10000/self/update
```
Self-update runs `systemctl daemon-reload` then `systemctl restart hubfly-tool-manager` with direct and `sudo` fallback attempts.
The endpoint returns immediately (`202 Accepted`) and executes update/restart asynchronously.

## CLI
Default server: `HTM_SERVER=http://127.0.0.1:10000`

```bash
htm health
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
