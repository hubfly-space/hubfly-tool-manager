# Hubfly Tool Manager

A stable PM2-based manager for binary tools.

It provides:
- HTTP API to provision/start/stop/restart/update tools
- CLI (`htm`) for the same operations
- SQLite version history per tool (manual updates only)
- Backup before each tool update, keeping only the last 3 backups per tool
- Rollback endpoint/command to restore from retained backups
- Safe version probing even when a tool has no version command
- Self-update endpoint/command for the manager (not stored in tool version history)
- First-run handling when tools are not yet registered in PM2

## Architecture
- Config: JSON (`configs/config.json`)
- Process manager: PM2 (`pm2` is checked and can be installed automatically with npm)
- Database: SQLite (`data/manager.sqlite`)
- Backup path: `backups/<tool>/<timestamp>/`

## Configuration
Copy and edit:

```bash
cp configs/config.example.json configs/config.json
```

Main fields per tool:
- `name`: PM2 process name
- `repo`: Git repository URL (optional if already present locally)
- `branch`: Git branch for updates/provision
- `work_dir`: local source/work directory (absolute path)
- `binary_path`: binary path (absolute path)
- `args`: runtime args passed to binary under PM2
- `version_command`: command to read version, e.g. `[/path/tool, version]`
- `install_command`: optional command to run during first provisioning
- `update_command`: optional command to run during updates
- `env_file`, `config_file`, `configs_dir`: optional paths included in backup

## Build

```bash
go build -o bin/hubfly-tool-manager ./cmd/server
go build -o bin/htm ./cmd/htm
```

## Run

```bash
./bin/hubfly-tool-manager -config ./configs/config.json
```

## HTTP API
Default base URL: `http://127.0.0.1:10000`

Health:
```bash
curl -s http://127.0.0.1:10000/health
```

List tools:
```bash
curl -s http://127.0.0.1:10000/tools
```

Tool status:
```bash
curl -s http://127.0.0.1:10000/tools/example-tool
```

Start / Stop / Restart:
```bash
curl -s -X POST http://127.0.0.1:10000/tools/example-tool/start
curl -s -X POST http://127.0.0.1:10000/tools/example-tool/stop
curl -s -X POST http://127.0.0.1:10000/tools/example-tool/restart
```

Provision (first install + PM2 registration):
```bash
curl -s -X POST http://127.0.0.1:10000/tools/example-tool/provision
```

Manual update (backup + git pull + build command + restart):
```bash
curl -s -X POST http://127.0.0.1:10000/tools/example-tool/update
```

List backups:
```bash
curl -s http://127.0.0.1:10000/tools/example-tool/backups
```

Rollback:
```bash
# rollback to most recent backup
curl -s -X POST http://127.0.0.1:10000/tools/example-tool/rollback

# rollback to specific backup id
curl -s -X POST http://127.0.0.1:10000/tools/example-tool/rollback \
  -H 'Content-Type: application/json' \
  -d '{"backup_id":"20260308T010203Z"}'
```

Version:
```bash
curl -s http://127.0.0.1:10000/tools/example-tool/version
```

History:
```bash
curl -s "http://127.0.0.1:10000/tools/example-tool/history?limit=10"
```

Self update (manager only):
```bash
curl -s -X POST http://127.0.0.1:10000/self/update \
  -H 'Content-Type: application/json' \
  -d '{"work_dir":"/opt/hubfly-tool-manager","update_command":["go","build","./cmd/server"]}'
```

## CLI
Set server URL if needed:

```bash
export HTM_SERVER=http://127.0.0.1:10000
```

Examples:

```bash
htm health
htm list
htm status example-tool
htm version example-tool
htm history example-tool 10
htm backups example-tool
htm provision example-tool
htm update example-tool
htm rollback example-tool
htm rollback example-tool 20260308T010203Z
htm restart example-tool
htm self-update /opt/hubfly-tool-manager go build ./cmd/server
```

## Stability Notes
- HTTP server has timeout settings and panic recovery middleware.
- If version command is missing/fails, response uses `"unknown"` instead of failing.
- `start` and `restart` handle first-time PM2 registration automatically.
- Tool updates are manual only (no background updater).
- Rollback creates a fresh safeguard snapshot before restoring the selected backup.
- Manager self-update does not write into `tool_versions` table.

## Recommended Production Setup
- Run this manager itself under PM2 or systemd.
- Configure PM2 startup (`pm2 startup`) and `pm2 save` persistence.
- Keep tool binaries and configs on durable storage.
